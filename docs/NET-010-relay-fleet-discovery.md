---
name: NET-010
description: "WindRex shares a stateless relay fleet with one QR scan (#r=), derives per-host relay tokens with HMAC-SHA256, and adds custom host display names and desktop relay discovery. Use when you change fleet QR URLs, DeriveFleetToken, relay_trust admission, host_name, fleet_auth metadata, or relays.json."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# NET-010: One-scan relay fleet auto-discovery

Status: phase 1 built (Go relay, server, CLI, and desktop). iOS client follows in a separate PR.

This doc defines the one-scan relay fleet pairing URL (`#r=`), deterministic per-host token derivation, `relay_trust` host admission, custom host display names, and desktop relay discovery backed by `relays.json`. It builds on [`NET-005`](NET-005-direct-relay.md), [`NET-007`](NET-007-desktop-direct-client.md), and [`NET-008`](NET-008-relay-discovery.md).

## Problem

After removing the built-in coordination server (`internal/coord`) and Tailscale ([`NET-009`](NET-009-remove-tailscale.md)), WindRex connects clients to hosts over Direct TLS or a self-hosted `windrex-relay`. Two frictions remain when one user runs several hosts (such as `alice-laptop`, `dev-box-1`, and `gpu-node-1`) on one relay:

1. `directListener.admit` in `internal/rexapi/direct.go` accepts only each host's random `DirectConfig.Token`, so a new phone or desktop must scan or paste N separate `#k=` URLs even though every host shares the same `windrex-relay` secret.
2. `DirectConfig` has no custom display name override, so `startRelayLocked` always reports `os.Hostname()`, which collides or lacks clarity across cloud boxes and containers.

## Goal

- One `#r=` Fleet QR or URL connects a client to a self-hosted `windrex-relay` and lets it discover and import all opted-in (`fleet_auth`) hosts without scanning each host individually.
- `windrex-relay` remains stateless and never stores or receives per-host access tokens.
- Per-host fleet tokens derive deterministically via HMAC-SHA256 and match byte-for-byte across Go and Swift clients.
- `directListener.admit` accepts a derived fleet token only on `transportRelay` when `RelayTrust` is enabled, never on `transportDirect` or `transportLAN`.
- Each host can override its reported display name via `DirectConfig.HostName` (`--name` in CLI, Display Name in Settings › Server).
- Existing single-host `#k=` pairing URLs and older hosts remain 100% compatible.

## Non-goals

- User accounts, login servers, or relay-side credential storage.
- Accepting fleet tokens on Direct TLS (`transportDirect`) or LAN (`transportLAN`) listeners.
- Multi-tenant per-fleet separate keys (`&f=`); this design keeps room to add an optional fleet secret later without breaking `#r=`.
- iOS `RelayStore` and SwiftUI views; those ship in a companion iOS PR against this exact contract.

## Design

### Behavior

1. On each host, the user connects to their self-hosted `windrex-relay` with a readable display name and opts into relay fleet access: `gorex rexapi direct relay --url https://relay.example.com:7443 --pin <pin> --secret <secret> --name gpu-node-1 --trust-relay` (or toggles "Allow Relay Fleet Access" in Desktop Settings › Server).
2. The host registers on `/relay/host?id=<host_id>&name=gpu-node-1&fleet_auth=1` and appears in `GET /api/hosts` with `"fleet_auth": true`.
3. From Desktop Settings › Server › Direct / Relay Access (or CLI `gorex rexapi direct fleet-qr` / `direct qr --fleet`), the user opens the pairing QR dialog. When `RelayURL`, `RelaySecret`, and `RelayTrust` are all set, the dialog shows a two-tab segmented control: **Relay Fleet** (`#r=`, default) and **This Host** (`#k=`). Neither tab renders the secret fragment on screen, and copying writes to the clipboard with `Concealed: true`.
4. On Desktop Settings › Remote Hosts, clicking **Add Remote Host…** accepts either a single-host `#k=` URL or a `#r=` Fleet URL. Pasting a `#r=` URL verifies it against `GET /api/hosts` (enforcing `p=` if set), persists the relay to `<GOREX_DIR>/relays.json` (`0600`), and lists discovered online hosts under **Relay Fleets**.
5. The discovered list excludes this machine's own `host_id` (read from `rexapi/direct.json`) by marking it as **This machine**, marks already-saved hosts as **Added**, marks hosts with `fleet_auth: false` as **Needs single-host QR**, and offers **Add** per host and **Add All** for one-click import and connection over `/api/v1/rex-tunnel` using `DeriveFleetToken`.

### Model

#### 1. Fleet QR / URL format

```text
https://<relay-host>[:port][<base-path>]/#r=<relay_secret>[&p=<tls_pin_b64url>][&n=<relay_label>]
```

- The relay shared secret stays strictly inside the `#fragment` under key `r` and is never sent in an HTTP path or query string.
- `p` is the optional 43-character unpadded base64url SHA-256 leaf TLS certificate pin (parsers also accept 64-character hex and normalize to base64url).
- `n` is an optional human-readable label for the relay (sanitized to at most 64 runes).
- Single-host pairing URLs (`https://.../#k=<host_token>[&p=<pin>][&n=<host_name>]`) remain unchanged and disjoint from `#r=`. A URL carrying both `r=` and `k=` is rejected.

#### 2. Per-host derived fleet token

```text
fleet_token = base64url_no_pad(HMAC-SHA256(key = relay_secret, msg = "windrex-fleet-v1:" + host_id))
```

- `key` is the UTF-8 bytes of `relay_secret`.
- `msg` is the UTF-8 bytes of `"windrex-fleet-v1:"` concatenated with `host_id`.
- Encoding is RFC 4648 §5 base64url without `=` padding (`base64.RawURLEncoding` in Go), producing exactly 43 ASCII characters.
- Empty `relay_secret` or empty `host_id` returns `""` and never matches any request.

| Input | Value |
| --- | --- |
| `relay_secret` | `"test-relay-secret"` |
| `host_id` | `"host-abc-123"` |
| HMAC message | `"windrex-fleet-v1:host-abc-123"` |
| Expected 43-char token | `3tFr5S1FzWIelzutS1758Y4jbi00QWxKIIQuCYVYtik` |

Both Go (`internal/directrelay`) and Swift (`GoRexTests`) pin this exact vector in unit tests.

#### 3. Relay registration and directory extension

- `/relay/host` accepts optional query parameter `fleet_auth=1` (also `true`). Omitting it defaults `fleet_auth` to `false`.
- `GET /api/hosts` adds optional `"fleet_auth": true` (`omitempty`) to each `HostInfo` item. Hosts with `fleet_auth == false` omit the field to preserve exact wire output for older deployments.

#### 4. Host configuration and admission

- `DirectConfig` (`rexapi/direct.json`) adds optional `host_name` (`HostName string`, `omitempty`) and `relay_trust` (`RelayTrust bool`, `omitempty`).
- Changing `HostName` or `RelayTrust` triggers `relayChanged` in `directListener.apply`, closing the previous relay tunnel and re-registering atomically with the new metadata. Turning `RelayTrust` off drops active relayed tunnels immediately.
- `directListener.admit` always checks `DirectConfig.Token` in constant time. Only when `transport == transportRelay && conf.RelayTrust && conf.RelaySecret != "" && conf.HostID != ""` does it also check `DeriveFleetToken(conf.RelaySecret, conf.HostID)` in constant time.

#### 5. Desktop relay store (`relays.json`)

`<GOREX_DIR>/relays.json` is written atomically with mode `0600` and holds saved relays:

```json
{
  "relays": [
    {
      "id": "a1b2c3d4e5f6",
      "url": "https://relay.example.com:7443",
      "label": "home-relay",
      "secret": "<relay_secret>",
      "cert_pin": "<optional_b64url_pin>",
      "ignored_host_ids": []
    }
  ]
}
```

`hosts.Host` in `hosts.json` adds optional `relay_id` and `relay_host_id` (`omitempty`) to match discovered hosts against already-imported hosts without breaking pre-NET-010 `hosts.json` files.

### Code

- `internal/directrelay/fleet.go`: `FleetTokenDomain`, `DeriveFleetToken`, `FleetTarget`, `IsFleetURL`, `ParseFleetURL`, `FormatFleetURL`, `SanitizeFleetURL`, and `FleetHostPairURL`.
- `internal/directrelay/server.go` and `internal/directrelay/client.go`: `HostInfo.FleetAuth`, `ClientConfig.FleetAuth`, `RelayRegisterURLWithOptions`, and `fleet_auth` parsing in `handleHost`.
- `internal/rexapi/direct.go` and `internal/rexapi/directcli.go`: `DirectConfig.HostName`, `DirectConfig.RelayTrust`, `ResolvedHostName`, `FleetPairURL`, `transportRelay`-only fleet token admission in `directListener.admit`, CLI `--name` / `--trust-relay` / `direct fleet-qr` / `direct qr --fleet`.
- `cmd/windrex-relay/main.go` and `cmd/windrex-server/main.go`: relay startup guidance with `--trust-relay` and Fleet URL template (secret redacted), server status `hostName` and `relayTrust` fields.
- `internal/hosts/hosts.go` and `internal/hosts/relays.go`: optional `RelayID`/`RelayHostID` on `Host`, and `relays.json` `LoadRelays`/`SaveRelays` (`0600`, refuses overwrite on parse error).
- `remoteview.go`, `hostqr.go`, `settingsview.go`, `hostsview.go`, `relays.go`, `locales/{en,zh-Hans}.json`, and `locales/catalog_gen.go`: Settings › Server Display Name, Allow Relay Fleet Access toggle, 2-tab QR sheet (`Relay Fleet` vs `This Host`), and Settings › Remote Hosts `#r=` paste + Relay Fleets discovery list with local `host_id` exclusion and 1-click import.

## Alternatives

- **Store per-host access tokens on `windrex-relay`**: Rejected because a stateless relay must never hold host credentials or become a credential database.
- **Accept raw `relay_secret` as a host Bearer token**: Rejected because deriving per-host tokens via HMAC-SHA256 isolates each host credential and prevents `relay_secret` from traversing `/h/<host_id>/*` streams.
- **Accept fleet tokens on Direct TLS too**: Rejected because `RelayTrust` explicitly scopes trust to traffic authenticated through the relay tunnel (`transportRelay`).

## Security

- **Transport boundary**: `directListener.admit` checks `DeriveFleetToken` strictly when `transport == transportRelay` and `RelayTrust` is true. Requests on `transportDirect`, `transportLAN`, or `transportTCP` reject fleet tokens.
- **Opt-in per host**: `RelayTrust` defaults to `false`. High-sensitivity hosts leave it off (`fleet_auth: false`) and require their own `#k=` single-host QR.
- **Secret isolation**: `relay_secret` lives only in URL `#fragment`, `rexapi/direct.json` (`0600`), and desktop `relays.json` (`0600`). It is never printed on screen, logged, or returned by `/api/hosts` or `server status`. Clipboard writes set `Concealed: true`.
- **TLS pinning**: `DiscoverHosts` and `rex.ConnectRemote` enforce `p=` leaf SHA-256 certificate verification and refuse HTTP redirects on `/api/hosts`.
- **Revocation**: Rotating `relay_secret` on the relay and hosts invalidates all derived fleet tokens at once. Turning `RelayTrust` off on one host immediately disconnects its relayed sessions and stops accepting fleet tokens.

## Migration

- Existing `rexapi/direct.json` files without `host_name` or `relay_trust` default to `""` (OS hostname fallback) and `false` (fleet auth disabled).
- Existing `hosts.json` files without `relay_id` or `relay_host_id` load unchanged.
- Existing hosts that omit `fleet_auth` on `/relay/host` register cleanly with `fleet_auth: false`.
- Older clients that only parse `#k=` safely reject `#r=` URLs without saving broken records.

## Tests

- Pinned HMAC vector and URL parsing: unit tests in `internal/directrelay/relay_test.go` verify `DeriveFleetToken("test-relay-secret", "host-abc-123") == "3tFr5S1FzWIelzutS1758Y4jbi00QWxKIIQuCYVYtik"`, `ParseFleetURL`, `FormatFleetURL`, `fleet_auth` registration, and `DiscoverHosts`.
- End-to-end tunnel and transport boundary: unit tests in `internal/rexapi/direct_tunnel_test.go` and `internal/rexapi/direct_test.go` verify a host with `RelayTrust: true` is discovered via `DiscoverHosts`, admits `DeriveFleetToken` over `/api/v1/rex-tunnel` and `/api/control` on `transportRelay`, rejects the exact same derived fleet token on `transportDirect`, and drops access when `RelayTrust` is turned off.
- Persistence and backward compatibility: unit tests in `internal/hosts/relays_test.go` verify `relays.json` `0600` permissions, round-trip, and refusal to overwrite invalid JSON.
- Desktop UI and design tokens: headless `ui.NewTester` tests in `remoteview_test.go` and `hosts_test.go` cover Display Name, Relay Trust toggle, 2-tab Fleet/Host QR sheet (no secret leak), `#r=` paste, local `host_id` exclusion, 1-click Add All, plus `TestDesignTokens`, `TestGlassInk`, and `TestCatalogsAgree`.
- Visual verification: Light and Dark `GOREX_DEBUG` screenshots of Settings › Server (Fleet QR sheet, Display Name, Relay Trust) and Settings › Remote Hosts (Relay Fleets discovery) using made-up data (`alice-laptop`, `dev-box-1`, `gpu-node-1`).

## Phases

1. Design doc, `internal/directrelay`, `internal/rexapi`, `cmd/windrex-relay`, `cmd/windrex-server`, `internal/hosts`, and Desktop UI (`Refs #55`).
2. iOS `RelayStore`, Keychain secret storage, `#r=` QR scanning, and automatic fleet sync (`Refs #55`).
