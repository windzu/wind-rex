package directrelay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		HostName: "alice-laptop",
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

func newTestTLSRelay(t *testing.T, secret string) (*Server, *httptest.Server, string, *http.Client) {
	t.Helper()
	dir := t.TempDir()
	cert, pin, err := LoadOrCreateCert(dir, "relay", []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("LoadOrCreateCert: %v", err)
	}
	relay := NewServer(secret, nil)
	t.Cleanup(relay.Close)
	srv := httptest.NewUnstartedServer(relay.Handler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	tr := &http.Transport{TLSClientConfig: TLSConfigWithPin(pin)}
	t.Cleanup(tr.CloseIdleConnections)
	return relay, srv, pin, &http.Client{Transport: tr}
}

func waitCondition(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", desc)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startTestHost(t *testing.T, relayURL, hostID, hostName, secret, pin string) *TunnelListener {
	t.Helper()
	connected := make(chan struct{}, 1)
	ln, err := StartTunnel(ClientConfig{
		RelayURL: relayURL,
		HostID:   hostID,
		HostName: hostName,
		Secret:   secret,
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
		t.Fatalf("StartTunnel(%s): %v", hostID, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatalf("host %s did not connect", hostID)
	}
	return ln
}

func TestSanitizeHostNameAndStripLocal(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"alice-laptop", "alice-laptop"},
		{"  alice\t\nlaptop  ", "alice laptop"},
		{"dev\x00\x1bbox", "devbox"},
		{string([]byte{'a', 0xff, 'b'}), "ab"},
		{"   \x00\x07\t\n  ", ""},
		{strings.Repeat("x", 100), strings.Repeat("x", maxHostNameRunes)},
		{strings.Repeat("a", maxHostNameRunes-1) + " z", strings.Repeat("a", maxHostNameRunes-1)},
		{"开发机-01", "开发机-01"},
	}
	for _, tc := range cases {
		if got := SanitizeHostName(tc.in); got != tc.want {
			t.Errorf("SanitizeHostName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	localCases := []struct {
		in   string
		want string
	}{
		{"alice-laptop.local", "alice-laptop"},
		{"alice-laptop.LOCAL", "alice-laptop"},
		{".local", ".local"},
		{"box.example.com", "box.example.com"},
	}
	for _, tc := range localCases {
		if got := stripLocalSuffix(tc.in); got != tc.want {
			t.Errorf("stripLocalSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDiscoveryDisabledWithoutSecret(t *testing.T) {
	relay, srv, pin, client := newTestTLSRelay(t, "")
	startTestHost(t, srv.URL, "host_open_000001", "alice-laptop", "", pin)
	waitCondition(t, "host online", func() bool { return relay.HostOnline("host_open_000001") })

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+"/api/hosts", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer any-token")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s /api/hosts: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s /api/hosts status = %d, want 404", method, resp.StatusCode)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := DiscoverHosts(ctx, srv.URL, "", pin)
	if !errors.Is(err, ErrDiscoveryDisabled) {
		t.Fatalf("DiscoverHosts err = %v, want ErrDiscoveryDisabled", err)
	}
}

func TestDiscoveryAuthAndMethods(t *testing.T) {
	const secret = "relay-shared-token"
	_, srv, _, client := newTestTLSRelay(t, secret)

	// Missing, wrong, and non-Bearer Authorization headers all fail with 401.
	badAuths := []string{"", "Bearer wrong-token", "Basic dXNlcjpwYXNz", "Bearer "}
	for _, auth := range badAuths {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/hosts", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /api/hosts (%q): %v", auth, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET /api/hosts (%q) status = %d, want 401", auth, resp.StatusCode)
		}
		if got := resp.Header.Get("WWW-Authenticate"); got != "Bearer" {
			t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
		}
	}

	// Non-GET methods return 405 with Allow: GET.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+"/api/hosts", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s /api/hosts: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s /api/hosts status = %d, want 405", method, resp.StatusCode)
		}
		if got := resp.Header.Get("Allow"); got != http.MethodGet {
			t.Fatalf("Allow = %q, want GET", got)
		}
	}

	// Authorized GET with zero online hosts returns {"hosts":[]} and Cache-Control: no-store.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/hosts", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/hosts: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if strings.TrimSpace(string(body)) != `{"hosts":[]}` {
		t.Fatalf("empty body = %q, want {\"hosts\":[]}", strings.TrimSpace(string(body)))
	}
}

func TestDiscoveryLifecycleAndSorting(t *testing.T) {
	const secret = "relay-shared-token"
	relay, srv, pin, client := newTestTLSRelay(t, secret)

	var clockMu sync.Mutex
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	relay.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}

	lnZeta := startTestHost(t, srv.URL, "host_zeta_0001", "zebra-box", secret, pin)
	lnBeta := startTestHost(t, srv.URL, "host_beta_0002", "alice-laptop", secret, pin)
	_ = startTestHost(t, srv.URL, "host_alpha_0003", "alice-laptop", secret, pin)

	waitCondition(t, "three hosts online", func() bool {
		return len(relay.Hosts()) == 3
	})

	// Check raw JSON shape, RFC3339 timestamp, deterministic sorting, and absence of secrets.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/hosts", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/hosts: %v", err)
	}
	rawBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(rawBody), secret) {
		t.Fatalf("response leaked relay secret: %s", rawBody)
	}
	var rawDecoded struct {
		Hosts []map[string]any `json:"hosts"`
	}
	if err := json.Unmarshal(rawBody, &rawDecoded); err != nil {
		t.Fatalf("decode raw JSON: %v", err)
	}
	if len(rawDecoded.Hosts) != 3 {
		t.Fatalf("got %d hosts, want 3", len(rawDecoded.Hosts))
	}
	for _, item := range rawDecoded.Hosts {
		if len(item) != 4 {
			t.Fatalf("unexpected fields in host entry: %v", item)
		}
		ts, ok := item["connected_at"].(string)
		if !ok {
			t.Fatalf("connected_at not string: %T", item["connected_at"])
		}
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Fatalf("connected_at %q is not RFC3339: %v", ts, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hosts, err := DiscoverHosts(ctx, srv.URL, secret, pin)
	if err != nil {
		t.Fatalf("DiscoverHosts: %v", err)
	}
	wantOrder := []struct {
		id   string
		name string
		path string
	}{
		{"host_alpha_0003", "alice-laptop", "/h/host_alpha_0003"},
		{"host_beta_0002", "alice-laptop", "/h/host_beta_0002"},
		{"host_zeta_0001", "zebra-box", "/h/host_zeta_0001"},
	}
	if len(hosts) != len(wantOrder) {
		t.Fatalf("len(hosts) = %d, want %d", len(hosts), len(wantOrder))
	}
	for i, w := range wantOrder {
		if hosts[i].ID != w.id || hosts[i].Name != w.name || hosts[i].Path != w.path {
			t.Fatalf("hosts[%d] = %+v, want %+v", i, hosts[i], w)
		}
		if !hosts[i].ConnectedAt.Equal(now) {
			t.Fatalf("hosts[%d].ConnectedAt = %v, want %v", i, hosts[i].ConnectedAt, now)
		}
	}

	// Disconnect one host and verify immediate removal from /api/hosts.
	_ = lnBeta.Close()
	waitCondition(t, "host_beta_0002 removed", func() bool {
		return !relay.HostOnline("host_beta_0002")
	})
	hosts, err = DiscoverHosts(ctx, srv.URL, secret, pin)
	if err != nil {
		t.Fatalf("DiscoverHosts after disconnect: %v", err)
	}
	if len(hosts) != 2 || hosts[0].ID != "host_alpha_0003" || hosts[1].ID != "host_zeta_0001" {
		t.Fatalf("hosts after disconnect = %+v", hosts)
	}

	// Re-register host_zeta_0001 with updated display name and timestamp.
	// Close old TunnelListener's reconnect loop first only after replacement so
	// we also exercise stale-mux replacement on the server without races.
	later := now.Add(2 * time.Minute)
	clockMu.Lock()
	now = later
	clockMu.Unlock()

	wsURL, err := RelayRegisterURLWithName(srv.URL, "host_zeta_0001", "build-server")
	if err != nil {
		t.Fatalf("RelayRegisterURLWithName: %v", err)
	}
	ws, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols:    []string{RelaySubprotocol},
		HTTPHeader:      http.Header{"Authorization": []string{"Bearer " + secret}},
		HTTPClient:      client,
		CompressionMode: websocket.CompressionDisabled,
	})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("re-register dial: %v", err)
	}
	defer ws.CloseNow()

	// Stop the old TunnelListener so it does not reconnect and overwrite again.
	_ = lnZeta.Close()

	waitCondition(t, "host_zeta_0001 metadata updated", func() bool {
		for _, h := range relay.Hosts() {
			if h.ID == "host_zeta_0001" && h.Name == "build-server" && h.ConnectedAt.Equal(later) {
				return true
			}
		}
		return false
	})

	hosts, err = DiscoverHosts(ctx, srv.URL, secret, pin)
	if err != nil {
		t.Fatalf("DiscoverHosts after re-register: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("len(hosts) = %d, want 2", len(hosts))
	}
	if hosts[0].ID != "host_alpha_0003" || hosts[1].ID != "host_zeta_0001" || hosts[1].Name != "build-server" {
		t.Fatalf("hosts after re-register = %+v", hosts)
	}
	if !hosts[1].ConnectedAt.Equal(later) {
		t.Fatalf("ConnectedAt = %v, want %v", hosts[1].ConnectedAt, later)
	}
}

func TestDiscoveryBackwardCompatAndHeader(t *testing.T) {
	const secret = "relay-shared-token"
	relay, srv, pin, client := newTestTLSRelay(t, secret)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dialRaw := func(rawPath string, headers http.Header) *websocket.Conn {
		t.Helper()
		wsURL := "wss" + strings.TrimPrefix(srv.URL, "https") + rawPath
		h := headers.Clone()
		if h == nil {
			h = http.Header{}
		}
		h.Set("Authorization", "Bearer "+secret)
		ws, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			Subprotocols:    []string{RelaySubprotocol},
			HTTPHeader:      h,
			HTTPClient:      client,
			CompressionMode: websocket.CompressionDisabled,
		})
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if err != nil {
			t.Fatalf("dial %s: %v", rawPath, err)
		}
		t.Cleanup(func() { _ = ws.CloseNow() })
		return ws
	}

	// 1. Legacy host sending neither query name nor header falls back to host_id.
	dialRaw("/relay/host?id=legacy_host_01", nil)
	// 2. Host sending display name via X-WindRex-Host-Name header.
	dialRaw("/relay/host?id=header_host_02", http.Header{HostNameHeader: []string{"  dev\tbox  "}})
	// 3. Host sending control-only name query falls back to host_id.
	dialRaw("/relay/host?id=ctrl_host_0003&name=%00%1b", nil)
	// 4. TunnelListener with empty HostName defaults to sanitized os.Hostname() (or host_id).
	startTestHost(t, srv.URL, "default_host_04", "", secret, pin)

	waitCondition(t, "four hosts online", func() bool { return len(relay.Hosts()) == 4 })

	hosts, err := DiscoverHosts(ctx, srv.URL, secret, pin)
	if err != nil {
		t.Fatalf("DiscoverHosts: %v", err)
	}
	byID := map[string]HostInfo{}
	for _, h := range hosts {
		byID[h.ID] = h
	}
	if got := byID["legacy_host_01"].Name; got != "legacy_host_01" {
		t.Fatalf("legacy name = %q, want legacy_host_01", got)
	}
	if got := byID["header_host_02"].Name; got != "dev box" {
		t.Fatalf("header name = %q, want %q", got, "dev box")
	}
	if got := byID["ctrl_host_0003"].Name; got != "ctrl_host_0003" {
		t.Fatalf("ctrl fallback name = %q, want ctrl_host_0003", got)
	}
	wantDefault := defaultHostName()
	if wantDefault == "" {
		wantDefault = "default_host_04"
	}
	if got := byID["default_host_04"].Name; got != wantDefault {
		t.Fatalf("default HostName = %q, want %q", got, wantDefault)
	}
}

func TestDiscoverHostsClientHelper(t *testing.T) {
	const secret = "relay-shared-token"
	relay, srv, pin, _ := newTestTLSRelay(t, secret)
	startTestHost(t, srv.URL, "host_client_01", "alice-laptop", secret, pin)
	waitCondition(t, "host_client_01 online", func() bool { return relay.HostOnline("host_client_01") })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Valid pin + valid token succeeds.
	hosts, err := DiscoverHosts(ctx, srv.URL, secret, pin)
	if err != nil {
		t.Fatalf("DiscoverHosts valid: %v", err)
	}
	if len(hosts) != 1 || hosts[0].ID != "host_client_01" || hosts[0].Name != "alice-laptop" {
		t.Fatalf("DiscoverHosts got %+v", hosts)
	}

	// Wrong pin is rejected.
	wrongPin := CertPin([]byte("different-cert"))
	if _, err := DiscoverHosts(ctx, srv.URL, secret, wrongPin); err == nil {
		t.Fatal("expected pin mismatch error")
	}

	// Invalid pin syntax is rejected.
	if _, err := DiscoverHosts(ctx, srv.URL, secret, "not-a-valid-pin"); err == nil {
		t.Fatal("expected invalid pin error")
	}

	// Wrong token returns ErrUnauthorized.
	if _, err := DiscoverHosts(ctx, srv.URL, "wrong-token", pin); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token err = %v, want ErrUnauthorized", err)
	}

	// Empty token returns ErrUnauthorized.
	if _, err := DiscoverHosts(ctx, srv.URL, "", pin); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("empty token err = %v, want ErrUnauthorized", err)
	}

	// RelayHostsURL preserves base path prefix and normalizes scheme.
	u, err := RelayHostsURL("wss://relay.example.com:7443/prefix/")
	if err != nil {
		t.Fatalf("RelayHostsURL: %v", err)
	}
	if u != "https://relay.example.com:7443/prefix/api/hosts" {
		t.Fatalf("RelayHostsURL = %q", u)
	}
}
