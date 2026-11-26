package rexapi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gorex/internal/directrelay"
	"gorex/internal/rex"
)

func base64urlPinToHex(t *testing.T, pin string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(pin)
	if err != nil || len(b) != sha256.Size {
		t.Fatalf("invalid base64url pin %q: %v", pin, err)
	}
	return hex.EncodeToString(b)
}

func TestDirectTunnelEndToEnd(t *testing.T) {
	sock, _ := startServer(t)
	gwClient, err := rex.ConnectSocketKeepalive(sock)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	g, err := New(Options{Client: gwClient, TailnetDir: stateDir, RexSocket: sock})
	if err != nil {
		gwClient.Close()
		t.Fatal(err)
	}
	defer g.Close()

	token := NewLANToken()
	g.direct.apply(DirectConfig{
		Enabled:   true,
		Listen:    "127.0.0.1:0",
		PublicURL: "https://127.0.0.1:7443",
		Token:     token,
	})
	st := LoadDirectStatus(stateDir)
	if st.Listening == "" || !directrelay.ValidPin(st.CertPin) {
		t.Fatalf("unexpected direct status: %+v", st)
	}

	// 1. Direct TLS: connect via rex.ConnectRemote with base64url pin.
	pairURL := "https://" + st.Listening + "/#k=" + token + "&p=" + st.CertPin + "&n=alice-laptop"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rc, err := rex.ConnectRemote(ctx, pairURL)
	if err != nil {
		t.Fatalf("ConnectRemote direct (base64url pin): %v", err)
	}
	hello, err := rc.Hello()
	if err != nil {
		rc.Close()
		t.Fatalf("Hello over direct tunnel: %v", err)
	}
	if hello.Version != rex.ProtocolVersion || hello.Keepalive <= 0 || hello.Conn == "" {
		rc.Close()
		t.Fatalf("unexpected Hello: %+v", hello)
	}

	created, err := rc.Create(rex.CreateOptions{Cols: 80, Rows: 24, Command: shellCommand("DIRECT_TUNNEL_OK")})
	if err != nil {
		rc.Close()
		t.Fatalf("Create over direct tunnel: %v", err)
	}
	list, err := rc.List()
	if err != nil {
		rc.Close()
		t.Fatalf("List over direct tunnel: %v", err)
	}
	found := false
	for _, info := range list {
		if info.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		rc.Close()
		t.Fatalf("created session %s missing from List()", created.ID)
	}
	// Verify stream attach dials a second tunnel connection cleanly.
	streamConn, err := rc.AttachConn(rex.Attach{SID: created.ID, Cols: 80, Rows: 24})
	if err != nil {
		rc.Close()
		t.Fatalf("AttachConn over direct tunnel: %v", err)
	}
	_ = streamConn.Close()
	if err := rc.Kill(created.ID); err != nil {
		rc.Close()
		t.Fatalf("Kill over direct tunnel: %v", err)
	}
	_ = rc.Close()

	// 2. Direct TLS also accepts a 64-hex SHA-256 pin.
	hexPin := base64urlPinToHex(t, st.CertPin)
	hexURL := "https://" + st.Listening + "/#k=" + token + "&p=" + hexPin
	rcHex, err := rex.ConnectRemote(ctx, hexURL)
	if err != nil {
		t.Fatalf("ConnectRemote direct (hex pin): %v", err)
	}
	if _, err := rcHex.Hello(); err != nil {
		rcHex.Close()
		t.Fatalf("Hello (hex pin): %v", err)
	}
	_ = rcHex.Close()

	// 3. Wrong or missing Bearer token returns 401 and ErrDirectAuth.
	pinnedClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: directrelay.TLSConfigWithPin(st.CertPin)},
	}
	t.Cleanup(pinnedClient.CloseIdleConnections)

	resp, err := pinnedClient.Get("https://" + st.Listening + rexTunnelPath)
	if err != nil {
		t.Fatalf("GET missing token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", resp.StatusCode)
	}

	wrongTokenURL := "https://" + st.Listening + "/#k=wrong-secret-token&p=" + st.CertPin
	if _, err := rex.ConnectRemote(ctx, wrongTokenURL); !errors.Is(err, rex.ErrDirectAuth) {
		t.Fatalf("wrong token err = %v, want ErrDirectAuth", err)
	} else if strings.Contains(err.Error(), "wrong-secret-token") {
		t.Fatalf("error leaked secret token: %v", err)
	}

	// 4. Non-GET method with valid token returns 405.
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+st.Listening+rexTunnelPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	postReq.Header.Set("Authorization", "Bearer "+token)
	postResp, err := pinnedClient.Do(postReq)
	if err != nil {
		t.Fatalf("POST rex-tunnel: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", postResp.StatusCode)
	}

	// 5. Wrong TLS pin is strictly rejected.
	wrongPin := strings.Repeat("a", 64)
	wrongPinURL := "https://" + st.Listening + "/#k=" + token + "&p=" + wrongPin
	if _, err := rex.ConnectRemote(ctx, wrongPinURL); !errors.Is(err, rex.ErrDirectPinMismatch) {
		t.Fatalf("wrong pin err = %v, want ErrDirectPinMismatch", err)
	}

	// 6. Stateless relay mode with /h/<host_id> prefix.
	relayDir := t.TempDir()
	relayCert, relayPin, err := directrelay.LoadOrCreateCert(relayDir, "relay", []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("relay cert: %v", err)
	}
	relay := directrelay.NewServer("relay-secret", nil)
	defer relay.Close()
	relaySrv := httptest.NewUnstartedServer(relay.Handler())
	relaySrv.TLS = &tls.Config{Certificates: []tls.Certificate{relayCert}}
	relaySrv.StartTLS()
	defer relaySrv.Close()

	hostID := NewHostID()
	g.direct.apply(DirectConfig{
		Enabled:      true,
		RelayURL:     relaySrv.URL,
		RelaySecret:  "relay-secret",
		RelayCertPin: relayPin,
		HostID:       hostID,
		Token:        token,
	})
	waitFor(t, "relay host online", func() bool { return relay.HostOnline(hostID) })

	relayPairURL := relaySrv.URL + "/h/" + hostID + "/#k=" + token + "&p=" + relayPin + "&n=relay-host"
	rrc, err := rex.ConnectRemote(ctx, relayPairURL)
	if err != nil {
		t.Fatalf("ConnectRemote via relay: %v", err)
	}
	if _, err := rrc.Hello(); err != nil {
		rrc.Close()
		t.Fatalf("Hello via relay: %v", err)
	}
	if _, err := rrc.List(); err != nil {
		rrc.Close()
		t.Fatalf("List via relay: %v", err)
	}

	// Rotating the pairing token closes the active relay tunnel.
	g.direct.apply(DirectConfig{
		Enabled:      true,
		RelayURL:     relaySrv.URL,
		RelaySecret:  "relay-secret",
		RelayCertPin: relayPin,
		HostID:       hostID,
		Token:        NewLANToken(),
	})
	select {
	case <-rrc.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("active tunnel client did not close after token rotation")
	}
	_ = rrc.Close()

	// Old token through relay now gets 401 / ErrDirectAuth.
	waitFor(t, "relay host reconnected after token rotate", func() bool { return relay.HostOnline(hostID) })
	if _, err := rex.ConnectRemote(ctx, relayPairURL); !errors.Is(err, rex.ErrDirectAuth) {
		t.Fatalf("old token after rotate err = %v, want ErrDirectAuth", err)
	}
}

func TestDirectTunnelRefusedOnNonDirectTransports(t *testing.T) {
	f := startFixture(t)

	// Plain TCP (anonymous / --listen) must refuse /api/v1/rex-tunnel with 403.
	resp, err := http.Get(f.srv.URL + rexTunnelPath)
	if err != nil {
		t.Fatalf("GET on tcp handler: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tcp rex-tunnel status = %d, want 403", resp.StatusCode)
	}

	// LAN listener (even with a valid LAN token) must refuse /api/v1/rex-tunnel.
	sock, _ := startServer(t)
	c, err := rex.ConnectSocketKeepalive(sock)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	g, err := New(Options{Client: c, TailnetDir: stateDir, RexSocket: sock})
	if err != nil {
		c.Close()
		t.Fatal(err)
	}
	defer g.Close()

	lanToken := NewLANToken()
	g.lan.apply(LANConfig{
		Enabled: true,
		Listen:  "127.0.0.1:0",
		Allow:   []string{"127.0.0.1/32"},
		Token:   lanToken,
	})
	lst := LoadLANStatus(stateDir)
	if lst.Listening == "" {
		t.Fatalf("LAN listener not active: %+v", lst)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, wsResp, wsErr := websocket.Dial(ctx, "ws://"+lst.Listening+rexTunnelPath, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + lanToken}},
	})
	if wsResp != nil && wsResp.Body != nil {
		wsResp.Body.Close()
	}
	if wsErr == nil || wsResp == nil || wsResp.StatusCode != http.StatusForbidden {
		t.Fatalf("LAN rex-tunnel err=%v resp=%v, want 403", wsErr, wsResp)
	}
}
