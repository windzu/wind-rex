---
name: NET-011
description: "WindRex relay hosts advertise local RFC1918 Direct TLS endpoints so desktop and iOS clients connect directly on the same LAN and fall back to the stateless relay away from the LAN. Use when you change relay host registration, GET /api/hosts metadata, GET /api/v1/lan-hint, LAN direct dialing, Happy Eyeballs fallback, or RelayTrust admission rules."
metadata:
  created: "2026-11-27"
  updated: "2026-11-27"
---

# NET-011: Automatic LAN direct upgrade for relay hosts

Status: phases 1–4 built (relay, server, desktop, and iOS).

This doc defines automatic LAN direct upgrade and relay fallback for hosts that connect through `windrex-relay`. It extends [`NET-005`](NET-005-direct-relay.md), [`NET-007`](NET-007-desktop-direct-client.md), [`NET-008`](NET-008-relay-discovery.md), and [`NET-010`](NET-010-relay-fleet-discovery.md). Replaces: [`NET-010`](NET-010-relay-fleet-discovery.md), section "Security", item "Transport boundary" for private-LAN `transportDirect` peers when `RelayTrust` and `LANDirect` are enabled.

## Problem

After [`NET-009`](NET-009-remove-tailscale.md) removed built-in Tailscale and [`NET-010`](NET-010-relay-fleet-discovery.md) added one-scan Relay Fleet discovery, each saved remote host stores a single endpoint address (`hosts.Host.Address` on desktop and `HostConfig.url` on iOS). When a user adds `dev-box-1` through a self-hosted relay so it stays reachable anywhere, every packet traverses the public internet and the relay server even when the client and `dev-box-1` sit on the same office or home LAN.

This WAN hairpin causes two permanent problems on local networks:

1. Interactive terminal latency depends on office multi-WAN routing, ISP jitter, and relay uplink load instead of local LAN latency (under 10 ms).
2. All panes of a host multiplex over one outbound relay TCP tunnel (`directrelay.Mux`), so busy background sessions on the same LAN create head-of-line blocking across the WAN link.

Manual workarounds break the single-host model. Changing a host address to a LAN IP stops access away from the office. Adding a second host entry splits sidebar state, layouts, and pane history across two records.

## Goal

- A client on the same LAN or routable private subnet as a relay host connects directly over pinned Direct TLS in under 15 ms with zero manual host switching.
- A client away from the LAN falls back to the relay automatically with at most 200 ms of added dial time.
- Each remote host stays a single entry in `hosts.json`, `HostStore`, and the sidebar on desktop and iOS.
- Switching networks (leaving the office Wi-Fi for cellular, or returning to the office Wi-Fi) switches between LAN direct and relay without manual reconfiguration.
- `windrex-relay` remains stateless and never stores host access tokens or private keys.
- No Tailscale stack, coordination server, or mDNS daemon is added.

## Non-goals

- Supporting LAN discovery when the internet and relay are completely offline (mDNS or UDP broadcast discovery can be evaluated separately if needed).
- Hole punching across NAT between two different private networks.
- Changing the required fields of `hosts.json` or `UserDefaults["hosts"]`.
- Changing pane streaming cadence or `directrelay.Mux` frame scheduling (those belong to a separate transport performance doc).

## Design

### Behavior

1. On a relay-enabled host (`dev-box-1`), `windrex-server` or desktop Server keeps its outbound tunnel to `windrex-relay` and automatically binds a local Direct TLS listener (`:7443`, falling back to `:0` if occupied) using a self-signed ECDSA P-256 certificate and leaf SHA-256 `direct_pin`, unless `lan_direct` is set to `false`.
2. When `dev-box-1` registers at `/relay/host`, it includes its non-secret local private IPv4/ULA `host:port` candidates (`lan_addrs`) and its `direct_pin`.
3. When a desktop or iOS client queries `GET /api/hosts` (or calls authenticated `GET /api/v1/lan-hint` on a single-host relay connection), it learns `lan_addrs` and `direct_pin` for `dev-box-1` in memory.
4. When the client connects to `dev-box-1`, it races the `lan_addrs` candidates first with a 200 ms head start before starting the relay dial.
5. On the same LAN (including routable cross-VLAN office subnets such as `10.0.13.0/24` to `10.0.8.0/24`), the pinned TLS handshake to `10.0.13.20:7443` succeeds in a few milliseconds, cancels the relay dial, and carries all control and terminal streams directly over the LAN.
6. Away from the LAN, the LAN dial fails or times out, the relay dial starts after 200 ms, and the client connects through `https://relay.example.com:7443/h/<host_id>` with no error shown to the user.
7. When the client leaves the LAN, the direct socket fails and the reconnect loop races again, settling on the relay.
8. When the client returns to the LAN while connected through the relay, a lightweight `GET /api/v1/lan-hint` probe on network change (and periodically while on relay) verifies a LAN candidate before switching the active connection to LAN direct.
9. Host diagnostics and connection badges show the active transport (`LAN direct` or `Relay`) and measured round-trip time while keeping one host row in the sidebar.

### Model

#### Advertised LAN candidates

A host collects up to 4 candidate addresses (`MaxLANAddrs = 4`) from active local network interfaces bound to its Direct TLS port:

| Rule | Accepted | Rejected |
| --- | --- | --- |
| IPv4 range | RFC1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) | Loopback (`127.0.0.0/8`), link-local (`169.254.0.0/16`), CGNAT (`100.64.0.0/10`), benchmark/proxy TUN (`198.18.0.0/15`), public IPv4 |
| IPv6 range | Unique local (`fc00::/7`) | Loopback (`::1`), link-local (`fe80::/10`), global unicast |
| Interface name | Physical LAN and Wi-Fi NICs (`en*`, `eth*`, `enp*`, `wlan*`, `wlp*`) | Virtual bridges and tunnels (`lo*`, `docker*`, `br-*`, `veth*`, `cni*`, `flannel*`, `virbr*`, `utun*`, `tun*`, `tap*`, `tailscale*`, `wg*`, `Meta`, `mihomo*`, `podman*`, `awdl*`, `llw*`, `anpi*`, `bridge*`) |
| Ordering | Primary default-route source IP first, then IPv4 before IPv6, sorted lexically | Unordered interface iteration |

`windrex-relay` validates every received `lan_addr` on `/relay/host`. It accepts only literal private IPs with a port in `1..65535`, discards invalid values, and caps the list at 4 entries. It validates `direct_pin` with `ValidPin` and drops `lan_addrs` if `direct_pin` is missing or invalid.

#### Staggered race (Happy Eyeballs)

For a relay host with non-empty `lan_addrs` and valid `direct_pin`, the dialer runs a single-winner race:

| Parameter | Value | Purpose |
| --- | --- | --- |
| `lanHeadStart` | `200 ms` | Gives local LAN candidates time to win before dialing the relay |
| `lanDialTimeout` | `800 ms` | Caps unreachable private-IP blackhole waits |
| `relayDialTimeout` | `15 s` | Matches existing `ConnectDirect` and iOS dial timeouts |
| `lanProbeTimeout` | `400 ms` | Bounds non-disruptive `GET /api/v1/lan-hint` probes |
| `lanProbeInterval` | `60 s` | Non-disruptive check for LAN return while connected over relay |

1. The dialer starts all `lan_addrs` attempts in parallel, each enforcing exact leaf SHA-256 verification against `direct_pin` and sending the host Bearer token.
2. If any LAN attempt completes upgrade before `lanHeadStart`, the dialer cancels all other attempts and returns the LAN connection.
3. If all LAN attempts fail immediately (for example, `EHOSTUNREACH` or `ECONNREFUSED`), the dialer starts the relay attempt immediately without waiting for `lanHeadStart`.
4. Otherwise, after `lanHeadStart`, the dialer starts the relay attempt in parallel while remaining LAN attempts finish up to `lanDialTimeout`.
5. The first attempt to complete upgrade wins. All slower attempts close immediately.
6. Desktop pane streams (`AttachConn` / `Stream`) and iOS `DataConnection` reuse the active winning route of the host rather than racing on every pane open.
7. A network-path change or full host disconnect clears the winning route and runs a fresh race.

#### Relay-to-LAN promotion and single-host (`#k=`) hints

While a host is connected through the relay and has `lan_addrs`, the client does not disrupt active terminals to test the LAN. On relay refresh, network change, or once every `lanProbeInterval`, it sends a single pinned TLS `GET /api/v1/lan-hint` probe with a 400 ms timeout and Bearer authentication to `lan_addrs`. Only when a probe returns HTTP `200` does the client reconnect the host over the winning LAN candidate.

A client that added a host by single-host relay QR (`#k=`) cannot call `GET /api/hosts` on the relay. After its initial relay connection completes, it calls `GET <relay-base>/api/v1/lan-hint` with the host Bearer token and caches the returned `lan_addrs` and `direct_pin` in memory for reconnects and LAN promotion.

### Code

- `internal/directrelay/lan.go`: Implements `MaxLANAddrs`, `CollectLANAddrs`, `SanitizeLANAddrs`, `IsPrivateLANIP`, `IsVirtualInterfaceName`, and `ParsePeerIP`.
- `internal/directrelay/server.go` and `internal/directrelay/client.go`: Extends `HostInfo` with `LANAddrs []string` (`json:"lan_addrs,omitempty"`) and `DirectPin string` (`json:"direct_pin,omitempty"`). Adds `RelayRegisterURLWithLAN` and `ClientConfig.LANAddrs` / `LANAddrsFunc` / `DirectPin` to send optional `lan_addr` and `direct_pin` query parameters on `/relay/host`.
- `internal/rexapi/direct.go`, `internal/rexapi/http.go`, `internal/rexapi/direct_tunnel.go`, `internal/rexapi/directcli.go`, and `cmd/windrex-server/main.go`: Adds `DirectConfig.LANDirect *bool` (`json:"lan_direct,omitempty"`, default enabled) and `DirectStatus.LANAddrs`. Automatically binds local Direct TLS (`:7443`, fallback `:0`) on relay hosts when `LANDirectEnabled()` is true, registers `GET /api/v1/lan-hint` on `transportDirect` and `transportRelay`, updates `directListener.admit` to accept `DeriveFleetToken` on `transportDirect` strictly for private RFC1918/ULA peers when `RelayTrust` and `LANDirectEnabled()` are true, and adds `--lan-direct` to `gorex rexapi direct relay`.
- `internal/rex/direct_client.go`: Adds `DirectCandidate`, `DirectRouteKind` (`RouteDirect`, `RouteLANDirect`, `RouteRelay`), `DirectRouteInfo`, `LANHintPath`, `LANHint`, `FetchLANHint`, `ProbeLANDirect`, and `ConnectDirectWithCandidates`.
- `hosts.go`, `relays.go`, `hostsview.go`, `locales/{en,zh-Hans}.json`, and `locales/catalog_gen.go`: Feeds discovered `LANAddrs` and `DirectPin` into `hostConn` in memory, connects with `ConnectDirectWithCandidates`, fetches `/api/v1/lan-hint` for single-host relay entries, promotes Relay to LAN on verified probes, and displays the `LAN direct` tag in `hostDiag`.
- `ios/GoRex/Rex/RelayFleet.swift`, `ios/GoRex/Rex/RelayStore.swift`, `ios/GoRex/Rex/Host.swift`, `ios/GoRex/Rex/NetworkPath.swift`, and `ios/WhatToTest.md`: Decodes optional `lan_addrs` and `direct_pin` on `DiscoveredRelayHost`, stores hints in memory on `Host`, probes and races pinned LAN Direct TLS before relay in `Host.connect`, and triggers LAN promotion on `NetworkPath` changes and foreground return.

## Alternatives

- **mDNS / Bonjour (`_windrex._tcp.local`)**: Fails across routed office VLANs (for example `10.0.13.0/24` Wi-Fi to `10.0.8.0/24` wired servers), requires an mDNS stack on Go/Linux/Windows and iOS Local Network permission prompts, and adds another discovery protocol.
- **Short-lived LAN ticket RPC over relay**: Preserves `transportDirect` rejecting `fleet_token`, but forces every launch to dial the relay first, adds an extra round-trip before LAN connect, and introduces stateful ticket storage on the host.
- **Separate LAN and Relay host entries in `hosts.json`**: Splits one physical machine into two sidebar hosts with separate layout state and forces manual switching whenever the user moves networks.
- **Re-introducing Tailscale or WireGuard**: Reverses [`NET-009`](NET-009-remove-tailscale.md), adds binary size and VPN slot contention on iOS, and restores a second network stack.
- **Unencrypted HTTP on LAN (`http://10.0.13.20:7297`)**: Exposes the host Bearer token to passive sniffing on shared office Wi-Fi and violates [`NET-007`](NET-007-desktop-direct-client.md), which restricts `/api/v1/rex-tunnel` to TLS transports.

## Security

- **TLS certificate pinning prevents LAN MITM**: Every LAN candidate requires a valid 43-character base64url SHA-256 `direct_pin`. The client verifies the exact leaf DER SHA-256 digest in constant time before sending the `Authorization: Bearer` header. An attacker on shared Wi-Fi spoofing `10.0.13.20` cannot pass pin verification and never receives the token.
- **Private-IP-only SSRF guard**: Both `windrex-relay` and clients reject non-private IPs, hostnames, loopback, CGNAT, and `198.18.0.0/15` in `lan_addrs`. A compromised or misconfigured host cannot make clients probe arbitrary public URLs or localhost services.
- **Stateless relay boundary**: `lan_addrs` and `direct_pin` are public connection hints, not secrets. `windrex-relay` still never receives host tokens or `direct.key`, and `/api/hosts` remains gated by `relay_secret`.
- **Single-host token isolation**: Hosts with `RelayTrust: false` continue to accept only their own 256-bit `DirectConfig.Token` on both LAN direct and relay.
- **Scoped fleet token admission on LAN Direct TLS**: When `DirectConfig.RelayTrust == true` and `LANDirectEnabled() == true`, `directListener.admit` accepts `DeriveFleetToken(relay_secret, host_id)` on `transportDirect` strictly when the peer TCP IP is a private RFC1918 or ULA address (`directrelay.IsPrivateLANIP`). Public internet peers, loopback peers, `transportLAN`, `transportTCP`, and hosts with `RelayTrust: false` or `lan_direct: false` reject fleet tokens.

## Migration

- **Old `windrex-relay` with new hosts**: Old relays ignore unknown `lan_addr` and `direct_pin` query parameters on `/relay/host` and omit them from `/api/hosts`. Clients see empty `lan_addrs` and use the existing relay path unchanged.
- **New `windrex-relay` with old hosts**: Old hosts omit `lan_addr` and `direct_pin`. `/api/hosts` omits `lan_addrs` and `direct_pin` via `omitempty`.
- **Old clients with new `windrex-relay` or new hosts**: Old desktop and iOS JSON decoders ignore unknown fields on `HostInfo` and keep using the relay URL.
- **New clients with old hosts**: If an old host returns `401` on LAN direct (for example, before private-peer fleet token admission), the Happy Eyeballs dialer falls back cleanly to the relay.
- **Saved files (`hosts.json`, `relays.json`, `UserDefaults["hosts"]`)**: No required schema changes. Existing paired hosts and Relay Fleets work immediately after upgrading servers and clients.

## Tests

- **Candidate filtering**: Unit tests in `internal/directrelay/lan_test.go` verify RFC1918 and ULA acceptance, exclusion of loopback/link-local/`100.64.0.0/10`/`198.18.0.0/15`/public IPs and virtual NIC names (`docker0`, `utun2`, `Meta`, `tailscale0`), primary-route ordering, and `MaxLANAddrs = 4` capping.
- **Relay metadata validation**: Unit tests in `internal/directrelay/relay_test.go` verify `/relay/host` parses valid `lan_addr` and `direct_pin`, rejects public IPs or malformed pins, exposes them on `GET /api/hosts`, and preserves backward compatibility when omitted.
- **Admission boundary and `/api/v1/lan-hint`**: Unit tests in `internal/rexapi/direct_test.go` and `internal/rexapi/direct_tunnel_test.go` verify `GET /api/v1/lan-hint` returns hints on direct and relay, private RFC1918 peer admits `DeriveFleetToken` when `RelayTrust` and `LANDirect` are enabled, public and loopback peers get `401`, `RelayTrust: false` gets `401`, and `lan_direct: false` binds no local TCP port.
- **Happy Eyeballs race and fallback**: Unit tests in `internal/rex/direct_client_test.go` verify (1) fast LAN candidate wins before `lanHeadStart` and never dials relay, (2) unreachable/refused LAN candidate falls back to relay without stalling, (3) pin mismatch or auth error on LAN candidate falls back to relay without leaking the token, and (4) `AttachConn` reuses the winning route without dropping input (`docs/invariants.md` item 5).
- **Desktop promotion and UI**: Unit tests in `hosts_test.go` verify Relay-to-LAN promotion on probe success, non-disruption on probe failure, `LAN direct` diagnostic tag rendering, and single-host sidebar representation.
- **iOS race and network change**: Unit tests in `ios/GoRexTests/DirectRelayTests.swift` and `ios/GoRexTests/RelayFleetTests.swift` verify decoding `lan_addrs`/`direct_pin`, private-IP sanitization, LAN-first race, relay fallback, and `NetworkPath` promotion.
- **End-to-end verification**: Run a host on an office LAN (`10.0.13.20`) registered to a remote relay, confirm desktop and iOS connect via `LAN direct` under 10 ms on office Wi-Fi, switch to cellular and confirm automatic relay fallback, and return to office Wi-Fi to confirm automatic LAN promotion.

## Phases

1. Design doc (`docs/NET-011-lan-direct-upgrade.md`) and decision record.
2. Relay and host server support (`internal/directrelay`, `internal/rexapi`, `cmd/windrex-server`).
3. Desktop client Happy Eyeballs dialing, `/api/v1/lan-hint` promotion, and `LAN direct` diagnostics (`internal/rex`, `hosts.go`, `relays.go`, `hostsview.go`).
4. iOS client `DiscoveredRelayHost` hints, LAN-first dialing, and `NetworkPath` promotion (`ios/GoRex/Rex/`).
