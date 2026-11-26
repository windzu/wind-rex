package rexapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorex/internal/rex"

	"github.com/coder/websocket"
	"rsc.io/qr"
)

// gatewayWelcome is the part of a gateway's welcome the app reports.
type gatewayWelcome struct {
	APIRevision       int `json:"api_revision"`
	APIServerRevision int `json:"api_server_revision"`
}

// callGateway calls one method of the gateway serving the socket at
// path, as a Rex client does, and returns its payload.
func callGateway(path, method string, payload any) (json.RawMessage, error) {
	out, _, err := callGatewayWelcome(path, 20*time.Second, gatewayCall{method, payload})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// gatewayCall is a method of the gateway to call, with its payload.
type gatewayCall struct {
	method  string
	payload any
}

// callGatewayWelcome is callGateway for each of calls, on one connection,
// with the gateway's welcome, given up after within: the payloads, in the
// order of calls.
func callGatewayWelcome(path string, within time.Duration, calls ...gatewayCall) ([]json.RawMessage, gatewayWelcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	httpc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
	ws, resp, err := websocket.Dial(ctx, "ws://localhost/api/control", &websocket.DialOptions{
		HTTPClient: httpc, Subprotocols: []string{controlProtocol},
	})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, gatewayWelcome{}, fmt.Errorf("no Rex API gateway on %s (run gorex rexapi): %w", path, err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxMessage)
	hello, _ := json.Marshal(map[string]any{
		"type": "hello", "api_revision": apiRevision,
		"client": map[string]any{"kind": "cli", "name": "gorex", "platform": platform()},
	})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		return nil, gatewayWelcome{}, err
	}
	var wel struct {
		Type string `json:"type"`
		gatewayWelcome
	}
	if _, b, err := ws.Read(ctx); err != nil {
		return nil, gatewayWelcome{}, err
	} else if json.Unmarshal(b, &wel); wel.Type != "welcome" {
		return nil, gatewayWelcome{}, fmt.Errorf("the gateway answered %s", b)
	}
	for i, call := range calls {
		req := map[string]any{"type": "request", "id": "cli-" + strconv.Itoa(i+1), "method": call.method}
		if call.payload != nil {
			req["payload"] = call.payload
		}
		b, _ := json.Marshal(req)
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			return nil, gatewayWelcome{}, err
		}
	}
	out := make([]json.RawMessage, len(calls))
	for left := len(calls); left > 0; {
		_, b, err := ws.Read(ctx)
		if err != nil {
			return nil, gatewayWelcome{}, err
		}
		var resp struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Payload json.RawMessage `json:"payload"`
			Error   *apiError       `json:"error"`
		}
		if json.Unmarshal(b, &resp) != nil || resp.Type != "response" {
			continue
		}
		n, ok := strings.CutPrefix(resp.ID, "cli-")
		i, err := strconv.Atoi(n)
		if !ok || err != nil || i < 1 || i > len(calls) || out[i-1] != nil {
			continue
		}
		if resp.Error != nil {
			return nil, gatewayWelcome{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
		}
		out[i-1] = resp.Payload
		if out[i-1] == nil {
			out[i-1] = json.RawMessage("null")
		}
		left--
	}
	ws.Close(websocket.StatusNormalClosure, "")
	return out, wel.gatewayWelcome, nil
}

// GatewayInfo is what the settings window shows of the Rex API gateway.
type GatewayInfo struct {
	// RexVersion is the version string the gateway reports.
	RexVersion string
	// APIRevision is the revision this build negotiated, and
	// ServerRevision the newest the gateway serves.
	APIRevision, ServerRevision int
	// MinRevision is the oldest the gateway serves.
	MinRevision int
	// Listeners are the endpoints the gateway accepts connections on,
	// primary first; none from a gateway older than revision 12.
	Listeners []Listener
}

// GatewayStatus asks the gateway that runs on this machine for its
// version, revisions and listeners, on one connection, failing when none
// runs.
func GatewayStatus() (GatewayInfo, error) {
	raw, wel, err := callGatewayWelcome(SocketPath(), 20*time.Second, gatewayCall{method: "server.status"}, gatewayCall{method: "server.version"})
	if err != nil {
		return GatewayInfo{}, err
	}
	var st statusResult
	var v struct {
		RexVersion string `json:"rex_version"`
	}
	if err := json.Unmarshal(raw[0], &st); err != nil {
		return GatewayInfo{}, err
	}
	if err := json.Unmarshal(raw[1], &v); err != nil {
		return GatewayInfo{}, err
	}
	return GatewayInfo{
		RexVersion:     v.RexVersion,
		APIRevision:    wel.APIRevision,
		ServerRevision: wel.APIServerRevision,
		MinRevision:    st.APIMinRevision,
		Listeners:      st.Listeners,
	}, nil
}

// callMain is "gorex rexapi call METHOD [JSON]": it calls a method of
// the gateway that runs and prints its result.
func callMain(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: gorex rexapi call METHOD [JSON]")
		return 2
	}
	var payload any
	if len(args) == 2 {
		if err := json.Unmarshal([]byte(args[1]), &payload); err != nil {
			fmt.Fprintf(os.Stderr, "gorex rexapi call: the arguments are not JSON: %v\n", err)
			return 2
		}
	}
	out, err := callGateway(SocketPath(), args[0], payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gorex rexapi call: %v\n", err)
		return 1
	}
	fmt.Println(string(out))
	return 0
}

// lanMain is "gorex rexapi lan on|off|status|listen ADDR|allow NET|deny
// NET|token show|token rotate|legacy add|remove|list": the gateway's
// listener on the local network, which the GoRex server runs while it is
// on.
func lanMain(args []string) int {
	usage := "usage: gorex rexapi lan on | off | status | qr [--origin URL] | listen HOST:PORT | allow NETWORK | deny NETWORK | token show | token rotate | legacy add ADDRESS | legacy remove ADDRESS | legacy list"
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	dir := StateDir()
	conf, err := LoadLAN(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gorex rexapi lan: %v\n", err)
		return 1
	}
	change := true
	switch {
	case args[0] == "status" && len(args) == 1:
		change = false
	case args[0] == "qr":
		return lanQRMain(dir, args[1:])
	case args[0] == "token" && len(args) == 2 && args[1] == "show":
		if conf.Token == "" {
			// Made now, as the gateway would make it.
			if err := SaveLAN(dir, conf); err != nil {
				fmt.Fprintf(os.Stderr, "gorex rexapi lan: %v\n", err)
				return 1
			}
			conf, _ = LoadLAN(dir)
		}
		fmt.Println(conf.Token)
		return 0
	case args[0] == "token" && len(args) == 2 && args[1] == "rotate":
		conf.Token = NewLANToken()
		fmt.Fprintln(os.Stderr, "gorex rexapi lan: a new pairing token: every device on the local network must scan the new pairing QR code")
	case args[0] == "legacy" && len(args) == 2 && args[1] == "list":
		for _, a := range conf.LegacyOpen {
			fmt.Println(a)
		}
		return 0
	case args[0] == "legacy" && len(args) == 3 && (args[1] == "add" || args[1] == "remove"):
		a, err := ParseLegacyAddr(args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "gorex rexapi lan: %v\n", err)
			return 2
		}
		conf.LegacyOpen = slices.DeleteFunc(conf.LegacyOpen, func(s string) bool {
			b, err := ParseLegacyAddr(s)
			return err == nil && b == a
		})
		if args[1] == "add" {
			conf.LegacyOpen = append(conf.LegacyOpen, a.String())
			if nets, err := ParseNetworks(conf.Allow); err == nil {
				if _, ok := allowedPeer(nets, net.JoinHostPort(a.String(), "1")); !ok {
					fmt.Fprintf(os.Stderr, "gorex rexapi lan: %s is in no network allowed: add it with gorex rexapi lan allow %s\n", a, a)
				}
			}
		}
	case args[0] == "on" && len(args) == 1:
		conf = conf.WithDefaults()
		conf.Enabled = true
		if conf.Listen == "" {
			fmt.Fprintln(os.Stderr, "gorex rexapi lan: this machine has no private IPv4 address: set one with gorex rexapi lan listen HOST:PORT")
			return 1
		}
	case args[0] == "off" && len(args) == 1:
		conf.Enabled = false
	case args[0] == "listen" && len(args) == 2:
		conf.Listen = args[1]
	case args[0] == "allow" && len(args) == 2:
		p, err := ParseNetwork(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "gorex rexapi lan: %v\n", err)
			return 2
		}
		if !slices.Contains(conf.Allow, p.String()) {
			conf.Allow = append(conf.Allow, p.String())
		}
	case args[0] == "deny" && len(args) == 2:
		p, err := ParseNetwork(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "gorex rexapi lan: %v\n", err)
			return 2
		}
		conf.Allow = slices.DeleteFunc(conf.Allow, func(s string) bool {
			q, err := ParseNetwork(s)
			return err == nil && q == p
		})
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if change {
		before := time.Now()
		if err := SaveLAN(dir, conf); err != nil {
			fmt.Fprintf(os.Stderr, "gorex rexapi lan: %v\n", err)
			return 2
		}
		if saved, err := LoadLAN(dir); err == nil {
			conf = saved // with the token SaveLAN gave it
		}
		// The server's gateway reads the file within a second; wait for
		// its word, when there is a server to give it.
		deadline := time.Now()
		if c, err := rex.ConnectExisting(); err == nil {
			c.Close()
			deadline = deadline.Add(4 * time.Second)
		}
		for ; time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if LoadLANStatus(dir).Updated.After(before) {
				break
			}
		}
	}
	printLAN(os.Stdout, conf, LoadLANStatus(dir))
	return 0
}

func printLAN(w io.Writer, c LANConfig, s LANStatus) {
	state := "off"
	switch {
	case s.Listening != "":
		state = "listening on http://" + s.Listening
	case c.Enabled && s.Error != "":
		state = "error"
	case c.Enabled:
		state = "on, waiting for the GoRex server"
	}
	fmt.Fprintf(w, "lan: %s\n", state)
	if c.Listen != "" {
		fmt.Fprintf(w, "  listen: %s\n", c.Listen)
	}
	if len(c.Allow) > 0 {
		fmt.Fprintf(w, "  allow: %s\n", strings.Join(c.Allow, ", "))
	} else {
		fmt.Fprintln(w, "  allow: none")
	}
	if c.Token != "" {
		fmt.Fprintln(w, "  pairing token: set (gorex rexapi lan token show)")
	}
	if len(c.LegacyOpen) > 0 {
		fmt.Fprintf(w, "  legacy, no token: %s\n", strings.Join(c.LegacyOpen, ", "))
	}
	if s.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", s.Error)
	}
	if s.WTListening != "" {
		fmt.Fprintf(w, "  webtransport: https://%s/api/wt\n", s.WTListening)
	}
	if s.WTCertHash != "" {
		fmt.Fprintf(w, "  cert: sha256-%s\n", s.WTCertHash)
	}
	if s.WTError != "" {
		fmt.Fprintf(w, "  webtransport error: %s\n", s.WTError)
	}
}

func lanQRMain(dir string, args []string) int {
	origin := WebOrigin
	for i := 0; i < len(args); i++ {
		if args[i] == "--origin" && i+1 < len(args) {
			origin = strings.TrimRight(args[i+1], "/")
			i++
		} else {
			fmt.Fprintln(os.Stderr, "usage: gorex rexapi lan qr [--origin URL]")
			return 2
		}
	}
	if origin == "" {
		fmt.Fprintln(os.Stderr, "wind-rex rexapi lan qr: set WINDREX_WEB_ORIGIN or pass --origin URL")
		return 2
	}
	s := LoadLANStatus(dir)
	conf, err := LoadLAN(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gorex rexapi lan qr: %v\n", err)
		return 1
	}
	if conf.Token == "" {
		fmt.Fprintln(os.Stderr, "gorex rexapi lan qr: no pairing token yet (run gorex rexapi lan on)")
		return 1
	}
	if s.WTListening == "" || s.WTCertHash == "" {
		fmt.Fprintln(os.Stderr, "gorex rexapi lan qr: WebTransport listener is not running (run gorex rexapi lan on)")
		return 1
	}
	pairURL := conf.WebPairURL(s, origin)
	code, err := qr.Encode(pairURL, qr.M)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gorex rexapi lan qr: %v\n", err)
		return 1
	}
	fmt.Println(pairURL)
	fmt.Print(renderQR(code))
	return 0
}

// renderQR draws a QR code in a terminal, two rows a line with half
// blocks, dark on light, with the quiet zone scanners need.
func renderQR(c *qr.Code) string {
	const quiet = 2
	n := c.Size + 2*quiet
	dark := func(x, y int) bool { return c.Black(x-quiet, y-quiet) }
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		for x := 0; x < n; x++ {
			top, bottom := dark(x, y), y+1 < n && dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteRune(' ')
			case top:
				b.WriteRune('▄')
			case bottom:
				b.WriteRune('▀')
			default:
				b.WriteRune('█')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
