package rex

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"gorex/internal/directrelay"
)

// DirectTunnelPath is the authenticated WebSocket endpoint on a Direct TLS or
// stateless relay gateway that tunnels the native rex protocol stream.
const DirectTunnelPath = "/api/v1/rex-tunnel"

// LANHintPath is the authenticated HTTP endpoint returning a host's private
// LAN Direct TLS addresses and certificate pin, and doubling as the
// non-disruptive LAN reachability probe (docs/NET-011-lan-direct-upgrade.md).
const LANHintPath = "/api/v1/lan-hint"

const (
	directRedialTimeout = 20 * time.Second
	maxLANHintBodyBytes = 64 << 10
)

var (
	// lanHeadStart gives local LAN candidates a short window to win before
	// dialing the relay fallback (staggered Happy Eyeballs). Tests may shorten.
	lanHeadStart    = 200 * time.Millisecond
	lanDialTimeout  = 800 * time.Millisecond
	lanProbeTimeout = 400 * time.Millisecond
	// allowLoopbackLANCandidate is enabled only in unit tests so loopback
	// httptest servers can exercise the LAN Happy Eyeballs dialer.
	allowLoopbackLANCandidate = false
)

// DirectCandidate is a pinned private-LAN Direct TLS endpoint offered as an
// automatic local upgrade for a relay host.
type DirectCandidate struct {
	Addr string
	Pin  string
}

// DirectRouteKind identifies which transport path a direct/relay client uses.
type DirectRouteKind string

const (
	RouteDirect    DirectRouteKind = "direct"
	RouteLANDirect DirectRouteKind = "lan_direct"
	RouteRelay     DirectRouteKind = "relay"
)

// DirectRouteInfo describes the winning transport route of a connected client.
type DirectRouteInfo struct {
	Kind DirectRouteKind
	Host string
}

// LANHint is the payload returned by GET /api/v1/lan-hint.
type LANHint struct {
	LANAddrs  []string `json:"lan_addrs,omitempty"`
	DirectPin string   `json:"direct_pin,omitempty"`
}

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
	c, _, err := ConnectDirectWithCandidates(ctx, raw, nil)
	return c, err
}

// ConnectDirectWithCandidates connects to a remote host like ConnectDirect,
// racing valid private-LAN Direct TLS candidates ahead of the relay URL with a
// staggered Happy Eyeballs head start (docs/NET-011-lan-direct-upgrade.md).
// The winning transport is pinned onto the returned *Client for pane stream redials.
func ConnectDirectWithCandidates(ctx context.Context, raw string, candidates []DirectCandidate) (*Client, DirectRouteInfo, error) {
	primary, err := ParseDirectTarget(raw)
	if err != nil {
		return nil, DirectRouteInfo{}, err
	}
	primaryKind := RouteDirect
	if primary.IsRelay {
		primaryKind = RouteRelay
	}
	primaryRoute := DirectRouteInfo{Kind: primaryKind, Host: primary.Host}

	lanTargets := buildLANCandidateTargets(primary.Token, candidates)
	if !primary.IsRelay || len(lanTargets) == 0 {
		c, err := connectSingleDirect(ctx, primary)
		if err != nil {
			return nil, DirectRouteInfo{}, err
		}
		return c, primaryRoute, nil
	}

	type attemptResult struct {
		conn   net.Conn
		target DirectTarget
		tr     *http.Transport
		client *http.Client
		route  DirectRouteInfo
		isLAN  bool
		err    error
	}

	raceCtx, raceCancel := context.WithCancel(ctx)
	defer raceCancel()

	results := make(chan attemptResult, len(lanTargets)+1)
	allLANFailed := make(chan struct{})
	var lanWG sync.WaitGroup

	for _, lt := range lanTargets {
		lanWG.Add(1)
		go func(t DirectTarget) {
			defer lanWG.Done()
			tr, hc := newDirectHTTPClient(t)
			lctx, lcancel := context.WithTimeout(raceCtx, lanDialTimeout)
			conn, derr := dialDirectConn(lctx, t, hc)
			lcancel()
			if derr != nil {
				tr.CloseIdleConnections()
				results <- attemptResult{isLAN: true, err: derr}
				return
			}
			results <- attemptResult{
				conn:   conn,
				target: t,
				tr:     tr,
				client: hc,
				route:  DirectRouteInfo{Kind: RouteLANDirect, Host: t.Host},
				isLAN:  true,
			}
		}(lt)
	}
	go func() {
		lanWG.Wait()
		close(allLANFailed)
	}()

	relayStarted := make(chan bool, 1)
	go func() {
		headTimer := time.NewTimer(lanHeadStart)
		defer headTimer.Stop()
		select {
		case <-raceCtx.Done():
			relayStarted <- false
			return
		case <-allLANFailed:
		case <-headTimer.C:
		}
		if raceCtx.Err() != nil {
			relayStarted <- false
			return
		}
		relayStarted <- true
		tr, hc := newDirectHTTPClient(primary)
		conn, derr := dialDirectConn(raceCtx, primary, hc)
		if derr != nil {
			tr.CloseIdleConnections()
			results <- attemptResult{isLAN: false, err: derr}
			return
		}
		results <- attemptResult{
			conn:   conn,
			target: primary,
			tr:     tr,
			client: hc,
			route:  primaryRoute,
			isLAN:  false,
		}
	}()

	var (
		winner   *attemptResult
		relayErr error
		lastErr  error
		lanDone  int
		gotRelay bool
	)
	for {
		if winner != nil {
			break
		}
		if lanDone == len(lanTargets) && gotRelay {
			break
		}
		res := <-results
		if res.isLAN {
			lanDone++
		} else {
			gotRelay = true
		}
		if res.err == nil {
			r := res
			winner = &r
			raceCancel()
			break
		}
		if !res.isLAN {
			relayErr = res.err
		}
		lastErr = res.err
	}

	// Drain any remaining in-flight attempts in background so late winners close cleanly.
	remainingLAN := len(lanTargets) - lanDone
	go func() {
		started := <-relayStarted
		remaining := remainingLAN
		if started && !gotRelay {
			remaining++
		}
		for i := 0; i < remaining; i++ {
			res := <-results
			if res.conn != nil {
				_ = res.conn.Close()
			}
			if res.tr != nil {
				res.tr.CloseIdleConnections()
			}
		}
	}()

	if winner == nil {
		if relayErr != nil {
			return nil, DirectRouteInfo{}, relayErr
		}
		return nil, DirectRouteInfo{}, lastErr
	}

	winTarget, winHTTP, winTr := winner.target, winner.client, winner.tr
	c := newClient(winner.conn, func() (net.Conn, error) {
		rctx, cancel := context.WithTimeout(context.Background(), directRedialTimeout)
		defer cancel()
		return dialDirectConn(rctx, winTarget, winHTTP)
	})
	c.keepalive = true
	c.closer = directTransportCloser{tr: winTr}
	return c, winner.route, nil
}

func connectSingleDirect(ctx context.Context, target DirectTarget) (*Client, error) {
	tr, httpClient := newDirectHTTPClient(target)
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

func newDirectHTTPClient(target DirectTarget) (*http.Transport, *http.Client) {
	tr := &http.Transport{
		TLSClientConfig: directTLSConfig(target),
		// WebSocket upgrades require HTTP/1.1.
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}
	return tr, &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func buildLANCandidateTargets(token string, candidates []DirectCandidate) []DirectTarget {
	if token == "" || len(candidates) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, min(len(candidates), directrelay.MaxLANAddrs))
	out := make([]DirectTarget, 0, min(len(candidates), directrelay.MaxLANAddrs))
	for _, cand := range candidates {
		ap, err := netip.ParseAddrPort(strings.TrimSpace(cand.Addr))
		if err != nil || ap.Port() == 0 {
			continue
		}
		ip := ap.Addr().Unmap()
		if !directrelay.IsPrivateLANIP(ip) && !(allowLoopbackLANCandidate && ip.IsLoopback()) {
			continue
		}
		pin := strings.TrimSpace(cand.Pin)
		if pin == "" {
			continue
		}
		pinBytes, err := decodeDirectPin(pin)
		if err != nil {
			continue
		}
		host := netip.AddrPortFrom(ip, ap.Port()).String()
		if _, dup := seen[host]; dup {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, DirectTarget{
			BaseURL:  "https://" + host,
			Host:     host,
			Hostname: ip.String(),
			Path:     "",
			Token:    token,
			Pin:      pin,
			IsRelay:  false,
			pinBytes: pinBytes,
			hasPin:   true,
		})
		if len(out) >= directrelay.MaxLANAddrs {
			break
		}
	}
	return out
}

// FetchLANHint queries authenticated GET /api/v1/lan-hint on a Direct TLS or
// relay target to learn its private LAN Direct TLS candidates and cert pin.
func FetchLANHint(ctx context.Context, raw string) (LANHint, error) {
	target, err := ParseDirectTarget(raw)
	if err != nil {
		return LANHint{}, err
	}
	tr, hc := newDirectHTTPClient(target)
	defer tr.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.BaseURL+LANHintPath, nil)
	if err != nil {
		return LANHint{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+target.Token)

	resp, err := hc.Do(req)
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), target.Token, "<redacted>")
		return LANHint{}, fmt.Errorf("rex: lan hint %s: %s", target.DisplayAddress(), msg)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return LANHint{}, fmt.Errorf("%w (%s)", ErrDirectAuth, target.DisplayAddress())
	}
	if resp.StatusCode != http.StatusOK {
		return LANHint{}, fmt.Errorf("rex: lan hint %s: HTTP %d", target.DisplayAddress(), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLANHintBodyBytes))
	if err != nil {
		return LANHint{}, err
	}
	var out LANHint
	if err := json.Unmarshal(body, &out); err != nil {
		return LANHint{}, err
	}
	out.DirectPin = strings.TrimSpace(out.DirectPin)
	if _, err := decodeDirectPin(out.DirectPin); err != nil {
		return LANHint{}, nil
	}
	out.LANAddrs = directrelay.SanitizeLANAddrs(out.LANAddrs)
	if len(out.LANAddrs) == 0 {
		out.DirectPin = ""
	}
	return out, nil
}

// ProbeLANDirect checks whether any private-LAN Direct TLS candidate answers
// authenticated GET /api/v1/lan-hint with HTTP 200 within lanProbeTimeout.
// It never disrupts existing terminal connections.
func ProbeLANDirect(ctx context.Context, raw string, candidates []DirectCandidate) (DirectCandidate, error) {
	target, err := ParseDirectTarget(raw)
	if err != nil {
		return DirectCandidate{}, err
	}
	lanTargets := buildLANCandidateTargets(target.Token, candidates)
	if len(lanTargets) == 0 {
		return DirectCandidate{}, errors.New("rex: no valid LAN candidates")
	}

	pctx, cancel := context.WithTimeout(ctx, lanProbeTimeout)
	defer cancel()

	type probeRes struct {
		cand DirectCandidate
		err  error
	}
	ch := make(chan probeRes, len(lanTargets))
	for _, lt := range lanTargets {
		go func(t DirectTarget) {
			tr, hc := newDirectHTTPClient(t)
			defer tr.CloseIdleConnections()
			req, rerr := http.NewRequestWithContext(pctx, http.MethodGet, t.BaseURL+LANHintPath, nil)
			if rerr != nil {
				ch <- probeRes{err: rerr}
				return
			}
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Authorization", "Bearer "+t.Token)
			resp, derr := hc.Do(req)
			if derr != nil {
				ch <- probeRes{err: derr}
				return
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				ch <- probeRes{err: fmt.Errorf("HTTP %d", resp.StatusCode)}
				return
			}
			ch <- probeRes{cand: DirectCandidate{Addr: t.Host, Pin: t.Pin}}
		}(lt)
	}

	var lastErr error
	for range lanTargets {
		res := <-ch
		if res.err == nil {
			cancel()
			return res.cand, nil
		}
		lastErr = res.err
	}
	if lastErr == nil {
		lastErr = errors.New("rex: LAN probe failed")
	}
	return DirectCandidate{}, lastErr
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
