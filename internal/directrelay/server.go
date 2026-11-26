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
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
)

const (
	RelaySubprotocol = "windrex.relay.v1"
	HostNameHeader   = "X-WindRex-Host-Name"

	maxHostNameRunes = 64
)

var validHostID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// ValidHostID reports whether id is a valid relay host identifier.
func ValidHostID(id string) bool { return validHostID.MatchString(id) }

// SanitizeHostName cleans a host display name by replacing whitespace runs with
// single ASCII spaces, dropping control characters and invalid UTF-8, trimming
// ends, and bounding length to 64 runes.
func SanitizeHostName(raw string) string {
	var b strings.Builder
	runes := 0
	inSpace := true
	for _, r := range raw {
		if r == utf8.RuneError || !utf8.ValidRune(r) {
			continue
		}
		if r == '\r' || r == '\n' || r == '\t' || unicode.IsSpace(r) {
			if !inSpace && runes < maxHostNameRunes {
				b.WriteByte(' ')
				runes++
				inSpace = true
			}
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if runes >= maxHostNameRunes {
			break
		}
		b.WriteRune(r)
		runes++
		inSpace = false
	}
	return strings.TrimRight(b.String(), " ")
}

// HostInfo describes a single host currently connected to the relay.
// It exposes only discovery metadata and never includes host access tokens.
type HostInfo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	ConnectedAt time.Time `json:"connected_at"`
	FleetAuth   bool      `json:"fleet_auth,omitempty"`
}

type hostEntry struct {
	id          string
	name        string
	connectedAt time.Time
	fleetAuth   bool
	mux         *Mux
}

// Server is a stateless reverse HTTP/WebSocket relay. Hosts register an outbound
// multiplexed WebSocket at /relay/host?id=<host_id>, clients connect to
// /h/<host_id>/<path>, and authenticated clients can list online hosts at
// GET /api/hosts when Secret is configured.
type Server struct {
	// Secret, when non-empty, must be presented by hosts registering via
	// Authorization: Bearer <secret> and by clients querying GET /api/hosts.
	Secret string
	Logf   func(format string, args ...any)

	now func() time.Time

	mu    sync.RWMutex
	hosts map[string]*hostEntry
}

// NewServer creates a stateless relay server.
func NewServer(secret string, logf func(string, ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{
		Secret: secret,
		Logf:   logf,
		now:    time.Now,
		hosts:  map[string]*hostEntry{},
	}
}

func (s *Server) nowUTC() time.Time {
	if s.now != nil {
		return s.now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

// Handler returns the HTTP handler serving /healthz, /api/hosts, /relay/host, and /h/<id>/*.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/api/hosts", s.handleHosts)
	mux.HandleFunc("/relay/host", s.handleHost)
	mux.HandleFunc("/h/", s.handleClient)
	return mux
}

// Close disconnects all registered hosts.
func (s *Server) Close() {
	s.mu.Lock()
	all := make([]*Mux, 0, len(s.hosts))
	for id, entry := range s.hosts {
		all = append(all, entry.mux)
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

// Hosts returns a deterministic snapshot of all currently connected hosts,
// sorted by display name then host id.
func (s *Server) Hosts() []HostInfo {
	s.mu.RLock()
	out := make([]HostInfo, 0, len(s.hosts))
	for _, entry := range s.hosts {
		out = append(out, HostInfo{
			ID:          entry.id,
			Name:        entry.name,
			Path:        "/h/" + entry.id,
			ConnectedAt: entry.connectedAt,
			FleetAuth:   entry.fleetAuth,
		})
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	if s.Secret == "" {
		writeJSONError(w, http.StatusNotFound, "host discovery disabled")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !bearerMatches(r, s.Secret) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(hostsResponse{Hosts: s.Hosts()})
}

type hostsResponse struct {
	Hosts []HostInfo `json:"hosts"`
}

func hostDisplayName(r *http.Request, hostID string) string {
	if name := SanitizeHostName(r.URL.Query().Get("name")); name != "" {
		return name
	}
	if name := SanitizeHostName(r.Header.Get(HostNameHeader)); name != "" {
		return name
	}
	return hostID
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
	hostName := hostDisplayName(r, hostID)
	fleetAuth := parseBoolParam(r.URL.Query().Get("fleet_auth"))
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
	entry := &hostEntry{
		id:          hostID,
		name:        hostName,
		connectedAt: s.nowUTC(),
		fleetAuth:   fleetAuth,
		mux:         m,
	}
	s.mu.Lock()
	old := s.hosts[hostID]
	s.hosts[hostID] = entry
	s.mu.Unlock()
	if old != nil {
		_ = old.mux.Close()
	}
	s.Logf("relay host %s (%q) connected from %s", hostID, hostName, r.RemoteAddr)
	<-m.Done()

	s.mu.Lock()
	if s.hosts[hostID] == entry {
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
	entry := s.hosts[hostID]
	s.mu.RUnlock()
	if entry == nil {
		writeJSONError(w, http.StatusBadGateway, "host is offline")
		return
	}
	m := entry.mux

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

func parseBoolParam(raw string) bool {
	s := strings.TrimSpace(raw)
	return s == "1" || strings.EqualFold(s, "true")
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
