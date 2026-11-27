package directrelay

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestIsPrivateLANIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.13.20", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"192.168.1.50", true},
		{"fd12:3456:789a::1", true},
		{"fc00::1", true},
		{"::ffff:10.0.13.20", true},

		// Rejected ranges:
		{"127.0.0.1", false},
		{"::1", false},
		{"169.254.1.2", false},
		{"fe80::1", false},
		{"100.64.0.1", false},
		{"100.111.76.128", false},
		{"198.18.0.1", false},
		{"198.19.255.254", false},
		{"172.15.0.1", false},
		{"172.32.0.1", false},
		{"203.0.113.10", false},
		{"2001:db8::1", false},
		{"224.0.0.1", false},
		{"0.0.0.0", false},
	}
	for _, tc := range cases {
		addr := netip.MustParseAddr(tc.ip)
		if got := IsPrivateLANIP(addr); got != tc.want {
			t.Errorf("IsPrivateLANIP(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
	if IsPrivateLANIP(netip.Addr{}) {
		t.Error("IsPrivateLANIP(invalid) = true, want false")
	}
}

func TestIsVirtualInterfaceName(t *testing.T) {
	virtual := []string{"lo", "lo0", "docker0", "br-a1b2c3", "veth1234", "utun2", "utun1024", "tun0", "tailscale0", "wg0", "Meta", "mihomo0", "awdl0", "llw0", "bridge0", "anpi0", ""}
	for _, name := range virtual {
		if !IsVirtualInterfaceName(name) {
			t.Errorf("IsVirtualInterfaceName(%q) = false, want true", name)
		}
	}
	physical := []string{"en0", "en1", "eth0", "enp3s0", "wlan0", "wlp2s0", "Ethernet"}
	for _, name := range physical {
		if IsVirtualInterfaceName(name) {
			t.Errorf("IsVirtualInterfaceName(%q) = true, want false", name)
		}
	}
}

func TestParsePeerIP(t *testing.T) {
	cases := []struct {
		raw    string
		wantOK bool
		wantIP string
	}{
		{"10.0.13.41:54321", true, "10.0.13.41"},
		{"[::ffff:10.0.13.41]:54321", true, "10.0.13.41"},
		{"[fd00::10]:443", true, "fd00::10"},
		{"127.0.0.1:9999", true, "127.0.0.1"},
		{"192.168.1.5", true, "192.168.1.5"},
		{"", false, ""},
		{"not-an-ip:7443", false, ""},
	}
	for _, tc := range cases {
		ip, ok := ParsePeerIP(tc.raw)
		if ok != tc.wantOK {
			t.Errorf("ParsePeerIP(%q) ok = %v, want %v", tc.raw, ok, tc.wantOK)
			continue
		}
		if ok && ip.String() != tc.wantIP {
			t.Errorf("ParsePeerIP(%q) = %s, want %s", tc.raw, ip, tc.wantIP)
		}
	}
}

func TestSanitizeLANAddrs(t *testing.T) {
	in := []string{
		"203.0.113.10:7443",     // public -> drop
		"127.0.0.1:7443",        // loopback -> drop
		"198.18.0.1:7443",       // proxy TUN -> drop
		"100.111.76.128:7443",   // CGNAT -> drop
		"evil.example.com:7443", // hostname -> drop
		"10.0.13.20:0",          // port 0 -> drop
		"10.0.13.20:7443",       // keep #1
		"10.0.13.20:7443",       // duplicate -> drop
		"[::ffff:10.0.8.20]:7443",
		"192.168.1.10:7443",
		"[fd12::1]:7443",
		"172.16.0.5:7443", // 5th valid -> capped at MaxLANAddrs (4)
	}
	got := SanitizeLANAddrs(in)
	want := []string{
		"10.0.13.20:7443",
		"10.0.8.20:7443",
		"192.168.1.10:7443",
		"[fd12::1]:7443",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SanitizeLANAddrs = %v, want %v", got, want)
	}
}

func TestSelectLANAddrsOrderingAndFiltering(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast
	ifaces := []ifaceCandidate{
		{name: "lo0", flags: net.FlagUp | net.FlagLoopback, addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		{name: "docker0", flags: up, addrs: []netip.Addr{netip.MustParseAddr("172.17.0.1")}},
		{name: "Meta", flags: net.FlagUp | net.FlagPointToPoint, addrs: []netip.Addr{netip.MustParseAddr("198.18.0.1")}},
		{name: "utun2", flags: net.FlagUp | net.FlagPointToPoint, addrs: []netip.Addr{netip.MustParseAddr("100.76.248.111")}},
		{name: "eth1", flags: net.FlagBroadcast, addrs: []netip.Addr{netip.MustParseAddr("10.0.99.1")}}, // down
		{name: "en0", flags: up, addrs: []netip.Addr{
			netip.MustParseAddr("192.168.2.5"),
			netip.MustParseAddr("203.0.113.9"),
			netip.MustParseAddr("fd00::2"),
		}},
		{name: "enp3s0", flags: up, addrs: []netip.Addr{
			netip.MustParseAddr("10.0.13.20"),
			netip.MustParseAddr("10.0.8.20"),
			netip.MustParseAddr("fe80::1"),
		}},
	}
	got := selectLANAddrs(ifaces, netip.MustParseAddr("10.0.13.20"), 7443)
	want := []string{
		"10.0.13.20:7443",  // primary default route first
		"10.0.8.20:7443",   // remaining IPv4 sorted
		"192.168.2.5:7443", // remaining IPv4 sorted
		"[fd00::2]:7443",   // ULA IPv6 after IPv4
	}
	if !slices.Equal(got, want) {
		t.Fatalf("selectLANAddrs = %v, want %v", got, want)
	}
}

func TestRelayLANHintsRegistrationAndDiscovery(t *testing.T) {
	const secret = "relay-lan-secret"
	relay, srv, pin, client := newTestTLSRelay(t, secret)
	directPin := CertPin([]byte("host-direct-tls-cert"))

	// 1. Valid LAN hints + direct_pin registered via TunnelListener appear in GET /api/hosts.
	ln, err := StartTunnel(ClientConfig{
		RelayURL:  srv.URL,
		HostID:    "host_lan_valid01",
		HostName:  "dev-box-1",
		Secret:    secret,
		CertPin:   pin,
		FleetAuth: true,
		LANAddrs:  []string{"10.0.13.20:7443", "203.0.113.5:7443", "198.18.0.1:7443", "10.0.8.20:7443"},
		DirectPin: directPin,
	})
	if err != nil {
		t.Fatalf("StartTunnel: %v", err)
	}
	defer ln.Close()

	// 2. Raw registration with malformed direct_pin drops lan_addrs completely.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	badWSURL := "wss" + strings.TrimPrefix(srv.URL, "https") + "/relay/host?id=host_lan_badpin2&name=bad-pin&lan_addr=10.0.13.21:7443&direct_pin=not-a-pin"
	badWS, resp, err := websocket.Dial(ctx, badWSURL, &websocket.DialOptions{
		Subprotocols:    []string{RelaySubprotocol},
		HTTPHeader:      http.Header{"Authorization": []string{"Bearer " + secret}},
		HTTPClient:      client,
		CompressionMode: websocket.CompressionDisabled,
	})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial badpin host: %v", err)
	}
	defer badWS.CloseNow()

	// 3. Raw registration with only public/loopback IPs drops both lan_addrs and direct_pin.
	pubWSURL := "wss" + strings.TrimPrefix(srv.URL, "https") + "/relay/host?id=host_lan_public3&name=pub-only&lan_addr=203.0.113.9:7443&lan_addr=127.0.0.1:7443&direct_pin=" + directPin
	pubWS, resp2, err := websocket.Dial(ctx, pubWSURL, &websocket.DialOptions{
		Subprotocols:    []string{RelaySubprotocol},
		HTTPHeader:      http.Header{"Authorization": []string{"Bearer " + secret}},
		HTTPClient:      client,
		CompressionMode: websocket.CompressionDisabled,
	})
	if resp2 != nil && resp2.Body != nil {
		resp2.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial public-only host: %v", err)
	}
	defer pubWS.CloseNow()

	waitCondition(t, "three hosts online", func() bool { return len(relay.Hosts()) == 3 })

	hosts, err := DiscoverHosts(ctx, srv.URL, secret, pin)
	if err != nil {
		t.Fatalf("DiscoverHosts: %v", err)
	}
	byID := map[string]HostInfo{}
	for _, h := range hosts {
		byID[h.ID] = h
	}

	gotValid := byID["host_lan_valid01"]
	if !slices.Equal(gotValid.LANAddrs, []string{"10.0.13.20:7443", "10.0.8.20:7443"}) || gotValid.DirectPin != directPin {
		t.Fatalf("valid host LAN hints = addrs:%v pin:%q, want [10.0.13.20:7443 10.0.8.20:7443] pin:%q", gotValid.LANAddrs, gotValid.DirectPin, directPin)
	}
	gotBadPin := byID["host_lan_badpin2"]
	if len(gotBadPin.LANAddrs) != 0 || gotBadPin.DirectPin != "" {
		t.Fatalf("badpin host should drop hints, got %+v", gotBadPin)
	}
	gotPub := byID["host_lan_public3"]
	if len(gotPub.LANAddrs) != 0 || gotPub.DirectPin != "" {
		t.Fatalf("public-only host should drop hints, got %+v", gotPub)
	}
}
