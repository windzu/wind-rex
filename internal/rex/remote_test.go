//go:build darwin || linux || windows

package rex

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testServer is a server on its own directory, not listening.
func testServer(t testing.TB) (*Server, *atomic.Int32) {
	t.Setenv("GOREX_DIR", t.TempDir())
	var listens atomic.Int32
	hostDone := make(chan struct{})
	close(hostDone)
	s := &Server{
		sessions:  map[string]*session{},
		started:   time.Now(),
		layoutRev: 1,
		hostDone:  hostDone,
		quit:      make(chan struct{}),
		linksFile: filepath.Join(os.Getenv("GOREX_DIR"), "teleports.json"),
		dir:       os.Getenv("GOREX_DIR"),
	}
	t.Cleanup(func() {
		// The server stops with the test, as one that shuts down: what it
		// does after, as a refresh or a session's end, writes no layout
		// into a directory the test removes; those under way finish first.
		s.mu.Lock()
		s.stopping = true
		s.mu.Unlock()
		s.applyMu.Lock()
		s.applyMu.Unlock()
		s.layoutFile.Lock()
		s.layoutFile.Unlock()
	})
	return s, &listens
}

// testPeer is the peer id of a remote client of pipe.
var testPeer = "peer:" + strings.Repeat("0", 16)

// pipe connects to s's handler, as a local connection for peer "", else
// as the remote client peer.
func pipe(s *Server, peer string) *Client {
	a, b := net.Pipe()
	go s.handle(b, peer)
	return newClient(a, func() (net.Conn, error) {
		a, b := net.Pipe()
		go s.handle(b, peer)
		return a, nil
	})
}

func testShell() (cmd []string, dir string) {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe"}, os.TempDir()
	}
	return []string{"/bin/sh"}, "/tmp"
}

func TestRemoteDenied(t *testing.T) {
	s, _ := testServer(t)
	c := pipe(s, testPeer)
	defer c.Close()
	if _, err := c.Hello(); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"shutdown", "setLayout", "action", "nope"} {
		req := Request{Op: op, Layout: json.RawMessage(`{}`)}
		if err := c.call(req, nil); err == nil || !strings.Contains(err.Error(), "not allowed remotely") {
			t.Errorf("%s: %v", op, err)
		}
	}
	select {
	case <-s.quit:
		t.Fatal("shut down")
	case <-time.After(100 * time.Millisecond):
	}
	cmd, dir := testShell()
	info, err := c.Create(CreateOptions{Command: cmd, Dir: dir, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if list, err := c.List(); err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	if err := c.Resize(info.ID, 100, 30); err != nil {
		t.Fatal(err)
	}
	if err := c.Kill(info.ID); err != nil {
		t.Fatal(err)
	}
}

// readsUntilClosed reads st in the background and tells, on the channel,
// its error once it ends.
func readsUntilClosed(st *Stream) <-chan error {
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 32<<10)
		for {
			if _, err := st.Read(buf); err != nil {
				done <- err
				return
			}
		}
	}()
	return done
}

// TestRemoteSharesSessions checks that every remote client sees and uses
// every session, another client's and the local ones too.
func TestRemoteSharesSessions(t *testing.T) {
	s, _ := testServer(t)
	local := pipe(s, "")
	defer local.Close()
	ac, bc := pipe(s, "peer-alice"), pipe(s, "peer-bob")
	defer ac.Close()
	defer bc.Close()
	cmd, dir := testShell()
	create := func(c *Client) string {
		t.Helper()
		info, err := c.Create(CreateOptions{Command: cmd, Dir: dir, Cols: 80, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { local.Kill(info.ID) })
		return info.ID
	}
	ls, as, bs := create(local), create(ac), create(bc)
	wantIDs := func(what string, c *Client, want ...string) {
		t.Helper()
		list, err := c.List()
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, in := range list {
			got = append(got, in.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s lists %v, want %v", what, got, want)
		}
	}
	wantIDs("local", local, ls, as, bs)
	wantIDs("alice", ac, ls, as, bs)
	if err := ac.Resize(ls, 90, 30); err != nil {
		t.Error(err)
	}
	if err := ac.Resize(bs, 90, 30); err != nil {
		t.Error(err)
	}
	theirs := ac.Stream(bs, 80, 24)
	defer theirs.Close()
	theirs.Resize(80, 24)
	end := readsUntilClosed(theirs)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		ss := s.sessions[bs]
		s.mu.Unlock()
		if ss.info().Attached == 1 {
			break
		}
		select {
		case err := <-end:
			t.Fatalf("attach to another's session: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("not attached to another's session")
		}
	}
}

func TestLocalAllowed(t *testing.T) {
	s, _ := testServer(t)
	c := pipe(s, "")
	defer c.Close()
	if err := c.SetLayout(json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if l, err := c.Layout(); err != nil || !bytes.Equal(l, []byte(`{"a":1}`)) {
		t.Fatalf("layout %s %v", l, err)
	}
	if err := c.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.quit:
	case <-time.After(2 * time.Second):
		t.Fatal("not shut down")
	}
}

func TestConnectRemoteRejectsLegacy(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	for _, addr := range []string{"", "nope", "tc!!", "tc0123456789abcdef", "tailscale:alice-mac:7464", "http://192.0.2.10:7443/#k=tok"} {
		if ValidRemoteAddress(addr) {
			t.Errorf("ValidRemoteAddress(%q) = true, want false", addr)
		}
		if _, err := ConnectRemote(context.Background(), addr); err == nil {
			t.Errorf("connected to %q", addr)
		}
	}
}

// TestMachineID checks that the machine's ID lasts, comes from its persistent
// key in remote-client.json, tells nothing of the key, and stays compatible
// with pre-NET-009 remote-client.json files.
func TestMachineID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOREX_DIR", dir)
	id := MachineID()
	if len(id) != 32 || id != MachineID() {
		t.Fatalf("machine id %q, then %q", id, MachineID())
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(filepath.Join(dir, "remote-client.json")); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v %v", st.Mode(), err)
		}
	}
	pub, err := machinePublicKey()
	if err != nil || pub == "" || strings.Contains(pub, id) || strings.Contains(id, strings.TrimPrefix(pub, "nodekey:")[:8]) {
		t.Errorf("machine id %q and key %q (%v)", id, pub, err)
	}

	// Verify pre-NET-009 privkey:<hex> compatibility.
	preDir := t.TempDir()
	t.Setenv("GOREX_DIR", preDir)
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"nodeKey":"privkey:` + hex.EncodeToString(priv.Bytes()) + `"}`
	if err := os.WriteFile(filepath.Join(preDir, "remote-client.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	wantSum := sha256.Sum256([]byte("gorex machine\x00nodekey:" + hex.EncodeToString(priv.PublicKey().Bytes())))
	wantID := hex.EncodeToString(wantSum[:16])
	if got := MachineID(); got != wantID {
		t.Fatalf("pre-NET-009 MachineID = %q, want %q", got, wantID)
	}

	t.Setenv("GOREX_DIR", t.TempDir())
	if MachineID() == id {
		t.Error("another machine has the same id")
	}
}

// TestHostMachines checks that hosts.json's machines are read by host ID,
// and that hosts without one are left out.
func TestHostMachines(t *testing.T) {
	dir := t.TempDir()
	if got := HostMachines(dir); len(got) != 0 {
		t.Errorf("no file: %v", got)
	}
	os.WriteFile(filepath.Join(dir, "hosts.json"), []byte(`{"hosts":[
		{"id":"a","address":"https://203.0.113.10:7443/#k=secret","machine":"m-a"},{"id":"b","address":"https://203.0.113.11:7443/#k=secret"},{"machine":"m-c"}]}`), 0o600)
	if got := HostMachines(dir); !reflect.DeepEqual(got, map[string]string{"a": "m-a"}) {
		t.Errorf("machines: %v", got)
	}
}

// TestServerKnowsItsMachine checks that a server knows its machine before
// it serves, so that no hello names none.
func TestServerKnowsItsMachine(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	if s := newServer(nil, nil); s.machine == "" || s.machine != MachineID() {
		t.Errorf("machine %q, want %q", s.machine, MachineID())
	}
}
