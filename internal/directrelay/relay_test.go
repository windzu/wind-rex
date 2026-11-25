package directrelay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestCertPinAndVerification(t *testing.T) {
	dir := t.TempDir()
	cert, pin, err := LoadOrCreateCert(dir, "test", []string{"127.0.0.1", "relay.example.com"})
	if err != nil {
		t.Fatalf("LoadOrCreateCert: %v", err)
	}
	if !ValidPin(pin) {
		t.Fatalf("invalid pin: %q", pin)
	}
	// Reloading returns the same persisted cert and pin.
	_, pin2, err := LoadOrCreateCert(dir, "test", nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if pin2 != pin {
		t.Fatalf("reloaded pin %q != %q", pin2, pin)
	}

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello-tls"))
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	defer ts.Close()

	// Matching pin succeeds.
	okClient := &http.Client{Transport: &http.Transport{TLSClientConfig: TLSConfigWithPin(pin)}}
	resp, err := okClient.Get(ts.URL)
	if err != nil {
		t.Fatalf("pinned GET failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello-tls" {
		t.Fatalf("body = %q, want hello-tls", body)
	}

	// Wrong pin fails.
	wrongPin := CertPin([]byte("different-cert"))
	badClient := &http.Client{Transport: &http.Transport{TLSClientConfig: TLSConfigWithPin(wrongPin)}}
	if resp, err := badClient.Get(ts.URL); err == nil {
		resp.Body.Close()
		t.Fatal("expected pin mismatch error")
	} else if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
}

func TestRelayHTTPAndWebSocketEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cert, pin, err := LoadOrCreateCert(dir, "relay", []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("cert: %v", err)
	}

	relay := NewServer("top-secret", nil)
	defer relay.Close()
	relaySrv := httptest.NewUnstartedServer(relay.Handler())
	relaySrv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	relaySrv.StartTLS()
	defer relaySrv.Close()

	hostID := "host_demo_123456"

	// Offline host returns 502.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: TLSConfigWithPin(pin)}}
	resp, err := client.Get(relaySrv.URL + "/h/" + hostID + "/api/help")
	if err != nil {
		t.Fatalf("offline GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("offline status = %d, want 502", resp.StatusCode)
	}

	// Start host tunnel and serve HTTP + WebSocket over it.
	connected := make(chan struct{}, 1)
	ln, err := StartTunnel(ClientConfig{
		RelayURL: relaySrv.URL,
		HostID:   hostID,
		Secret:   "top-secret",
		CertPin:  pin,
		OnStatus: func(ok bool, _ error) {
			if ok {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("StartTunnel: %v", err)
	}
	defer ln.Close()

	hostMux := http.NewServeMux()
	hostMux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Auth-Seen", r.Header.Get("Authorization"))
		_, _ = io.Copy(w, r.Body)
	})
	hostMux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:       []string{"rex.control.v1"},
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			typ, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			if err := ws.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	})
	hostSrv := &http.Server{Handler: hostMux}
	go func() { _ = hostSrv.Serve(ln) }()
	defer hostSrv.Close()

	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("host tunnel did not connect")
	}

	// Large HTTP payload crossing the 512 KiB flow-control window.
	payload := make([]byte, 1<<20)
	_, _ = rand.Read(payload)
	req, _ := http.NewRequest(http.MethodPost, relaySrv.URL+"/h/"+hostID+"/api/echo", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer my-host-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("HTTP echo: %v", err)
	}
	gotBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read echo body: %v", err)
	}
	if resp.Header.Get("X-Auth-Seen") != "Bearer my-host-token" {
		t.Fatalf("auth header = %q", resp.Header.Get("X-Auth-Seen"))
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("echoed %d bytes, mismatch", len(gotBody))
	}

	// WebSocket subprotocol and bidirectional framing over the same relay tunnel.
	wsURL := "wss" + strings.TrimPrefix(relaySrv.URL, "https") + "/h/" + hostID + "/api/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols:    []string{"rex.control.v1"},
		HTTPClient:      client,
		CompressionMode: websocket.CompressionDisabled,
	})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("WebSocket dial: %v", err)
	}
	defer ws.CloseNow()
	if ws.Subprotocol() != "rex.control.v1" {
		t.Fatalf("subprotocol = %q", ws.Subprotocol())
	}
	for _, msg := range []string{"hello", "world", "ordered-3"} {
		if err := ws.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatalf("ws write: %v", err)
		}
		_, got, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("ws read: %v", err)
		}
		if string(got) != msg {
			t.Fatalf("ws echo = %q, want %q", got, msg)
		}
	}
	_ = ws.Close(websocket.StatusNormalClosure, "done")
}
