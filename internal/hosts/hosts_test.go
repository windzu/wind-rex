package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func write(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The app, the CLI and the server read one rule: a host with no ID or
// address, or a legacy coordinator-network, Tailscale, or tailcat record,
// is dropped while Direct TLS and stateless relay hosts survive intact.
func TestLoadLeavesOutWhatTheAppDropped(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"hosts":[
		{"id":"h1","name":"Studio","alias":"dev","address":"https://203.0.113.10:7443/#k=directToken1&p=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef&n=Studio","machine":"m1","glyph":"desktop","icon":"server","kills":["s1"]},
		{"id":"h2","name":"RelayBox","address":"https://relay.example.com:7443/h/host_abc/#k=relayToken2&n=RelayBox","machine":"m2"},
		{"id":"","name":"no id","address":"https://203.0.113.11:7443/#k=tok"},
		{"id":"h3","name":"no address"},
		{"id":"net:abc","name":"network","address":"tc0legacyNetworkAddr","network":true},
		{"id":"tailscale:node1:7464","name":"alice-mac","address":"tailscale:node1:7464","tailnetOpened":true,"alias":"mac","icon":"laptop"},
		{"id":"h4","name":"tailnet-custom-id","address":"tailscale:node2:7464"},
		{"id":"h5","name":"tailcat-real","address":"tc0123456789abcdef0123456789abcdef"},
		{"id":"h6","name":"tailcat-uri","address":"tcat://legacy-host"},
		{"id":"h7","name":"tailcat-padded","address":"  tc1paddedAddress  "}
	]}`)
	list, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("Load returned %d hosts (%+v), want 2 (h1 and h2)", len(list), list)
	}
	if list[0].ID != "h1" || list[0].Name != "Studio" || list[0].Alias != "dev" ||
		list[0].Machine != "m1" || list[0].Glyph != "desktop" || list[0].Icon != "server" ||
		!slices.Equal(list[0].Kills, []string{"s1"}) ||
		list[0].Address != "https://203.0.113.10:7443/#k=directToken1&p=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef&n=Studio" {
		t.Fatalf("h1 = %+v", list[0])
	}
	if list[1].ID != "h2" || list[1].Name != "RelayBox" || list[1].Machine != "m2" ||
		list[1].Address != "https://relay.example.com:7443/h/host_abc/#k=relayToken2&n=RelayBox" {
		t.Fatalf("h2 = %+v", list[1])
	}
}

func TestLoadNoneAndInvalid(t *testing.T) {
	dir := t.TempDir()
	if list, err := Load(dir); err != nil || list != nil {
		t.Errorf("Load of no file = %+v, %v", list, err)
	}
	write(t, dir, "{")
	if _, err := Load(dir); !errors.Is(err, ErrInvalid) {
		t.Errorf("Load of a broken file = %v, want ErrInvalid", err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	in := []Host{{ID: "h1", Name: "Studio", Alias: "dev", Address: "https://203.0.113.10:7443/#k=tok123", Machine: "m1", Kills: []string{"s1"}}}
	if err := Save(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "h1" || out[0].Machine != "m1" || len(out[0].Kills) != 1 || out[0].Label() != "dev" {
		t.Fatalf("Load after Save = %+v", out)
	}
	if fi, err := os.Stat(Path(dir)); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("hosts.json mode = %v, %v, want 0600", fi, err)
	}
}

// A file that does not parse holds addresses that cannot be had again:
// Save leaves it as it is.
func TestSaveRefusesOverInvalid(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "{")
	if err := Save(dir, []Host{{ID: "h1", Address: "https://203.0.113.10:7443/#k=tok123"}}); err == nil {
		t.Fatal("Save wrote over a file that does not parse")
	}
}
