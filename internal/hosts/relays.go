package hosts

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Relay is a saved self-hosted windrex-relay entry in relays.json
// (docs/NET-010-relay-fleet-discovery.md). Secret is a credential: relays.json
// is readable by the user only (0600) and Secret is never logged or displayed.
type Relay struct {
	ID             string   `json:"id"`
	URL            string   `json:"url"`
	Label          string   `json:"label,omitempty"`
	Secret         string   `json:"secret"`
	CertPin        string   `json:"cert_pin,omitempty"`
	IgnoredHostIDs []string `json:"ignored_host_ids,omitempty"`
}

// DisplayName returns the relay's user-facing label, falling back to its
// hostname or sanitized URL.
func (r Relay) DisplayName() string {
	if s := strings.TrimSpace(r.Label); s != "" {
		return s
	}
	if u, err := url.Parse(strings.TrimSpace(r.URL)); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return cmp.Or(strings.TrimSpace(r.URL), r.ID)
}

// RelaysFile is relays.json.
type RelaysFile struct {
	Relays []Relay `json:"relays"`
}

// ErrRelaysInvalid is a relays.json that does not parse.
var ErrRelaysInvalid = errors.New("relays.json is not valid")

// RelaysPath is the relays file of the data directory dir.
func RelaysPath(dir string) string { return filepath.Join(dir, "relays.json") }

// LoadRelays reads the saved relays of dir; none when the file does not exist.
// Incomplete entries missing ID, URL, or Secret are filtered out.
func LoadRelays(dir string) ([]Relay, error) {
	b, err := os.ReadFile(RelaysPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f RelaysFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, ErrRelaysInvalid
	}
	out := slices.DeleteFunc(f.Relays, func(r Relay) bool {
		return strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.URL) == "" || strings.TrimSpace(r.Secret) == ""
	})
	return out, nil
}

// SaveRelays writes relays to dir, readable by the user only (0600), atomically.
// It refuses to write over a file that it cannot read or that does not parse.
func SaveRelays(dir string, list []Relay) error {
	if list == nil {
		list = []Relay{}
	}
	b, err := json.MarshalIndent(RelaysFile{Relays: list}, "", "  ")
	if err != nil {
		return err
	}
	path := RelaysPath(dir)
	if old, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(old, &RelaysFile{}) != nil {
			return errors.New("gorex: relays.json is not valid: not writing over it")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(path, b, 0o600)
}
