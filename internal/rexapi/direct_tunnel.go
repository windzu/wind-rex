package rexapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"gorex/internal/rex"
)

// rexTunnelPath is the authenticated WebSocket endpoint that tunnels the
// native rex session-server stream for desktop Direct TLS and relay clients
// (docs/NET-007-desktop-direct-client.md).
const rexTunnelPath = "/api/v1/rex-tunnel"

// lanHintPath is the authenticated HTTP endpoint that reports this host's
// private LAN Direct TLS addresses and certificate pin, and doubles as the
// non-disruptive LAN reachability/auth probe (docs/NET-011-lan-direct-upgrade.md).
const lanHintPath = "/api/v1/lan-hint"

const rexTunnelDialTimeout = 5 * time.Second

func (g *Gateway) handleLANHint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	transport := transportOf(r)
	if transport != transportDirect && transport != transportRelay {
		writeRefusal(w, "lan-hint is only available over direct TLS or relay")
		return
	}
	if g.direct == nil {
		writeRefusal(w, "direct access is off")
		return
	}
	addrs, pin := g.direct.lanHint()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		OK        bool     `json:"ok"`
		LANAddrs  []string `json:"lan_addrs,omitempty"`
		DirectPin string   `json:"direct_pin,omitempty"`
	}{
		OK:        true,
		LANAddrs:  addrs,
		DirectPin: pin,
	})
}

func (g *Gateway) rexSocketPath() string {
	if g.opt.RexSocket != "" {
		return g.opt.RexSocket
	}
	return rex.SocketPath()
}

// handleRexTunnel upgrades an authenticated Direct TLS or stateless relay
// request to a binary WebSocket and splices it 1:1 with the local session
// server Unix socket. It is never served on LAN, anonymous TCP, or other
// transports.
func (g *Gateway) handleRexTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	transport := transportOf(r)
	if transport != transportDirect && transport != transportRelay {
		writeRefusal(w, "rex-tunnel is only available over direct TLS or relay")
		return
	}
	if g.direct == nil {
		writeRefusal(w, "direct access is off")
		return
	}

	g.mu.Lock()
	if g.closing {
		g.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "the server is stopping")
		return
	}
	g.wg.Add(1)
	g.mu.Unlock()
	defer g.wg.Done()

	dialCtx, dialCancel := context.WithTimeout(r.Context(), rexTunnelDialTimeout)
	unixConn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", g.rexSocketPath())
	dialCancel()
	if err != nil {
		tracef("rex-tunnel dial session server: %v", err)
		writeError(w, http.StatusServiceUnavailable, "session server unavailable")
		return
	}

	// Origin was already checked by g.checkOrigin in Gateway.Handler(), and
	// the 256-bit Bearer token was verified in directListener.admit().
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		_ = unixConn.Close()
		tracef("rex-tunnel accept: %v", err)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-g.done:
			cancel()
		case <-stopWatch:
		}
	}()

	wsConn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer wsConn.Close()
	defer unixConn.Close()

	spliceTunnel(wsConn, unixConn)
}

// spliceTunnel copies bytes bidirectionally between a and b until either side
// reaches EOF or errors, closing both connections cleanly before returning.
func spliceTunnel(a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(b, a)
		_ = b.Close()
		_ = a.Close()
	}()
	_, _ = io.Copy(a, b)
	_ = a.Close()
	_ = b.Close()
	<-done
}
