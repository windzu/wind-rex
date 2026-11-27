package directrelay

import (
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// MaxLANAddrs bounds how many local private-LAN Direct TLS addresses a host
// advertises to a relay or returns in /api/v1/lan-hint (docs/NET-011-lan-direct-upgrade.md).
const MaxLANAddrs = 4

var (
	prefixRFC1918_10  = netip.MustParsePrefix("10.0.0.0/8")
	prefixRFC1918_172 = netip.MustParsePrefix("172.16.0.0/12")
	prefixRFC1918_192 = netip.MustParsePrefix("192.168.0.0/16")
	prefixULA         = netip.MustParsePrefix("fc00::/7")
)

var virtualIfacePrefixes = []string{
	"lo",
	"docker",
	"br-",
	"veth",
	"cni",
	"flannel",
	"virbr",
	"utun",
	"tun",
	"tap",
	"tailscale",
	"wg",
	"meta",
	"mihomo",
	"podman",
	"awdl",
	"llw",
	"anpi",
	"bridge",
	"gif",
	"stf",
	"ap",
	"vmnet",
	"vboxnet",
}

// IsPrivateLANIP reports whether ip is a routable private LAN address:
// RFC1918 IPv4 (10/8, 172.16/12, 192.168/16) or IPv6 ULA (fc00::/7).
// Loopback, link-local, CGNAT (100.64/10), proxy/benchmark (198.18/15),
// multicast, unspecified, and public addresses return false.
func IsPrivateLANIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	if ip.Is4() {
		return prefixRFC1918_10.Contains(ip) ||
			prefixRFC1918_172.Contains(ip) ||
			prefixRFC1918_192.Contains(ip)
	}
	return prefixULA.Contains(ip)
}

// IsVirtualInterfaceName reports whether ifaceName matches a known loopback,
// container bridge, VPN, or proxy TUN interface prefix.
func IsVirtualInterfaceName(ifaceName string) bool {
	low := strings.ToLower(strings.TrimSpace(ifaceName))
	if low == "" {
		return true
	}
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// ParsePeerIP parses the IP address from an http.Request.RemoteAddr ("ip:port"
// or "[ipv6]:port") or bare IP literal, unmapping IPv4-in-IPv6 addresses.
func ParsePeerIP(remoteAddr string) (netip.Addr, bool) {
	s := strings.TrimSpace(remoteAddr)
	if s == "" {
		return netip.Addr{}, false
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.Trim(s, "[]")
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip.Unmap(), true
	}
	return netip.Addr{}, false
}

// SanitizeLANAddrs validates, normalizes, deduplicates, and caps raw host:port
// LAN Direct TLS candidates to MaxLANAddrs. Only literal private RFC1918/ULA
// IPs with ports in 1..65535 are retained; hostnames, public IPs, loopback,
// CGNAT, and 198.18/15 addresses are discarded.
func SanitizeLANAddrs(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, min(len(raw), MaxLANAddrs))
	out := make([]string, 0, min(len(raw), MaxLANAddrs))
	for _, item := range raw {
		ap, err := netip.ParseAddrPort(strings.TrimSpace(item))
		if err != nil || ap.Port() == 0 {
			continue
		}
		ip := ap.Addr().Unmap()
		if !IsPrivateLANIP(ip) {
			continue
		}
		norm := netip.AddrPortFrom(ip, ap.Port()).String()
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
		if len(out) >= MaxLANAddrs {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ifaceCandidate is a local interface address snapshot used for testing
// CollectLANAddrs ordering without depending on host OS interfaces.
type ifaceCandidate struct {
	name  string
	flags net.Flags
	addrs []netip.Addr
}

// CollectLANAddrs enumerates up physical LAN/Wi-Fi interfaces on this machine
// and returns up to MaxLANAddrs formatted "<private-ip>:<port>" candidates,
// ordered with the default-route IPv4 source first, then IPv4, then ULA IPv6.
func CollectLANAddrs(port int) []string {
	if port <= 0 || port > 65535 {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	cands := make([]ifaceCandidate, 0, len(ifaces))
	for _, iface := range ifaces {
		rawAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		var ips []netip.Addr
		for _, a := range rawAddrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			if addr, ok := netip.AddrFromSlice(ip); ok {
				ips = append(ips, addr.Unmap())
			}
		}
		cands = append(cands, ifaceCandidate{
			name:  iface.Name,
			flags: iface.Flags,
			addrs: ips,
		})
	}
	return selectLANAddrs(cands, primaryOutboundIPv4(), port)
}

func primaryOutboundIPv4() netip.Addr {
	conn, err := net.Dial("udp4", "1.1.1.1:80")
	if err != nil {
		return netip.Addr{}
	}
	defer conn.Close()
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok && ua != nil {
		if addr, ok := netip.AddrFromSlice(ua.IP); ok {
			return addr.Unmap()
		}
	}
	return netip.Addr{}
}

func selectLANAddrs(ifaces []ifaceCandidate, primary netip.Addr, port int) []string {
	if port <= 0 || port > 65535 {
		return nil
	}
	var v4 []netip.Addr
	var v6 []netip.Addr
	seen := map[netip.Addr]struct{}{}
	for _, iface := range ifaces {
		if iface.flags&net.FlagUp == 0 {
			continue
		}
		if iface.flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		if IsVirtualInterfaceName(iface.name) {
			continue
		}
		for _, raw := range iface.addrs {
			ip := raw.Unmap()
			if !IsPrivateLANIP(ip) {
				continue
			}
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			if ip.Is4() {
				v4 = append(v4, ip)
			} else {
				v6 = append(v6, ip)
			}
		}
	}
	sort.Slice(v4, func(i, j int) bool {
		if primary.IsValid() {
			pi := v4[i] == primary
			pj := v4[j] == primary
			if pi != pj {
				return pi
			}
		}
		return v4[i].Less(v4[j])
	})
	sort.Slice(v6, func(i, j int) bool {
		return v6[i].Less(v6[j])
	})

	portStr := strconv.Itoa(port)
	out := make([]string, 0, min(len(v4)+len(v6), MaxLANAddrs))
	for _, ip := range v4 {
		out = append(out, net.JoinHostPort(ip.String(), portStr))
		if len(out) >= MaxLANAddrs {
			return out
		}
	}
	for _, ip := range v6 {
		out = append(out, net.JoinHostPort(ip.String(), portStr))
		if len(out) >= MaxLANAddrs {
			return out
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
