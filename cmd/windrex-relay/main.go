package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gorex/internal/directrelay"
)

func main() {
	defaultDir := "."
	if home, err := os.UserHomeDir(); err == nil {
		defaultDir = filepath.Join(home, ".config", "windrex-relay")
	}
	listen := flag.String("listen", ":7443", "address to listen on, e.g. :7443 or :443")
	dataDir := flag.String("data-dir", defaultDir, "directory for auto-generated TLS certificate")
	certFile := flag.String("cert", "", "optional PEM certificate file (auto-generates self-signed cert when empty)")
	keyFile := flag.String("key", "", "optional PEM private key file")
	hosts := flag.String("hosts", "", "comma-separated public IP or DNS names to embed in self-signed cert SANs")
	secret := flag.String("secret", os.Getenv("WINDREX_RELAY_SECRET"), "optional shared secret required for host registration")
	plainHTTP := flag.Bool("http", false, "serve plain HTTP (only when running behind a TLS-terminating reverse proxy)")
	flag.Parse()

	srv := directrelay.NewServer(*secret, log.Printf)
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("windrex-relay: listen %s: %v", *listen, err)
	}

	var pin string
	scheme := "https"
	if *plainHTTP {
		scheme = "http"
	} else {
		var cert tls.Certificate
		if *certFile != "" || *keyFile != "" {
			cert, pin, err = directrelay.LoadCertPair(*certFile, *keyFile)
			if err != nil {
				log.Fatalf("windrex-relay: load cert: %v", err)
			}
		} else {
			var san []string
			for _, h := range strings.Split(*hosts, ",") {
				if h = strings.TrimSpace(h); h != "" {
					san = append(san, h)
				}
			}
			cert, pin, err = directrelay.LoadOrCreateCert(*dataDir, "relay", san)
			if err != nil {
				log.Fatalf("windrex-relay: create cert: %v", err)
			}
		}
		ln = tls.NewListener(ln, &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		})
	}

	fmt.Fprintf(os.Stdout, "windrex-relay listening on %s://%s\n", scheme, ln.Addr())
	if pin != "" && *certFile == "" {
		fmt.Fprintf(os.Stdout, "self-signed TLS cert pin: %s\n", pin)
		fmt.Fprintln(os.Stdout, "connect a host with:")
		fmt.Fprintf(os.Stdout, "  gorex rexapi direct relay --url https://<your-public-host-or-ip>%s --pin %s", portSuffix(*listen), pin)
		if *secret != "" {
			fmt.Fprint(os.Stdout, " --secret <secret>")
		}
		fmt.Fprintln(os.Stdout)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		srv.Close()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sctx)
	}()

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("windrex-relay: serve: %v", err)
	}
}

func portSuffix(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" || port == "443" {
		return ""
	}
	return ":" + port
}
