//go:build darwin || linux || windows

package rex

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLineLimit(t *testing.T) {
	s, _ := testServer(t)
	big := append(bytes.Repeat([]byte("x"), maxLine+1), '\n')
	for _, name := range []string{"first line", "later line"} {
		a, b := net.Pipe()
		go s.handle(b, "")
		r := bufio.NewReader(a)
		if name == "later line" {
			a.Write([]byte(`{"op":"hello","id":1}` + "\n"))
			if _, err := r.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
		}
		go a.Write(big)
		line, err := r.ReadString('\n')
		if err != nil || !strings.Contains(line, "too long") {
			t.Errorf("%s: %q %v", name, line, err)
		}
		if _, err := r.ReadString('\n'); err == nil {
			t.Errorf("%s: the connection stayed open", name)
		}
		a.Close()
	}
}

func TestClampSize(t *testing.T) {
	for in, want := range map[int]int{-5: 1, 0: 1, 1: 1, 80: 80, 1000: 1000, 1001: 1000, 1 << 30: 1000} {
		if got := clampSize(in); got != want {
			t.Errorf("clampSize(%d) = %d, want %d", in, got, want)
		}
	}
	s, _ := testServer(t)
	c := pipe(s, testPeer)
	defer c.Close()
	cmd, dir := testShell()
	info, err := c.Create(CreateOptions{Command: cmd, Dir: dir, Cols: 1 << 20, Rows: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Kill(info.ID)
	if info.Cols != 1000 || info.Rows != 1000 {
		t.Errorf("created at %dx%d", info.Cols, info.Rows)
	}
	if err := c.Resize(info.ID, 5000, -1); err != nil {
		t.Fatal(err)
	}
	// The size lands once it settles (resize).
	waitSize(t, c, info.ID, "1000x1")
}

func TestKillsAreBounded(t *testing.T) {
	s, _ := testServer(t)
	c := pipe(s, testPeer)
	defer c.Close()
	var cur, most atomic.Int32
	release := make(chan struct{})
	killHook = func() {
		n := cur.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		<-release
		cur.Add(-1)
	}
	defer func() { killHook = nil }()
	var wg sync.WaitGroup
	for range 3 * maxAside {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Kill("nothing")
		}()
	}
	time.Sleep(300 * time.Millisecond)
	if got := cur.Load(); got != maxAside {
		t.Errorf("%d kills at once, want %d", got, maxAside)
	}
	close(release)
	wg.Wait()
	if most.Load() > maxAside {
		t.Errorf("at most %d kills at once, want %d", most.Load(), maxAside)
	}
}

func TestAddressNotInErrors(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	secret := "tc" + strings.Repeat("Zq9_-x", 12) + "!!"
	_, err := ConnectRemote(context.Background(), secret)
	if err == nil {
		t.Fatal("connected")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), secret[2:30]) {
		t.Errorf("the error holds the address: %v", err)
	}
	got := scrub("dial tcp "+secret+": refused, near "+strings.Repeat("Ab1-", 12), secret)
	if strings.Contains(got, "Ab1-Ab1-") || strings.Contains(got, "Zq9_") || !strings.Contains(got, "refused") {
		t.Errorf("scrub = %q", got)
	}
}

func TestMachineIDConcurrent(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	ids := make([]string, 16)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i] = MachineID()
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("machine IDs differ: %q and %q", id, ids[0])
		}
	}
}
