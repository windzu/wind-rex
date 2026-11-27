package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"gorex/internal/directrelay"
	"gorex/internal/hosts"
	"gorex/internal/layout"
	"gorex/internal/rex"
	"gorex/locales"
)

// dialGate, when set, holds the hosts' connections until it closes.
var dialGate atomic.Pointer[chan struct{}]

// fakeHosts serves hosts on Unix sockets, in place of Direct TLS / Relay URLs:
// an address is a socket's path.
func fakeHosts(t *testing.T) {
	t.Helper()
	oldDial, oldValid := dialHost, validAddress
	dialHost = func(ctx context.Context, address string) (*rex.Client, error) {
		if g := dialGate.Load(); g != nil {
			<-*g
		}
		return rex.ConnectSocketKeepalive(address)
	}
	validAddress = func(s string) bool {
		s = strings.TrimSpace(s)
		return strings.HasPrefix(s, "/") || filepath.IsAbs(s)
	}
	t.Cleanup(func() { dialHost, validAddress = oldDial, oldValid })
}

// startHostServer starts a second session server, as a remote host's, and
// returns its socket and a client of it.
func startHostServer(t *testing.T) (string, *rex.Client) {
	t.Helper()
	dir := testDir(t)
	old := os.Getenv("GOREX_DIR")
	os.Setenv("GOREX_DIR", dir)
	sock := rex.SocketPath()
	go rex.Serve()
	// The server reads its directory as it starts: it has once it answers.
	var c *rex.Client
	for i := 0; ; i++ {
		var err error
		if c, err = rex.ConnectSocket(sock); err == nil {
			if _, err = c.Hello(); err == nil {
				break
			}
			c.Close()
		}
		if i == 200 {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	os.Setenv("GOREX_DIR", old)
	t.Cleanup(func() {
		c.Shutdown()
		time.Sleep(100 * time.Millisecond)
		os.RemoveAll(dir)
	})
	return sock, c
}

// hostProxy forwards the connections of a socket of its own to a host's,
// as a network between them that drops: down closes them all and refuses
// new ones until up. While gate is set, new connections wait for it to
// close, as over a slow network; held counts those waiting. blackhole is
// a network gone without a word: nothing passes and nothing closes, new
// connections are accepted and dead too, and the flows of then stay dead
// once it heals, as a NAT that forgot them. While slow is set, what passes
// either way waits for it to close.
type hostProxy struct {
	path, to string
	ln       net.Listener
	mu       sync.Mutex
	conns    []net.Conn
	down     bool
	out      bool
	gen      int // of the network: a flow of an older one is dead
	gate     chan struct{}
	held     atomic.Int32
	slow     chan struct{}
}

// hold holds what passes either way, as a slow network, till release.
func (p *hostProxy) hold() (release func()) {
	g := make(chan struct{})
	p.mu.Lock()
	p.slow = g
	p.mu.Unlock()
	return sync.OnceFunc(func() {
		p.mu.Lock()
		p.slow = nil
		p.mu.Unlock()
		close(g)
	})
}

func (p *hostProxy) blackhole() {
	p.mu.Lock()
	p.out = true
	p.gen++
	p.mu.Unlock()
}

func (p *hostProxy) heal() {
	p.mu.Lock()
	p.out = false
	p.mu.Unlock()
}

// alive tells whether a flow of network gen still passes.
func (p *hostProxy) alive(gen int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.out && p.gen == gen
}

// pump copies src to dst while the flow of network gen passes; it closes
// dst as src closes, but once the flow is dead it passes nothing.
func (p *hostProxy) pump(gen int, dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		p.mu.Lock()
		slow := p.slow
		p.mu.Unlock()
		if slow != nil {
			<-slow
		}
		alive := p.alive(gen)
		if n > 0 && alive {
			dst.Write(buf[:n])
		}
		if err != nil {
			if alive {
				dst.Close()
			}
			return
		}
	}
}

func newHostProxy(t *testing.T, to string) *hostProxy {
	t.Helper()
	dir := testDir(t)
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := &hostProxy{path: filepath.Join(dir, "p.sock"), to: to}
	ln, err := net.Listen("unix", p.path)
	if err != nil {
		t.Fatal(err)
	}
	p.ln = ln
	t.Cleanup(func() { ln.Close(); p.setDown(true) })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			down, gate, out, gen := p.down, p.gate, p.out, p.gen
			if !down {
				p.conns = append(p.conns, c)
			}
			p.mu.Unlock()
			if down {
				c.Close()
				continue
			}
			if out { // unanswered: it reads what comes, until closed
				go io.Copy(io.Discard, c)
				continue
			}
			go func() {
				if gate != nil {
					p.held.Add(1)
					<-gate
					p.held.Add(-1)
				}
				s, err := net.Dial("unix", to)
				if err != nil {
					c.Close()
					return
				}
				p.mu.Lock()
				if p.down { // went down while it waited
					p.mu.Unlock()
					c.Close()
					s.Close()
					return
				}
				p.conns = append(p.conns, s)
				p.mu.Unlock()
				go p.pump(gen, s, c)
				p.pump(gen, c, s)
			}()
		}
	}()
	return p
}

func (p *hostProxy) setDown(down bool) {
	p.mu.Lock()
	p.down = down
	conns := p.conns
	if down {
		p.conns = nil
	}
	p.mu.Unlock()
	if down {
		for _, c := range conns {
			c.Close()
		}
	}
}

// sessionInfos lists the sessions of a server.
func sessionInfos(t *testing.T, c *rex.Client) []rex.SessionInfo {
	t.Helper()
	infos, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	return infos
}

// sessionIDs lists the sessions a server runs.
func sessionIDs(t *testing.T, c *rex.Client) []string {
	t.Helper()
	infos, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, in := range infos {
		if !in.Exited {
			ids = append(ids, in.ID)
		}
	}
	return ids
}

// hostTab opens a tab on host h in dir there, and returns it once the host
// made its session and the window shows it.
func hostTab(t *testing.T, a *App, tt *ui.Tester, h *hostConn, dir string) *Tab {
	t.Helper()
	n := len(a.tabsOn(h))
	a.newTabOn(h, dir)
	waitFor(t, tt, "the tab on the host", func() bool { return len(a.tabsOn(h)) > n })
	return a.tab()
}

func TestValidateHost(t *testing.T) {
	fakeHosts(t)
	hosts := []*hostConn{newHostConn(savedHost{ID: "1", Name: "office-mac", Address: "/tmp/a.sock"})}
	for _, tc := range []struct{ address, name, want string }{
		{"/tmp/b.sock", "studio", ""},
		{"/tmp/b.sock", " ", "Enter a host name."},
		{"", "studio", "Paste the host's address."},
		{"tcXYZ", "studio", locales.HostsBadDirectAddress()},
		{" /tmp/a.sock ", "studio", "That remote host is already saved."},
		{"/tmp/b.sock", "Office-Mac", "has that name already"},
	} {
		wantMessage(t, "validateHost("+tc.address+", "+tc.name+")", validateHost(tc.address, tc.name, hosts), tc.want)
	}
	for _, s := range []string{"", "  ", "/tmp/a.sock", "tcXYZ", "tailscale:user:node"} {
		if rex.ValidRemoteAddress(s) {
			t.Errorf("%q passes for a remote address", s)
		}
	}
}

func TestHostsFile(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	if hs, err := loadHosts(); err != nil || len(hs) != 0 {
		t.Fatalf("no file: %v, %v", hs, err)
	}
	want := []savedHost{
		{ID: "a1", Name: "office-mac", Address: "https://office.example.com:7443/#k=SECRET_TOKEN_ONE"},
		{ID: "b2", Name: "studio", Alias: "build box", Address: "https://studio.example.com:7443/#k=SECRET_TOKEN_TWO"},
	}
	if err := saveHosts(want); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("hosts.json has mode %v", st.Mode().Perm())
	}
	got, err := loadHosts()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, %v", got, err)
	}
	// The address is not in the settings.
	if b, _ := os.ReadFile(settingsPath()); strings.Contains(string(b), "SECRET_TOKEN_ONE") {
		t.Error("the settings hold an address")
	}

	// A file that does not parse is not written over: reading it moves it
	// aside, readable by the user only, and the next save starts anew.
	bad := `{"hosts": [{"id": "a1", "address": "https://office.example.com:7443/#k=SECRET_TOKEN_ONE"`
	os.WriteFile(hostsPath(), []byte(bad), 0o644)
	if err := saveHosts(want); err == nil {
		t.Error("saved over a file that does not parse")
	}
	if b, _ := os.ReadFile(hostsPath()); string(b) != bad {
		t.Errorf("the file became %q", b)
	}
	if hs, err := loadHosts(); err == nil || len(hs) != 0 || !strings.Contains(err.Error(), "moved aside") || strings.Contains(err.Error(), "SECRET_TOKEN_ONE") {
		t.Errorf("a bad file: %v, %v", hs, err)
	}
	aside, _ := filepath.Glob(hostsPath() + ".bad-*")
	if len(aside) != 1 {
		t.Fatalf("set aside: %v", aside)
	}
	if b, _ := os.ReadFile(aside[0]); string(b) != bad {
		t.Errorf("the file set aside holds %q", b)
	}
	if st, _ := os.Stat(aside[0]); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("the file set aside has mode %v", st.Mode().Perm())
	}
	if _, err := os.Stat(hostsPath()); err == nil {
		t.Error("hosts.json stays")
	}
	if err := saveHosts(want); err != nil {
		t.Errorf("saving after: %v", err)
	}
}

func TestHostBackoff(t *testing.T) {
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 5: 16 * time.Second, 6: 30 * time.Second, 40: 30 * time.Second} {
		if got := hostBackoff(n); got != want {
			t.Errorf("hostBackoff(%d) = %v, want %v", n, got, want)
		}
	}
}

// TestHostCreateHoldsNothing opens panes on a host that answers late: the
// window goes on meanwhile, without them, and they show as it answers, in
// the order asked: a split, then two tabs after the active one.
func TestHostCreateHoldsNothing(t *testing.T) {
	a, tt := newTestApp(t)
	h, proxy, p := remotePane(t, a, tt, t.TempDir())
	tab := p.Tab
	release := proxy.hold()
	defer release()
	late := time.AfterFunc(3*time.Second, release) // were the window held
	defer late.Stop()
	start := time.Now()
	a.split(false)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the split held the window %v", d)
	}
	time.Sleep(200 * time.Millisecond)
	tt.Frame()
	if len(tab.panes()) != 1 || tab.Focus != p {
		t.Fatal("the split showed before the host answered")
	}
	release()
	waitFor(t, tt, "the split", func() bool { return len(tab.panes()) == 2 })
	if q := tab.Focus; q == p || q.host != h || q.SID == "" || q.term == nil {
		t.Fatalf("the split is %+v", q)
	}

	release = proxy.hold()
	a.newTabOn(h, "")
	a.newTabOn(h, "")
	if len(a.tabsOn(h)) != 1 {
		t.Fatal("a tab showed before the host answered")
	}
	release()
	waitFor(t, tt, "the two tabs", func() bool { return len(a.tabsOn(h)) == 3 })
	// The host made them, after the tab active then, the window showing
	// the last made.
	if tabs := a.tabsOn(h); tabs[0] != tab || a.tab() == tab {
		t.Errorf("the tabs: first %v, active the first %v", tabs[0] == tab, a.tab() == tab)
	}
}

// TestHostCreateFails splits a host's pane while the host does not
// answer: the error shows once the client gives up, and the tab is as it
// was.
func TestHostCreateFails(t *testing.T) {
	a, tt := newTestApp(t)
	_, proxy, p := remotePane(t, a, tt, t.TempDir())
	tab := p.Tab
	defer rex.SetTimeoutsForTest(30*time.Second, 500*time.Millisecond)()
	proxy.blackhole()
	a.split(false)
	waitFor(t, tt, "the error", func() bool { return strings.Contains(a.err, "timed out") })
	if len(tab.panes()) != 1 || tab.Focus != p || len(a.tabs) != 1 {
		t.Errorf("the tab changed: %d panes, %d tabs", len(tab.panes()), len(a.tabs))
	}
}

// TestHostBlackholeReattaches is a remote pane whose network goes away
// without a word, again and again, as the Mac's to a Windows host on an
// unstable network: each time the app connects anew and the pane attaches
// again. The host must count one window attached to the session, not one
// more per drop, and the app must keep no goroutine of the connections
// gone.
func TestHostBlackholeReattaches(t *testing.T) {
	defer rex.SetTimeoutsForTest(600*time.Millisecond, 2*time.Second)()
	fakeHosts(t)
	oldHealthy := hostHealthy
	hostHealthy = 0 // a drop tries again at once
	t.Cleanup(func() { hostHealthy = oldHealthy })
	sock, remote := startHostServer(t)
	info, err := remote.Create(rex.CreateOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	proxy := newHostProxy(t, sock)
	h := newHostConn(savedHost{ID: "h1", Name: "office-pc", Address: proxy.path})
	a := &App{}
	go a.runHost(h, func(fn func()) {})
	t.Cleanup(h.close)
	s := newHostStream(h, info.ID, 80, 24)
	t.Cleanup(func() { s.Close() })
	go func() {
		buf := make([]byte, 32<<10)
		for {
			if _, err := s.Read(buf); err != nil {
				return
			}
		}
	}()
	attached := func() int {
		infos, err := remote.List()
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range infos {
			if in.ID == info.ID {
				return in.Attached
			}
		}
		t.Fatal("the session is gone")
		return 0
	}
	attaches := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.attaches
	}
	until := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; attached %d", what, attached())
			}
		}
	}

	const cycles = 4
	base := 0
	var counts []int
	for i := range cycles {
		until("the pane to attach", func() bool { return attaches() == i+1 && !s.Detached() })
		if i == 0 {
			time.Sleep(200 * time.Millisecond)
			base = runtime.NumGoroutine()
		}
		proxy.blackhole()
		until("the pane to notice", s.Detached)
		proxy.heal()
		h.poke()
		until("the pane to attach again", func() bool { return attaches() == i+2 && !s.Detached() })
		// The host drops the connections of before in a bounded time.
		for deadline := time.Now().Add(5 * time.Second); attached() != 1 && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
		}
		counts = append(counts, attached())
	}
	for _, n := range counts {
		if n != 1 {
			t.Fatalf("the host counts %v windows attached after each drop, want 1 each time", counts)
		}
	}
	// What is left of each drop, on the app's side: nothing, but the dead
	// flows of the proxy that wait for their ends to close.
	want := base + 4
	n := runtime.NumGoroutine()
	for deadline := time.Now().Add(5 * time.Second); n > want && time.Now().Before(deadline); n = runtime.NumGoroutine() {
		time.Sleep(20 * time.Millisecond)
	}
	if n > want {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Errorf("%d goroutines after %d drops, want at most %d\n%s", n, cycles, want, buf)
	}
}

// TestLocalStreamReattaches is a pane of this machine's session whose
// stream's connection went away, as a server that drops a client closes
// it, while the app's client lives: the pane attaches again by itself,
// and takes what is typed again. Left as it was, the pane kept the screen
// it last showed and dropped every key, with nothing said.
func TestLocalStreamReattaches(t *testing.T) {
	sock, remote := startHostServer(t)
	info, err := remote.Create(rex.CreateOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	c, err := rex.ConnectSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Hello(); err != nil {
		t.Fatal(err)
	}
	s := newLocalStream(c, info.ID, 80, 24)
	t.Cleanup(func() { s.Close() })
	go func() {
		buf := make([]byte, 32<<10)
		for {
			if _, err := s.Read(buf); err != nil {
				return
			}
		}
	}()
	attaches := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.attaches
	}
	until := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; attached %d", what, attaches())
			}
		}
	}
	until("the pane to attach", func() bool { return attaches() == 1 && !s.Detached() })
	// The stream's connection ends, as a server's does for the clients it
	// drops; the app's own connection stays.
	s.mu.Lock()
	st := s.cur
	s.mu.Unlock()
	if st == nil {
		t.Fatal("the stream of the pane is gone")
	}
	st.Close()
	until("the pane to notice", s.Detached)
	until("the pane to attach again", func() bool { return attaches() == 2 && !s.Detached() })
	// What is typed reaches the session again.
	var before time.Time
	for _, in := range sessionInfos(t, remote) {
		if in.ID == info.ID {
			before = in.LastInput
		}
	}
	s.Write([]byte("x"))
	until("the session to take what is typed", func() bool {
		for _, in := range sessionInfos(t, remote) {
			if in.ID == info.ID {
				return in.LastInput.After(before)
			}
		}
		return false
	})
	// One window attached, not one more per drop.
	attached := 0
	for _, in := range sessionInfos(t, remote) {
		if in.ID == info.ID {
			attached = in.Attached
		}
	}
	if attached != 1 {
		t.Errorf("the server counts %d windows attached, want 1", attached)
	}
}

// TestLocalStreamTakesWhatIsTypedAsItAttaches is a pane of this machine
// made a moment ago, before the view asked its size: what is typed then
// reaches the program once its first stream attached, rather than going
// nowhere — a shell asked to exit as its pane opens still exits.
func TestLocalStreamTakesWhatIsTypedAsItAttaches(t *testing.T) {
	sock, remote := startHostServer(t)
	info, err := remote.Create(rex.CreateOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	c, err := rex.ConnectSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Hello(); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	for _, in := range sessionInfos(t, remote) {
		if in.ID == info.ID {
			before = in.LastInput
		}
	}
	s := newLocalStream(c, info.ID, 80, 24)
	t.Cleanup(func() { s.Close() })
	// Typed before the first stream attached: the terminal reads after.
	if n, err := s.Write([]byte("x")); err != nil || n != 1 {
		t.Fatalf("write = %d, %v", n, err)
	}
	go func() {
		buf := make([]byte, 32<<10)
		for {
			if _, err := s.Read(buf); err != nil {
				return
			}
		}
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		taken := false
		for _, in := range sessionInfos(t, remote) {
			if in.ID == info.ID {
				taken = in.LastInput.After(before)
			}
		}
		if taken {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the session to take what was typed before it attached")
		}
	}
}

// TestLocalPaneTakesKeysAfterItsStreamWasLost is the lesson: a pane of
// this machine's session whose stream's connection ended says so over the
// screen it last showed, attaches again by itself, and takes what is
// typed again — at the shell, as a user types it. Left as it was, the
// pane stood there taking no key, with nothing said.
func TestLocalPaneTakesKeysAfterItsStreamWasLost(t *testing.T) {
	a, tt := newTestApp(t)
	p := a.tab().Focus
	if p == nil || p.stream == nil {
		t.Fatal("the tab has no pane of this machine")
	}
	waitFor(t, tt, "the shell", func() bool {
		return p.term != nil && strings.Contains(p.term.Text(), testShell.prompt)
	})
	// The stream's connection ends, as it does for the clients a server
	// drops; the app's own connection stays.
	p.stream.mu.Lock()
	st := p.stream.cur
	p.stream.mu.Unlock()
	if st == nil {
		t.Fatal("the stream of the pane is gone")
	}
	st.Close()
	for deadline := time.Now().Add(5 * time.Second); !p.stream.Detached(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the pane did not notice the stream was lost")
		}
	}
	// A stream lost for a moment says nothing (streamNoteDelay), and what
	// is typed while it is away waits for it: the attach again writes it in
	// order, and the shell runs it.
	cmd := "echo AFTER_LOSS_$((6*7))\r"
	if runtime.GOOS == "windows" {
		cmd = `powershell -NoProfile -Command "Write-Host ('AFTER_LOSS_' + (6*7))"` + "\r"
	}
	p.term.Send([]byte(cmd))
	waitFor(t, tt, "the stream to attach again", func() bool {
		return !p.stream.Detached()
	})
	waitFor(t, tt, "the screen the session sends again", func() bool {
		return strings.Contains(p.term.Text(), testShell.prompt)
	})
	waitFor(t, tt, "the shell's output of what was typed while away", func() bool {
		return strings.Contains(p.term.Text(), "AFTER_LOSS_42")
	})
}

// logBuf is a log sink that tolerates the app's goroutines writing to it.
type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestLostStreamIsLogged: a pane's stream that is lost and attaches again
// leaves one line for each, with the session, so that a "stream
// interrupted" note can be traced.
func TestLostStreamIsLogged(t *testing.T) {
	a, tt := newTestApp(t)
	p := a.tab().Focus
	if p == nil || p.stream == nil {
		t.Fatal("the tab has no pane of this machine")
	}
	waitFor(t, tt, "the shell", func() bool {
		return p.term != nil && strings.Contains(p.term.Text(), testShell.prompt)
	})
	var buf logBuf
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	p.stream.mu.Lock()
	st := p.stream.cur
	p.stream.mu.Unlock()
	st.Close()
	waitFor(t, tt, "the stream to attach again", func() bool {
		return strings.Contains(buf.String(), "pane stream back:")
	})
	out := buf.String()
	sid := "sid=" + p.stream.sid
	if n := strings.Count(out, "pane stream lost: host=local "+sid+" after="); n != 1 {
		t.Errorf("%d lost lines, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "pane stream back: host=local "+sid+" away="); n != 1 {
		t.Errorf("%d back lines, want 1:\n%s", n, out)
	}
}

// TestLostStreamWaitsForItsInput is the bound on what a pane keeps while
// its stream is away: typed at once it waits, in order, and typed 10
// seconds after the screen stopped it does not — a program the user has
// stopped seeing takes no key. The pane says so a second after the loss at
// the earliest.
func TestLostStreamWaitsForItsInput(t *testing.T) {
	s := newLocalStream(nil, "sid", 80, 24)
	defer s.Close()
	if d := s.DetachedFor(); d != 0 {
		t.Errorf("an attached stream says it was lost for %v", d)
	}
	s.mu.Lock()
	// A millisecond back: a coarse clock (Windows) can read the same
	// instant twice, and a duration of 0.
	s.detached, s.detachedAt = true, time.Now().Add(-time.Millisecond)
	s.mu.Unlock()
	if d := s.DetachedFor(); d <= 0 || d >= streamNoteDelay {
		t.Errorf("a stream just lost says it was lost for %v, want under %v", d, streamNoteDelay)
	}
	s.Write([]byte("ab"))
	s.Write([]byte("cd"))
	s.mu.Lock()
	queued := string(s.queued)
	s.mu.Unlock()
	if queued != "abcd" {
		t.Errorf("what was typed while away waited as %q, want \"abcd\"", queued)
	}
	// The bound in size: what goes past it is dropped, not held.
	s.mu.Lock()
	s.queued = []byte(strings.Repeat("x", maxQueuedInput-1))
	s.mu.Unlock()
	s.Write([]byte("yz"))
	s.mu.Lock()
	n := len(s.queued)
	s.mu.Unlock()
	if n != maxQueuedInput {
		t.Errorf("the queue took %d bytes, want %d", n, maxQueuedInput)
	}
	// The bound in time: past queuedInputTTL the keys are dropped, and
	// what waited before them with them.
	s.mu.Lock()
	s.detachedAt = time.Now().Add(-queuedInputTTL - time.Second)
	s.mu.Unlock()
	s.Write([]byte("late"))
	s.mu.Lock()
	n = len(s.queued)
	s.mu.Unlock()
	if n != 0 {
		t.Errorf("what was typed %v after the loss waited: %d bytes", queuedInputTTL, n)
	}
	if d := s.DetachedFor(); d < queuedInputTTL {
		t.Errorf("a stream lost %v ago says %v", queuedInputTTL, d)
	}
}

// TestHostStreamResizeWhileAttaching resizes a remote pane while its
// stream attaches, as the view does as it first lays out a new tab, made
// at 80x24, over a slow network: the session takes the window's size. At
// the old one, a Windows host's console draws its scrolling rows for a
// screen other than the window's, and the lines of ls land on each other.
func TestHostStreamResizeWhileAttaching(t *testing.T) {
	sock, remote := startHostServer(t)
	info, err := remote.Create(rex.CreateOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	proxy := newHostProxy(t, sock)
	c, err := rex.ConnectSocket(proxy.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	h := newHostConn(savedHost{ID: "h1", Name: "office-pc"})
	h.client = c
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	proxy.mu.Lock()
	proxy.gate = gate
	proxy.mu.Unlock()

	s := newHostStream(h, info.ID, 80, 24)
	t.Cleanup(func() { s.Close() })
	// The stream returns what the session prints once it attached and
	// told the session its size.
	printed := make(chan struct{})
	go func() {
		buf := make([]byte, 32<<10)
		for first := true; ; first = false {
			if _, err := s.Read(buf); err != nil {
				return
			}
			if first {
				close(printed)
			}
		}
	}()
	for proxy.held.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	s.Resize(121, 41)
	open()
	select {
	case <-printed:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream does not attach")
	}
	// The session takes the size the stream told it once attached, as its
	// last size settles (the server debounces a window's resize).
	waitForThat(t, func() bool {
		for _, in := range sessionInfos(t, remote) {
			if in.ID == info.ID {
				return in.Cols == 121 && in.Rows == 41
			}
		}
		return false
	}, "the session to take the window's size")
}

// What a fake host does with a connection.
const (
	fakeRefuse   int32 = iota // says it refuses, and closes
	fakeApproved              // serves hello and list
	fakeSilent                // closes, saying nothing, as a connection lost
	fakeFlaky                 // greets, then drops
)

// fakeHost is a test server on a Unix socket that counts connections made to it.
type fakeHost struct {
	path  string
	mode  atomic.Int32
	conns atomic.Int32
}

func newFakeHost(t *testing.T, mode int32) *fakeHost {
	t.Helper()
	dir := testDir(t)
	f := &fakeHost{path: filepath.Join(dir, "s.sock")}
	f.mode.Store(mode)
	ln, err := net.Listen("unix", f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); os.RemoveAll(dir) })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.conns.Add(1)
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeHost) serve(c net.Conn) {
	defer c.Close()
	reply := func(res rex.Response) {
		b, _ := json.Marshal(res)
		c.Write(append(b, '\n'))
	}
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req rex.Request
		json.Unmarshal(line, &req)
		res := rex.Response{ID: req.ID}
		switch f.mode.Load() {
		case fakeSilent:
			return
		case fakeRefuse:
			reply(rex.Response{Error: "rex: this machine is not allowed here"})
			return
		case fakeFlaky:
			if req.Op != "hello" {
				return
			}
			res.Data, _ = json.Marshal(rex.Hello{PID: 7, Version: rex.ProtocolVersion})
		default:
			if req.Op == "list" {
				res.Data = json.RawMessage("[]")
			} else {
				res.Data, _ = json.Marshal(rex.Hello{PID: 7, Version: rex.ProtocolVersion})
			}
		}
		reply(res)
	}
}

// hostsPage is the Remote Hosts page of an app with no terminal window.
func hostsPage(t *testing.T) (*App, *ui.Tester) {
	isolateSettings(t, "")
	t.Setenv("GOREX_DIR", t.TempDir())
	fakeHosts(t)
	a := &App{}
	t.Cleanup(a.stopHosts)
	t.Cleanup(closeAddHost)
	settingsUI.section = "hosts"
	tt := ui.NewTester(a.settingsView, 780, 540)
	tt.Frame()
	return a, tt
}

// TestAddHostSheet adds a host through the sheet: fields are validated,
// unreachable hosts return an error without saving, and a reachable host
// is saved and connects.
func TestAddHostSheet(t *testing.T) {
	a, tt := hostsPage(t)
	f := newFakeHost(t, fakeApproved)
	if !tt.HasText("No remote hosts yet.") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("Add Remote Host…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Paste the host's address.") {
		t.Errorf("no error for an empty form; texts %q", tt.Texts())
	}
	tt.Type(f.path) // the address field has the focus
	hostsUI.name = "office-mac"
	tt.Frame()
	if tt.HasText(f.path) {
		t.Error("the address shows")
	}
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "the host to be saved", func() bool { return len(a.hosts) == 1 && !hostsUI.adding })
	if hostsUI.address != "" {
		t.Error("the sheet keeps the address")
	}
	if hs, _ := loadHosts(); len(hs) != 1 || hs[0].Address != f.path || hs[0].Name != "office-mac" {
		t.Errorf("hosts.json has %v", hs)
	}
	waitFor(t, tt, "the host to connect", func() bool { return a.hosts[0].state == hostConnected && tt.HasText("Connected") })

	// An unreachable host is not saved and shows the error.
	other := newFakeHost(t, fakeSilent)
	a.openAddHost()
	hostsUI.address, hostsUI.name = other.path, "studio"
	a.submitHost()
	waitFor(t, tt, "the error", func() bool { return hostsUI.stage == addForm && hostsUI.formErr != nil })
	if msg := hostsUI.formErr(); !strings.Contains(msg, "Could not reach the host") {
		t.Errorf("error %q, want unreachable", msg)
	}
	if len(a.hosts) != 1 {
		t.Error("an unreachable host was saved")
	}
}

// TestHostFailures checks what a saved host says when it is not reached,
// and when it returns an error on hello.
func TestHostFailures(t *testing.T) {
	a, tt := hostsPage(t)
	f := newFakeHost(t, fakeSilent)
	h := a.addHost(savedHost{ID: "h1", Name: "studio", Address: f.path})
	waitFor(t, tt, "the failure", func() bool { return h.state == hostFailed })
	if _, detail, _ := hostStatus(h); detail != locales.HostsUnreachable() {
		t.Errorf("lost on the way: %q", detail)
	}
	f.mode.Store(fakeRefuse)
	h.poke()
	waitFor(t, tt, "the refusal", func() bool {
		_, detail, _ := hostStatus(h)
		return detail == "This machine is not allowed here"
	})
}

// TestRenameHost renames a saved host in its row: Enter keeps the alias,
// Escape goes back, and an empty one shows the host's name again; the
// host stays as it was.
func TestRenameHost(t *testing.T) {
	a, tt := hostsPage(t)
	f := newFakeHost(t, fakeApproved)
	h := a.addHost(savedHost{ID: "h1", Name: "ws-01", Address: f.path})
	waitFor(t, tt, "the host to connect", func() bool { return h.state == hostConnected })
	c := h.current()
	rename := func(text string, key ui.Key) {
		t.Helper()
		if err := tt.Click(locales.HostsRename()); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
		// The old name is selected, and goes.
		if text == "" {
			tt.Key(0, ui.KeyBackspace)
		} else {
			tt.Type(text)
		}
		tt.Key(0, key)
		tt.Frame()
	}
	rename("build box", ui.KeyEnter)
	if !tt.HasText("build box") || tt.HasText("ws-01") || h.Name != "ws-01" {
		t.Fatalf("renamed: %+v; texts %q", h.savedHost, tt.Texts())
	}
	if hs, _ := loadHosts(); len(hs) != 1 || hs[0].Alias != "build box" {
		t.Fatalf("hosts.json: %+v", hs)
	}
	rename("other", ui.KeyEscape)
	if h.Alias != "build box" || tt.HasText("other") {
		t.Fatalf("Escape kept %q", h.Alias)
	}
	rename("", ui.KeyEnter)
	if h.Alias != "" || !tt.HasText("ws-01") {
		t.Fatalf("cleared: %+v; texts %q", h.savedHost, tt.Texts())
	}
	if h.current() != c || h.state != hostConnected {
		t.Error("renaming the host reconnected it")
	}

	// An alias another host goes by keeps the field open, and says so.
	a.addHost(savedHost{ID: "h2", Name: "studio", Address: f.path})
	tt.Frame()
	// The Rename button of a row, as far below its name as the first's.
	first, _ := tt.Find(locales.HostsRename())
	n1, _ := tt.Find("ws-01")
	rename("Studio", ui.KeyEnter)
	if h.Alias != "" || hostsUI.renaming != h.ID || !tt.HasText(locales.HostsNameTaken()) {
		t.Fatalf("a taken alias: %+v, editing %q; texts %q", h.savedHost, hostsUI.renaming, tt.Texts())
	}

	// Rename on another host keeps the alias being edited, as moving the
	// focus away does, and edits the other's.
	tt.Key(ui.Super, ui.KeyA)
	tt.Type("build box")
	tt.Frame()
	if tt.HasText(locales.HostsNameTaken()) {
		t.Error("the hint stays as another alias is typed")
	}
	n2, _ := tt.Find("studio")
	tt.ClickAt(first.X+first.W/2, n2.Y+first.Y+first.H/2-n1.Y)
	tt.Frame()
	if h.Alias != "build box" || hostsUI.renaming != "h2" || tt.HasText(locales.HostsNameTaken()) {
		t.Fatalf("switching: %+v, editing %q; texts %q", h.savedHost, hostsUI.renaming, tt.Texts())
	}

	// Removing the host, or closing the page, forgets its edit.
	a.removeHost(a.hostByID("h2"))
	if hostsUI.renaming != "" {
		t.Errorf("the removed host is still edited: %q", hostsUI.renaming)
	}
	hostsUI.renaming, hostsUI.alias = h.ID, "abandoned"
	closeHostRename()
	tt.Frame()
	if h.Alias != "build box" || hostsUI.alias != "" {
		t.Errorf("an abandoned edit was kept: %+v", h.savedHost)
	}
}

// TestHostDropsBackOff checks that a host that greets and drops at once is
// not dialed in a loop: the failures wait longer each time.
func TestHostDropsBackOff(t *testing.T) {
	a, tt := hostsPage(t)
	f := newFakeHost(t, fakeFlaky)
	h := a.addHost(savedHost{ID: "h1", Name: "studio", Address: f.path})
	deadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(50 * time.Millisecond)
	}
	// Dialed at 0, then after 1 s and 2 s more.
	if n := f.conns.Load(); n < 2 || n > 4 {
		t.Errorf("%d connections in 3.5 s", n)
	}
	if h.attempt < 2 {
		t.Errorf("attempt %d", h.attempt)
	}

	// Up a while, a drop tries again at once.
	old := hostHealthy
	hostHealthy = 0
	t.Cleanup(func() { hostHealthy = old })
	f.mode.Store(fakeApproved)
	h.poke()
	waitFor(t, tt, "the host to connect", func() bool { return h.state == hostConnected })
	n := f.conns.Load()
	f.mode.Store(fakeSilent) // the next list drops it
	waitFor(t, tt, "the host to drop", func() bool { return h.state != hostConnected })
	f.mode.Store(fakeApproved)
	waitFor(t, tt, "the host to connect again", func() bool { return h.state == hostConnected })
	if f.conns.Load() == n {
		t.Error("no new connection")
	}
}

// TestPendingKills checks that closing a host's tab while it is away ends
// its session once the host is back, though the app quit meanwhile.
func TestPendingKills(t *testing.T) {
	a, tt := newTestApp(t)
	fakeHosts(t)
	sock, remote := startHostServer(t)
	proxy := newHostProxy(t, sock)
	t.Cleanup(a.stopHosts)
	h := a.addHost(savedHost{ID: "h1", Name: "studio", Address: proxy.path})
	waitFor(t, tt, "the host to connect", func() bool { return h.state == hostConnected })
	rt := hostTab(t, a, tt, h, "")
	sid := rt.Focus.SID
	proxy.setDown(true)
	waitFor(t, tt, "the host to drop", func() bool { return h.state != hostConnected && h.current() == nil })
	a.closeTab(rt)
	if hs, _ := loadHosts(); len(hs) != 1 || !slices.Contains(hs[0].Kills, sid) {
		t.Fatalf("hosts.json has %+v", hs)
	}
	if !slices.Contains(sessionIDs(t, remote), sid) {
		t.Fatal("the session ended while the host was away")
	}
	// The app quits, and opens again with the host back.
	a.stopHosts()
	proxy.setDown(false)
	b := &App{client: a.client}
	b.loadHosts()
	t.Cleanup(b.stopHosts)
	tb := ui.NewTester(b.view, 1000, 620)
	waitFor(t, tb, "the session to end on the host", func() bool { return !slices.Contains(sessionIDs(t, remote), sid) })
	waitFor(t, tb, "the kill to be forgotten", func() bool {
		hs, _ := loadHosts()
		return len(hs) == 1 && len(hs[0].Kills) == 0
	})
}

// TestTabWidthBadge checks that only a tab with a host's badge grows past
// the widest of tabs.
func TestTabWidthBadge(t *testing.T) {
	registerFonts()
	h := newHostConn(savedHost{ID: "h1", Name: "office-mac"})
	tab := func(host *hostConn) *Tab {
		t := &Tab{Name: strings.Repeat("a very long name ", 4)}
		p := &Pane{host: host, Tab: t}
		t.Root = &Node{Pane: p}
		p.Node = t.Root
		return t
	}
	local, remote := tab(nil), tab(h)
	var lw, rw float32
	tt := ui.NewTester(func(c *ui.Context) { lw, rw = tabWidth(c, local), tabWidth(c, remote) }, 400, 100)
	tt.Frame()
	if lw != tabMaxW {
		t.Errorf("a long local tab is %v wide, not %v", lw, tabMaxW)
	}
	if rw <= tabMaxW || rw > tabMaxW+badgeMaxW+21 {
		t.Errorf("a long remote tab is %v wide", rw)
	}
}

func TestHostMessagesInChinese(t *testing.T) {
	zh, en := locales.In("zh-Hans"), locales.In("en")
	for got, want := range map[string]string{
		zh.HostsReconnecting(3):  "正在重新连接（第 3 次）…",
		zh.HostsSessions(2):      "2 个终端",
		en.HostsSessions(1):      "1 terminal",
		en.HostsSessions(0):      "No terminals",
		zh.CmdNewTabOn("studio"): "在 studio 上新建标签页",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// TestNetworkHostsLoad: a host a coordinator network once brought, kept
// in hosts.json with "network": true, is dropped as it loads: no machine
// admits it as a member any more.
func TestNetworkHostsLoad(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	old := `{"hosts": [{"id": "net:nodekey:01", "name": "bob", "address": "tcBOB", "network": true}]}`
	if err := os.WriteFile(hostsPath(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := loadHosts(); err != nil || len(got) != 0 {
		t.Errorf("loaded %+v, %v", got, err)
	}
}

// TestLegacyHostsFiltered checks Decision A=1 through loadHosts: legacy
// tailscale:, tailcat (tc.../tcat://), and net: entries are filtered out
// on load while Direct TLS and Relay hosts are preserved intact.
func TestLegacyHostsFiltered(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	mixed := `{"hosts": [
		{"id": "net:nodekey:01", "name": "old-net", "address": "tcNET"},
		{"id": "tailscale:a:1", "name": "tailnet-box", "address": "tailscale:a:1", "tailnetOpened": true},
		{"id": "m1", "name": "legacy-tc", "address": "tcMANUAL"},
		{"id": "m2", "name": "legacy-tcat", "address": "tcat://legacy"},
		{"id": "d1", "name": "direct-box", "alias": "Build Box", "address": "https://203.0.113.10:7443/#k=tok1", "icon": "server"},
		{"id": "r1", "name": "relay-box", "address": "https://relay.example.com:7443/h/h1/#k=tok2"}
	]}`
	if err := os.WriteFile(hostsPath(), []byte(mixed), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadHosts()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, h := range got {
		ids = append(ids, h.ID)
	}
	if want := []string{"d1", "r1"}; !slices.Equal(ids, want) {
		t.Errorf("loaded %v, want %v", ids, want)
	}
}

// TestHostAlias: the alias the user gives a host is what it shows by,
// and clearing it, or typing the host's own name, shows that name again.
func TestHostAlias(t *testing.T) {
	a, _ := newTestApp(t)
	fakeHosts(t)
	t.Cleanup(a.stopHosts)
	h := a.addHost(savedHost{ID: "h-ws01", Name: "ws-01", Address: "https://ws-01.example.com:7443/#k=tok1"})
	a.setHostAlias(h, "  build box ")
	if a.hostName(h) != "build box" || h.Name != "ws-01" {
		t.Fatalf("alias %q, name %q", a.hostName(h), h.Name)
	}
	if hs, _ := loadHosts(); len(hs) != 1 || hs[0].Name != "ws-01" || hs[0].Alias != "build box" {
		t.Fatalf("hosts.json: %+v", hs)
	}
	// A new host takes neither name nor alias of another.
	for _, name := range []string{"Build Box", "WS-01"} {
		if validateHost("/tmp/other", name, a.hosts) != locales.HostsNameTaken() {
			t.Errorf("%q is not taken", name)
		}
	}
	// Nor does an alias: another host's name or alias is refused.
	other := a.addHost(savedHost{ID: "m1", Name: "studio", Address: "/tmp/nowhere"})
	for _, alias := range []string{"WS-01", "build box"} {
		if a.setHostAlias(other, alias) || other.Alias != "" {
			t.Errorf("alias %q taken: %+v", alias, other.savedHost)
		}
	}
	// An alias that does not change saves nothing.
	os.Remove(hostsPath())
	if !a.setHostAlias(h, "build box") {
		t.Error("the same alias is refused")
	}
	if _, err := os.Stat(hostsPath()); err == nil {
		t.Error("the same alias saved the hosts")
	}
	for _, alias := range []string{"", "ws-01"} {
		a.setHostAlias(h, "x")
		a.setHostAlias(h, alias)
		if h.Alias != "" || a.hostName(h) != "ws-01" {
			t.Errorf("alias %q: %+v", alias, h.savedHost)
		}
	}
}

// TestTakeKillsOnce takes the layout's kills with no server to tell: the
// app keeps them to end once its hosts are read (3), all of a host's in
// one save of hosts.json (4); it tells the server of one of a host it does
// not have only when hosts.json read (2), and of one of its hosts only
// once saved (2); and an older layout that still has a kill taken takes
// it no more (1).
func TestTakeKillsOnce(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	st := &store{}
	a := &App{st: st}
	st.apps = []*App{a}
	acks := func() [][]layout.Kill {
		var out [][]layout.Kill
		for _, x := range st.out.queue {
			if x.cmd.Op == "kills.taken" {
				out = append(out, x.cmd.Kills)
			}
		}
		return out
	}
	k1, k2, gone := layout.Kill{Host: "h1", SID: "s1"}, layout.Kill{Host: "h1", SID: "s2"}, layout.Kill{Host: "h9", SID: "s9"}
	st.base.Kills = []layout.Kill{k1, k2, gone}
	st.takeKills(a)
	if len(acks()) != 0 {
		t.Fatal("kills taken before the hosts were read")
	}
	h := newHostConn(savedHost{ID: "h1", Name: "studio", Address: "/nowhere"})
	a.hosts = []*hostConn{h}
	a.syncHosts()
	if hs, _ := loadHosts(); len(hs) != 1 || !slices.Equal(hs[0].Kills, []string{"s1", "s2"}) {
		t.Fatalf("hosts.json has %+v", hs)
	}
	if got := acks(); len(got) != 1 || !slices.Equal(got[0], []layout.Kill{gone, k1, k2}) {
		t.Fatalf("told %+v", got)
	}
	// The host ended them; a layout read before the server forgot them
	// takes none again.
	h.mu.Lock()
	h.Kills = nil
	h.mu.Unlock()
	st.takeKills(a)
	if len(acks()) != 1 || len(h.saved().Kills) != 0 {
		t.Fatalf("taken again: told %+v, host keeps %v", acks(), h.saved().Kills)
	}
	// Once a layout no longer has them, they are forgotten.
	st.out.queue = nil
	st.base.Kills = nil
	st.takeKills(a)
	if len(st.killsTaken) != 0 {
		t.Fatalf("still taken: %v", st.killsTaken)
	}
	// hosts.json would not read, nor be written: the kills are kept to end,
	// and the server is told of none.
	st.hostsLost = true
	if err := os.WriteFile(hostsPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	st.base.Kills = []layout.Kill{k1, gone}
	st.takeKills(a)
	if len(acks()) != 0 || !slices.Contains(h.saved().Kills, "s1") {
		t.Errorf("told %+v, host keeps %v", acks(), h.saved().Kills)
	}
}

// TestLearnMachine checks that a host keeps the machine its last hello
// named, and none after a hello that names none.
func TestLearnMachine(t *testing.T) {
	h := newHostConn(savedHost{ID: "h", Address: "a"})
	if !h.learn(rex.Hello{Machine: "m1"}) || h.saved().Machine != "m1" {
		t.Fatalf("learned %q", h.saved().Machine)
	}
	if h.learn(rex.Hello{Machine: "m1"}) {
		t.Error("the same machine changed it")
	}
	if !h.learn(rex.Hello{}) || h.saved().Machine != "" {
		t.Errorf("a hello with no machine kept %q", h.saved().Machine)
	}
}

// TestLearnGlyph checks that a host keeps the icon of the machine its
// last hello described, and shows a server before one did.
func TestLearnGlyph(t *testing.T) {
	h := newHostConn(savedHost{ID: "h", Address: "a"})
	if g := h.remoteGlyph(); g != "server" {
		t.Errorf("before a hello: %q", g)
	}
	for _, c := range []struct {
		host rex.HostInfo
		want string
	}{
		{rex.HostInfo{Model: "MacBook Pro", OS: "macOS 26.1"}, "laptop"},
		{rex.HostInfo{Model: "Mac mini", OS: "macOS 26.1"}, "mac-mini"},
		{rex.HostInfo{Model: "Mac Studio", OS: "macOS 26.1"}, "mac-studio"},
		{rex.HostInfo{Model: "Linux", OS: "Debian GNU/Linux 13"}, "server"},
		{rex.HostInfo{Model: "Z790", OS: "Windows 11 Pro"}, "monitor"},
		{rex.HostInfo{Model: "Z790", OS: "Windows 11 Pro", Portable: true}, "laptop"},
	} {
		if !h.learn(rex.Hello{Host: c.host}) || h.remoteGlyph() != c.want || h.saved().Glyph != c.want {
			t.Errorf("%+v: %q, saved %q, want %q", c.host, h.remoteGlyph(), h.saved().Glyph, c.want)
		}
	}
	if h.learn(rex.Hello{}) || h.remoteGlyph() != "laptop" {
		t.Errorf("a hello that describes no machine changed the icon to %q", h.remoteGlyph())
	}
}

// TestHostIcon chooses a host's icon from its row in Settings › Hosts:
// the choice goes before the icon its machine has, is kept in hosts.json
// through hellos and a restart, and Automatic follows the machine again.
func TestHostIcon(t *testing.T) {
	a, tt := hostsPage(t)
	h := a.addHost(savedHost{ID: "h1", Name: "build-box", Address: "/tmp/nowhere"})
	h.learn(rex.Hello{Host: rex.HostInfo{Model: "Mac mini", OS: "macOS 26.1"}})
	tt.Frame()
	choose := func(item string) {
		t.Helper()
		if err := tt.Click(locales.HostsIcon() + ", build-box"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.ChooseMenuItem(item); err != nil {
			t.Fatalf("%v; menu %q", err, tt.Menu())
		}
		tt.Frame()
	}
	choose(locales.HostIconCloud())
	if g := h.remoteGlyph(); g != "cloud" {
		t.Fatalf("chose the cloud, shows %q", g)
	}
	if hs, _ := loadHosts(); len(hs) != 1 || hs[0].Icon != "cloud" || hs[0].Glyph != "mac-mini" {
		t.Fatalf("hosts.json: %+v", hs)
	}
	// A hello of another machine changes what Automatic shows, not the
	// choice.
	if !h.learn(rex.Hello{Host: rex.HostInfo{Model: "MacBook Pro", OS: "macOS 26.1"}}) || h.remoteGlyph() != "cloud" {
		t.Errorf("after a hello the host shows %q", h.remoteGlyph())
	}
	hs, _ := loadHosts()
	if g := newHostConn(hs[0]).remoteGlyph(); g != "cloud" {
		t.Errorf("read again, the host shows %q", g)
	}
	choose(locales.HostIconAutomatic())
	if g := h.remoteGlyph(); g != "laptop" || h.Icon != "" {
		t.Errorf("Automatic shows %q, chose %q", g, h.Icon)
	}
	if hs, _ := loadHosts(); len(hs) != 1 || hs[0].Icon != "" {
		t.Errorf("hosts.json after Automatic: %+v", hs)
	}
	// An icon this build lacks, from a newer one or an edit, is not drawn.
	h.Icon, h.Glyph = "rocket", "toaster"
	if g := h.remoteGlyph(); g != "server" {
		t.Errorf("unknown icons show %q", g)
	}
	tt.Frame()
	if err := tt.Click(locales.HostsIcon() + ", build-box"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if m := tt.Menu(); len(m) == 0 {
		t.Error("the icon menu does not open")
	}
}

// TestWatchHostTakesChanges checks that a host's rounds of the same list
// run nothing on the main thread, so draw no window, and that a new list,
// or a poke while connected, is taken.
func TestWatchHostTakesChanges(t *testing.T) {
	sock, remote := startHostServer(t)
	if _, err := remote.Create(rex.CreateOptions{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	c, err := rex.ConnectSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	h := newHostConn(savedHost{ID: "h1", Name: "office-pc", Address: sock})
	t.Cleanup(h.close)
	updates := make(chan struct{}, 100)
	go (&App{}).watchHost(h, c, rex.RouteDirect, func(func()) { updates <- struct{}{} })
	taken := func(what string) {
		t.Helper()
		select {
		case <-updates:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s is not taken", what)
		}
	}
	// The list settles: then rounds go by without a change.
	quiet := func() {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			select {
			case <-updates:
				if time.Now().After(deadline) {
					t.Fatal("every round is taken")
				}
			case <-time.After(2500 * time.Millisecond):
				return
			}
		}
	}
	taken("the first list")
	quiet()
	h.poke()
	taken("the list after a poke")
	quiet()
	if _, err := remote.Create(rex.CreateOptions{Cols: 80, Rows: 24}); err != nil {
		// Once on CI the server did not answer this create in time
		// (callTimeout), for no cause found: the stacks show what held it.
		buf := make([]byte, 4<<20)
		t.Logf("goroutines:\n%s", buf[:runtime.Stack(buf, true)])
		t.Fatal(err)
	}
	taken("a new session")
}

// TestHostPathShows: a connected host's row shows its transport tag,
// connect time, and ping round trip once measured.
func TestHostPathShows(t *testing.T) {
	a, tt := hostsPage(t)
	var pings atomic.Int32
	oldPing := hostPing
	hostPing = func(*rex.Client, time.Duration) (time.Duration, error) {
		pings.Add(1)
		return 32 * time.Millisecond, nil
	}
	t.Cleanup(func() { hostPing = oldPing })

	f := newFakeHost(t, fakeApproved)
	h := a.addHost(savedHost{ID: "h1", Name: "ws-01", Address: f.path})
	waitFor(t, tt, "the host to connect", func() bool { return h.state == hostConnected })
	h.connectMs = 214 // the fake's dial is faster than a millisecond
	waitFor(t, tt, "the diagnostics line", func() bool {
		return tt.HasText("connect 214 ms · ping 32 ms") && tt.HasText(locales.HostsDiagTagDirect())
	})
	if pings.Load() == 0 {
		t.Error("no ping ran")
	}

	// A host not connected says nothing of a path.
	down := newHostConn(savedHost{ID: "h3", Name: "away-box", Address: "/nowhere"})
	a.hosts = append(a.hosts, down)
	tt.Frame()
	if tt.HasText("region ") {
		t.Errorf("texts %q", tt.Texts())
	}
}

// TestAddDirectHostUI tests pasting Direct TLS and Relay pairing URLs in the
// Add Remote Host sheet: auto-filling the name, rejecting invalid URLs/tokens/pins,
// saving to hosts.json, showing direct/relay badges, and never leaking #k=<token>
// in any visible UI text.
func TestAddDirectHostUI(t *testing.T) {
	a, tt := hostsPage(t)
	f := newFakeHost(t, fakeApproved)

	const (
		directSecret = "DIRECT_SECRET_TOKEN_987654"
		relaySecret  = "RELAY_SECRET_TOKEN_123456"
	)
	goodPin := strings.Repeat("a", 64)
	badPin := strings.Repeat("b", 64)

	oldPing := hostPing
	hostPing = func(*rex.Client, time.Duration) (time.Duration, error) {
		return 14 * time.Millisecond, nil
	}
	oldDial := dialHost
	dialHost = func(ctx context.Context, address string) (*rex.Client, error) {
		if rex.IsDirectTarget(address) {
			target, err := rex.ParseDirectTarget(address)
			if err != nil {
				return nil, err
			}
			if target.Pin == badPin {
				return nil, rex.ErrDirectPinMismatch
			}
			if target.Token != directSecret && target.Token != relaySecret {
				return nil, rex.ErrDirectAuth
			}
			return rex.ConnectSocketKeepalive(f.path)
		}
		return oldDial(ctx, address)
	}
	t.Cleanup(func() { dialHost, hostPing = oldDial, oldPing })

	assertNoSecretInUI := func(secret string) {
		t.Helper()
		for _, txt := range tt.Texts() {
			if strings.Contains(txt, secret) {
				t.Fatalf("UI text leaked secret %q: %q", secret, txt)
			}
		}
	}

	// Open Add Remote Host sheet.
	if err := tt.Click(locales.HostsAdd()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// Missing token in https:// URL is rejected.
	hostsUI.address = "https://203.0.113.10:7443/"
	tt.Frame()
	if err := tt.Click(locales.RemoteAdd()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(locales.HostsBadDirectAddress()) {
		t.Fatalf("expected missing-token error; texts %q", tt.Texts())
	}

	// Malformed pin in https:// URL is rejected.
	hostsUI.address = "https://203.0.113.10:7443/#k=" + directSecret + "&p=shortpin"
	tt.Frame()
	if err := tt.Click(locales.RemoteAdd()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(locales.HostsBadDirectPin()) {
		t.Fatalf("expected invalid-pin error; texts %q", tt.Texts())
	}
	assertNoSecretInUI(directSecret)

	// Wrong token returns HostsDirectAuthFailed and does not save.
	wrongTok := "WRONG_DIRECT_SECRET_000"
	hostsUI.address = "https://203.0.113.10:7443/#k=" + wrongTok + "&p=" + goodPin + "&n=alice-laptop"
	hostsUI.name = ""
	tt.Frame()
	a.submitHost()
	waitFor(t, tt, "auth failure", func() bool { return hostsUI.stage == addForm && hostsUI.formErr != nil })
	if !tt.HasText(locales.HostsDirectAuthFailed()) {
		t.Fatalf("expected auth error; texts %q", tt.Texts())
	}
	assertNoSecretInUI(wrongTok)
	if len(a.hosts) != 0 {
		t.Fatal("host saved despite auth failure")
	}

	// Wrong pin returns HostsDirectPinMismatch and does not save.
	hostsUI.address = "https://203.0.113.10:7443/#k=" + directSecret + "&p=" + badPin + "&n=alice-laptop"
	hostsUI.name = ""
	tt.Frame()
	a.submitHost()
	waitFor(t, tt, "pin mismatch", func() bool { return hostsUI.stage == addForm && hostsUI.formErr != nil })
	if !tt.HasText(locales.HostsDirectPinMismatch()) {
		t.Fatalf("expected pin mismatch error; texts %q", tt.Texts())
	}
	assertNoSecretInUI(directSecret)
	if len(a.hosts) != 0 {
		t.Fatal("host saved despite pin mismatch")
	}

	// Valid Direct TLS URL: typing auto-fills name, shows sanitized preview, saves & connects.
	directURL := "https://203.0.113.10:7443/#k=" + directSecret + "&p=" + goodPin + "&n=alice-laptop"
	a.openAddHost()
	tt.Frame()
	tt.Type(directURL)
	tt.Frame()
	if hostsUI.name != "alice-laptop" {
		t.Fatalf("auto-filled name = %q, want alice-laptop", hostsUI.name)
	}
	if !tt.HasText(locales.HostsDirectDetected("https://203.0.113.10:7443")) {
		t.Fatalf("missing sanitized direct preview; texts %q", tt.Texts())
	}
	assertNoSecretInUI(directSecret)

	if err := tt.Click(locales.RemoteAdd()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "direct host saved", func() bool { return len(a.hosts) == 1 && !hostsUI.adding })
	if hostsUI.address != "" {
		t.Error("sheet retained address after saving")
	}
	waitFor(t, tt, "direct host connected", func() bool {
		return a.hosts[0].state == hostConnected && tt.HasText(locales.HostsDiagTagDirect()) && tt.HasText("203.0.113.10:7443")
	})
	assertNoSecretInUI(directSecret)

	// Duplicate direct endpoint (even with a different #n=) is rejected.
	dupURL := "https://203.0.113.10:7443/#k=" + directSecret + "&p=" + goodPin + "&n=other-name"
	if got := validateHost(dupURL, "other-name", a.hosts); got != locales.HostsAlreadySaved() {
		t.Errorf("duplicate direct host validation = %q, want %q", got, locales.HostsAlreadySaved())
	}

	// Valid Relay URL saves and shows relay badge without leaking secret token.
	relayURL := "https://relay.example.com:7443/h/host_1234/#k=" + relaySecret + "&p=" + goodPin + "&n=build-box"
	a.openAddHost()
	tt.Frame()
	tt.Type(relayURL)
	tt.Frame()
	if hostsUI.name != "build-box" {
		t.Fatalf("auto-filled relay name = %q, want build-box", hostsUI.name)
	}
	if !tt.HasText(locales.HostsRelayDetected("https://relay.example.com:7443/h/host_1234")) {
		t.Fatalf("missing sanitized relay preview; texts %q", tt.Texts())
	}
	assertNoSecretInUI(relaySecret)

	if err := tt.Click(locales.RemoteAdd()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "relay host saved", func() bool { return len(a.hosts) == 2 && !hostsUI.adding })
	waitFor(t, tt, "relay host connected", func() bool {
		return a.hosts[1].state == hostConnected && tt.HasText(locales.HostsDiagTagRelay()) && tt.HasText("relay.example.com:7443")
	})
	assertNoSecretInUI(relaySecret)
	if hs, err := loadHosts(); err != nil || len(hs) != 2 || hs[0].Address != directURL || hs[1].Address != relayURL {
		t.Fatalf("hosts.json = %+v, err = %v", hs, err)
	}
}

func TestRelayHostLANDirectPromotionAndDiag(t *testing.T) {
	a, tt := hostsPage(t)
	f := newFakeHost(t, fakeApproved)

	oldPing := hostPing
	hostPing = func(*rex.Client, time.Duration) (time.Duration, error) { return 4 * time.Millisecond, nil }
	t.Cleanup(func() { hostPing = oldPing })

	oldDialCand := dialHostWithCandidates
	oldFetch := fetchLANHint
	oldProbe := probeLANDirect
	oldInterval := lanPromoteInterval
	t.Cleanup(func() {
		dialHostWithCandidates = oldDialCand
		fetchLANHint = oldFetch
		probeLANDirect = oldProbe
		lanPromoteInterval = oldInterval
	})

	var probeAllowed atomic.Bool
	dialHostWithCandidates = func(ctx context.Context, address string, candidates []rex.DirectCandidate) (*rex.Client, rex.DirectRouteInfo, error) {
		c, err := rex.ConnectSocketKeepalive(f.path)
		if err != nil {
			return nil, rex.DirectRouteInfo{}, err
		}
		if len(candidates) > 0 && probeAllowed.Load() {
			return c, rex.DirectRouteInfo{Kind: rex.RouteLANDirect, Host: candidates[0].Addr}, nil
		}
		return c, defaultHostRoute(address), nil
	}
	fetchLANHint = func(context.Context, string) (rex.LANHint, error) {
		return rex.LANHint{}, errors.New("no hint yet")
	}
	probeLANDirect = func(_ context.Context, _ string, candidates []rex.DirectCandidate) (rex.DirectCandidate, error) {
		if probeAllowed.Load() && len(candidates) > 0 {
			return candidates[0], nil
		}
		return rex.DirectCandidate{}, errors.New("lan unreachable")
	}
	lanPromoteInterval = 0

	pin := strings.Repeat("a", 64)
	relayURL := "https://relay.example.com:7443/h/host_lan_01/#k=secret_token_999&p=" + pin + "&n=dev-box-1"
	h := a.addHost(savedHost{ID: "h-lan", Name: "dev-box-1", Address: relayURL, RelayID: "r1", RelayHostID: "host_lan_01"})
	waitFor(t, tt, "initial relay connection", func() bool {
		return h.state == hostConnected && h.routeKind == rex.RouteRelay && tt.HasText(locales.HostsDiagTagRelay())
	})

	// 1. Receiving LAN hints while off-LAN (probe fails) does not disrupt the relay connection.
	initialClient := h.current()
	h.setLANHint([]string{"10.0.13.20:7443"}, pin)
	time.Sleep(60 * time.Millisecond)
	tt.Frame()
	if h.current() != initialClient || h.routeKind != rex.RouteRelay {
		t.Fatalf("failed LAN probe disrupted relay connection: route=%v", h.routeKind)
	}

	// 2. Once LAN becomes reachable and relay refresh delivers hints, host promotes to LAN direct
	//    and shows the LAN direct diagnostic badge with the private LAN host:port.
	probeAllowed.Store(true)
	a.applyRelayLANHints(hosts.Relay{ID: "r1", URL: "https://relay.example.com:7443"}, []directrelay.HostInfo{
		{ID: "host_lan_01", Name: "dev-box-1", LANAddrs: []string{"10.0.13.21:7443"}, DirectPin: pin},
	})
	waitFor(t, tt, "LAN direct promotion", func() bool {
		return h.state == hostConnected &&
			h.routeKind == rex.RouteLANDirect &&
			h.routeHost == "10.0.13.21:7443" &&
			tt.HasText(locales.HostsDiagTagLANDirect()) &&
			tt.HasText("10.0.13.21:7443")
	})
	if len(a.hosts) != 1 {
		t.Fatalf("expected single host entry after LAN promotion, got %d", len(a.hosts))
	}
}
