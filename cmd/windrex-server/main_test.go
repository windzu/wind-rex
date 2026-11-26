package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gorex/internal/cli"
	"gorex/internal/rex"
	"gorex/internal/rexapi"
)

// TestMain lets tests spawn and in-place-upgrade this test binary as the
// headless session server.
func TestMain(m *testing.M) {
	configureServerHooks()
	if server, err := rex.ServerMain(os.Args[1:]); server {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if exec, err := rex.SessionExecMain(os.Args[1:]); exec {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestHelpAndUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != cli.ExitOK {
		t.Fatalf("--help exit = %d, want %d (stderr=%q)", code, cli.ExitOK, errOut.String())
	}
	if !strings.Contains(out.String(), "windrex-server") || !strings.Contains(out.String(), "rexapi direct") {
		t.Fatalf("help output missing expected commands: %q", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run(nil, &out, &errOut); code != cli.ExitUsage {
		t.Fatalf("empty args exit = %d, want %d", code, cli.ExitUsage)
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"update"}, &out, &errOut); code != cli.ExitUsage {
		t.Fatalf("update exit = %d, want %d", code, cli.ExitUsage)
	}
	if !strings.Contains(errOut.String(), "server upgrade") {
		t.Fatalf("update hint missing server upgrade: %q", errOut.String())
	}
}

func TestVersion(t *testing.T) {
	old := version
	version = "0.56.1-test"
	defer func() { version = old }()

	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != cli.ExitOK {
		t.Fatalf("version exit = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "0.56.1-test") {
		t.Fatalf("version output = %q, want 0.56.1-test", out.String())
	}
}

func TestServerStatusStoppedAndSecretRedaction(t *testing.T) {
	dir := shortTempDir(t, "ws-stat-")
	t.Setenv("GOREX_DIR", dir)

	const secretToken = "super-secret-bearer-token-value"
	const relaySecret = "super-secret-relay-key-value"
	const validPin = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := rexapi.SaveDirect(rexapi.StateDir(), rexapi.DirectConfig{
		Enabled:      true,
		RelayURL:     "https://relay.example.com:7443",
		RelaySecret:  relaySecret,
		RelayCertPin: validPin,
		Token:        secretToken,
	}); err != nil {
		t.Fatalf("SaveDirect: %v", err)
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"server", "status", "--json"}, &out, &errOut); code != cli.ExitServer {
		t.Fatalf("stopped status exit = %d, want %d (stderr=%q)", code, cli.ExitServer, errOut.String())
	}
	raw := out.String()
	if strings.Contains(raw, secretToken) || strings.Contains(raw, relaySecret) {
		t.Fatalf("server status leaked secret material: %s", raw)
	}
	var report serverStatusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal: %v (raw=%q)", err, raw)
	}
	if report.Running {
		t.Fatalf("report.Running = true, want false")
	}
	if report.Dir != dir {
		t.Fatalf("report.Dir = %q, want %q", report.Dir, dir)
	}
	if !report.Direct.Enabled || report.Direct.Mode != "relay" || report.Direct.RelayURL != "https://relay.example.com:7443" {
		t.Fatalf("unexpected direct summary: %+v", report.Direct)
	}
}

func TestServerStartStatusAndInPlaceUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix in-place exec upgrade test")
	}
	dir := shortTempDir(t, "ws-up-")
	t.Setenv("GOREX_DIR", dir)
	t.Cleanup(func() {
		if c, err := rex.ConnectExisting(); err == nil {
			_ = c.Shutdown()
			c.Close()
		}
	})

	var out, errOut bytes.Buffer
	if code := run([]string{"server", "start"}, &out, &errOut); code != cli.ExitOK {
		t.Fatalf("server start exit = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "started (pid ") {
		t.Fatalf("unexpected start output: %q", out.String())
	}

	// Second start is idempotent.
	out.Reset()
	errOut.Reset()
	if code := run([]string{"server", "start"}, &out, &errOut); code != cli.ExitOK {
		t.Fatalf("second server start exit = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "already running") {
		t.Fatalf("expected already running, got %q", out.String())
	}

	// Create a live shell session so we can prove in-place upgrade preserves it.
	c, err := rex.ConnectExisting()
	if err != nil {
		t.Fatalf("ConnectExisting: %v", err)
	}
	hello, err := c.Hello()
	if err != nil {
		c.Close()
		t.Fatalf("Hello: %v", err)
	}
	sess, err := c.Create(rex.CreateOptions{Command: []string{"/bin/sh"}, Dir: dir, Cols: 80, Rows: 24})
	if err != nil {
		c.Close()
		t.Fatalf("Create session: %v", err)
	}

	// Status reports running server and 1 session.
	out.Reset()
	errOut.Reset()
	if code := run([]string{"server", "status", "--json"}, &out, &errOut); code != cli.ExitOK {
		c.Close()
		t.Fatalf("server status exit = %d, stderr=%q", code, errOut.String())
	}
	var report serverStatusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		c.Close()
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if !report.Running || report.PID != hello.PID || report.Sessions != 1 || !report.Upgrade {
		c.Close()
		t.Fatalf("unexpected running report: %+v (hello=%+v)", report, hello)
	}

	// Same build without --force is a clean no-op.
	out.Reset()
	if err := upgradeRunningServer(c, hello, hello.Exe, hello.ExeTime, false, &out); err != nil {
		c.Close()
		t.Fatalf("same-build upgrade error: %v", err)
	}
	if !strings.Contains(out.String(), "already runs this build") {
		c.Close()
		t.Fatalf("same-build output = %q", out.String())
	}

	// Simulate migrating to a separate executable path (even with an older mtime,
	// as when migrating from a locally built desktop binary to a release tarball).
	nextExe := filepath.Join(dir, "windrex-server-next")
	if err := copyExe(hello.Exe, nextExe); err != nil {
		c.Close()
		t.Fatalf("copyExe: %v", err)
	}
	nextExe, err = filepath.EvalSymlinks(nextExe)
	if err != nil {
		c.Close()
		t.Fatalf("EvalSymlinks: %v", err)
	}
	olderTime := hello.ExeTime.Add(-time.Minute).Truncate(time.Second)
	if err := os.Chtimes(nextExe, olderTime, olderTime); err != nil {
		c.Close()
		t.Fatalf("Chtimes: %v", err)
	}

	out.Reset()
	if err := upgradeRunningServer(c, hello, nextExe, olderTime, false, &out); err != nil {
		c.Close()
		t.Fatalf("cross-binary upgrade failed: %v", err)
	}
	c.Close()
	if !strings.Contains(out.String(), "upgraded in place; terminal sessions kept") {
		t.Fatalf("upgrade output = %q", out.String())
	}

	after, err := rex.ConnectExisting()
	if err != nil {
		t.Fatalf("ConnectExisting after upgrade: %v", err)
	}
	defer after.Close()
	gotHello, err := after.Hello()
	if err != nil {
		t.Fatalf("Hello after upgrade: %v", err)
	}
	if gotHello.PID != hello.PID || gotHello.Exe != nextExe || !gotHello.ExeTime.Equal(olderTime) {
		t.Fatalf("server after upgrade = %+v, want PID=%d Exe=%q ExeTime=%v", gotHello, hello.PID, nextExe, olderTime)
	}
	sessions, err := after.List()
	if err != nil || len(sessions) != 1 || sessions[0].ID != sess.ID || sessions[0].PID != sess.PID || sessions[0].Exited {
		t.Fatalf("sessions after upgrade = %+v, err=%v; want live session %+v", sessions, err, sess)
	}
}

func shortTempDir(t *testing.T, prefix string) string {
	t.Helper()
	base := "/tmp"
	if runtime.GOOS == "windows" {
		base = ""
	}
	dir, err := os.MkdirTemp(base, prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func copyExe(from, to string) error {
	r, err := os.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}
