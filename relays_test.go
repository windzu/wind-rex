package main

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"gorex/internal/directrelay"
	"gorex/internal/hosts"
	"gorex/internal/rex"
	"gorex/locales"
)

// TestRelayFleetPasteDiscoverAndImport verifies the full Desktop Settings ›
// Remote Hosts Relay Fleet workflow (docs/NET-010-relay-fleet-discovery.md):
//   - Pasting a #r= Fleet URL in Add Remote Host… detects it without leaking the secret.
//   - Submitting queries DiscoverHosts and persists the relay to relays.json (0600).
//   - Local host_id is marked "This machine" and excluded from import.
//   - Hosts with fleet_auth=false show "Single-host QR only".
//   - 1-click "Add All" imports all fleet_auth remote hosts with derived HMAC tokens.
func TestRelayFleetPasteDiscoverAndImport(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	dir := t.TempDir()
	t.Setenv("GOREX_DIR", dir)
	t.Cleanup(func() {
		closeAddHost()
		resetRelaysUI()
		stopRemotePoll()
	})

	const (
		relayURL    = "https://relay.example.com:7443"
		relaySecret = "test-relay-secret"
		localID     = "host-local-001"
		devBoxID    = "host-abc-123"
		gpuNodeID   = "host-gpu-456"
		lockedID    = "host-locked-99"
	)
	pin := directrelay.CertPin([]byte("test-relay-cert"))

	oldDiscover := discoverRelayHosts
	oldLocalID := localDirectHostID
	oldDial := dialHost
	t.Cleanup(func() {
		discoverRelayHosts = oldDiscover
		localDirectHostID = oldLocalID
		dialHost = oldDial
	})
	localDirectHostID = func() string { return localID }
	dialHost = func(ctx context.Context, addr string) (*rex.Client, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	var gotURL, gotSecret, gotPin string
	discoverRelayHosts = func(_ context.Context, rURL, rSecret, rPin string) ([]directrelay.HostInfo, error) {
		gotURL, gotSecret, gotPin = rURL, rSecret, rPin
		return []directrelay.HostInfo{
			{ID: localID, Name: "alice-laptop", Path: "/h/" + localID, FleetAuth: true},
			{ID: devBoxID, Name: "dev-box-1", Path: "/h/" + devBoxID, FleetAuth: true},
			{ID: gpuNodeID, Name: "gpu-node-1", Path: "/h/" + gpuNodeID, FleetAuth: true},
			{ID: lockedID, Name: "secure-box", Path: "/h/" + lockedID, FleetAuth: false},
		}, nil
	}

	a := &App{}
	t.Cleanup(a.stopHosts)
	settingsUI.section = "hosts"

	tt := ui.NewTester(a.settingsView, 780, 1400)
	tt.Frame()

	// Open Add Remote Host… and paste a #r= Fleet URL.
	if err := tt.Click(locales.HostsAdd()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	fleetURL, err := directrelay.FormatFleetURL(relayURL, relaySecret, pin, "Lab Relay")
	if err != nil {
		t.Fatal(err)
	}
	hostsUI.address = fleetURL
	hostsUI.name = "Lab Relay"
	tt.Frame()

	// Detected hint shows sanitized relay URL without leaking the secret.
	if !tt.HasText(locales.HostsFleetDetected(relayURL)) {
		t.Fatalf("missing fleet detected hint in %q", tt.Texts())
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, relaySecret) {
			t.Fatalf("relay secret leaked on screen: %q", s)
		}
	}

	// Submit: verifies via DiscoverHosts and saves to relays.json (0600).
	a.submitHost()
	waitPosted := func(cond func() bool) {
		t.Helper()
		for range 100 {
			tt.Frame()
			if cond() {
				return
			}
		}
		t.Fatalf("condition not reached; texts=%q", tt.Texts())
	}
	waitPosted(func() bool { return !hostsUI.adding && len(relaysUI.relays) == 1 })

	if gotURL != relayURL || gotSecret != relaySecret || gotPin != pin {
		t.Fatalf("DiscoverHosts called with (%q, %q, %q)", gotURL, gotSecret, gotPin)
	}
	if fi, err := os.Stat(hosts.RelaysPath(dir)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("relays.json mode = %v, %v; want 0600", fi, err)
	}
	savedRelays, err := hosts.LoadRelays(dir)
	if err != nil || len(savedRelays) != 1 || savedRelays[0].URL != relayURL || savedRelays[0].Label != "Lab Relay" {
		t.Fatalf("saved relays = %+v, err=%v", savedRelays, err)
	}

	// Relay Fleets group displays all 4 hosts with accurate badges and Add All (2).
	tt.Frame()
	for _, want := range []string{
		locales.HostsFleetTitle(),
		"Lab Relay",
		locales.HostsFleetOnlineCount(4),
		locales.HostsFleetAddAll(2),
		"alice-laptop",
		locales.HostsFleetThisMachine(),
		"dev-box-1",
		"gpu-node-1",
		"secure-box",
		locales.HostsFleetNeedsQR(),
	} {
		if !tt.HasText(want) {
			t.Errorf("Relay Fleets UI missing %q in %q", want, tt.Texts())
		}
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, relaySecret) {
			t.Errorf("relay secret leaked in Relay Fleets UI: %q", s)
		}
	}

	// Click Add All (2): imports dev-box-1 and gpu-node-1 with derived HMAC tokens,
	// excludes local alice-laptop and non-fleet secure-box.
	if err := tt.Click(locales.HostsFleetAddAll(2)); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if len(a.hosts) != 2 {
		t.Fatalf("imported %d hosts, want 2: %+v", len(a.hosts), a.hosts)
	}
	if a.hosts[0].Name != "dev-box-1" || a.hosts[0].RelayHostID != devBoxID {
		t.Fatalf("host[0] = %+v", a.hosts[0].savedHost)
	}
	// Verify pinned derived token for test-relay-secret + host-abc-123!
	parsed0, err := rex.ParseDirectTarget(a.hosts[0].Address)
	if err != nil {
		t.Fatalf("ParseDirectTarget(host[0]): %v", err)
	}
	if parsed0.Token != "3tFr5S1FzWIelzutS1758Y4jbi00QWxKIIQuCYVYtik" || parsed0.Pin != pin || !parsed0.IsRelay {
		t.Fatalf("parsed host[0] = %+v", parsed0)
	}
	if a.hosts[1].Name != "gpu-node-1" || a.hosts[1].RelayHostID != gpuNodeID {
		t.Fatalf("host[1] = %+v", a.hosts[1].savedHost)
	}

	// After import, Add All disappears and imported hosts show "Added".
	if tt.HasText(locales.HostsFleetAddAll(2)) || !tt.HasText(locales.HostsFleetAdded()) {
		t.Errorf("post-import UI state unexpected; texts %q", tt.Texts())
	}
}
