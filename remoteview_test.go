package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"gorex/internal/rexapi"
	"gorex/locales"
)

// wantMessage checks got against want: "" for no message, else a part of it.
func wantMessage(t *testing.T, what, got, want string) {
	t.Helper()
	if (want == "") != (got == "") || !strings.Contains(got, want) {
		t.Errorf("%s = %q, want it to contain %q", what, got, want)
	}
}

func TestServerMessage(t *testing.T) {
	if got := serverMessage(errors.New("rex: connection refused")); got != "Connection refused" {
		t.Errorf("serverMessage = %q", got)
	}
}

// TestRemoteHostsPage checks that the hosts this machine connects to have
// a page of their own, and are not on the Server page.
func TestRemoteHostsPage(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "hosts"
	tt := ui.NewTester(a.settingsView, 780, 900)
	tt.Frame()
	for _, s := range []string{locales.HostsNote(), locales.HostsNone(), locales.HostsAdd()} {
		if !tt.HasText(s) {
			t.Errorf("hosts: no %q in %q", s, tt.Texts())
		}
	}
	if tt.HasText(locales.RemoteDirectTitle()) {
		t.Errorf("hosts: the Server page shows; texts %q", tt.Texts())
	}
	settingsUI.section = "server"
	tt.Frame()
	if tt.HasText(locales.HostsAdd()) || tt.HasText(locales.HostsNote()) {
		t.Errorf("server: the hosts show; texts %q", tt.Texts())
	}
}

// TestLocalAPIGroup draws the server page's Local API group: the
// gateway's revisions and version, the primary endpoint's URL, then each
// other listener's address and whom it lets in, then the local network's
// listener with its controls, once and not again among the others; the
// gateway off, one row says so, and the local network's row shows still.
func TestLocalAPIGroup(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Setenv("GOREX_DIR", t.TempDir())
	t.Cleanup(stopRemotePoll)
	t.Cleanup(stopServerInfoPoll)
	a := &App{}
	settingsUI.section = "server"
	remoteUI.lan = rexapi.LANConfig{Enabled: true, Listen: "192.0.2.10:7297", Allow: []string{"192.0.2.0/24"}}
	remoteUI.lanStatus = rexapi.LANStatus{Running: true, Listening: "192.0.2.10:7297"}
	remoteUI.lanListen, remoteUI.lanEdited = "", true
	settingsUI.apiLoaded, settingsUI.apiErr = true, ""
	settingsUI.apiInfo = rexapi.GatewayInfo{RexVersion: "1.2.3-test", APIRevision: 12, ServerRevision: 13, MinRevision: 1, Listeners: []rexapi.Listener{
		{Endpoint: "unix:///tmp/a%20b/rexapi.sock", Addresses: []string{"/tmp/a b/rexapi.sock"}, Transport: "unix", Admission: rexapi.AdmissionPeer, Primary: true},
		{Endpoint: "127.0.0.1:0", Addresses: []string{"127.0.0.1:52061"}, Transport: "tcp", Admission: rexapi.AdmissionAnonymous},
		{Endpoint: "192.0.2.10:7297", Addresses: []string{"192.0.2.10:7297"}, Transport: "tcp", Admission: rexapi.AdmissionAnonymous},
		{Endpoint: "198.51.100.1:7297", Transport: "tcp", Admission: "login_server"},
	}}
	tt := ui.NewTester(a.settingsView, 780, 2200)
	tt.Frame()
	for _, s := range []string{
		locales.RemoteApiTitle(), locales.HostApiTitle(), locales.HostApiRevisions(12, 13, 1), locales.HostApiVersion(), "1.2.3-test",
		locales.RemoteApiUrl(), "unix:///tmp/a%20b/rexapi.sock",
		"127.0.0.1:52061", locales.RemoteApiAnyone(),
		"198.51.100.1:7297", "login_server", locales.RemoteApiNote(),
		locales.RemoteLanSwitch(), "Listening on http://192.0.2.10:7297", locales.RemoteLanNetworks(), "192.0.2.0/24", locales.RemoteLanNote(),
	} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %q", s, tt.Texts())
		}
	}
	if tt.HasText(locales.RemoteApiPeer()) {
		t.Error("the primary endpoint's row says whom it lets in, which Rex's does not")
	}
	if slices.Contains(tt.Texts(), "192.0.2.10:7297") {
		t.Error("the local network's listener is listed among the others too")
	}

	// Off, the local network's row shows still, so that it can be turned on,
	// and a listener at its address is --listen's, marked.
	remoteUI.lan.Enabled, remoteUI.lanStatus = false, rexapi.LANStatus{}
	tt.Frame()
	if !tt.HasText(locales.RemoteLanSwitch()) || !tt.HasText(locales.RemoteLanOff()) || !slices.Contains(tt.Texts(), "192.0.2.10:7297") {
		t.Errorf("local network off: texts %q", tt.Texts())
	}

	settingsUI.apiErr = "no gateway"
	settingsUI.apiInfo = rexapi.GatewayInfo{}
	tt.Frame()
	if !tt.HasText(locales.RemoteApiUrl()) || !tt.HasText(locales.HostApiOff()) || tt.HasText("127.0.0.1:52061") || !tt.HasText(locales.RemoteLanSwitch()) ||
		tt.HasText(locales.HostApiVersion()) {
		t.Errorf("gateway off: texts %q", tt.Texts())
	}
	off := 0
	for _, s := range tt.Texts() {
		if s == locales.HostApiOff() {
			off++
		}
	}
	if off != 1 {
		t.Errorf("the gateway is said off %d times", off)
	}
}

func TestAdmissionLabel(t *testing.T) {
	for _, c := range []struct {
		l     rexapi.Listener
		label string
		open  bool
	}{
		{rexapi.Listener{Transport: "unix", Admission: rexapi.AdmissionPeer}, locales.RemoteApiPeer(), false},
		{rexapi.Listener{Endpoint: "0.0.0.0:7297", Transport: "tcp", Admission: rexapi.AdmissionAnonymous}, locales.RemoteApiAnyone(), true},
		{rexapi.Listener{Transport: "unix", Admission: rexapi.AdmissionAnonymous}, locales.RemoteApiAnyone(), false},
		{rexapi.Listener{Transport: "tcp", Admission: "login_server"}, "login_server", false},
		{rexapi.Listener{Transport: "tcp"}, "", false},
	} {
		if label, open := admissionLabel(c.l); label != c.label || open != c.open {
			t.Errorf("admissionLabel(%+v) = %q, %v; want %q, %v", c.l, label, open, c.label, c.open)
		}
	}
}

func TestLanListener(t *testing.T) {
	lan := rexapi.LANConfig{Enabled: true, Listen: "192.0.2.10:7297"}
	at := rexapi.Listener{Endpoint: "192.0.2.10:7297", Transport: "tcp", Admission: rexapi.AdmissionAnonymous}
	if !lanListener(at, lan) {
		t.Error("the local network's listener is not told")
	}
	other := at
	other.Endpoint = "0.0.0.0:7297"
	if lanListener(other, lan) {
		t.Error("another listener is the local network's")
	}
	// Turned off, a listener at its address is --listen's.
	if lanListener(at, rexapi.LANConfig{Listen: "192.0.2.10:7297"}) {
		t.Error("a listener at a turned-off LAN's address is the LAN's")
	}
}

// TestConnectPhoneEntry verifies the above-the-fold Connect iPhone card on
// Settings › Server (docs/IOS-023-pairing-entry.md): visible while off,
// reflects Direct/Relay or LAN readiness, and keeps the QR action above the fold.
func TestConnectPhoneEntry(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Setenv("GOREX_DIR", t.TempDir())
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	fetchDirect()
	fetchLAN()
	tt := ui.NewTester(a.settingsView, 780, 540)
	tt.Frame()

	// Above the fold in the default 540px Settings window even while off.
	for _, s := range []string{locales.SettingsServerCard(), locales.RemoteConnectPhoneTitle(),
		locales.RemoteDirectOff(), locales.RemoteConnectPhoneOff()} {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("off: missing %q in %q", s, tt.Texts())
		}
		if r.Y+r.H > 540 {
			t.Errorf("off: %q lies below the fold (bottom %.1f > 540)", s, r.Y+r.H)
		}
	}
	if !strings.Contains(locales.SettingsServerCard(), "WindRex") {
		t.Errorf("expected WindRex naming: card=%q", locales.SettingsServerCard())
	}

	// LAN listener running with Direct/Relay off offers LAN QR above the fold.
	remoteUI.lan = rexapi.LANConfig{Enabled: true, Listen: "192.0.2.10:7297", Allow: []string{"192.0.2.0/24"}, Token: "lan-token"}
	remoteUI.lanStatus = rexapi.LANStatus{Running: true, Listening: "192.0.2.10:7297"}
	tt.Frame()
	if r, ok := tt.Find(locales.HostsRexQR()); !ok || r.Y+r.H > 540 {
		t.Errorf("lan fallback: missing or below fold %+v in %q", r, tt.Texts())
	}

	// Direct/Relay connected takes precedence and offers Direct / Relay QR above the fold.
	remoteUI.direct = rexapi.DirectConfig{Enabled: true, RelayURL: "https://relay.example.com:7443", HostID: "host12345678", Token: "test-token"}
	remoteUI.directStatus = rexapi.DirectStatus{Running: true, RelayConnected: true}
	tt.Frame()
	for _, s := range []string{locales.RemoteDirectRelayQR(), locales.RemoteConnectPhoneReady(), locales.RemoteDirectRelayConnected()} {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("relay connected: missing %q in %q", s, tt.Texts())
		}
		if r.Y+r.H > 540 {
			t.Errorf("relay connected: %q lies below the fold (bottom %.1f > 540)", s, r.Y+r.H)
		}
	}
}

// TestDirectRelayStatusAndConfig verifies the Direct / Relay Access section's
// status states and that Connect iPhone surfaces the Direct / Relay QR button
// above the fold once Direct/Relay is active and ready.
func TestDirectRelayStatusAndConfig(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Setenv("GOREX_DIR", t.TempDir())
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	fetchDirect()

	tt := ui.NewTester(a.settingsView, 780, 2200)
	tt.Frame()
	for _, s := range []string{
		locales.RemoteDirectTitle(), locales.RemoteDirectSwitch(), locales.RemoteDirectOff(),
		locales.RemoteDirectRelayURL(), locales.RemoteDirectRelayToken(), locales.RemoteDirectRelayPin(),
		locales.RemoteDirectPublicURL(), locales.RemoteDirectListen(),
	} {
		if !tt.HasText(s) {
			t.Errorf("off: missing %q in %q", s, tt.Texts())
		}
	}
	if tt.HasText(locales.RemoteDirectRelayQR()) {
		t.Errorf("off: Direct / Relay QR button should stay hidden; texts %q", tt.Texts())
	}

	// Enabled without URLs reports unconfigured state.
	remoteUI.direct = rexapi.DirectConfig{Enabled: true, Token: "test-token"}
	remoteUI.directStatus = rexapi.DirectStatus{}
	tt.Frame()
	if !tt.HasText(locales.RemoteDirectUnconfigured()) {
		t.Errorf("unconfigured: missing %q in %q", locales.RemoteDirectUnconfigured(), tt.Texts())
	}

	// Enabled with relay URL waiting to connect.
	remoteUI.direct = rexapi.DirectConfig{Enabled: true, RelayURL: "https://relay.example.com:7443", HostID: "host12345678", Token: "test-token"}
	tt.Frame()
	if !tt.HasText(locales.RemoteDirectRelayConnecting()) {
		t.Errorf("connecting: missing %q in %q", locales.RemoteDirectRelayConnecting(), tt.Texts())
	}
	if tt.HasText(locales.RemoteDirectRelayQR()) {
		t.Errorf("connecting: QR button should stay hidden until relay connects")
	}

	// Relay connected: shows status and surfaces Direct / Relay QR above the fold.
	remoteUI.directStatus = rexapi.DirectStatus{Running: true, RelayConnected: true}
	tt.SetSize(780, 540)
	tt.Frame()
	if !tt.HasText(locales.RemoteDirectRelayConnected()) {
		t.Errorf("connected: missing %q in %q", locales.RemoteDirectRelayConnected(), tt.Texts())
	}
	qrRect, ok := tt.Find(locales.RemoteDirectRelayQR())
	if !ok {
		t.Fatalf("connected: missing %q in %q", locales.RemoteDirectRelayQR(), tt.Texts())
	}
	if qrRect.Y+qrRect.H > 540 {
		t.Errorf("connected: Direct / Relay QR lies below the fold (bottom %.1f > 540)", qrRect.Y+qrRect.H)
	}

	// Relay error and direct TLS listening states.
	tt.SetSize(780, 2200)
	remoteUI.directStatus = rexapi.DirectStatus{Running: true, RelayError: "connection refused"}
	tt.Frame()
	if !tt.HasText(locales.RemoteDirectRelayError("connection refused")) {
		t.Errorf("relay error: missing message in %q", tt.Texts())
	}

	remoteUI.direct = rexapi.DirectConfig{Enabled: true, PublicURL: "https://203.0.113.10:7443", Listen: ":7443", Token: "test-token"}
	remoteUI.directStatus = rexapi.DirectStatus{Running: true, Listening: "[::]:7443", SelfSigned: true, CertPin: "abc123pin"}
	tt.Frame()
	for _, s := range []string{locales.RemoteDirectListening("https://[::]:7443"), locales.RemoteDirectCertPin(), "abc123pin", locales.RemoteDirectRelayQR()} {
		if !tt.HasText(s) {
			t.Errorf("direct listening: missing %q in %q", s, tt.Texts())
		}
	}

	remoteUI.directStatus = rexapi.DirectStatus{Running: true, Error: "address already in use"}
	tt.Frame()
	if !tt.HasText(locales.RemoteDirectError("address already in use")) {
		t.Errorf("direct error: missing message in %q", tt.Texts())
	}
}

// TestDirectRelaySaveAndValidate verifies saving self-hosted relay and direct
// TLS settings, rejecting invalid URLs, preserving CLI-rotated tokens, and
// toggling enable/disable in direct.json.
func TestDirectRelaySaveAndValidate(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Setenv("GOREX_DIR", t.TempDir())
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	fetchDirect()

	tt := ui.NewTester(a.settingsView, 780, 2400)
	tt.Frame()
	saveLabel := locales.RemoteDirectTitle() + ": " + locales.RemoteLanSave()

	// Invalid relay URL is refused and surfaced inline without persisting.
	remoteUI.directRelayURL = "not-a-valid-url"
	remoteUI.directEdited = true
	tt.Frame()
	if err := tt.Click(saveLabel); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if remoteUI.directErr == "" || !tt.HasText(remoteUI.directErr) {
		t.Fatalf("invalid relay URL was not refused: err=%q texts=%q", remoteUI.directErr, tt.Texts())
	}
	dir := rexapi.StateDir()
	if saved, _ := rexapi.LoadDirect(dir); saved.RelayURL != "" {
		t.Fatalf("invalid relay URL was persisted: %+v", saved)
	}

	// Valid relay URL + relay token saves to direct.json and generates Token + HostID.
	remoteUI.directRelayURL = "  https://relay.example.com:7443  "
	remoteUI.directRelaySecret = "relay-secret-token"
	remoteUI.directEdited = true
	tt.Frame()
	if err := tt.Click(saveLabel); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if remoteUI.directErr != "" || remoteUI.directEdited {
		t.Fatalf("valid relay save failed: err=%q edited=%v", remoteUI.directErr, remoteUI.directEdited)
	}
	saved, err := rexapi.LoadDirect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.RelayURL != "https://relay.example.com:7443" || saved.RelaySecret != "relay-secret-token" || saved.Token == "" || saved.HostID == "" {
		t.Fatalf("saved relay config = %+v", saved)
	}

	// Simulate a CLI token rotation on disk and confirm GUI Save preserves it.
	cliConf := saved
	cliConf.Token = rexapi.NewLANToken()
	if err := rexapi.SaveDirect(dir, cliConf); err != nil {
		t.Fatal(err)
	}
	remoteUI.directPublicURL = "https://203.0.113.10:7443"
	remoteUI.directListen = ""
	remoteUI.directEdited = true
	tt.Frame()
	if err := tt.Click(saveLabel); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	saved, err = rexapi.LoadDirect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Token != cliConf.Token {
		t.Errorf("saveDirect overwrote CLI-rotated token: got %q, want %q", saved.Token, cliConf.Token)
	}
	if saved.Listen != ":7443" {
		t.Errorf("saveDirect did not default listen port when public URL set: got %q", saved.Listen)
	}

	// Toggle switch enables Direct / Relay in direct.json.
	// The switch sits at the right of its title and status line.
	swTitle, ok := tt.Find(locales.RemoteDirectSwitch())
	if !ok {
		t.Fatalf("no %q in %q", locales.RemoteDirectSwitch(), tt.Texts())
	}
	for y := swTitle.Y - 6; y < swTitle.Y+swTitle.H+20 && !remoteUI.direct.Enabled; y += 4 {
		for x := float32(775); x > swTitle.X+60 && !remoteUI.direct.Enabled; x -= 6 {
			tt.ClickAt(x, y)
			tt.Frame()
		}
	}
	saved, _ = rexapi.LoadDirect(dir)
	if !saved.Enabled {
		t.Errorf("enable switch did not persist Enabled=true: %+v", saved)
	}
}

// TestDirectRelayQRSheetAndRotate verifies opening the Direct / Relay QR sheet
// without leaking the secret pairing token on screen, rotating the pairing
// token via the Rotate Token button, and auto-closing the QR sheet when the
// token changes or the relay disconnects.
func TestDirectRelayQRSheetAndRotate(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Setenv("GOREX_DIR", t.TempDir())
	t.Cleanup(stopRemotePoll)
	qr := remoteUI.pairQR
	t.Cleanup(func() { remoteUI.pairQR = qr })

	dir := rexapi.StateDir()
	if err := rexapi.SaveDirect(dir, rexapi.DirectConfig{
		Enabled:  true,
		RelayURL: "https://relay.example.com:7443",
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := rexapi.LoadDirect(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldToken := saved.Token
	if oldToken == "" || saved.HostID == "" {
		t.Fatalf("SaveDirect did not populate token/hostID: %+v", saved)
	}

	a := &App{}
	settingsUI.section = "server"
	fetchDirect()
	remoteUI.directStatus = rexapi.DirectStatus{Running: true, RelayConnected: true}

	tt := ui.NewTester(a.settingsView, 780, 2400)
	tt.Frame()
	if err := tt.Click(locales.RemoteDirectRelayQR()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if remoteUI.pairQR == nil || !remoteUI.pairQR.directTLS || remoteUI.pairQR.image == nil {
		t.Fatalf("Direct / Relay QR sheet did not open: %+v", remoteUI.pairQR)
	}
	if !strings.Contains(remoteUI.pairQR.address, "#k="+oldToken) || !strings.Contains(remoteUI.pairQR.address, "/h/"+saved.HostID+"/") {
		t.Errorf("unexpected QR pairing URL: %q", remoteUI.pairQR.address)
	}
	if !tt.HasText(locales.RemoteDirectTLSQRHint()) {
		t.Errorf("missing QR hint in %q", tt.Texts())
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, oldToken) {
			t.Errorf("pairing token leaked onto screen: %q", s)
		}
	}

	// Close QR sheet, rotate token, and verify new token is persisted.
	if err := tt.Click(locales.HostsRexQRClose()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if remoteUI.pairQR != nil {
		t.Fatal("Close did not close Direct / Relay QR sheet")
	}

	if err := tt.Click(locales.RemoteDirectRotateToken()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	rotated, err := rexapi.LoadDirect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Token == "" || rotated.Token == oldToken {
		t.Fatalf("token did not rotate: old=%q new=%q", oldToken, rotated.Token)
	}
	if remoteUI.direct.Token != rotated.Token {
		t.Errorf("remoteUI.direct.Token = %q, want %q", remoteUI.direct.Token, rotated.Token)
	}

	// Open QR again with rotated token, then verify disconnect auto-closes it.
	a.openDirectPairQR()
	tt.Frame()
	if remoteUI.pairQR == nil || !strings.Contains(remoteUI.pairQR.address, "#k="+rotated.Token) {
		t.Fatalf("reopened QR missing rotated token: %+v", remoteUI.pairQR)
	}
	remoteUI.directStatus.RelayConnected = false
	tt.Frame()
	if remoteUI.pairQR != nil {
		t.Error("Direct / Relay QR stayed open after relay disconnected")
	}
}

// TestDirectRelayFleetControlsAndQRTabs verifies Display Name, the Allow Relay
// Fleet access switch, and the two-tab Relay Fleet / This Host QR dialog on
// Settings › Server (docs/NET-010-relay-fleet-discovery.md).
func TestDirectRelayFleetControlsAndQRTabs(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Setenv("GOREX_DIR", t.TempDir())
	t.Cleanup(stopRemotePoll)
	qr := remoteUI.pairQR
	t.Cleanup(func() { remoteUI.pairQR = qr })

	const secret = "test-relay-secret"
	const pin = "an5ZArj8H5A9Xvdo174igXtA3zF8Uz6G0te-EkPK8rg"
	dir := rexapi.StateDir()
	if err := rexapi.SaveDirect(dir, rexapi.DirectConfig{
		Enabled:      true,
		RelayURL:     "https://relay.example.com:7443",
		RelaySecret:  secret,
		RelayCertPin: pin,
		HostName:     "alice-laptop",
		RelayTrust:   true,
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := rexapi.LoadDirect(dir)
	if err != nil {
		t.Fatal(err)
	}

	a := &App{}
	settingsUI.section = "server"
	fetchDirect()
	remoteUI.directStatus = rexapi.DirectStatus{Running: true, RelayConnected: true}

	tt := ui.NewTester(a.settingsView, 780, 2400)
	tt.Frame()
	for _, s := range []string{locales.RemoteDirectHostName(), locales.RemoteDirectRelayTrust(), locales.RemoteDirectRelayTrustNote()} {
		if !tt.HasText(s) {
			t.Errorf("server page missing %q in %q", s, tt.Texts())
		}
	}

	// Open QR dialog: shows two-tab switcher defaulting to Relay Fleet (#r=).
	if err := tt.Click(locales.RemoteDirectRelayQR()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	tt.Frame()
	page := remoteUI.pairQR
	if page == nil || page.fleetImage == nil || page.image == nil {
		t.Fatalf("expected two-tab Fleet/Host QR page, got %+v", page)
	}
	if !strings.HasPrefix(page.fleetAddress, "https://relay.example.com:7443/#r="+secret) {
		t.Fatalf("unexpected fleetAddress = %q", page.fleetAddress)
	}
	for _, s := range []string{locales.RemoteDirectFleetQRTitle(), locales.RemoteDirectQRTabFleet(), locales.RemoteDirectQRTabHost(), locales.RemoteDirectFleetQRHint()} {
		if !tt.HasText(s) {
			t.Errorf("fleet QR tab missing %q in %q", s, tt.Texts())
		}
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, secret) || strings.Contains(s, saved.Token) {
			t.Errorf("secret leaked on Fleet QR dialog: %q", s)
		}
	}

	// Switch to This Host tab (#k=).
	if err := tt.Click(locales.RemoteDirectQRTabHost()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	tt.Frame()
	if page.tab != 1 {
		t.Fatalf("expected tab 1 (This Host), got %d", page.tab)
	}
	if !tt.HasText(locales.RemoteDirectTLSQRTitle("alice-laptop")) || !tt.HasText(locales.RemoteDirectTLSQRHint()) {
		t.Errorf("This Host tab missing title/hint in %q", tt.Texts())
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, secret) || strings.Contains(s, saved.Token) {
			t.Errorf("secret leaked on This Host QR tab: %q", s)
		}
	}

	// Disabling RelayTrust closes the Fleet QR sheet automatically.
	remoteUI.direct.RelayTrust = false
	tt.Frame()
	if remoteUI.pairQR != nil {
		t.Error("QR sheet stayed open after RelayTrust was turned off")
	}
}
