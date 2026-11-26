package rex

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// DirectTunnelPath is the authenticated WebSocket endpoint on a Direct TLS or
// stateless relay gateway that tunnels the native rex protocol stream.
const DirectTunnelPath = "/api/v1/rex-tunnel"

const directRedialTimeout = 20 * time.Second

// ErrDirectAuth indicates the remote host rejected the Direct TLS / Relay
// Bearer token (HTTP 401 or 403).
var ErrDirectAuth = errors.New("rex: direct authentication failed")

// ErrDirectPinMismatch indicates the remote server's leaf TLS certificate
// SHA-256 fingerprint does not match the pinned digest in the pairing URL.
var ErrDirectPinMismatch = errors.New("rex: TLS certificate pin mismatch")

// ErrDirectInvalidPin indicates the pairing URL contains a malformed TLS
// certificate pin.
var ErrDirectInvalidPin = errors.New("rex: invalid TLS certificate pin")

// DirectTarget is a parsed Direct TLS or stateless relay pairing URL:
// https://host:port[/h/<id>]/#k=<token>[&p=<sha256>][&n=<name>].
type DirectTarget struct {
	// BaseURL is the sanitized https://host[:port][/h/<id>] endpoint without
	// trailing slash, query, or secret fragment.
	BaseURL string
	// Host is host[:port] from the URL.
	Host string
	// Hostname is the host without port.
	Hostname string
	// Path is the base path prefix without trailing slash ("" or "/h/<id>").
	Path string
	// Token is the 256-bit Bearer token from #k=... (secret: never log).
	Token string
	// Pin is the raw optional certificate pin string from #p=...
	Pin string
	// Name is the optional host display name from #n=...
	Name string
	// IsRelay reports whether the URL routes through a relay /h/<id> prefix.
	IsRelay bool

	pinBytes [sha256.Size]byte
	hasPin   bool
}

// TunnelURL returns the wss:// WebSocket URL for the native rex tunnel,
// preserving any relay base path prefix such as /h/<host_id>.
func (t DirectTarget) TunnelURL() string {
	return "wss://" + t.Host + t.Path + DirectTunnelPath
}

// DefaultName returns the host name from #n= when set, else the URL hostname.
func (t DirectTarget) DefaultName() string {
	if t.Name != "" {
		return t.Name
	}
	return t.Hostname
}

// DisplayAddress returns a secret-free endpoint label safe for UI and logs.
func (t DirectTarget) DisplayAddress() string {
	return t.BaseURL
}

// IsDirectTarget reports whether target looks like a Direct TLS or stateless
// relay pairing URL (an https:// URL). Plain http:// URLs return false.
func IsDirectTarget(target string) bool {
	s := strings.TrimSpace(target)
	return len(s) >= len("https://") && strings.EqualFold(s[:len("https://")], "https://")
}

// ParseDirectTarget validates and parses a Direct TLS or relay pairing URL.
// It accepts both 43-char unpadded base64url SHA-256 pins (emitted by
// DirectConfig.PairURL and windrex-relay) and 64-char hex SHA-256 pins.
func ParseDirectTarget(raw string) (DirectTarget, error) {
	s := strings.TrimSpace(raw)
	if !IsDirectTarget(s) {
		return DirectTarget{}, errors.New("rex: direct target must start with https://")
	}
	u, err := url.Parse(s)
	if err != nil {
		return DirectTarget{}, errors.New("rex: invalid direct target URL")
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.Hostname() == "" {
		return DirectTarget{}, errors.New("rex: direct target must be an https://host[:port] URL")
	}
	if u.User != nil {
		return DirectTarget{}, errors.New("rex: direct target must not contain user info")
	}
	frag, err := url.ParseQuery(u.Fragment)
	if err != nil {
		return DirectTarget{}, errors.New("rex: direct target has invalid fragment")
	}
	token := strings.TrimSpace(frag.Get("k"))
	if token == "" {
		return DirectTarget{}, errors.New("rex: direct target is missing pairing token")
	}

	var (
		pinBytes [sha256.Size]byte
		hasPin   bool
	)
	pin := strings.TrimSpace(frag.Get("p"))
	if pin != "" {
		pinBytes, err = decodeDirectPin(pin)
		if err != nil {
			return DirectTarget{}, err
		}
		hasPin = true
	}

	basePath := strings.TrimRight(u.Path, "/")
	isRelay := strings.HasPrefix(basePath, "/h/") && len(basePath) > len("/h/")
	name := strings.TrimSpace(frag.Get("n"))

	return DirectTarget{
		BaseURL:  "https://" + u.Host + basePath,
		Host:     u.Host,
		Hostname: u.Hostname(),
		Path:     basePath,
		Token:    token,
		Pin:      pin,
		Name:     name,
		IsRelay:  isRelay,
		pinBytes: pinBytes,
		hasPin:   hasPin,
	}, nil
}

// decodeDirectPin parses a SHA-256 leaf certificate pin encoded either as
// 43-character unpadded base64url or 64-character hexadecimal.
func decodeDirectPin(pin string) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	switch len(pin) {
	case 43:
		b, err := base64.RawURLEncoding.DecodeString(pin)
		if err != nil || len(b) != sha256.Size {
			return out, ErrDirectInvalidPin
		}
		copy(out[:], b)
		return out, nil
	case 64:
		b, err := hex.DecodeString(pin)
		if err != nil || len(b) != sha256.Size {
			return out, ErrDirectInvalidPin
		}
		copy(out[:], b)
		return out, nil
	default:
		return out, ErrDirectInvalidPin
	}
}

// SanitizeDirectTarget removes any secret fragment (#k=...) or query token
// from a Direct TLS / Relay URL so it is safe to log or display.
func SanitizeDirectTarget(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if t, err := ParseDirectTarget(s); err == nil {
		return t.BaseURL
	}
	if idx := strings.IndexByte(s, '#'); idx >= 0 {
		s = s[:idx]
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<redacted>"
	}
	u.Fragment = ""
	u.User = nil
	if u.RawQuery != "" {
		q := u.Query()
		q.Del("k")
		u.RawQuery = q.Encode()
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

// directToken extracts the secret token from a Direct TLS / Relay URL, even if
// other parts of the URL are malformed, so scrubbing can strip it.
func directToken(raw string) string {
	s := strings.TrimSpace(raw)
	if idx := strings.IndexByte(s, '#'); idx >= 0 {
		if frag, err := url.ParseQuery(s[idx+1:]); err == nil {
			if tok := strings.TrimSpace(frag.Get("k")); tok != "" {
				return tok
			}
		}
	}
	if u, err := url.Parse(s); err == nil {
		if tok := strings.TrimSpace(u.Query().Get("k")); tok != "" {
			return tok
		}
	}
	return ""
}

// ConnectDirect connects to a remote host over Direct TLS or a stateless relay
// using the pairing URL's Bearer token and optional leaf certificate SHA-256
// pin, returning a native *rex.Client with keepalive enabled.
func ConnectDirect(ctx context.Context, raw string) (*Client, error) {
	target, err := ParseDirectTarget(raw)
	if err != nil {
		return nil, err
	}

	tlsCfg := directTLSConfig(target)
	tr := &http.Transport{
		TLSClientConfig: tlsCfg,
		// WebSocket upgrades require HTTP/1.1.
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}
	httpClient := &http.Client{Transport: tr}

	conn, err := dialDirectConn(ctx, target, httpClient)
	if err != nil {
		tr.CloseIdleConnections()
		return nil, err
	}

	c := newClient(conn, func() (net.Conn, error) {
		rctx, cancel := context.WithTimeout(context.Background(), directRedialTimeout)
		defer cancel()
		return dialDirectConn(rctx, target, httpClient)
	})
	c.keepalive = true
	c.closer = directTransportCloser{tr: tr}
	return c, nil
}

func directTLSConfig(target DirectTarget) *tls.Config {
	if !target.hasPin {
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}
	want := target.pinBytes
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Standard CA verification is skipped for self-signed pinned certs;
		// VerifyPeerCertificate enforces an exact leaf DER SHA-256 match.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("rex: peer presented no TLS certificate")
			}
			got := sha256.Sum256(rawCerts[0])
			if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				return ErrDirectPinMismatch
			}
			return nil
		},
	}
}

func dialDirectConn(ctx context.Context, target DirectTarget, httpClient *http.Client) (net.Conn, error) {
	ws, resp, err := websocket.Dial(ctx, target.TunnelURL(), &websocket.DialOptions{
		HTTPClient: httpClient,
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + target.Token},
		},
		CompressionMode: websocket.CompressionDisabled,
	})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return nil, fmt.Errorf("%w (%s)", ErrDirectAuth, target.DisplayAddress())
		}
		if errors.Is(err, ErrDirectPinMismatch) {
			return nil, ErrDirectPinMismatch
		}
		msg := strings.ReplaceAll(err.Error(), target.Token, "<redacted>")
		return nil, fmt.Errorf("rex: connecting to %s: %s", target.DisplayAddress(), msg)
	}

	// Bound the connection lifetime to Close(), not the short-lived dial ctx.
	connCtx, connCancel := context.WithCancel(context.Background())
	nc := websocket.NetConn(connCtx, ws, websocket.MessageBinary)
	return &directWSConn{Conn: nc, cancel: connCancel}, nil
}

type directWSConn struct {
	net.Conn
	cancel context.CancelFunc
	once   sync.Once
}

func (c *directWSConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		c.cancel()
	})
	return err
}

type directTransportCloser struct {
	tr *http.Transport
}

func (d directTransportCloser) Close() error {
	d.tr.CloseIdleConnections()
	return nil
}
