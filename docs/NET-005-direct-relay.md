---
name: NET-005
description: "WindRex adds a native TLS direct listener and a stateless reverse relay so iOS reaches remote hosts over cellular networks without an in-process VPN stack. Use when you change direct TLS, windrex-relay, certificate pinning, foreground probes, or pipelined terminal writes."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# NET-005: Direct TLS and stateless relay transport

Status: phase 1 built.

This doc defines a native TLS transport for cellular remote work, with two deployment modes that share one iOS client path, plus fast foreground recovery and pipelined terminal writes. It extends [`IOS-012`](IOS-012-background-and-network-changes.md) and coexists with [`IOS-001`](IOS-001-tailnet.md) and [`IOS-002`](IOS-002-tailcat.md) during migration.

## Problem

On iOS over cellular networks, the built-in Tailscale and tailcat transports in `ios/tailnet` run a Go userspace WireGuard stack behind a loopback SOCKS5 proxy. Carrier symmetric NAT and iOS background suspension often force traffic onto distant public DERP relays, which creates TCP-in-TCP head-of-line blocking and high latency.

Four code defects compound the transport overhead:

1. Returning from background can stall up to 10 seconds in `ResetSockets` (`ios/tailnet/lifecycle.go`) while `TerminalScreen` immediately calls `TerminalSession.resume()` against a proxy being replaced.
2. `HostStore.foreground` (`ios/GoRex/Rex/Host.swift`) skips liveness checks on `.system` hosts when `control != nil`, so half-open sockets wait up to 45 seconds for `ControlConnection.pingLoop` to fail.
3. `DataConnection` (`ios/GoRex/Rex/DataStream.swift`) performs no client-side liveness probe after returning to the foreground.
4. `TerminalSession.write` (`ios/GoRex/Terminal/TerminalSession.swift`) serializes one `com.superlogical.terminal.write` JSON-RPC call per round-trip because the server ran block writes concurrently.

## Goal

- An iOS client on cellular networks reaches a host directly over native TLS (for example, through a home router public IP and port forward) or through a self-hosted stateless relay on a nearby VPS, without occupying the iOS VPN slot and without the in-process SOCKS5 proxy.
- Both direct TLS and stateless relay use the same native `URLSession` WebSocket path on iOS, authenticated by a 256-bit Bearer token and protected by either CA-signed TLS or leaf SHA-256 certificate pinning.
- Returning to the foreground probes an existing connection within 1.5 seconds: a live connection stays open with no disruption, and a dead connection reconnects immediately and resumes the terminal stream with `RESUME`.
- Typing over high-RTT links pipelines ordered writes so consecutive keys do not stall for one round-trip each.

## Non-goals

- Removing `ios/tailnet`, built-in Tailscale, or tailcat in this phase. Removal happens in phase 2 after real-device cellular verification.
- Keeping sockets alive indefinitely while iOS suspends the app. The session server keeps processes alive, and the iOS client resumes rapidly in the foreground.
- Adding user accounts or a stateful coordination server.

## Design

### Behavior

1. On a host reachable through a public IP or home router port forward (such as `203.0.113.10:7443`), the user runs `gorex rexapi direct on --listen :7443 --public https://203.0.113.10:7443`.
2. The host starts a TLS listener on `:7443`, creates a 256-bit pairing token and a self-signed ECDSA P-256 certificate (unless `--cert` and `--key` are provided), and prints a pairing URL and QR code: `https://203.0.113.10:7443/#k=<token>&p=<cert_pin>&n=<host>`.
3. On a host behind NAT without port forwarding, the user runs `windrex-relay -listen :7443` on a reachable machine (a home router, Linux box, or VPS) and runs `gorex rexapi direct relay --url https://relay.example.com:7443 --pin <relay_pin>` on the host.
4. The host maintains one persistent outbound WebSocket tunnel to the relay under a random `host_id` and prints the pairing URL `https://relay.example.com:7443/h/<host_id>/#k=<token>&p=<relay_pin>&n=<host>`.
5. On iOS, the user scans the QR code (or pastes the URL) in Remote Hosts. WindRex stores the URL, Bearer token in Keychain, and optional certificate pin, then connects directly with native `URLSession`.
6. When the user switches back from another app or unlocks the phone, WindRex probes the active control and data sockets with a 1.5-second ping timeout. Live sockets stay attached without flicker. Dead sockets close with `goingAway` and reconnect immediately using the reconnect token and stream byte offset.

### Model

- **Direct config**: `direct.json` in `rexapi`'s state directory stores `enabled`, `listen`, `public_url`, `token`, optional `cert_file` and `key_file`, and optional relay fields (`relay_url`, `relay_secret`, `relay_cert_pin`, `host_id`). `direct-status.json` stores runtime status (`running`, `listening`, `cert_pin`, `relay_connected`, `pair_url`, `error`, `updated`).
- **Stateless relay protocol**: The host dials `wss://<relay>/relay/host?id=<host_id>` (carrying optional `Authorization: Bearer <relay_secret>`). Over that WebSocket, a binary frame multiplexer (`OPEN`, `DATA`, `CLOSE`, `ACK`) carries independent byte streams. When a client connects to `/h/<host_id>/<path>`, the relay strips `/h/<host_id>`, opens a mux stream to the host with zero extra dial round-trips, and reverse-proxies HTTP and WebSocket traffic verbatim to the host's `rexapi.Gateway`.
- **Authentication**: The host verifies the 256-bit Bearer token (`Authorization: Bearer <token>` or query `k`) in constant time on every direct and relay request. No IP allowlist is required because cellular IPs change frequently.
- **Certificate pinning**: When `HostConfig.tlsPin` is set, iOS builds a `URLSession` with a delegate that hashes the server's leaf certificate DER with SHA-256 and requires an exact match against the pinned base64url digest. When `tlsPin` is nil, standard iOS CA trust applies.
- **Path-preserving URLs**: `URL.apiURL(_:)` and `URL.rexURL(_:)` preserve any non-empty base path prefix (such as `/h/<host_id>`) before appending `/api/control` or `/api/data`.
- **Foreground liveness and ordering**: `HostStore.foreground` probes `.system` hosts with a 1.5-second ping when `control != nil`, resumes dead hosts immediately, and finishes any `TailnetEngine.resume()` before signaling `TerminalScreen` to verify or resume panes.
- **Ordered pipelined writes**: The server adds `com.superlogical.terminal.write` to `orderedMethods` and advertises feature `gorex.ordered_write` in `welcome`. When that feature is present, `TerminalSession` pipelines up to 8 in-flight writes in order while keeping the existing 1-at-a-time fallback for older servers.

### Code

- `internal/directrelay/mux.go`, `server.go`, `client.go`, `cert.go`: Binary WebSocket stream multiplexer, stateless reverse-proxy relay server, outbound host tunnel client, and self-signed TLS certificate helper with SHA-256 pin generation.
- `cmd/windrex-relay/main.go`: Standalone relay server binary supporting custom ports, auto self-signed TLS with pin output, or custom `-cert`/`-key`.
- `internal/rexapi/direct.go`, `directcli.go`: `DirectConfig`, `DirectStatus`, `directListener` (direct TLS + outbound relay tunnel), `transportDirect` and `transportRelay` admission, and `gorex rexapi direct` CLI commands (`on`, `off`, `status`, `qr`, `relay`, `rotate`).
- `internal/rexapi/serve.go`, `origin.go`, `host.go`, `gateway.go`, `protocol.go`, `upload.go`: Wire `directListener` into `Gateway` and `Wanted`, allow direct and relay transports, add `com.superlogical.terminal.write` to `orderedMethods`, and advertise `gorex.ordered_write`.
- `ios/GoRex/Rex/TLSPin.swift`: `PinnedTLSSession` delegate verifying leaf certificate SHA-256 against `HostConfig.tlsPin`.
- `ios/GoRex/Rex/Host.swift`, `ControlConnection.swift`, `DataStream.swift`, `GoRexApp.swift`, `Views/TerminalScreen.swift`, `Views/RemoteHostsView.swift`: Parse `#k=...&p=...&n=...` and path-prefixed pairing URLs, preserve URL base paths, add `ControlConnection.probe` and `DataConnection.probe`, coordinate foreground resume before terminal resume, and shorten default control ping intervals to 15 seconds with an 8-second pong timeout.
- `ios/GoRex/Terminal/TerminalSession.swift`: Pipeline up to 8 ordered `com.superlogical.terminal.write` requests when the server advertises `gorex.ordered_write`.

## Alternatives

- **Require the system Tailscale app on iOS**: Rejected because iOS allows only one active VPN tunnel, which conflicts with proxy tools and alters DNS for other apps.
- **Keep `ios/tailnet` and only host a private DERP**: Rejected as the long-term transport because iOS still suffers TCP-in-TCP over DERP and `ResetSockets` failures after suspension.
- **Plain HTTP over cellular networks**: Rejected because Bearer tokens and terminal traffic cross untrusted carrier networks. Self-signed TLS with SHA-256 pinning gives full TLS 1.3 encryption even on a raw home router IP without a domain.

## Security

- Direct TLS and relay connections always use TLS and require a 256-bit constant-time Bearer token validated by the host.
- Self-signed certificates are accepted only when the exact leaf DER SHA-256 hash matches the pinned digest scanned from the host's QR code.
- The stateless relay accepts host registrations only with the configured `-secret` when set, stores no credentials or session data on disk, and cannot pass host admission without the host's Bearer token.
- Query parameter `k` is stripped after admission (`withoutToken`) and never logged.

## Migration

- `HostConfig.tlsPin` is an optional JSON field that defaults to `nil`. Existing saved hosts in `UserDefaults` decode unchanged.
- Existing `http://IP:PORT/#k=<token>` LAN QR codes continue to parse identically.
- `direct.json` defaults to disabled when absent, so existing servers behave identically until the user enables direct TLS or relay mode.
- Servers without `gorex.ordered_write` keep the single-flight write path on iOS.

## Tests

- Relay mux and end-to-end WebSocket proxy: unit tests in `internal/directrelay` verifying concurrent HTTP and WebSocket streams, large payloads, close propagation, and reconnect.
- Direct TLS listener, relay mode, Bearer auth, cert pinning, and ordered writes: unit tests in `internal/rexapi` covering valid/invalid tokens, cert pin verification, relay WebSocket `/api/control` + `/api/data` + `RESUME`, and strict write ordering (`docs/invariants.md` item 5).
- iOS pairing, path-preserving URLs, TLS pinning, foreground probe, and pipelined writes: unit tests in `ios/GoRexTests` (`PairingTests`, `ConnectionTests`, `HostTests`) built and executed with `xcodebuild` on the iOS Simulator.
- Real-device verification: deploy on the user's home router public IP and measure cellular connect time, Wi-Fi to 5G switch recovery, and typing echo latency.

## Phases

1. Direct TLS listener, stateless `windrex-relay`, iOS native TLS pinning, foreground liveness probes, and pipelined ordered writes (this PR).
2. Remove `ios/tailnet` and simplify onboarding UI (#19) after cellular real-device verification.
