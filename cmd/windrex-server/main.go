// Command windrex-server is the standalone headless WindRex session server,
// Rex API gateway, and administration CLI. It shares the session server
// (internal/rex), gateway (internal/rexapi), and CLI (internal/cli) with the
// desktop app while omitting desktop UI code, fonts, shaders, and icons.
//
//	windrex-server -server                 run the session server in the foreground
//	windrex-server server start            start the detached session server
//	windrex-server server status [--json]  inspect running server and transports
//	windrex-server server upgrade          upgrade the running server in place
//	windrex-server rexapi direct on ...    configure direct TLS or relay access
//	windrex-server remote status           manage remote pairing and access
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"gorex/internal/cli"
	"gorex/internal/rex"
	"gorex/internal/rexapi"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configureServerHooks()
	if server, err := rex.ServerMain(os.Args[1:]); server {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if exec, err := rex.SessionExecMain(os.Args[1:]); exec {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func configureServerHooks() {
	rex.OnServe = rexapi.Supervise
	rex.RexPairURL = rexapi.PairURL
	rex.TailnetNode = rexapi.DesktopNode
	rex.EnableBuiltinTailscale = rexapi.EnableBuiltinTailscale
	if v := strings.TrimSpace(version); v != "" {
		rexapi.RexVersion = v + "-windrex"
	}
}

func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		printUsage(errOut)
		return cli.ExitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		printUsage(out)
		return cli.ExitOK
	case "rexapi":
		return rexapi.Main(args[1:])
	case "server":
		return serverCmd(args[1:], out, errOut)
	case "update":
		fmt.Fprintln(errOut, "windrex-server update: replace the binary with install-server.sh (or a release archive) and run \"windrex-server server upgrade\"")
		return cli.ExitUsage
	}
	code, handled := cli.Main(args, out, errOut, version)
	if handled {
		return code
	}
	fmt.Fprintf(errOut, "windrex-server: unknown argument %q\n", args[0])
	printUsage(errOut)
	return cli.ExitUsage
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `windrex-server — headless WindRex session server and CLI

Usage:
  windrex-server -server                              run the session server in the foreground
  windrex-server server start [--foreground]          start the session server
  windrex-server server status [--json]               show server, session, and transport status
  windrex-server server upgrade [--force]             upgrade a running server in place (keeps shells)
  windrex-server server restart                       restart the server (starts new shells)
  windrex-server rexapi direct on|relay|qr|status|rotate|off
  windrex-server rexapi lan|pair|tailscale|web|call
  windrex-server remote status|enable|disable|address|pair|pending|approve|reject|allow|revoke
  windrex-server ls|clients|layout|new|split|close|rename|send|paste|capture|events|wait|run
  windrex-server version [--json]

Environment:
  GOREX_DIR            server data directory (socket, layout, remote and rexapi config)
  GOREX_SESSION_USER   run sessions as this OS user when the server starts as root (Linux)
  MYGO_GHOSTTY_VT      explicit path to libghostty-vt.so (defaults to beside the binary)

Codes: 0 ok, 1 failed, 2 usage, 3 no such session or target, 4 server unreachable.
`)
}

func serverCmd(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "windrex-server server: start [--foreground] | status [--json] | upgrade [--force] | restart")
		return cli.ExitUsage
	}
	switch args[0] {
	case "start":
		return serverStartCmd(args[1:], out, errOut)
	case "status":
		return serverStatusCmd(args[1:], out, errOut)
	case "upgrade":
		return serverUpgradeCmd(args[1:], out, errOut)
	case "restart":
		code, _ := cli.Main(append([]string{"server"}, args...), out, errOut, version)
		return code
	default:
		fmt.Fprintln(errOut, "windrex-server server: start [--foreground] | status [--json] | upgrade [--force] | restart")
		return cli.ExitUsage
	}
}

func serverStartCmd(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("server start", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fg := fs.Bool("foreground", false, "run the session server in the foreground")
	shortFG := fs.Bool("f", false, "run the session server in the foreground")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cli.ExitOK
		}
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(errOut, "windrex-server server start: unexpected arguments")
		return cli.ExitUsage
	}
	if *fg || *shortFG {
		if err := rex.Serve(); err != nil {
			fmt.Fprintf(errOut, "windrex-server: %v\n", err)
			return cli.ExitFailed
		}
		return cli.ExitOK
	}
	if c, err := rex.ConnectExisting(); err == nil {
		h, herr := c.Hello()
		c.Close()
		if herr == nil {
			fmt.Fprintf(out, "already running (pid %d, socket %s)\n", h.PID, rex.SocketPath())
			return cli.ExitOK
		}
	}
	c, err := rex.Connect()
	if err != nil {
		fmt.Fprintf(errOut, "windrex-server server start: %v\n", err)
		return cli.ExitServer
	}
	defer c.Close()
	h, err := c.Hello()
	if err != nil {
		fmt.Fprintf(errOut, "windrex-server server start: %v\n", err)
		return cli.ExitServer
	}
	fmt.Fprintf(out, "started (pid %d, socket %s)\n", h.PID, rex.SocketPath())
	return cli.ExitOK
}

type serverStatusReport struct {
	Running      bool                 `json:"running"`
	Version      string               `json:"version"`
	Dir          string               `json:"dir"`
	Socket       string               `json:"socket"`
	PID          int                  `json:"pid,omitempty"`
	Exe          string               `json:"exe,omitempty"`
	ExeTime      time.Time            `json:"exeTime,omitzero"`
	Started      time.Time            `json:"started,omitzero"`
	Protocol     int                  `json:"protocol,omitempty"`
	Upgrade      bool                 `json:"upgrade,omitempty"`
	Capabilities []string             `json:"capabilities,omitempty"`
	Sessions     int                  `json:"sessions"`
	Remote       *remoteStatusSummary `json:"remote,omitempty"`
	Direct       directStatusSummary  `json:"direct"`
	LAN          lanStatusSummary     `json:"lan"`
}

type remoteStatusSummary struct {
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Peers     int    `json:"peers"`
	LastError string `json:"lastError,omitempty"`
}

type directStatusSummary struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode"`
	Listen         string `json:"listen,omitempty"`
	PublicURL      string `json:"publicURL,omitempty"`
	Listening      string `json:"listening,omitempty"`
	RelayURL       string `json:"relayURL,omitempty"`
	RelayConnected bool   `json:"relayConnected,omitempty"`
	CertPin        string `json:"certPin,omitempty"`
	Error          string `json:"error,omitempty"`
	RelayError     string `json:"relayError,omitempty"`
}

type lanStatusSummary struct {
	Enabled   bool   `json:"enabled"`
	Listen    string `json:"listen,omitempty"`
	Listening string `json:"listening,omitempty"`
	Error     string `json:"error,omitempty"`
}

func serverStatusCmd(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("server status", flag.ContinueOnError)
	fs.SetOutput(errOut)
	asJSON := fs.Bool("json", false, "output status as JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cli.ExitOK
		}
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(errOut, "windrex-server server status: unexpected arguments")
		return cli.ExitUsage
	}

	report := collectServerStatus()
	if *asJSON {
		if err := cli.WriteJSON(out, report); err != nil {
			fmt.Fprintf(errOut, "windrex-server server status: %v\n", err)
			return cli.ExitFailed
		}
	} else {
		printServerStatus(out, report)
	}
	if !report.Running {
		return cli.ExitServer
	}
	return cli.ExitOK
}

func collectServerStatus() serverStatusReport {
	dir := rex.Dir()
	report := serverStatusReport{
		Version: version,
		Dir:     dir,
		Socket:  rex.SocketPath(),
		Direct:  readDirectSummary(rexapi.TailnetDir()),
		LAN:     readLANSummary(rexapi.TailnetDir()),
	}
	c, err := rex.ConnectExisting()
	if err != nil {
		return report
	}
	defer c.Close()
	h, err := c.Hello()
	if err != nil {
		return report
	}
	report.Running = true
	report.PID = h.PID
	report.Exe = h.Exe
	report.ExeTime = h.ExeTime
	report.Started = h.Started
	report.Protocol = h.Version
	report.Upgrade = h.Upgrade
	report.Capabilities = h.Capabilities
	if sessions, err := c.List(); err == nil {
		report.Sessions = len(sessions)
	}
	if rs, err := c.RemoteStatus(); err == nil {
		report.Remote = &remoteStatusSummary{
			Enabled:   rs.Enabled,
			Running:   rs.Running,
			Peers:     len(rs.Allowed),
			LastError: rs.LastError,
		}
	}
	return report
}

func readDirectSummary(dir string) directStatusSummary {
	conf, err := rexapi.LoadDirect(dir)
	if err != nil {
		return directStatusSummary{Mode: "error", Error: err.Error()}
	}
	st := rexapi.LoadDirectStatus(dir)
	mode := "off"
	if conf.Enabled {
		switch {
		case conf.RelayURL != "":
			mode = "relay"
		default:
			mode = "direct"
		}
	}
	return directStatusSummary{
		Enabled:        conf.Enabled,
		Mode:           mode,
		Listen:         conf.Listen,
		PublicURL:      conf.PublicURL,
		Listening:      st.Listening,
		RelayURL:       conf.RelayURL,
		RelayConnected: st.RelayConnected,
		CertPin:        st.CertPin,
		Error:          st.Error,
		RelayError:     st.RelayError,
	}
}

func readLANSummary(dir string) lanStatusSummary {
	conf, err := rexapi.LoadLAN(dir)
	if err != nil {
		return lanStatusSummary{Error: err.Error()}
	}
	st := rexapi.LoadLANStatus(dir)
	return lanStatusSummary{
		Enabled:   conf.Enabled,
		Listen:    conf.Listen,
		Listening: st.Listening,
		Error:     st.Error,
	}
}

func printServerStatus(w io.Writer, r serverStatusReport) {
	if r.Running {
		fmt.Fprintf(w, "server: running (pid %d, protocol %d, sessions %d)\n", r.PID, r.Protocol, r.Sessions)
		if r.Exe != "" {
			fmt.Fprintf(w, "  exe: %s\n", r.Exe)
		}
		if !r.Started.IsZero() {
			fmt.Fprintf(w, "  started: %s\n", r.Started.Format(time.RFC3339))
		}
	} else {
		fmt.Fprintln(w, "server: stopped")
	}
	fmt.Fprintf(w, "  dir: %s\n", r.Dir)
	fmt.Fprintf(w, "  socket: %s\n", r.Socket)
	if r.Remote != nil {
		state := "off"
		if r.Remote.Enabled {
			if r.Remote.Running {
				state = fmt.Sprintf("on (%d paired)", r.Remote.Peers)
			} else {
				state = "enabled (not running)"
			}
		}
		fmt.Fprintf(w, "  remote: %s\n", state)
		if r.Remote.LastError != "" {
			fmt.Fprintf(w, "  remote error: %s\n", r.Remote.LastError)
		}
	}
	fmt.Fprintf(w, "  direct: %s", r.Direct.Mode)
	switch {
	case r.Direct.Listening != "":
		fmt.Fprintf(w, " (https://%s)", r.Direct.Listening)
	case r.Direct.RelayURL != "":
		fmt.Fprintf(w, " (%s, connected: %v)", r.Direct.RelayURL, r.Direct.RelayConnected)
	}
	fmt.Fprintln(w)
	if r.Direct.Error != "" {
		fmt.Fprintf(w, "  direct error: %s\n", r.Direct.Error)
	}
	if r.Direct.RelayError != "" {
		fmt.Fprintf(w, "  relay error: %s\n", r.Direct.RelayError)
	}
	if r.LAN.Enabled {
		if r.LAN.Listening != "" {
			fmt.Fprintf(w, "  lan: on (http://%s)\n", r.LAN.Listening)
		} else {
			fmt.Fprintln(w, "  lan: on")
		}
		if r.LAN.Error != "" {
			fmt.Fprintf(w, "  lan error: %s\n", r.LAN.Error)
		}
	}
}

func serverUpgradeCmd(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("server upgrade", flag.ContinueOnError)
	fs.SetOutput(errOut)
	force := fs.Bool("force", false, "upgrade even when the executable modification time is not newer")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cli.ExitOK
		}
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(errOut, "windrex-server server upgrade: unexpected arguments")
		return cli.ExitUsage
	}
	c, err := rex.ConnectExisting()
	if err != nil {
		fmt.Fprintln(errOut, "windrex-server server upgrade: no server runs")
		return cli.ExitServer
	}
	defer c.Close()
	h, err := c.Hello()
	if err != nil {
		fmt.Fprintln(errOut, "windrex-server server upgrade: no server runs")
		return cli.ExitServer
	}
	exe, at := rex.ServerExecutable()
	if err := upgradeRunningServer(c, h, exe, at, *force, out); err != nil {
		fmt.Fprintf(errOut, "windrex-server server upgrade: %v\n", err)
		return cli.ExitFailed
	}
	return cli.ExitOK
}

// upgradeRunningServer replaces the running server executable in place while
// keeping its PTYs and child shells. Switching from a different executable path
// (such as migrating from the desktop binary to windrex-server) is permitted
// without --force because the running server still probes -handoff-version
// before touching any session.
func upgradeRunningServer(c *rex.Client, hello rex.Hello, exe string, exeTime time.Time, force bool, out io.Writer) error {
	if !hello.Upgrade {
		return errors.New("the running server cannot upgrade in place")
	}
	if exe == "" {
		return errors.New("cannot resolve current executable path")
	}
	if exeTime.IsZero() {
		info, err := os.Stat(exe)
		if err != nil {
			return err
		}
		exeTime = info.ModTime()
	}
	sameExe := hello.Exe == exe
	if sameExe && hello.ExeTime.Equal(exeTime) && !force {
		fmt.Fprintln(out, "the server already runs this build")
		return nil
	}
	if !force && sameExe && !exeTime.After(hello.ExeTime) {
		return errors.New("the running server is as new as or newer than this build; pass --force to replace it anyway")
	}
	next, err := c.Upgrade(exe)
	if err != nil {
		return err
	}
	next.Close()
	fmt.Fprintln(out, "upgraded in place; terminal sessions kept")
	return nil
}
