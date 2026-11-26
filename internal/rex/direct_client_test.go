//go:build darwin || linux || windows

package rex

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestParseDirectTarget(t *testing.T) {
	sum := sha256.Sum256([]byte("test-cert-der"))
	b64Pin := base64.RawURLEncoding.EncodeToString(sum[:])
	hexPin := hex.EncodeToString(sum[:])

	t.Run("direct with base64url pin and name", func(t *testing.T) {
		raw := "https://203.0.113.10:7443/#k=tok_secret_123&p=" + b64Pin + "&n=alice-laptop"
		if !IsDirectTarget(raw) || !ValidRemoteAddress(raw) {
			t.Fatalf("expected valid direct target")
		}
		got, err := ParseDirectTarget(raw)
		if err != nil {
			t.Fatalf("ParseDirectTarget: %v", err)
		}
		if got.BaseURL != "https://203.0.113.10:7443" || got.Host != "203.0.113.10:7443" || got.Hostname != "203.0.113.10" {
			t.Fatalf("unexpected host fields: %+v", got)
		}
		if got.Token != "tok_secret_123" || got.Name != "alice-laptop" || got.DefaultName() != "alice-laptop" {
			t.Fatalf("unexpected token/name: %+v", got)
		}
		if got.IsRelay || got.TunnelURL() != "wss://203.0.113.10:7443/api/v1/rex-tunnel" {
			t.Fatalf("unexpected tunnel URL: relay=%v url=%q", got.IsRelay, got.TunnelURL())
		}
	})

	t.Run("direct with 64-hex pin and default hostname", func(t *testing.T) {
		raw := "https://build.example.com:7443#k=tok_hex&p=" + strings.ToUpper(hexPin)
		got, err := ParseDirectTarget(raw)
		if err != nil {
			t.Fatalf("ParseDirectTarget: %v", err)
		}
		if !got.hasPin || got.pinBytes != sum {
			t.Fatalf("pin bytes mismatch")
		}
		if got.DefaultName() != "build.example.com" {
			t.Fatalf("DefaultName = %q, want build.example.com", got.DefaultName())
		}
	})

	t.Run("relay preserves /h/<id> prefix", func(t *testing.T) {
		raw := "https://relay.example.com:7443/h/host_abc123/#k=tok_relay&p=" + b64Pin + "&n=linux-box"
		got, err := ParseDirectTarget(raw)
		if err != nil {
			t.Fatalf("ParseDirectTarget: %v", err)
		}
		if !got.IsRelay || got.Path != "/h/host_abc123" {
			t.Fatalf("unexpected relay fields: %+v", got)
		}
		wantTunnel := "wss://relay.example.com:7443/h/host_abc123/api/v1/rex-tunnel"
		if got.TunnelURL() != wantTunnel {
			t.Fatalf("TunnelURL = %q, want %q", got.TunnelURL(), wantTunnel)
		}
		if got.DisplayAddress() != "https://relay.example.com:7443/h/host_abc123" {
			t.Fatalf("DisplayAddress = %q", got.DisplayAddress())
		}
	})

	t.Run("invalid targets rejected", func(t *testing.T) {
		cases := []struct {
			raw     string
			wantErr error
		}{
			{"http://203.0.113.10:7443/#k=tok", nil},
			{"https://203.0.113.10:7443/", nil},
			{"https://203.0.113.10:7443/#p=" + hexPin, nil},
			{"https://user:pass@203.0.113.10:7443/#k=tok", nil},
			{"https://203.0.113.10:7443/#k=tok&p=not-a-valid-pin", ErrDirectInvalidPin},
			{"https://203.0.113.10:7443/#k=tok&p=" + strings.Repeat("z", 64), ErrDirectInvalidPin},
		}
		for _, tc := range cases {
			if ValidRemoteAddress(tc.raw) {
				t.Errorf("ValidRemoteAddress(%q) = true, want false", tc.raw)
			}
			_, err := ParseDirectTarget(tc.raw)
			if err == nil {
				t.Errorf("ParseDirectTarget(%q) succeeded, want error", tc.raw)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("ParseDirectTarget(%q) err = %v, want %v", tc.raw, err, tc.wantErr)
			}
		}
	})
}

func TestSanitizeDirectTargetAndScrub(t *testing.T) {
	secret := "superSecretToken_999"
	raw := "https://relay.example.com:7443/h/host1/#k=" + secret + "&p=abc&n=alice-laptop"

	san := SanitizeDirectTarget(raw)
	if san != "https://relay.example.com:7443/h/host1" {
		t.Fatalf("SanitizeDirectTarget = %q", san)
	}
	if strings.Contains(san, secret) {
		t.Fatalf("SanitizeDirectTarget leaked secret: %q", san)
	}

	scrubbed := Scrub("dial failed for "+raw+" with token "+secret, raw)
	if strings.Contains(scrubbed, secret) {
		t.Fatalf("Scrub leaked secret: %q", scrubbed)
	}
	if !strings.Contains(scrubbed, "https://relay.example.com:7443/h/host1") {
		t.Fatalf("Scrub lost sanitized host: %q", scrubbed)
	}
}

func TestConnectRemoteDirectTLSAndRelayPath(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	sock := SocketPath()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Serve()
	}()
	t.Cleanup(func() {
		if c, err := ConnectSocket(sock); err == nil {
			_ = c.Shutdown()
			_ = c.Close()
		}
		<-done
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("unix", sock)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rex server did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	const wantToken = "direct-token-1234567890abcdef"
	const relayPrefix = "/h/alice-host"
	var gotPath string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		auth := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+wantToken)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != DirectTunnelPath && r.URL.Path != relayPrefix+DirectTunnelPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		unixConn, err := net.Dial("unix", sock)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionDisabled,
		})
		if err != nil {
			_ = unixConn.Close()
			return
		}
		wsConn := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
		defer wsConn.Close()
		defer unixConn.Close()
		ch := make(chan struct{})
		go func() {
			defer close(ch)
			_, _ = io.Copy(unixConn, wsConn)
			_ = unixConn.Close()
			_ = wsConn.Close()
		}()
		_, _ = io.Copy(wsConn, unixConn)
		_ = wsConn.Close()
		_ = unixConn.Close()
		<-ch
	}))
	defer srv.Close()

	leafSum := sha256.Sum256(srv.Certificate().Raw)
	hexPin := hex.EncodeToString(leafSum[:])
	b64Pin := base64.RawURLEncoding.EncodeToString(leafSum[:])

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Relay prefix + hex pin succeeds and hits /h/alice-host/api/v1/rex-tunnel.
	relayURL := srv.URL + relayPrefix + "/#k=" + wantToken + "&p=" + hexPin + "&n=alice-laptop"
	c, err := ConnectRemote(ctx, relayURL)
	if err != nil {
		t.Fatalf("ConnectRemote relay: %v", err)
	}
	h, err := c.Hello()
	if err != nil {
		c.Close()
		t.Fatalf("Hello: %v", err)
	}
	if h.Version != ProtocolVersion || h.Conn == "" {
		c.Close()
		t.Fatalf("unexpected Hello: %+v", h)
	}
	if gotPath != relayPrefix+DirectTunnelPath {
		c.Close()
		t.Fatalf("request path = %q, want %q", gotPath, relayPrefix+DirectTunnelPath)
	}
	_ = c.Close()

	// 2. Direct path + base64url pin succeeds.
	directURL := srv.URL + "/#k=" + wantToken + "&p=" + b64Pin
	c2, err := ConnectRemote(ctx, directURL)
	if err != nil {
		t.Fatalf("ConnectRemote direct: %v", err)
	}
	if _, err := c2.List(); err != nil {
		c2.Close()
		t.Fatalf("List: %v", err)
	}
	_ = c2.Close()

	// 3. Wrong token fails with ErrDirectAuth without leaking token.
	badTok := "wrong-token-secret-xyz"
	if _, err := ConnectRemote(ctx, srv.URL+"/#k="+badTok+"&p="+hexPin); !errors.Is(err, ErrDirectAuth) {
		t.Fatalf("wrong token err = %v, want ErrDirectAuth", err)
	} else if strings.Contains(err.Error(), badTok) {
		t.Fatalf("wrong token error leaked secret: %v", err)
	}

	// 4. Wrong pin fails with ErrDirectPinMismatch.
	wrongPin := strings.Repeat("0", 64)
	if _, err := ConnectRemote(ctx, srv.URL+"/#k="+wantToken+"&p="+wrongPin); !errors.Is(err, ErrDirectPinMismatch) {
		t.Fatalf("wrong pin err = %v, want ErrDirectPinMismatch", err)
	}
}
