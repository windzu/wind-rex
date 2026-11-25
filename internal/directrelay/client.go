package directrelay

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ClientConfig configures a host's outbound tunnel to a stateless relay.
type ClientConfig struct {
	RelayURL string
	HostID   string
	Secret   string
	CertPin  string
	OnStatus func(connected bool, err error)
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
		dialURL, err := RelayRegisterURL(l.cfg.RelayURL, l.cfg.HostID)
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
