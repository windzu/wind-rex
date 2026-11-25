package rexapi

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorex/internal/directrelay"
)

// DirectPort is the default TLS port for direct cellular access.
const DirectPort = 7443

// DirectConfig configures the gateway's native TLS direct listener and/or
// outbound stateless relay tunnel, stored in direct.json.
type DirectConfig struct {
	Enabled bool `json:"enabled"`
	// Listen is the local TCP bind address for direct TLS (e.g. ":7443").
	// Empty disables the local TLS listener when only RelayURL is used.
	Listen string `json:"listen,omitempty"`
	// PublicURL is the externally reachable https:// address advertised in
	// direct pairing QR codes (e.g. "https://203.0.113.10:7443").
	PublicURL string `json:"public_url,omitempty"`
	// Token is the 256-bit Bearer token required on every direct/relay request.
	Token string `json:"token,omitempty"`
	// CertFile and KeyFile optionally supply a CA-signed TLS certificate pair.
	// When empty, a self-signed ECDSA P-256 certificate is generated and pinned.
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`

	// RelayURL, when set, connects this host outbound to a windrex-relay server.
	RelayURL     string `json:"relay_url,omitempty"`
	RelaySecret  string `json:"relay_secret,omitempty"`
	RelayCertPin string `json:"relay_cert_pin,omitempty"`
	// HostID identifies this host on the relay (/h/<HostID>). Auto-generated once.
	HostID string `json:"host_id,omitempty"`
}

// DirectStatus records runtime state of the direct/relay transport in direct-status.json.
type DirectStatus struct {
	Running        bool      `json:"running"`
	Listening      string    `json:"listening,omitempty"`
	CertPin        string    `json:"cert_pin,omitempty"`
	SelfSigned     bool      `json:"self_signed,omitempty"`
	RelayConnected bool      `json:"relay_connected,omitempty"`
	RelayError     string    `json:"relay_error,omitempty"`
	Error          string    `json:"error,omitempty"`
	Updated        time.Time `json:"updated"`
}

func directConfPath(dir string) string   { return filepath.Join(dir, "direct.json") }
func directStatusPath(dir string) string { return filepath.Join(dir, "direct-status.json") }

// NewHostID generates a 128-bit base64url relay host identifier.
func NewHostID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// LoadDirect reads direct.json from dir; a missing file defaults to disabled.
func LoadDirect(dir string) (DirectConfig, error) {
	var c DirectConfig
	b, err := os.ReadFile(directConfPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", directConfPath(dir), err)
	}
	return c, nil
}

// SaveDirect validates c, populates Token and HostID if missing, and writes direct.json.
func SaveDirect(dir string, c DirectConfig) error {
	if err := c.check(); err != nil {
		return err
	}
	if c.Token == "" {
		if old, err := LoadDirect(dir); err == nil && old.Token != "" {
			c.Token = old.Token
		} else {
			c.Token = NewLANToken()
		}
	}
	if c.RelayURL != "" && c.HostID == "" {
		if old, err := LoadDirect(dir); err == nil && old.HostID != "" {
			c.HostID = old.HostID
		} else {
			c.HostID = NewHostID()
		}
	}
	return writeJSON(directConfPath(dir), c)
}

// LoadDirectStatus reads direct-status.json from dir.
func LoadDirectStatus(dir string) DirectStatus {
	var s DirectStatus
	if b, err := os.ReadFile(directStatusPath(dir)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveDirectStatus(dir string, s DirectStatus) {
	s.Updated = time.Now()
	if err := writeJSON(directStatusPath(dir), s); err != nil {
		logf("direct: %v", err)
	}
}

func (c DirectConfig) check() error {
	if c.Listen != "" {
		if _, port, err := net.SplitHostPort(c.Listen); err != nil || port == "" {
			return fmt.Errorf("listen address %q is not host:port", c.Listen)
		}
	}
	if c.PublicURL != "" {
		u, err := url.Parse(strings.TrimSpace(c.PublicURL))
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("public_url %q must be an https://host[:port] URL", c.PublicURL)
		}
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return errors.New("cert_file and key_file must be set together")
	}
	if c.RelayURL != "" {
		u, err := url.Parse(strings.TrimSpace(c.RelayURL))
		if err != nil || (u.Scheme != "https" && u.Scheme != "wss" && u.Scheme != "http" && u.Scheme != "ws") || u.Host == "" {
			return fmt.Errorf("relay_url %q must be an https:// or wss:// URL", c.RelayURL)
		}
		if c.RelayCertPin != "" && !directrelay.ValidPin(c.RelayCertPin) {
			return errors.New("relay_cert_pin is not a valid base64url SHA-256 certificate pin")
		}
		if c.HostID != "" && !directrelay.ValidHostID(c.HostID) {
			return errors.New("host_id is invalid")
		}
	}
	return nil
}

// PairURL returns the pairing URL for iOS (including fragment token, optional
// leaf certificate pin, and optional host display name).
func (c DirectConfig) PairURL(s DirectStatus, hostName string) string {
	if !c.Enabled || c.Token == "" {
		return ""
	}
	var base, pin string
	switch {
	case c.RelayURL != "" && c.HostID != "":
		u, err := directrelay.RelayClientBaseURL(c.RelayURL, c.HostID)
		if err != nil {
			return ""
		}
		base = u
		pin = c.RelayCertPin
	case c.PublicURL != "":
		base = strings.TrimRight(strings.TrimSpace(c.PublicURL), "/")
		if s.SelfSigned {
			pin = s.CertPin
		}
	default:
		return ""
	}
	q := url.Values{}
	q.Set("k", c.Token)
	if pin != "" {
		q.Set("p", pin)
	}
	if hostName = strings.TrimSpace(hostName); hostName != "" {
		q.Set("n", hostName)
	}
	return base + "/#" + q.Encode()
}

type directListener struct {
	g   *Gateway
	dir string

	mu         sync.Mutex
	conf       DirectConfig
	applied    bool
	ln         *trackedListener
	tunnel     *directrelay.TunnelListener
	certPin    string
	selfSigned bool
	relayOK    bool
	relayErr   string
	err        string
}

func newDirectListener(g *Gateway, dir string) *directListener {
	return &directListener{g: g, dir: dir}
}

func sameDirect(a, b DirectConfig) bool {
	return a == b
}

func (d *directListener) apply(c DirectConfig) {
	err := c.check()
	if err == nil && c.Enabled && c.Token == "" {
		c.Token = NewLANToken()
		_ = writeJSON(directConfPath(d.dir), c)
	}
	if err == nil && c.Enabled && c.RelayURL != "" && c.HostID == "" {
		c.HostID = NewHostID()
		_ = writeJSON(directConfPath(d.dir), c)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.applied && sameDirect(d.conf, c) {
		return
	}
	tokenChanged := d.applied && d.conf.Token != c.Token
	directChanged := !d.applied || d.conf.Enabled != c.Enabled || d.conf.Listen != c.Listen ||
		d.conf.CertFile != c.CertFile || d.conf.KeyFile != c.KeyFile || d.conf.PublicURL != c.PublicURL
	relayChanged := !d.applied || d.conf.Enabled != c.Enabled || d.conf.RelayURL != c.RelayURL ||
		d.conf.RelaySecret != c.RelaySecret || d.conf.RelayCertPin != c.RelayCertPin || d.conf.HostID != c.HostID

	d.applied = true
	d.conf = c
	if err != nil {
		d.err = err.Error()
		d.closeLocked()
		d.reportLocked()
		return
	}
	d.err = ""
	if !c.Enabled {
		d.closeLocked()
		d.reportLocked()
		return
	}

	if directChanged {
		d.closeDirectLocked()
		if c.Listen != "" {
			if err := d.startDirectLocked(c); err != nil {
				d.err = err.Error()
				logf("direct: %v", err)
			}
		}
	} else if tokenChanged && d.ln != nil {
		d.ln.closeConns()
	}

	if relayChanged || (tokenChanged && d.tunnel != nil) {
		d.closeRelayLocked()
		if c.RelayURL != "" {
			if err := d.startRelayLocked(c); err != nil {
				d.relayErr = err.Error()
				logf("direct relay: %v", err)
			}
		}
	}
	d.reportLocked()
}

func (d *directListener) startDirectLocked(c DirectConfig) error {
	var (
		cert       tls.Certificate
		pin        string
		selfSigned bool
		err        error
	)
	if c.CertFile != "" {
		cert, pin, err = directrelay.LoadCertPair(c.CertFile, c.KeyFile)
	} else {
		var hosts []string
		if u, uerr := url.Parse(c.PublicURL); uerr == nil && u.Hostname() != "" {
			hosts = append(hosts, u.Hostname())
		}
		cert, pin, err = directrelay.LoadOrCreateCert(d.dir, "direct", hosts)
		selfSigned = true
	}
	if err != nil {
		return err
	}
	rawLn, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	tlsLn := tls.NewListener(rawLn, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	})
	tl := newTrackedListener(tlsLn)
	d.ln = tl
	d.certPin = pin
	d.selfSigned = selfSigned
	go func() {
		if err := d.g.serveOn(tl, transportDirect); err != nil {
			logf("direct: %v", err)
		}
	}()
	logf("direct: listening on https://%s", rawLn.Addr())
	return nil
}

func (d *directListener) startRelayLocked(c DirectConfig) error {
	tl, err := directrelay.StartTunnel(directrelay.ClientConfig{
		RelayURL: c.RelayURL,
		HostID:   c.HostID,
		Secret:   c.RelaySecret,
		CertPin:  c.RelayCertPin,
		OnStatus: func(connected bool, statusErr error) {
			d.mu.Lock()
			d.relayOK = connected
			if statusErr != nil {
				d.relayErr = statusErr.Error()
			} else if connected {
				d.relayErr = ""
			}
			d.reportLocked()
			d.mu.Unlock()
		},
	})
	if err != nil {
		return err
	}
	d.tunnel = tl
	go func() {
		if err := d.g.serveOn(tl, transportRelay); err != nil {
			logf("direct relay: %v", err)
		}
	}()
	logf("direct: connecting to relay %s as %s", c.RelayURL, c.HostID)
	return nil
}

func (d *directListener) closeDirectLocked() {
	if d.ln != nil {
		d.ln.Close()
		d.ln = nil
	}
	d.certPin = ""
	d.selfSigned = false
}

func (d *directListener) closeRelayLocked() {
	if d.tunnel != nil {
		d.tunnel.Close()
		d.tunnel = nil
	}
	d.relayOK = false
	d.relayErr = ""
}

func (d *directListener) closeLocked() {
	d.closeDirectLocked()
	d.closeRelayLocked()
}

func (d *directListener) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeLocked()
	saveDirectStatus(d.dir, DirectStatus{})
}

func (d *directListener) reportLocked() {
	s := DirectStatus{
		Running:        true,
		CertPin:        d.certPin,
		SelfSigned:     d.selfSigned,
		RelayConnected: d.relayOK,
		RelayError:     d.relayErr,
		Error:          d.err,
	}
	if d.ln != nil {
		s.Listening = d.ln.Addr().String()
	}
	saveDirectStatus(d.dir, s)
}

// admit validates the 256-bit Bearer token for direct TLS and stateless relay callers.
func (d *directListener) admit(w http.ResponseWriter, r *http.Request, transport string) (map[string]any, bool) {
	d.mu.Lock()
	token := d.conf.Token
	d.mu.Unlock()
	if !tokenMatches(r, token) {
		writeRefusal(w, "WindRex needs this device's pairing token")
		return nil, false
	}
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil && host != "" {
		remote = host
	}
	return map[string]any{
		"kind":        "device",
		"transport":   transport,
		"remote_addr": remote,
		"display":     transport + ":" + remote,
	}, true
}
