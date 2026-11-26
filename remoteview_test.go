package main

import (
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"gorex/internal/rex"
	"gorex/internal/rexapi"
	"gorex/locales"
)

var testKey = "nodekey:" + strings.Repeat("0123456789abcdef", 4)

// wantMessage checks got against want: "" for no message, else a part of it.
func wantMessage(t *testing.T, what, got, want string) {
	t.Helper()
	if (want == "") != (got == "") || !strings.Contains(got, want) {
		t.Errorf("%s = %q, want it to contain %q", what, got, want)
	}
}

func TestValidateClientKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{testKey, ""},
		{"  " + testKey + "\n", ""},
		{"", "Paste"},
		{testKey[8:], "not a public key"},
		{testKey[:20], "not a public key"},
		{testKey + "0", "not a public key"},
		{"nodekey:" + strings.Repeat("g", 64), "not a public key"},
	} {
		wantMessage(t, "validateClientKey("+tc.in+")", validateClientKey(tc.in), tc.want)
	}
}

func TestValidateNewClient(t *testing.T) {
	allowed := []rex.RemoteClient{{Name: "bob-desktop", PublicKey: testKey}}
	other := "nodekey:" + strings.Repeat("fedcba9876543210", 4)
	for _, tc := range []struct{ name, key, want string }{
		{"alice-laptop", other, ""},
		{" ", other, "Name the client"},
		{"alice-laptop", "x", "not a public key"},
		{"alice-laptop", " " + testKey, "bob-desktop"},
	} {
		wantMessage(t, "validateNewClient("+tc.name+")", validateNewClient(tc.name, tc.key, allowed), tc.want)
	}
}

func TestShortKey(t *testing.T) {
	if got, want := shortKey(testKey), "nodekey:012345…abcdef"; got != want {
		t.Errorf("shortKey = %q, want %q", got, want)
	}
	for _, s := range []string{"", "alice", "nodekey:abc"} {
		if got := shortKey(s); got != s {
			t.Errorf("shortKey(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestRemoteState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		st    rex.RemoteStatus
		down  bool
		label string
		kind  remoteKind
	}{
		{"off", rex.RemoteStatus{}, false, "Off", remoteOff},
		{"starting", rex.RemoteStatus{Enabled: true}, false, "Starting…", remoteStarting},
		{"listening", rex.RemoteStatus{Enabled: true, Running: true}, false, "Listening", remoteListening},
		{"retrying", rex.RemoteStatus{Enabled: true, LastError: "no route"}, false, "Error", remoteError},
		{"off with error", rex.RemoteStatus{LastError: "bad file"}, false, "Error", remoteError},
		{"down", rex.RemoteStatus{Enabled: true, Running: true}, true, "Server not reachable", remoteDown},
	} {
		label, detail, kind := remoteState(tc.st, tc.down)
		if label != tc.label || kind != tc.kind {
			t.Errorf("%s: got %q, %v; want %q, %v", tc.name, label, kind, tc.label, tc.kind)
		}
		if !strings.Contains(detail, tc.st.LastError) {
			t.Errorf("%s: detail %q lacks the server's error", tc.name, detail)
		}
	}
}

func TestPairingLeft(t *testing.T) {
	now := time.Now()
	for d, want := range map[time.Duration]string{
		5 * time.Minute: "5:00", 61*time.Second + 400*time.Millisecond: "1:01", 9 * time.Second: "0:09", -time.Second: "0:00",
	} {
		if got := pairingLeft(now.Add(d), now); got != want {
			t.Errorf("pairingLeft(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestServerMessage(t *testing.T) {
	if got := serverMessage(errors.New("rex: that client is allowed already")); got != "That client is allowed already" {
		t.Errorf("serverMessage = %q", got)
	}
}

// TestDevicesButtons checks the Devices group's buttons: the form that
// adds a client by its key shows only once Add by Key… is clicked, and
// Cancel hides it; Pair New Device… asks the server to start pairing, as
// Start pairing did, and is gone while the pairing window is open.
func TestDevicesButtons(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	remoteUI.loaded, remoteUI.st, remoteUI.err = true, rex.RemoteStatus{Enabled: true}, nil
	// The host key row adds to the details, so the window needs the
	// height the row costs, or the add-by-key form's buttons leave it.
	tt := ui.NewTester(a.settingsView, 780, 1600)
	tt.Frame()
	if err := tt.Click(locales.RemoteShowDetails()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if remoteUI.addingKey || tt.HasText(locales.RemoteCancel()) {
		t.Fatal("the add-by-key form shows before Add by Key… is clicked")
	}
	if err := tt.Click(locales.RemoteAddByKeyAsk()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText(locales.RemoteCancel()) || !remoteUI.addingKey {
		t.Fatalf("no add-by-key form after the click; texts %q", tt.Texts())
	}
	if err := tt.Click(locales.RemoteCancel()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if remoteUI.addingKey || tt.HasText(locales.RemoteCancel()) {
		t.Fatal("Cancel did not hide the add-by-key form")
	}

	// No server here: the call says so, as every change of the page does.
	if err := tt.Click(locales.RemotePairNew()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if remoteUI.err == nil || remoteUI.err() != locales.RemoteNotConnected() {
		t.Error("Pair New Device… did not ask the server")
	}
	remoteUI.st.Pairing = rex.RemotePairing{Open: true, ExpiresAt: time.Now().Add(time.Minute)}
	tt.Frame()
	if tt.HasText(locales.RemotePairNew()) || !tt.HasText(locales.RemotePairStop()) {
		t.Errorf("pairing open: texts %q", tt.Texts())
	}
}

// TestRexGoPairURL: Rex Go's code carries the address and the machine's
// name, a space as %20, which Rex Go reads.
func TestRexGoPairURL(t *testing.T) {
	s := rexGoPairURL("tcAbc-_9", "Yibo's Mac & mini+")
	if strings.Contains(s, "+") {
		t.Errorf("a + in %s", s)
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "rexgo" || u.Host != "tailcat" || u.Path != "" {
		t.Fatalf("%s: %v %+v", s, err, u)
	}
	if q := u.Query(); q.Get("a") != "tcAbc-_9" || q.Get("n") != "Yibo's Mac & mini+" {
		t.Errorf("%s reads as %v", s, q)
	}
}

// TestRexGoQR: Rex Go QR… shows while remote access listens; its code
// shows neither the address nor Copy, and closes once remote access
// stops.
func TestRexGoQR(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Cleanup(stopRemotePoll)
	qr := remoteUI.pairQR
	t.Cleanup(func() { remoteUI.pairQR = qr })
	a := &App{}
	settingsUI.section = "server"
	remoteUI.loaded, remoteUI.st, remoteUI.err = true, rex.RemoteStatus{Enabled: true}, nil
	tt := ui.NewTester(a.settingsView, 780, 1400)
	tt.Frame()
	if tt.HasText(locales.RemoteRexGoQR()) {
		t.Fatal("Rex Go QR… shows before remote access listens")
	}
	remoteUI.st.Running = true
	tt.Frame()
	// No server here: the call says so.
	if err := tt.Click(locales.RemoteRexGoQR()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if remoteUI.err == nil || remoteUI.err() != locales.RemoteNotConnected() || remoteUI.pairQR != nil {
		t.Error("Rex Go QR… did not ask the server")
	}
	// While a change is on its way, it waits, as every change does.
	remoteUI.busy, remoteUI.err = true, nil
	a.openRexGoQR(false)
	if remoteUI.err != nil {
		t.Error("Rex Go QR… asked while busy")
	}
	remoteUI.busy = false

	const secret = "tcSECRETADDRESS"
	remoteUI.pairQR = &hostPairQR{host: "mac", image: qrImage(rexGoPairURL(secret, "mac")), open: true, rexGo: true}
	tt.Frame()
	if !tt.HasText(locales.RemoteRexGoQRHint()) {
		t.Fatalf("no hint in %q", tt.Texts())
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, secret) || s == locales.HostsRexQRCopy() {
			t.Errorf("the dialog shows %q", s)
		}
	}
	remoteUI.st.Running = false
	tt.Frame()
	if remoteUI.pairQR != nil {
		t.Error("the code stayed once remote access stopped")
	}
}

// TestRemoteOff checks the Remote page with remote access off: the
// switches alone, with a hint, and Tailscale's discovery as a way in; the
// devices only for a request still waiting. On, the devices show, with
// this machine's details behind Show Details.
func TestRemoteOff(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	remoteUI.loaded, remoteUI.err = true, nil
	remoteUI.st = rex.RemoteStatus{Tailnet: &rex.TailnetStatus{}, Allowed: []rex.RemoteClient{{Name: "bob-desktop", PublicKey: testKey}}}
	tt := ui.NewTester(a.settingsView, 780, 1400)
	tt.Frame()
	// Off, Show Details stays, for this machine's key: the other way in.
	for _, s := range []string{locales.RemoteConnect(), locales.RemoteOff(), locales.RemoteOffNote(),
		locales.RemoteShowDetails()} {
		if !tt.HasText(s) {
			t.Errorf("off: no %q in %q", s, tt.Texts())
		}
	}
	for _, s := range []string{locales.TailnetMode(), locales.RemoteDevices(), "bob-desktop",
		locales.RemoteMyKey(), locales.HostsNone()} {
		if tt.HasText(s) {
			t.Errorf("off: %q shows; texts %q", s, tt.Texts())
		}
	}

	// A request still waiting shows, alone.
	remoteUI.st.Pending = []rex.RemotePending{{Name: "alice-laptop", PublicKey: testKey}}
	tt.Frame()
	if !tt.HasText("alice-laptop") || tt.HasText("bob-desktop") || tt.HasText(locales.RemoteAddByKeyAsk()) {
		t.Errorf("off with a request: texts %q", tt.Texts())
	}
	remoteUI.st.Pending = nil

	// Tailscale's discovery running serves too.
	remoteUI.st.Tailnet.Running = true
	tt.Frame()
	if !tt.HasText(locales.RemoteDevices()) || !tt.HasText("bob-desktop") || !tt.HasText(locales.RemoteAddDevice()) {
		t.Errorf("discovery running: texts %q", tt.Texts())
	}

	// On, the details open and close.
	remoteUI.st = rex.RemoteStatus{Enabled: true}
	tt.Frame()
	if !tt.HasText(locales.RemoteDevices()) || !tt.HasText(locales.RemoteAddDevice()) || tt.HasText(locales.RemoteOffNote()) {
		t.Fatalf("on: texts %q", tt.Texts())
	}
	if tt.HasText(locales.RemoteMyKey()) || tt.HasText(locales.RemoteAddByKey()) {
		t.Fatal("the details show before Show Details is clicked")
	}
	if err := tt.Click(locales.RemoteShowDetails()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText(locales.RemoteMyKey()) || !tt.HasText(locales.RemoteMyKeyNote()) || !tt.HasText(locales.RemoteAddByKey()) || !tt.HasText(locales.RemoteHideDetails()) {
		t.Fatalf("details: texts %q", tt.Texts())
	}
	if err := tt.Click(locales.RemoteHideDetails()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if tt.HasText(locales.RemoteMyKey()) {
		t.Error("Hide Details did not hide them")
	}
}

// TestRemoteHostKeyRow: with remote access on, the details show the
// host's node key beside its address, its fingerprint and a Copy, for the
// list a relay's operator keeps; off, the row is gone. The copy itself
// runs mygo.Clipboard.Write, which needs the app: the headless tester
// does not click it, as for the other copy buttons.
func TestRemoteHostKeyRow(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	remoteUI.loaded, remoteUI.err, remoteUI.details = true, nil, true
	remoteUI.st = rex.RemoteStatus{Enabled: true, Running: true, PublicKey: testKey}
	tt := ui.NewTester(a.settingsView, 780, 1600)
	tt.Frame()
	for _, s := range []string{locales.RemoteHostKey(), locales.RemoteFingerprint(rex.Fingerprint(testKey)),
		testKey, locales.RemoteHostKeyNote(), locales.RemoteCopy()} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %q", s, tt.Texts())
		}
	}
	remoteUI.st.Enabled = false
	tt.Frame()
	if tt.HasText(locales.RemoteHostKey()) {
		t.Error("the host key shows with remote access off")
	}
}

// TestRemoteRelayAlias: the relay row takes gorex, the team's relay its
// hint names: the save passes the field's check and asks the server,
// which is not there in the test.
func TestRemoteRelayAlias(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Setenv("GOREX_DIR", t.TempDir())
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	remoteUI.loaded, remoteUI.st, remoteUI.details = true, rex.RemoteStatus{Enabled: true}, true
	remoteUI.relay, remoteUI.relayEdited, remoteUI.relayErr = "gorex", true, ""
	tt := ui.NewTester(a.settingsView, 780, 1400)
	tt.Frame()
	if !strings.Contains(locales.RemoteRelayNote(), "gorex") {
		t.Fatal("the relay hint does not name gorex")
	}
	title, _ := tt.Find(locales.RemoteRelay())
	for x := float32(770); x > title.X+title.W && remoteUI.relayErr == ""; x -= 4 {
		tt.ClickAt(x, title.Y+title.H/2)
		tt.Frame()
	}
	if remoteUI.relayErr != locales.RemoteNotConnected() {
		t.Errorf("gorex refused: %q, want the server's unreachable answer", remoteUI.relayErr)
	}
}

// TestRemoteHostsPage checks that the hosts this machine connects to have
// a page of their own, and are no longer on the Remote page.
func TestRemoteHostsPage(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "hosts"
	remoteUI.loaded, remoteUI.st = true, rex.RemoteStatus{Enabled: true}
	tt := ui.NewTester(a.settingsView, 780, 900)
	tt.Frame()
	for _, s := range []string{locales.HostsNote(), locales.HostsNone(), locales.HostsAdd()} {
		if !tt.HasText(s) {
			t.Errorf("hosts: no %q in %q", s, tt.Texts())
		}
	}
	if tt.HasText(locales.RemoteConnect()) {
		t.Errorf("hosts: the Remote page shows; texts %q", tt.Texts())
	}
	settingsUI.section = "server"
	tt.Frame()
	if tt.HasText(locales.HostsAdd()) || tt.HasText(locales.HostsNote()) {
		t.Errorf("remote: the hosts show; texts %q", tt.Texts())
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
	remoteUI.loaded, remoteUI.st = true, rex.RemoteStatus{}
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
	tt := ui.NewTester(a.settingsView, 780, 1400)
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
		{rexapi.Listener{Transport: "tcp", Admission: "tailscale_owner"}, "tailscale_owner", false},
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

// TestControlServerUnderTailscale checks that built-in Tailscale's
// control server is set on the Server page, under its switch, whether or
// not remote access is on.
func TestControlServerUnderTailscale(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	remoteUI.loaded, remoteUI.err, remoteUI.details = true, nil, true // under Show Details
	remoteUI.st = rex.RemoteStatus{Tailnet: &rex.TailnetStatus{}}
	tt := ui.NewTester(a.settingsView, 780, 1600)
	tt.Frame()
	for _, s := range []string{locales.TailnetMode(), locales.RemoteTailscaleControl(), locales.RemoteTailscaleNote()} {
		if !tt.HasText(s) {
			t.Errorf("off: no %q in %q", s, tt.Texts())
		}
	}
	sw, _ := tt.Find(locales.TailnetMode())
	field, _ := tt.Find(locales.RemoteTailscaleControl())
	if field.Y <= sw.Y {
		t.Errorf("the control server (y %v) is not under the switch (y %v)", field.Y, sw.Y)
	}
	remoteUI.st.Enabled = true
	tt.Frame()
	if !tt.HasText(locales.RemoteTailscaleControl()) {
		t.Errorf("on: no control server in %q", tt.Texts())
	}
	// The server not answering, the field is still there to fix.
	remoteUI.loaded, remoteUI.unreachable = false, true
	tt.Frame()
	if !tt.HasText(locales.RemoteTailscaleControl()) {
		t.Errorf("unreachable: no control server in %q", tt.Texts())
	}
}

// TestServerPageWords checks the default Server page: the switch, Devices
// and Add a device, with the advanced rows behind Show Details.
func TestServerPageWords(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	remoteUI.loaded, remoteUI.err, remoteUI.details = true, nil, false
	remoteUI.st = rex.RemoteStatus{Enabled: true, Running: true, Allowed: []rex.RemoteClient{
		{Name: "bob-desktop", PublicKey: testKey, LastSeen: time.Now().Add(-5 * time.Minute)}}}
	tt := ui.NewTester(a.settingsView, 780, 1600)
	tt.Frame()
	for _, s := range []string{locales.RemoteConnect(), locales.RemoteDevices(), locales.RemoteAddDevice(), "bob-desktop",
		locales.RemoteLastSeen("5m ago")} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %q", s, tt.Texts())
		}
	}
	for _, s := range []string{locales.RemoteReachableTitle(), locales.RemoteRelay()} {
		if slices.Contains(tt.Texts(), s) {
			t.Errorf("%q shows before Show Details; texts %q", s, tt.Texts())
		}
	}
	if err := tt.Click(locales.RemoteShowDetails()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, s := range []string{locales.RemoteReachableTitle(), locales.RemoteRelay(), locales.RemoteAddByKey()} {
		if !slices.Contains(tt.Texts(), s) {
			t.Errorf("no %q under Show Details; texts %q", s, tt.Texts())
		}
	}
}

// TestConnectPhoneEntry verifies the above-the-fold Connect iPhone card on
// Settings › Server (docs/IOS-023-pairing-entry.md): visible while remote
// access is off, guides enabling + waiting + WindRex QR, and surfaces
// pending fingerprint approval requests without scrolling.
func TestConnectPhoneEntry(t *testing.T) {
	isolateSettings(t, "")
	registerFonts()
	t.Cleanup(stopRemotePoll)
	a := &App{}
	settingsUI.section = "server"
	remoteUI.loaded, remoteUI.err, remoteUI.wantPhoneQR = true, nil, false
	remoteUI.st = rex.RemoteStatus{Enabled: false}
	tt := ui.NewTester(a.settingsView, 780, 540)
	tt.Frame()

	// Above the fold in the default 540px Settings window even while remote access is off.
	for _, s := range []string{locales.SettingsServerCard(), locales.RemoteConnectPhoneTitle(),
		locales.RemoteConnectPhoneOff(), locales.RemoteConnectPhoneEnable()} {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("off: missing %q in %q", s, tt.Texts())
		}
		if r.Y+r.H > 540 {
			t.Errorf("off: %q lies below the fold (bottom %.1f > 540)", s, r.Y+r.H)
		}
	}
	if !strings.Contains(locales.SettingsServerCard(), "WindRex") || !strings.Contains(locales.RemoteRexGoQR(), "WindRex") {
		t.Errorf("expected WindRex naming: card=%q qr=%q", locales.SettingsServerCard(), locales.RemoteRexGoQR())
	}

	// Clicking "Turn On & Show QR…" arms wantPhoneQR and asks the server.
	if err := tt.Click(locales.RemoteConnectPhoneEnable()); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if !remoteUI.wantPhoneQR {
		t.Error("clicking Turn On & Show QR… did not arm wantPhoneQR")
	}

	// While starting, shows waiting guidance and keeps WindRex QR hidden until listening.
	remoteUI.st = rex.RemoteStatus{Enabled: true, Running: false}
	remoteUI.err = nil
	tt.Frame()
	if !tt.HasText(locales.RemoteConnectPhoneStarting()) {
		t.Errorf("starting: missing waiting guidance in %q", tt.Texts())
	}
	if tt.HasText(locales.RemoteRexGoQR()) {
		t.Errorf("starting: WindRex QR button shown before listening")
	}

	// Once listening, shows WindRex QR and inline pending fingerprint approval above the fold.
	remoteUI.st = rex.RemoteStatus{
		Enabled: true,
		Running: true,
		Pending: []rex.RemotePending{{Name: "alice-phone", PublicKey: testKey}},
	}
	tt.Frame()
	for _, s := range []string{locales.RemoteRexGoQR(), locales.RemoteConnectPhoneReady(),
		"alice-phone", rex.Fingerprint(testKey), locales.RemoteApprove(), locales.RemoteReject()} {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("listening: missing %q in %q", s, tt.Texts())
		}
		if r.Y+r.H > 540 {
			t.Errorf("listening: %q lies below the fold (bottom %.1f > 540)", s, r.Y+r.H)
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
	remoteUI.loaded, remoteUI.st = true, rex.RemoteStatus{}
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
	remoteUI.loaded, remoteUI.st = true, rex.RemoteStatus{}
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
	dir := rexapi.TailnetDir()
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

	dir := rexapi.TailnetDir()
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
	remoteUI.loaded, remoteUI.st = true, rex.RemoteStatus{}
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
