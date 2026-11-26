package rexapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gorex/internal/directrelay"
	"gorex/internal/rex"
)

func TestDirectPairURL(t *testing.T) {
	c := DirectConfig{
		Enabled:   true,
		PublicURL: "https://203.0.113.10:7443",
		Token:     "tok123",
	}
	st := DirectStatus{Running: true, CertPin: "pinABC", SelfSigned: true}
	got := c.PairURL(st, "alice-laptop")
	if !strings.HasPrefix(got, "https://203.0.113.10:7443/#") {
		t.Fatalf("unexpected direct pair URL: %s", got)
	}
	if !strings.Contains(got, "k=tok123") || !strings.Contains(got, "p=pinABC") || !strings.Contains(got, "n=alice-laptop") {
		t.Fatalf("missing params in %s", got)
	}

	// CA-signed direct cert omits pin.
	stCA := DirectStatus{Running: true, CertPin: "pinABC", SelfSigned: false}
	gotCA := c.PairURL(stCA, "alice-laptop")
	if strings.Contains(gotCA, "p=") {
		t.Fatalf("CA-signed URL should omit pin: %s", gotCA)
	}

	// Relay URL uses /h/<host_id> and relay cert pin.
	cr := DirectConfig{
		Enabled:      true,
		RelayURL:     "https://relay.example.com:7443",
		HostID:       "host_12345678",
		RelayCertPin: "relayPinXYZ",
		Token:        "tok999",
	}
	gotRelay := cr.PairURL(DirectStatus{Running: true, RelayConnected: true}, "alice-laptop")
	if !strings.HasPrefix(gotRelay, "https://relay.example.com:7443/h/host_12345678/#") {
		t.Fatalf("unexpected relay pair URL: %s", gotRelay)
	}
	if !strings.Contains(gotRelay, "k=tok999") || !strings.Contains(gotRelay, "p=relayPinXYZ") {
		t.Fatalf("missing relay params in %s", gotRelay)
	}

	// Custom HostName overrides passed fallback in PairURL.
	cr.HostName = "gpu-node-1"
	gotNamed := cr.PairURL(DirectStatus{Running: true, RelayConnected: true}, "alice-laptop")
	if !strings.Contains(gotNamed, "n=gpu-node-1") || strings.Contains(gotNamed, "alice-laptop") {
		t.Fatalf("PairURL did not prefer custom HostName: %s", gotNamed)
	}

	// FleetPairURL requires Enabled + RelayTrust + RelayURL + RelaySecret.
	if cr.FleetPairURL("") != "" {
		t.Fatalf("FleetPairURL without RelayTrust/RelaySecret should be empty")
	}
	cr.RelaySecret = "test-relay-secret"
	cr.RelayCertPin = directrelay.CertPin([]byte("relay-cert"))
	if cr.FleetPairURL("") != "" {
		t.Fatalf("FleetPairURL with RelayTrust=false should be empty")
	}
	cr.RelayTrust = true
	gotFleet := cr.FleetPairURL("Home")
	if !strings.HasPrefix(gotFleet, "https://relay.example.com:7443/#r=test-relay-secret&p="+cr.RelayCertPin+"&n=Home") {
		t.Fatalf("unexpected FleetPairURL: %s", gotFleet)
	}
}

func TestDirectCLIFleetFlagsAndQR(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	validPin := directrelay.CertPin([]byte("cli-relay-cert"))

	var out, errOut bytes.Buffer
	code := directMainIO([]string{
		"relay",
		"--url", "https://relay.example.com:7443",
		"--pin", validPin,
		"--secret", "test-relay-secret",
		"--name", "gpu-node-1",
		"--trust-relay",
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("direct relay exit = %d, stderr=%q", code, errOut.String())
	}
	conf, err := LoadDirect(StateDir())
	if err != nil {
		t.Fatalf("LoadDirect: %v", err)
	}
	if conf.HostName != "gpu-node-1" || !conf.RelayTrust || conf.RelaySecret != "test-relay-secret" {
		t.Fatalf("saved direct config = %+v", conf)
	}
	if !strings.Contains(out.String(), "name: gpu-node-1") || !strings.Contains(out.String(), "relay fleet auth: enabled") {
		t.Fatalf("unexpected direct relay output: %q", out.String())
	}

	// direct fleet-qr outputs #r= URL.
	out.Reset()
	errOut.Reset()
	if code := directMainIO([]string{"fleet-qr", "--label", "Lab"}, &out, &errOut); code != 0 {
		t.Fatalf("direct fleet-qr exit = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "https://relay.example.com:7443/#r=test-relay-secret&p="+validPin+"&n=Lab") {
		t.Fatalf("fleet-qr output missing #r= URL: %q", out.String())
	}

	// direct qr --fleet outputs #r= URL too.
	out.Reset()
	errOut.Reset()
	if code := directMainIO([]string{"qr", "--fleet"}, &out, &errOut); code != 0 {
		t.Fatalf("direct qr --fleet exit = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "https://relay.example.com:7443/#r=test-relay-secret&p="+validPin) {
		t.Fatalf("qr --fleet output missing #r= URL: %q", out.String())
	}

	// Disabling --trust-relay makes fleet-qr fail cleanly.
	out.Reset()
	errOut.Reset()
	if code := directMainIO([]string{"relay", "--url", "https://relay.example.com:7443", "--secret", "test-relay-secret", "--trust-relay=false"}, &out, &errOut); code != 0 {
		t.Fatalf("disable trust-relay exit = %d, stderr=%q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := directMainIO([]string{"fleet-qr"}, &out, &errOut); code == 0 {
		t.Fatalf("fleet-qr should fail when trust-relay is false; stdout=%q", out.String())
	}
}

func TestDirectTLSAndRelayGateway(t *testing.T) {
	sock, _ := startServer(t)
	c, err := rex.ConnectSocketKeepalive(sock)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	g, err := New(Options{Client: c, StateDir: stateDir})
	if err != nil {
		c.Close()
		t.Fatal(err)
	}
	defer g.Close()

	// 1. Direct TLS listener with auto-generated self-signed cert + Bearer token.
	token := NewLANToken()
	g.direct.apply(DirectConfig{
		Enabled:   true,
		Listen:    "127.0.0.1:0",
		PublicURL: "https://127.0.0.1:7443",
		Token:     token,
	})
	st := LoadDirectStatus(stateDir)
	if st.Listening == "" || !directrelay.ValidPin(st.CertPin) || !st.SelfSigned {
		t.Fatalf("unexpected direct status: %+v", st)
	}

	pinnedClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: directrelay.TLSConfigWithPin(st.CertPin)},
	}

	// Unauthenticated request gets 403.
	resp, err := pinnedClient.Get("https://" + st.Listening + "/api/help")
	if err != nil {
		t.Fatalf("GET unauthenticated: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated status = %d, want 403", resp.StatusCode)
	}

	// Authenticated WebSocket control handshake succeeds and advertises gorex.ordered_write.
	wsURL := "wss://" + st.Listening + "/api/control"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols:    []string{controlProtocol},
		HTTPHeader:      http.Header{"Authorization": []string{"Bearer " + token}},
		HTTPClient:      pinnedClient,
		CompressionMode: websocket.CompressionDisabled,
	})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("direct WS dial: %v", err)
	}
	features := assertControlHello(t, ctx, ws)
	if !slices.Contains(features, featureOrderedWrite) {
		t.Fatalf("welcome features %v missing %s", features, featureOrderedWrite)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")

	// 2. Stateless relay mode through directrelay.Server.
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if relay.HostOnline(hostID) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !relay.HostOnline(hostID) {
		t.Fatal("gateway did not connect to relay")
	}

	relayClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: directrelay.TLSConfigWithPin(relayPin)},
	}
	relayWSURL := "wss" + strings.TrimPrefix(relaySrv.URL, "https") + "/h/" + hostID + "/api/control"

	// Wrong token through relay is rejected by the host with 403.
	if _, rresp, err := websocket.Dial(ctx, relayWSURL, &websocket.DialOptions{
		Subprotocols:    []string{controlProtocol},
		HTTPHeader:      http.Header{"Authorization": []string{"Bearer wrong-token"}},
		HTTPClient:      relayClient,
		CompressionMode: websocket.CompressionDisabled,
	}); err == nil || (rresp != nil && rresp.StatusCode != http.StatusForbidden) {
		if rresp != nil && rresp.Body != nil {
			rresp.Body.Close()
		}
		t.Fatalf("expected 403 with wrong token, err=%v resp=%v", err, rresp)
	} else if rresp != nil && rresp.Body != nil {
		rresp.Body.Close()
	}

	// Valid token through relay completes full Rex control handshake.
	rws, rresp, err := websocket.Dial(ctx, relayWSURL, &websocket.DialOptions{
		Subprotocols:    []string{controlProtocol},
		HTTPHeader:      http.Header{"Authorization": []string{"Bearer " + token}},
		HTTPClient:      relayClient,
		CompressionMode: websocket.CompressionDisabled,
	})
	if rresp != nil && rresp.Body != nil {
		rresp.Body.Close()
	}
	if err != nil {
		t.Fatalf("relay WS dial: %v", err)
	}
	assertControlHello(t, ctx, rws)
	_ = rws.Close(websocket.StatusNormalClosure, "")
}

func assertControlHello(t *testing.T, ctx context.Context, ws *websocket.Conn) []string {
	t.Helper()
	hello := `{"type":"hello","api_revision":12,"api_min_revision":10}`
	if err := ws.Write(ctx, websocket.MessageText, []byte(hello)); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	var wel struct {
		Type     string   `json:"type"`
		Protocol string   `json:"protocol"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(data, &wel); err != nil {
		t.Fatalf("unmarshal welcome: %v", err)
	}
	if wel.Type != "welcome" || wel.Protocol != controlProtocol {
		t.Fatalf("unexpected welcome: %+v", wel)
	}
	return wel.Features
}
