package directrelay

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const RelaySubprotocol = "windrex.relay.v1"

var validHostID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// ValidHostID reports whether id is a valid relay host identifier.
func ValidHostID(id string) bool { return validHostID.MatchString(id) }

// Server is a stateless reverse HTTP/WebSocket relay. Hosts register an outbound
// multiplexed WebSocket at /relay/host?id=<host_id>, and clients connect to
// /h/<host_id>/<path>.
type Server struct {
	// Secret, when non-empty, must be presented by hosts registering via
	// Authorization: Bearer <secret>.
	Secret string
	Logf   func(format string, args ...any)

	mu    sync.RWMutex
	hosts map[string]*Mux
}

// NewServer creates a stateless relay server.
func NewServer(secret string, logf func(string, ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{
		Secret: secret,
		Logf:   logf,
		hosts:  map[string]*Mux{},
	}
}

// Handler returns the HTTP handler serving /healthz, /relay/host, and /h/<id>/*.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/relay/host", s.handleHost)
	mux.HandleFunc("/h/", s.handleClient)
	return mux
}

// Close disconnects all registered hosts.
func (s *Server) Close() {
	s.mu.Lock()
	all := make([]*Mux, 0, len(s.hosts))
	for id, m := range s.hosts {
		all = append(all, m)
		delete(s.hosts, id)
	}
	s.mu.Unlock()
	for _, m := range all {
		_ = m.Close()
	}
}

// HostOnline reports whether hostID currently has an active relay tunnel.
func (s *Server) HostOnline(hostID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.hosts[hostID]
	return ok
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	if s.Secret != "" && !bearerMatches(r, s.Secret) {
		writeJSONError(w, http.StatusForbidden, "invalid relay secret")
		return
	}
	hostID := r.URL.Query().Get("id")
	if !ValidHostID(hostID) {
		writeJSONError(w, http.StatusBadRequest, "invalid host id")
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{RelaySubprotocol},
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		s.Logf("relay host %s accept: %v", hostID, err)
		return
	}
	m := NewMux(ws, "relay:"+hostID, r.RemoteAddr)
	s.mu.Lock()
	old := s.hosts[hostID]
	s.hosts[hostID] = m
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	s.Logf("relay host %s connected from %s", hostID, r.RemoteAddr)
	<-m.Done()

	s.mu.Lock()
	if s.hosts[hostID] == m {
		delete(s.hosts, hostID)
	}
	s.mu.Unlock()
	s.Logf("relay host %s disconnected", hostID)
}

func (s *Server) handleClient(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/h/")
	hostID, tail, hasSlash := strings.Cut(rest, "/")
	if !ValidHostID(hostID) {
		writeJSONError(w, http.StatusNotFound, "unknown host")
		return
	}
	s.mu.RLock()
	m := s.hosts[hostID]
	s.mu.RUnlock()
	if m == nil {
		writeJSONError(w, http.StatusBadGateway, "host is offline")
		return
	}

	targetPath := "/"
	if hasSlash {
		targetPath = "/" + tail
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return m.Open(ctx)
		},
		DisableCompression:    true,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "windrex.relay"
			pr.Out.URL.Path = targetPath
			pr.Out.URL.RawPath = ""
			pr.Out.Host = pr.In.Host
		},
		Transport:     transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			writeJSONError(w, http.StatusBadGateway, "host unreachable")
		},
	}
	proxy.ServeHTTP(w, r)
}

func bearerMatches(r *http.Request, secret string) bool {
	scheme, rest, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(rest)), []byte(secret)) == 1
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
