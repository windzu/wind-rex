package directrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	defaultDiscoverTimeout = 15 * time.Second
	maxDiscoverBodyBytes   = 1 << 20
)

var (
	// ErrDiscoveryDisabled is returned by DiscoverHosts when the relay returns
	// 404 Not Found because host discovery is disabled (no relay secret set).
	ErrDiscoveryDisabled = errors.New("directrelay: host discovery disabled")
	// ErrUnauthorized is returned by DiscoverHosts when the relay rejects the
	// Bearer token with 401 Unauthorized.
	ErrUnauthorized = errors.New("directrelay: unauthorized")
)

// ClientConfig configures a host's outbound tunnel to a stateless relay.
type ClientConfig struct {
	RelayURL  string
	HostID    string
	HostName  string
	Secret    string
	CertPin   string
	FleetAuth bool
	OnStatus  func(connected bool, err error)
}

// TunnelListener maintains a persistent outbound connection to a relay server
// and exposes incoming relayed client streams via net.Listener.
type TunnelListener struct {
	cfg      ClientConfig
	ctx      context.Context
	cancel   context.CancelFunc
	acceptCh chan net.Conn
	done     chan struct{}
	once     sync.Once

	mu  sync.Mutex
	mux *Mux
}

// StartTunnel starts a TunnelListener that reconnects automatically until closed.
func StartTunnel(cfg ClientConfig) (*TunnelListener, error) {
	if _, err := RelayRegisterURL(cfg.RelayURL, cfg.HostID); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &TunnelListener{
		cfg:      cfg,
		ctx:      ctx,
		cancel:   cancel,
		acceptCh: make(chan net.Conn, 64),
		done:     make(chan struct{}),
	}
	go l.loop()
	return l, nil
}

// RelayRegisterURL constructs the WebSocket URL used by a host to register on a relay.
func RelayRegisterURL(relayBase, hostID string) (string, error) {
	return RelayRegisterURLWithName(relayBase, hostID, "")
}

// RelayRegisterURLWithName constructs the WebSocket URL used by a host to
// register on a relay, attaching an optional sanitized display name.
func RelayRegisterURLWithName(relayBase, hostID, hostName string) (string, error) {
	return RelayRegisterURLWithOptions(relayBase, hostID, hostName, false)
}

// RelayRegisterURLWithOptions constructs the WebSocket URL used by a host to
// register on a relay, attaching an optional sanitized display name and
// fleet_auth flag.
func RelayRegisterURLWithOptions(relayBase, hostID, hostName string, fleetAuth bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(relayBase))
	if err != nil || u.Host == "" {
		return "", errors.New("invalid relay URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", errors.New("relay URL must use https:// or wss://")
	}
	if !ValidHostID(hostID) {
		return "", errors.New("invalid host id")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/relay/host"
	q := u.Query()
	q.Set("id", hostID)
	if name := SanitizeHostName(hostName); name != "" {
		q.Set("name", name)
	}
	if fleetAuth {
		q.Set("fleet_auth", "1")
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

// RelayClientBaseURL returns the HTTPS/HTTP base URL that clients connect to for hostID.
func RelayClientBaseURL(relayBase, hostID string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(relayBase))
	if err != nil || u.Host == "" {
		return "", errors.New("invalid relay URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "https"
	case "http", "ws":
		u.Scheme = "http"
	default:
		return "", errors.New("relay URL must use https:// or wss://")
	}
	if !ValidHostID(hostID) {
		return "", errors.New("invalid host id")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/h/" + hostID
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// RelayHostsURL returns the HTTPS/HTTP discovery endpoint URL for a relay.
func RelayHostsURL(relayBase string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(relayBase))
	if err != nil || u.Host == "" {
		return "", errors.New("invalid relay URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "https"
	case "http", "ws":
		u.Scheme = "http"
	default:
		return "", errors.New("relay URL must use https:// or wss://")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/hosts"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// DiscoverHosts queries GET /api/hosts on a windrex-relay server using
// relayToken for Bearer authentication and optional tlsPin for leaf
// certificate SHA-256 verification.
func DiscoverHosts(ctx context.Context, relayURL, relayToken, tlsPin string) ([]HostInfo, error) {
	endpoint, err := RelayHostsURL(relayURL)
	if err != nil {
		return nil, err
	}
	if tlsPin != "" && !ValidPin(tlsPin) {
		return nil, errors.New("directrelay: invalid certificate pin")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultDiscoverTimeout)
		defer cancel()
	}
	tr := &http.Transport{
		TLSClientConfig: TLSConfigWithPin(tlsPin),
	}
	defer tr.CloseIdleConnections()

	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if relayToken != "" {
		req.Header.Set("Authorization", "Bearer "+relayToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoverBodyBytes))
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrDiscoveryDisabled
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	default:
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("directrelay: discover hosts: HTTP %d: %s", resp.StatusCode, apiErr.Error)
		}
		return nil, fmt.Errorf("directrelay: discover hosts: HTTP %d", resp.StatusCode)
	}

	var out hostsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("directrelay: decode host list: %w", err)
	}
	if out.Hosts == nil {
		out.Hosts = []HostInfo{}
	}
	return out.Hosts, nil
}

func defaultHostName() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return SanitizeHostName(stripLocalSuffix(h))
}

func stripLocalSuffix(s string) string {
	s = strings.TrimSpace(s)
	const suffix = ".local"
	if len(s) > len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		s = s[:len(s)-len(suffix)]
	}
	return s
}

func (l *TunnelListener) resolvedHostName() string {
	if name := SanitizeHostName(l.cfg.HostName); name != "" {
		return name
	}
	return defaultHostName()
}

func (l *TunnelListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.acceptCh:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *TunnelListener) Close() error {
	l.once.Do(func() {
		l.cancel()
		l.mu.Lock()
		m := l.mux
		l.mux = nil
		l.mu.Unlock()
		if m != nil {
			_ = m.Close()
		}
		close(l.done)
	})
	return nil
}

func (l *TunnelListener) Addr() net.Addr { return muxAddr(l.cfg.RelayURL) }

func (l *TunnelListener) report(connected bool, err error) {
	if l.cfg.OnStatus != nil {
		l.cfg.OnStatus(connected, err)
	}
}

func (l *TunnelListener) loop() {
	backoff := 200 * time.Millisecond
	const maxBackoff = 5 * time.Second
	for {
		if l.ctx.Err() != nil {
			return
		}
		dialURL, err := RelayRegisterURLWithOptions(l.cfg.RelayURL, l.cfg.HostID, l.resolvedHostName(), l.cfg.FleetAuth)
		if err != nil {
			l.report(false, err)
			return
		}
		opts := &websocket.DialOptions{
			Subprotocols:    []string{RelaySubprotocol},
			CompressionMode: websocket.CompressionDisabled,
		}
		if l.cfg.Secret != "" {
			opts.HTTPHeader = http.Header{"Authorization": []string{"Bearer " + l.cfg.Secret}}
		}
		if tlsCfg := TLSConfigWithPin(l.cfg.CertPin); tlsCfg != nil {
			opts.HTTPClient = &http.Client{
				Transport: &http.Transport{TLSClientConfig: tlsCfg},
			}
		}
		dctx, cancel := context.WithTimeout(l.ctx, 15*time.Second)
		ws, resp, err := websocket.Dial(dctx, dialURL, opts)
		cancel()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			l.report(false, err)
			select {
			case <-l.ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, maxBackoff)
				continue
			}
		}
		backoff = 200 * time.Millisecond
		m := NewMux(ws, "host:"+l.cfg.HostID, l.cfg.RelayURL)
		l.mu.Lock()
		l.mux = m
		l.mu.Unlock()
		l.report(true, nil)

		for {
			conn, err := m.Accept()
			if err != nil {
				break
			}
			select {
			case l.acceptCh <- conn:
			case <-l.done:
				_ = conn.Close()
				_ = m.Close()
				return
			}
		}

		l.mu.Lock()
		if l.mux == m {
			l.mux = nil
		}
		l.mu.Unlock()
		_ = m.Close()
		l.report(false, nil)
		select {
		case <-l.ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}
