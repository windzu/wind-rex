package rexapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// The Host check is defence in depth against DNS rebinding: a page that
// points a name of its own at the gateway's address is same-origin with
// the gateway, and its requests carry no Origin to refuse. They do carry
// that name in Host, which the gateway answers only when it is one of its
// own: localhost, an IP address (the listener's own among them), or a name
// web.json lists (gorex rexapi web host add NAME). Any other gets 421.
//
// The local socket, direct TLS, and stateless relay transports are not
// checked: a Unix socket has no name to rebind, and direct/relay requests
// require a 256-bit Bearer token.

// checksHost reports whether requests over transport have their Host
// checked.
func checksHost(transport string) bool {
	return transport != transportUnix && transport != transportDirect && transport != transportRelay
}

// hostAllowed reports whether a request's Host is one of the gateway's
// own names.
func (g *Gateway) hostAllowed(r *http.Request) bool {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")), ".")
	if h == "" {
		return false
	}
	if h == "localhost" {
		return true
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	return g.web.host(h)
}

// refuseHost answers a request whose Host is not the gateway's.
func refuseHost(w http.ResponseWriter, r *http.Request) {
	tracef("refused %s: host %q", r.URL.Path, r.Host)
	writeError(w, http.StatusMisdirectedRequest, "this GoRex gateway does not answer to the name "+r.Host+
		" (add it with gorex rexapi web host add NAME)")
}
