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

func TestConnectDirectHappyEyeballsAndFallback(t *testing.T) {
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

	oldAllow := allowLoopbackLANCandidate
	oldHead := lanHeadStart
	oldDial := lanDialTimeout
	oldProbe := lanProbeTimeout
	allowLoopbackLANCandidate = true
	lanHeadStart = 120 * time.Millisecond
	lanDialTimeout = 400 * time.Millisecond
	lanProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() {
		allowLoopbackLANCandidate = oldAllow
		lanHeadStart = oldHead
		lanDialTimeout = oldDial
		lanProbeTimeout = oldProbe
	})

	const wantToken = "happy-eyeballs-token-1234567890"
	const relayPrefix = "/h/host-eyeballs"

	newTunnelServer := func(expectedPath string, onHit func(), rejectAuth bool) (*httptest.Server, string) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if onHit != nil {
				onHit()
			}
			if rejectAuth || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+wantToken)) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Path == LANHintPath || r.URL.Path == relayPrefix+LANHintPath {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"ok":true}`)
				return
			}
			if r.URL.Path != expectedPath {
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
		t.Cleanup(srv.Close)
		sum := sha256.Sum256(srv.Certificate().Raw)
		return srv, base64.RawURLEncoding.EncodeToString(sum[:])
	}

	relayHits := 0
	relaySrv, relayPin := newTunnelServer(relayPrefix+DirectTunnelPath, func() { relayHits++ }, false)
	lanHits := 0
	lanSrv, lanPin := newTunnelServer(DirectTunnelPath, func() { lanHits++ }, false)

	relayURL := relaySrv.URL + relayPrefix + "/#k=" + wantToken + "&p=" + relayPin + "&n=dev-box-1"
	lanAddr := strings.TrimPrefix(lanSrv.URL, "https://")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Fast LAN candidate wins before lanHeadStart and never dials relay.
	c, route, err := ConnectDirectWithCandidates(ctx, relayURL, []DirectCandidate{{Addr: lanAddr, Pin: lanPin}})
	if err != nil {
		t.Fatalf("ConnectDirectWithCandidates LAN win: %v", err)
	}
	if route.Kind != RouteLANDirect || route.Host != lanAddr {
		c.Close()
		t.Fatalf("route = %+v, want RouteLANDirect %s", route, lanAddr)
	}
	if relayHits != 0 {
		c.Close()
		t.Fatalf("relayHits = %d, want 0 when LAN won before head start", relayHits)
	}
	if _, err := c.Hello(); err != nil {
		c.Close()
		t.Fatalf("Hello over LAN direct: %v", err)
	}
	// Verify AttachConn reuses winning LAN direct route (no relay hits).
	created, err := c.Create(CreateOptions{Cols: 80, Rows: 24})
	if err != nil {
		c.Close()
		t.Fatalf("Create over LAN direct: %v", err)
	}
	sc, err := c.AttachConn(Attach{SID: created.ID, Cols: 80, Rows: 24})
	if err != nil {
		c.Close()
		t.Fatalf("AttachConn over LAN direct: %v", err)
	}
	_ = sc.Close()
	_ = c.Kill(created.ID)
	_ = c.Close()
	if relayHits != 0 {
		t.Fatalf("relayHits after AttachConn = %d, want 0", relayHits)
	}

	// 2. ProbeLANDirect succeeds against healthy pinned LAN server and rejects bad pin.
	if got, err := ProbeLANDirect(ctx, relayURL, []DirectCandidate{{Addr: lanAddr, Pin: lanPin}}); err != nil || got.Addr != lanAddr {
		t.Fatalf("ProbeLANDirect valid = (%+v, %v), want %s", got, err, lanAddr)
	}
	wrongPin := strings.Repeat("a", 64)
	if _, err := ProbeLANDirect(ctx, relayURL, []DirectCandidate{{Addr: lanAddr, Pin: wrongPin}}); err == nil {
		t.Fatal("ProbeLANDirect with wrong pin succeeded, want error")
	}

	// 3. Pin mismatch on spoofed LAN server falls back to relay cleanly.
	c2, route2, err := ConnectDirectWithCandidates(ctx, relayURL, []DirectCandidate{{Addr: lanAddr, Pin: wrongPin}})
	if err != nil {
		t.Fatalf("ConnectDirectWithCandidates pin-mismatch fallback: %v", err)
	}
	if route2.Kind != RouteRelay {
		c2.Close()
		t.Fatalf("route2 = %+v, want RouteRelay", route2)
	}
	_ = c2.Close()

	// 4. Auth rejection on old LAN server falls back to relay cleanly.
	rejectSrv, rejectPin := newTunnelServer(DirectTunnelPath, nil, true)
	rejectAddr := strings.TrimPrefix(rejectSrv.URL, "https://")
	c3, route3, err := ConnectDirectWithCandidates(ctx, relayURL, []DirectCandidate{{Addr: rejectAddr, Pin: rejectPin}})
	if err != nil {
		t.Fatalf("ConnectDirectWithCandidates auth-reject fallback: %v", err)
	}
	if route3.Kind != RouteRelay {
		c3.Close()
		t.Fatalf("route3 = %+v, want RouteRelay", route3)
	}
	_ = c3.Close()

	// 5. Connection-refused LAN port falls back immediately to relay (before lanHeadStart).
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := closedLn.Addr().String()
	_ = closedLn.Close()
	c4, route4, err := ConnectDirectWithCandidates(ctx, relayURL, []DirectCandidate{{Addr: deadAddr, Pin: lanPin}})
	if err != nil {
		t.Fatalf("ConnectDirectWithCandidates refused fallback: %v", err)
	}
	if route4.Kind != RouteRelay {
		c4.Close()
		t.Fatalf("route4 = %+v, want RouteRelay", route4)
	}
	_ = c4.Close()
}
