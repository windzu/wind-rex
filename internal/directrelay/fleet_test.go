package directrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDeriveFleetTokenPinnedVector(t *testing.T) {
	// Pinned cross-platform test vector (docs/NET-010-relay-fleet-discovery.md),
	// shared byte-for-byte with Swift GoRexTests.
	const (
		secret = "test-relay-secret"
		hostID = "host-abc-123"
		want   = "3tFr5S1FzWIelzutS1758Y4jbi00QWxKIIQuCYVYtik"
	)
	got := DeriveFleetToken(secret, hostID)
	if len(got) != 43 {
		t.Fatalf("len(DeriveFleetToken) = %d, want 43 (%q)", len(got), got)
	}
	if got != want {
		t.Fatalf("DeriveFleetToken(%q, %q) = %q, want %q", secret, hostID, got, want)
	}
	if strings.ContainsAny(got, "+/=") {
		t.Fatalf("token %q is not unpadded base64url", got)
	}

	// Empty inputs return empty string.
	if DeriveFleetToken("", hostID) != "" {
		t.Fatal("empty secret must return empty token")
	}
	if DeriveFleetToken(secret, "") != "" {
		t.Fatal("empty hostID must return empty token")
	}

	// Distinct host IDs or secrets derive distinct tokens.
	if DeriveFleetToken(secret, "host-xyz-999") == got {
		t.Fatal("different hostIDs derived identical token")
	}
	if DeriveFleetToken("other-relay-secret", hostID) == got {
		t.Fatal("different secrets derived identical token")
	}
}

func TestFleetURLFormatAndParse(t *testing.T) {
	sum := sha256.Sum256([]byte("relay-leaf-cert-der"))
	b64Pin := CertPin([]byte("relay-leaf-cert-der"))
	hexPin := hex.EncodeToString(sum[:])

	formatted, err := FormatFleetURL("wss://relay.example.com:7443/", "sec_123", b64Pin, "Home Relay")
	if err != nil {
		t.Fatalf("FormatFleetURL: %v", err)
	}
	if !strings.HasPrefix(formatted, "https://relay.example.com:7443/#r=sec_123&p="+b64Pin+"&n=Home+Relay") {
		t.Fatalf("unexpected FormatFleetURL = %q", formatted)
	}
	if !IsFleetURL(formatted) {
		t.Fatalf("IsFleetURL(%q) = false", formatted)
	}

	parsed, err := ParseFleetURL(formatted)
	if err != nil {
		t.Fatalf("ParseFleetURL: %v", err)
	}
	if parsed.RelayURL != "https://relay.example.com:7443" || parsed.Host != "relay.example.com:7443" ||
		parsed.Hostname != "relay.example.com" || parsed.Secret != "sec_123" ||
		parsed.Pin != b64Pin || parsed.Label != "Home Relay" || parsed.DefaultLabel() != "Home Relay" {
		t.Fatalf("parsed = %+v", parsed)
	}

	// Hex pin in #p= is accepted and normalized to 43-char base64url.
	hexParsed, err := ParseFleetURL("https://relay.example.com:7443/#r=sec_hex&p=" + strings.ToUpper(hexPin))
	if err != nil {
		t.Fatalf("ParseFleetURL(hex): %v", err)
	}
	if hexParsed.Pin != b64Pin || hexParsed.DefaultLabel() != "relay.example.com" {
		t.Fatalf("hexParsed = %+v, want pin %q", hexParsed, b64Pin)
	}

	// SanitizeFleetURL never exposes the secret.
	san := SanitizeFleetURL(formatted)
	if san != "https://relay.example.com:7443" || strings.Contains(san, "sec_123") {
		t.Fatalf("SanitizeFleetURL = %q", san)
	}

	// FleetHostPairURL derives the per-host token and formats a #k= single-host URL.
	hostURL, err := FleetHostPairURL("https://relay.example.com:7443", "host-abc-123", "test-relay-secret", b64Pin, "gpu-node-1")
	if err != nil {
		t.Fatalf("FleetHostPairURL: %v", err)
	}
	if !strings.HasPrefix(hostURL, "https://relay.example.com:7443/h/host-abc-123/#") {
		t.Fatalf("unexpected FleetHostPairURL = %q", hostURL)
	}
	if !strings.Contains(hostURL, "k=3tFr5S1FzWIelzutS1758Y4jbi00QWxKIIQuCYVYtik") || !strings.Contains(hostURL, "p="+b64Pin) || !strings.Contains(hostURL, "n=gpu-node-1") {
		t.Fatalf("FleetHostPairURL missing params: %q", hostURL)
	}
	if strings.Contains(hostURL, "test-relay-secret") {
		t.Fatalf("FleetHostPairURL leaked relay secret: %q", hostURL)
	}

	// Invalid inputs are rejected cleanly.
	invalid := []struct {
		raw     string
		wantErr error
	}{
		{"http://relay.example.com:7443/#r=sec", nil},
		{"https://relay.example.com:7443/", nil},
		{"https://relay.example.com:7443/#k=single-host-token", nil},
		{"https://relay.example.com:7443/#r=sec&k=also-k", nil},
		{"https://user:pass@relay.example.com:7443/#r=sec", nil},
		{"https://relay.example.com:7443/#r=sec&p=badpin", ErrInvalidFleetPin},
	}
	for _, tc := range invalid {
		if _, err := ParseFleetURL(tc.raw); err == nil {
			t.Errorf("ParseFleetURL(%q) succeeded, want error", tc.raw)
		} else if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
			t.Errorf("ParseFleetURL(%q) err = %v, want %v", tc.raw, err, tc.wantErr)
		}
	}
	if IsFleetURL("https://relay.example.com:7443/h/host1/#k=single-host-token") {
		t.Fatal("single-host #k= URL must not be treated as a Fleet URL")
	}
}

func TestDiscoveryFleetAuthMetadata(t *testing.T) {
	const secret = "fleet-relay-secret"
	relay, srv, pin, client := newTestTLSRelay(t, secret)

	// Host 1: opted into fleet_auth=1.
	connected1 := make(chan struct{}, 1)
	ln1, err := StartTunnel(ClientConfig{
		RelayURL:  srv.URL,
		HostID:    "host_fleet_001",
		HostName:  "gpu-node-1",
		Secret:    secret,
		CertPin:   pin,
		FleetAuth: true,
		OnStatus: func(ok bool, _ error) {
			if ok {
				select {
				case connected1 <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("StartTunnel fleet host: %v", err)
	}
	t.Cleanup(func() { _ = ln1.Close() })
	select {
	case <-connected1:
	case <-time.After(5 * time.Second):
		t.Fatal("fleet host did not connect")
	}

	// Host 2: fleet_auth omitted (false).
	_ = startTestHost(t, srv.URL, "host_manual_02", "dev-box-1", secret, pin)

	waitCondition(t, "two hosts online", func() bool { return len(relay.Hosts()) == 2 })

	// Raw JSON includes "fleet_auth": true only for opted-in host and omits it otherwise.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/hosts", nil)
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
	var raw struct {
		Hosts []map[string]any `json:"hosts"`
	}
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(raw.Hosts) != 2 {
		t.Fatalf("got %d hosts, want 2", len(raw.Hosts))
	}
	for _, h := range raw.Hosts {
		switch h["id"] {
		case "host_fleet_001":
			if v, ok := h["fleet_auth"].(bool); !ok || !v {
				t.Fatalf("host_fleet_001 fleet_auth = %v, want true", h["fleet_auth"])
			}
		case "host_manual_02":
			if _, present := h["fleet_auth"]; present {
				t.Fatalf("host_manual_02 should omit fleet_auth: %v", h)
			}
		}
	}

	// DiscoverHosts parses FleetAuth accurately.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := DiscoverHosts(ctx, srv.URL, secret, pin)
	if err != nil {
		t.Fatalf("DiscoverHosts: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	// Sorted by Name: dev-box-1, then gpu-node-1.
	if list[0].ID != "host_manual_02" || list[0].FleetAuth {
		t.Fatalf("list[0] = %+v, want dev-box-1 with FleetAuth=false", list[0])
	}
	if list[1].ID != "host_fleet_001" || !list[1].FleetAuth {
		t.Fatalf("list[1] = %+v, want gpu-node-1 with FleetAuth=true", list[1])
	}
}
