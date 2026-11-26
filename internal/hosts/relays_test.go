package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestRelaysSaveLoadAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")

	// Missing file returns nil, nil.
	if list, err := LoadRelays(dir); err != nil || list != nil {
		t.Fatalf("LoadRelays missing = %+v, %v", list, err)
	}

	in := []Relay{{
		ID:             "r1",
		URL:            "https://relay.example.com:7443",
		Label:          "Home Relay",
		Secret:         "secret-relay-token",
		CertPin:        "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		IgnoredHostIDs: []string{"host_ignored_1"},
	}}
	if err := SaveRelays(dir, in); err != nil {
		t.Fatalf("SaveRelays: %v", err)
	}
	if fi, err := os.Stat(RelaysPath(dir)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("relays.json mode = %v, %v; want 0600", fi, err)
	}

	out, err := LoadRelays(dir)
	if err != nil {
		t.Fatalf("LoadRelays: %v", err)
	}
	if len(out) != 1 || out[0].ID != "r1" || out[0].URL != "https://relay.example.com:7443" ||
		out[0].Label != "Home Relay" || out[0].Secret != "secret-relay-token" ||
		out[0].CertPin != in[0].CertPin || !slices.Equal(out[0].IgnoredHostIDs, []string{"host_ignored_1"}) {
		t.Fatalf("round-trip = %+v", out)
	}
	if out[0].DisplayName() != "Home Relay" {
		t.Fatalf("DisplayName = %q", out[0].DisplayName())
	}
	if (Relay{ID: "r2", URL: "https://relay.example.com:7443", Secret: "s"}).DisplayName() != "relay.example.com" {
		t.Fatal("DisplayName fallback to hostname failed")
	}
}

func TestRelaysFiltersIncompleteAndRefusesOverwriteInvalid(t *testing.T) {
	dir := t.TempDir()
	raw := `{"relays":[
		{"id":"r1","url":"https://relay.example.com:7443","secret":"sec1"},
		{"id":"","url":"https://relay.example.com:7443","secret":"sec2"},
		{"id":"r3","url":"","secret":"sec3"},
		{"id":"r4","url":"https://relay.example.com:7443","secret":""}
	]}`
	if err := os.WriteFile(RelaysPath(dir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := LoadRelays(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "r1" {
		t.Fatalf("filtered list = %+v, want only r1", list)
	}

	if err := os.WriteFile(RelaysPath(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRelays(dir); !errors.Is(err, ErrRelaysInvalid) {
		t.Fatalf("LoadRelays invalid = %v, want ErrRelaysInvalid", err)
	}
	if err := SaveRelays(dir, []Relay{{ID: "r1", URL: "https://relay.example.com:7443", Secret: "s"}}); err == nil {
		t.Fatal("SaveRelays overwrote invalid relays.json")
	}
}

func TestHostRelayMetadataRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := []Host{{
		ID:          "h1",
		Name:        "gpu-node-1",
		Address:     "https://relay.example.com:7443/h/host_gpu_01/#k=derivedToken",
		RelayID:     "r1",
		RelayHostID: "host_gpu_01",
	}}
	if err := Save(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].RelayID != "r1" || out[0].RelayHostID != "host_gpu_01" {
		t.Fatalf("host metadata round-trip = %+v", out)
	}
}
