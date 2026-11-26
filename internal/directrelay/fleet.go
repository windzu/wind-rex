package directrelay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

// FleetTokenDomain is the domain separation prefix for per-host HMAC-SHA256
// fleet token derivation (docs/NET-010-relay-fleet-discovery.md).
const FleetTokenDomain = "windrex-fleet-v1:"

// ErrInvalidFleetPin indicates a Fleet URL contains a malformed TLS certificate pin.
var ErrInvalidFleetPin = errors.New("directrelay: invalid fleet TLS certificate pin")

// DeriveFleetToken deterministically derives a 256-bit per-host access token
// from a shared relay secret and host identifier:
//
//	base64.RawURLEncoding(HMAC-SHA256(key = relaySecret, msg = "windrex-fleet-v1:" + hostID))
//
// It returns "" when relaySecret or hostID is empty.
func DeriveFleetToken(relaySecret, hostID string) string {
	if relaySecret == "" || hostID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(relaySecret))
	_, _ = mac.Write([]byte(FleetTokenDomain))
	_, _ = mac.Write([]byte(hostID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// FleetTarget is a parsed Relay Fleet QR/URL:
// https://<relay-host>[:port][<base-path>]/#r=<relay_secret>[&p=<tls_pin_b64url>][&n=<relay_label>].
type FleetTarget struct {
	// RelayURL is the sanitized https://host[:port][base-path] endpoint without
	// trailing slash, query, or secret fragment.
	RelayURL string
	// Host is host[:port] from the URL.
	Host string
	// Hostname is the host without port.
	Hostname string
	// Secret is the relay shared secret from #r= (secret: never log).
	Secret string
	// Pin is the optional 43-char unpadded base64url SHA-256 leaf TLS pin from #p=.
	Pin string
	// Label is the optional sanitized relay label from #n=.
	Label string
}

// DefaultLabel returns Label when non-empty, else Hostname.
func (t FleetTarget) DefaultLabel() string {
	if t.Label != "" {
		return t.Label
	}
	return t.Hostname
}

// IsFleetURL reports whether raw looks like a Relay Fleet URL (an https:// URL
// whose fragment carries a non-empty r= parameter).
func IsFleetURL(raw string) bool {
	s := strings.TrimSpace(raw)
	if len(s) < len("https://") || !strings.EqualFold(s[:len("https://")], "https://") {
		return false
	}
	idx := strings.IndexByte(s, '#')
	if idx < 0 {
		return false
	}
	frag, err := url.ParseQuery(s[idx+1:])
	if err != nil {
		return false
	}
	return strings.TrimSpace(frag.Get("r")) != ""
}

// ParseFleetURL validates and parses a Relay Fleet QR/URL. It accepts both
// 43-char unpadded base64url and 64-char hex SHA-256 pins in #p= and normalizes
// them to 43-char unpadded base64url.
func ParseFleetURL(raw string) (FleetTarget, error) {
	s := strings.TrimSpace(raw)
	if len(s) < len("https://") || !strings.EqualFold(s[:len("https://")], "https://") {
		return FleetTarget{}, errors.New("directrelay: fleet URL must start with https://")
	}
	u, err := url.Parse(s)
	if err != nil {
		return FleetTarget{}, errors.New("directrelay: invalid fleet URL")
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.Hostname() == "" {
		return FleetTarget{}, errors.New("directrelay: fleet URL must be an https://host[:port] URL")
	}
	if u.User != nil {
		return FleetTarget{}, errors.New("directrelay: fleet URL must not contain user info")
	}
	frag, err := url.ParseQuery(u.Fragment)
	if err != nil {
		return FleetTarget{}, errors.New("directrelay: fleet URL has invalid fragment")
	}
	if strings.TrimSpace(frag.Get("k")) != "" {
		return FleetTarget{}, errors.New("directrelay: fleet URL must not include a single-host k= token")
	}
	secret := strings.TrimSpace(frag.Get("r"))
	if secret == "" {
		return FleetTarget{}, errors.New("directrelay: fleet URL is missing relay secret (#r=)")
	}

	pin, err := normalizePin(strings.TrimSpace(frag.Get("p")))
	if err != nil {
		return FleetTarget{}, err
	}

	basePath := strings.TrimRight(u.Path, "/")
	label := SanitizeHostName(frag.Get("n"))

	return FleetTarget{
		RelayURL: "https://" + u.Host + basePath,
		Host:     u.Host,
		Hostname: u.Hostname(),
		Secret:   secret,
		Pin:      pin,
		Label:    label,
	}, nil
}

// FormatFleetURL constructs the canonical Relay Fleet URL:
// https://<relay-host>[:port][<base-path>]/#r=<secret>[&p=<pin>][&n=<label>].
func FormatFleetURL(relayBase, secret, pin, label string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(relayBase))
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("directrelay: invalid relay URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "https"
	default:
		return "", errors.New("directrelay: fleet URL requires an https:// or wss:// relay URL")
	}
	if u.User != nil {
		return "", errors.New("directrelay: relay URL must not contain user info")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", errors.New("directrelay: fleet URL requires a relay secret")
	}
	normPin, err := normalizePin(strings.TrimSpace(pin))
	if err != nil {
		return "", err
	}

	basePath := strings.TrimRight(u.Path, "/")
	var frag strings.Builder
	frag.WriteString("r=")
	frag.WriteString(url.QueryEscape(secret))
	if normPin != "" {
		frag.WriteString("&p=")
		frag.WriteString(url.QueryEscape(normPin))
	}
	if cleanLabel := SanitizeHostName(label); cleanLabel != "" {
		frag.WriteString("&n=")
		frag.WriteString(url.QueryEscape(cleanLabel))
	}
	return "https://" + u.Host + basePath + "/#" + frag.String(), nil
}

// SanitizeFleetURL strips the secret fragment and query parameters from a Fleet
// URL so it is safe to display or log.
func SanitizeFleetURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if t, err := ParseFleetURL(s); err == nil {
		return t.RelayURL
	}
	if idx := strings.IndexByte(s, '#'); idx >= 0 {
		s = s[:idx]
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<redacted>"
	}
	u.Fragment = ""
	u.RawQuery = ""
	u.User = nil
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

// FleetHostPairURL constructs a single-host relay pairing URL for hostID using
// the per-host derived fleet token from relaySecret.
func FleetHostPairURL(relayBase, hostID, relaySecret, pin, hostName string) (string, error) {
	base, err := RelayClientBaseURL(relayBase, hostID)
	if err != nil {
		return "", err
	}
	token := DeriveFleetToken(relaySecret, hostID)
	if token == "" {
		return "", errors.New("directrelay: relay secret and host id are required")
	}
	normPin, err := normalizePin(strings.TrimSpace(pin))
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("k", token)
	if normPin != "" {
		q.Set("p", normPin)
	}
	if cleanName := SanitizeHostName(hostName); cleanName != "" {
		q.Set("n", cleanName)
	}
	return base + "/#" + q.Encode(), nil
}

func normalizePin(pin string) (string, error) {
	if pin == "" {
		return "", nil
	}
	switch len(pin) {
	case 43:
		b, err := base64.RawURLEncoding.DecodeString(pin)
		if err != nil || len(b) != sha256.Size {
			return "", ErrInvalidFleetPin
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	case 64:
		b, err := hex.DecodeString(pin)
		if err != nil || len(b) != sha256.Size {
			return "", ErrInvalidFleetPin
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	default:
		return "", ErrInvalidFleetPin
	}
}
