//go:build darwin || linux || windows

package rex

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gorex/internal/layout"
)

// writeJSON writes v to path, readable by the user only, atomically.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// readJSON reads path into v; a file that does not exist leaves v as is.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("rex: reading %s: %w", filepath.Base(path), err)
	}
	return nil
}

// secretRun finds what could be a secret token or key: a long run of base64 characters.
var secretRun = regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`)

// scrub removes remote pairing credentials and long token-like substrings from
// s so that an error or a log line never exposes them.
func scrub(s string, addrs ...string) string {
	for _, a := range addrs {
		if a != "" {
			if IsDirectTarget(a) {
				if tok := directToken(a); tok != "" {
					s = strings.ReplaceAll(s, tok, "<redacted>")
				}
				s = strings.ReplaceAll(s, a, SanitizeDirectTarget(a))
			} else {
				s = strings.ReplaceAll(s, a, "<address>")
			}
		}
	}
	return secretRun.ReplaceAllString(s, "<redacted>")
}

// Scrub removes secret tokens and remote pairing URLs addrs from s for an error
// to show or log.
func Scrub(s string, addrs ...string) string { return scrub(s, addrs...) }

// ValidRemoteAddress reports whether s is a valid Direct TLS or stateless relay
// pairing URL.
func ValidRemoteAddress(s string) bool {
	s = strings.TrimSpace(s)
	if !IsDirectTarget(s) {
		return false
	}
	_, err := ParseDirectTarget(s)
	return err == nil
}

// remoteAllowed reports the ops a remote connection may make; it may also
// attach to sessions. dir.list goes to the clients that may take a file
// with file.get, which reads any path as the server's user already. Every
// client the host lets in reads the layout and follows its changes: it is
// where the host's projects and tabs live (docs/MOD-002-projects.md).
func remoteAllowed(op string) bool {
	switch op {
	case "hello", "ping", "list", "create", "kill", "resize", "focus", "input", "paste", "capture", "clients",
		"key", "mouse", "focusReport", "modes", "format", "inputMode", "clear", "reset", "command", "seeStatus", "resync",
		"dir.list", "agents.list", "getLayout", "getLayoutState", "subscribe":
		return true
	}
	return false
}

var clientKeyMu sync.Mutex

// machinePublicKey returns this machine's stable X25519 public key identity
// ("nodekey:<64 hex>"), kept in Dir()/remote-client.json as "privkey:<64 hex>".
// Preserving this format keeps MachineID stable across upgrades from earlier
// WindRex / GoRex builds without any external Tailscale dependency.
func machinePublicKey() (string, error) {
	clientKeyMu.Lock()
	defer clientKeyMu.Unlock()
	type identity struct {
		NodeKey string `json:"nodeKey,omitempty"`
	}
	parsePriv := func(s string) (*ecdh.PrivateKey, error) {
		rawHex, ok := strings.CutPrefix(strings.TrimSpace(s), "privkey:")
		if !ok {
			return nil, errors.New("rex: invalid machine key format")
		}
		b, err := hex.DecodeString(rawHex)
		if err != nil || len(b) != 32 {
			return nil, errors.New("rex: invalid machine key length")
		}
		return ecdh.X25519().NewPrivateKey(b)
	}
	formatPub := func(k *ecdh.PrivateKey) string {
		return "nodekey:" + hex.EncodeToString(k.PublicKey().Bytes())
	}

	var id identity
	path := filepath.Join(Dir(), "remote-client.json")
	if err := readJSON(path, &id); err != nil {
		return "", err
	}
	if id.NodeKey != "" {
		k, err := parsePriv(id.NodeKey)
		if err != nil {
			return "", err
		}
		return formatPub(k), nil
	}
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	id.NodeKey = "privkey:" + hex.EncodeToString(k.Bytes())
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := writeJSON(tmp, id); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, path); err != nil {
		if _, serr := os.Stat(path); serr != nil {
			// No hard links here: write the file as it is.
			return formatPub(k), writeJSON(path, id)
		}
		var other identity
		if err := readJSON(path, &other); err != nil || other.NodeKey == "" {
			return "", errors.New("rex: reading the machine key")
		}
		okKey, err := parsePriv(other.NodeKey)
		if err != nil {
			return "", err
		}
		return formatPub(okKey), nil
	}
	return formatPub(k), nil
}

// MachineID names this machine for good, whichever way an app reaches
// it: a hash of its persistent key in remote-client.json, which the
// machine keeps from the first time and no one renames. The hash tells
// nothing of the key. "" when the key cannot be read nor made.
func MachineID() string {
	k, err := machinePublicKey()
	if err != nil || k == "" {
		if err != nil {
			log.Printf("rex: the machine key: %v", err)
		}
		return ""
	}
	sum := sha256.Sum256([]byte("gorex machine\x00" + k))
	return hex.EncodeToString(sum[:16])
}

// ConnectRemote connects to a remote WindRex host over Direct TLS or a
// stateless windrex-relay pairing URL (https://...).
func ConnectRemote(ctx context.Context, addr string) (*Client, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, errors.New("rex: no remote address")
	}
	if !IsDirectTarget(addr) {
		// Never quote addr in errors: legacy addresses may hold secrets.
		return nil, errors.New("rex: remote address must be an https:// Direct TLS or Relay URL")
	}
	return ConnectDirect(ctx, addr)
}

// remoteCommand reports whether a remote client may ask for the layout
// command c. A client the host lets in arranges the host's tabs as its
// own window does (docs/MOD-002-projects.md, "The desktop window"): tab.set and
// layout.apply, of the host's own panes only. What names this machine's
// windows, or what only its own window takes (kills, shows, adoptions),
// stays local.
func remoteCommand(c LayoutCommand) bool {
	switch c.Op {
	case "windows.set", "kills.taken", "show.taken", "adopt.taken":
		return false
	case "tab.set":
		return c.TabLayout == nil || hostPanesOnly(*c.TabLayout)
	case "layout.apply":
		for _, x := range c.Batch {
			if x.Op == "layout.apply" || !remoteCommand(x) {
				return false
			}
		}
	}
	return true
}

// hostPanesOnly reports that every pane of t is of this host: none names
// another host, nor is a place a filtered layout kept for a pane the
// client could not see.
func hostPanesOnly(t layout.Tab) bool {
	for _, r := range t.Roots() {
		for _, n := range layout.Leaves(r) {
			if n.Host != "" || n.Unavailable {
				return false
			}
		}
	}
	return true
}
