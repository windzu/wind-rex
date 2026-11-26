// Package hosts is the app's hosts.json: the remote hosts of the window,
// which the app writes and the CLI and the session server read. Its
// addresses are secrets, readable by the user only, never logged.
package hosts

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Host is a remote host as hosts.json keeps it.
type Host struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Alias is the name the user gave the host here, "" for none.
	Alias   string `json:"alias,omitempty"`
	Address string `json:"address"`
	// Machine is the machine the host is (rex.Hello.Machine), as it said
	// when it last connected: the Rex API names the host's panes by it,
	// for a phone that connects to that machine too.
	Machine string `json:"machine,omitempty"`
	// Glyph is the icon of the machine, as its last hello described it,
	// "" before (hostConn.remoteGlyph).
	Glyph string `json:"glyph,omitempty"`
	// Icon is the icon the user chose for the host (hostIcons), "" to
	// follow its machine's, Glyph.
	Icon string `json:"icon,omitempty"`
	// Kills are the sessions closed here while the host was away, to end
	// there once connected again, though GoRex quit meanwhile.
	Kills []string `json:"kills,omitempty"`
	// RelayID and RelayHostID link a fleet-discovered host back to its saved
	// relay entry in relays.json (docs/NET-010-relay-fleet-discovery.md).
	RelayID     string `json:"relay_id,omitempty"`
	RelayHostID string `json:"relay_host_id,omitempty"`
}

// Label is the name the host shows by: its alias, else its name.
func (h Host) Label() string { return cmp.Or(h.Alias, h.Name) }

// File is hosts.json.
type File struct {
	Hosts []Host `json:"hosts"`
}

// ErrInvalid is a hosts.json that does not parse.
var ErrInvalid = errors.New("hosts.json is not valid")

// Path is the hosts file of the data directory dir.
func Path(dir string) string { return filepath.Join(dir, "hosts.json") }

// isLegacy reports whether h is a pre-NET-009 host record that can no longer
// connect: coordinator network ("net:"), built-in/system Tailscale
// ("tailscale:"), or tailcat ("tc..." / "tcat://...") addresses, or a record
// missing its ID or address.
func isLegacy(h Host) bool {
	if h.ID == "" || h.Address == "" {
		return true
	}
	if strings.HasPrefix(h.ID, "net:") || strings.HasPrefix(h.ID, "tailscale:") {
		return true
	}
	addr := strings.TrimSpace(h.Address)
	return strings.HasPrefix(addr, "tailscale:") || strings.HasPrefix(addr, "tc")
}

// Load reads the hosts of dir; none when the file does not exist. Legacy
// coordinator-network ("net:"), Tailscale ("tailscale:"), and tailcat
// ("tc...") records, as well as hosts with no ID or address, are filtered out
// while Direct TLS and stateless relay ("https://...") hosts are preserved.
// The file is only read; a caller that finds ErrInvalid decides whether to
// move it aside.
func Load(dir string) ([]Host, error) {
	b, err := os.ReadFile(Path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, ErrInvalid
	}
	return slices.DeleteFunc(f.Hosts, isLegacy), nil
}

// Save writes hosts to dir, readable by the user only, atomically. It does
// not write over a file it cannot read, nor one that does not parse.
func Save(dir string, list []Host) error {
	b, err := json.MarshalIndent(File{list}, "", "  ")
	if err != nil {
		return err
	}
	path := Path(dir)
	if old, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(old, &File{}) != nil {
			return errors.New("gorex: hosts.json is not valid: not writing over it")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(path, b, 0o600)
}

// writeAtomic writes data to path through a file beside it, renamed over
// it: a crash leaves the old file or the new one, not half of either.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), perm)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
