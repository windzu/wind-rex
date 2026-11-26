package rexapi

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"rsc.io/qr"

	"gorex/internal/rex"
)

// directMain implements "gorex rexapi direct on|off|status|qr|relay|rotate".
func directMain(args []string) int {
	usage := "usage: gorex rexapi direct on [--listen :7443] --public https://HOST:PORT [--cert FILE --key FILE] | relay --url https://RELAY:PORT [--pin PIN] [--secret SECRET] | off | status | qr | rotate"
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	dir := StateDir()
	conf, err := LoadDirect(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gorex rexapi direct: %v\n", err)
		return 1
	}

	change := true
	switch args[0] {
	case "status":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		change = false
	case "qr":
		return directQRMain(dir, conf)
	case "off":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		conf.Enabled = false
	case "rotate":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		conf.Token = NewLANToken()
		fmt.Fprintln(os.Stderr, "gorex rexapi direct: rotated pairing token; re-scan the QR code on iPhone")
	case "on":
		fs := flag.NewFlagSet("direct on", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		listen := fs.String("listen", defaultString(conf.Listen, fmt.Sprintf(":%d", DirectPort)), "local TLS bind address")
		pub := fs.String("public", conf.PublicURL, "public https://HOST:PORT reachable from iPhone")
		certFile := fs.String("cert", conf.CertFile, "optional PEM certificate file")
		keyFile := fs.String("key", conf.KeyFile, "optional PEM private key file")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		conf.Enabled = true
		conf.Listen = strings.TrimSpace(*listen)
		conf.PublicURL = strings.TrimSpace(*pub)
		conf.CertFile = strings.TrimSpace(*certFile)
		conf.KeyFile = strings.TrimSpace(*keyFile)
		conf.RelayURL = ""
		conf.RelaySecret = ""
		conf.RelayCertPin = ""
		if conf.PublicURL == "" {
			fmt.Fprintln(os.Stderr, "gorex rexapi direct on: --public https://<your-public-ip-or-domain>:<port> is required")
			return 2
		}
	case "relay":
		fs := flag.NewFlagSet("direct relay", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		relayURL := fs.String("url", conf.RelayURL, "windrex-relay https:// URL")
		pin := fs.String("pin", conf.RelayCertPin, "optional self-signed relay TLS cert SHA-256 pin")
		secret := fs.String("secret", conf.RelaySecret, "optional relay registration secret")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if strings.TrimSpace(*relayURL) == "" {
			fmt.Fprintln(os.Stderr, "gorex rexapi direct relay: --url https://<relay>:<port> is required")
			return 2
		}
		conf.Enabled = true
		conf.Listen = ""
		conf.PublicURL = ""
		conf.RelayURL = strings.TrimSpace(*relayURL)
		conf.RelayCertPin = strings.TrimSpace(*pin)
		conf.RelaySecret = strings.TrimSpace(*secret)
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}

	if change {
		before := time.Now()
		if err := SaveDirect(dir, conf); err != nil {
			fmt.Fprintf(os.Stderr, "gorex rexapi direct: %v\n", err)
			return 2
		}
		if saved, err := LoadDirect(dir); err == nil {
			conf = saved
		}
		deadline := time.Now()
		if c, err := rex.ConnectExisting(); err == nil {
			c.Close()
			deadline = deadline.Add(4 * time.Second)
		}
		for ; time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if LoadDirectStatus(dir).Updated.After(before) {
				break
			}
		}
	}
	st := LoadDirectStatus(dir)
	printDirect(os.Stdout, conf, st)
	if change && conf.Enabled && (args[0] == "on" || args[0] == "relay" || args[0] == "rotate") {
		if pairURL := conf.PairURL(st, hostDisplayName()); pairURL != "" {
			fmt.Fprintln(os.Stdout)
			printDirectQR(os.Stdout, pairURL)
		}
	}
	return 0
}

func directQRMain(dir string, conf DirectConfig) int {
	st := LoadDirectStatus(dir)
	pairURL := conf.PairURL(st, hostDisplayName())
	if pairURL == "" {
		fmt.Fprintln(os.Stderr, "gorex rexapi direct qr: direct/relay access is not active yet (run gorex rexapi direct on or relay)")
		return 1
	}
	printDirectQR(os.Stdout, pairURL)
	return 0
}

func printDirectQR(w io.Writer, pairURL string) {
	fmt.Fprintln(w, pairURL)
	if code, err := qr.Encode(pairURL, qr.M); err == nil {
		fmt.Fprint(w, renderQR(code))
	}
}

func printDirect(w io.Writer, c DirectConfig, s DirectStatus) {
	state := "off"
	switch {
	case c.Enabled && s.Listening != "":
		state = "listening on https://" + s.Listening
	case c.Enabled && s.RelayConnected:
		state = "connected to relay"
	case c.Enabled && (s.Error != "" || s.RelayError != ""):
		state = "error"
	case c.Enabled:
		state = "on, waiting for server"
	}
	fmt.Fprintf(w, "direct: %s\n", state)
	if c.Listen != "" {
		fmt.Fprintf(w, "  listen: %s\n", c.Listen)
	}
	if c.PublicURL != "" {
		fmt.Fprintf(w, "  public: %s\n", c.PublicURL)
	}
	if s.CertPin != "" {
		fmt.Fprintf(w, "  cert pin: %s\n", s.CertPin)
	}
	if c.RelayURL != "" {
		fmt.Fprintf(w, "  relay: %s (host id: %s, connected: %v)\n", c.RelayURL, c.HostID, s.RelayConnected)
	}
	if c.Token != "" {
		fmt.Fprintln(w, "  pairing token: set (gorex rexapi direct qr)")
	}
	if s.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", s.Error)
	}
	if s.RelayError != "" {
		fmt.Fprintf(w, "  relay error: %s\n", s.RelayError)
	}
}

func defaultString(cur, fallback string) string {
	if cur != "" {
		return cur
	}
	return fallback
}

func hostDisplayName() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	h, _, _ = strings.Cut(h, ".")
	return strings.TrimSpace(h)
}
