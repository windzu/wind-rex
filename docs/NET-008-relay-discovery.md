---
name: NET-008
description: "WindRex relay exposes an authenticated read-only online host directory at GET /api/hosts and accepts optional host display names during tunnel registration. Use when you change relay host discovery, /api/hosts authentication, host display name metadata, or DiscoverHosts."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# NET-008: Authenticated relay host discovery

Status: phase 1 built (relay server, host metadata, and DiscoverHosts helper). Desktop and iOS discovery UI follow in later phases.

This doc adds a stateless, authenticated online host directory to `windrex-relay` so clients sharing a self-hosted relay can list connected hosts without an account server. It extends [`NET-005`](NET-005-direct-relay.md).

## Problem

When a user connects several hosts (such as `alice-laptop` and Linux development boxes) to one self-hosted `windrex-relay`, every new client still has to scan a QR code or paste a pairing URL for each host. `internal/directrelay/server.go` tracks active tunnels only by `host_id` and exposes no endpoint to list online hosts. After removing the built-in coordination server (`internal/coord`), users need lightweight host discovery without introducing accounts or persistent relay state.

## Goal

- `directrelay.Server` exposes `GET /api/hosts` protected by the relay shared secret (`Secret` / `-secret` / `WINDREX_RELAY_SECRET`).
- When the relay secret is empty, `/api/hosts` is disabled and returns `404 Not Found`.
- Hosts can send an optional display name during `/relay/host` registration, while older hosts that omit it continue to register cleanly and fall back to `host_id`.
- `GET /api/hosts` reflects live connections immediately: new registrations appear, re-registrations replace stale metadata, and disconnects remove the host.
- `internal/directrelay` provides `DiscoverHosts` with SHA-256 leaf certificate pinning so desktop, CLI, and iOS clients can share one discovery contract.

## Non-goals

- Storing or returning per-host pairing tokens or access secrets on the relay. Clients still authenticate to each host with that host's own credential.
- Adding user accounts, persistent host databases, or push notifications for host presence.
- Desktop Settings or iOS Remote Hosts discovery UI. Those ship in follow-up phases on top of `GET /api/hosts` and `DiscoverHosts`.

## Design

### Behavior

1. An operator starts `windrex-relay -listen :7443 -secret <relay_token>` on a reachable machine such as `relay.example.com` or `203.0.113.10`.
2. Each host opens its outbound relay tunnel to `wss://relay.example.com:7443/relay/host?id=<host_id>&name=<display_name>` with `Authorization: Bearer <relay_token>`.
3. An authorized client sends `GET https://relay.example.com:7443/api/hosts` with `Authorization: Bearer <relay_token>` and pins the relay certificate when self-signed.
4. The relay returns JSON listing every currently connected host with its `id`, `name`, `path` (`/h/<host_id>`), and `connected_at` timestamp in RFC3339 UTC, sorted by `name` then `id`.
5. If the host re-registers with a new display name or new socket, the relay replaces the previous tunnel and updates the directory entry atomically.
6. When a host disconnects, the relay removes its entry as soon as the tunnel closes.
7. If the operator runs `windrex-relay` without `-secret`, `/api/hosts` returns `404 Not Found` for all requests so an unauthenticated relay never leaks connected host identifiers.

### Model

- **Host display name**: `handleHost` reads the optional `name` query parameter first and falls back to the `X-WindRex-Host-Name` HTTP header. `SanitizeHostName` strips control characters, replaces runs of whitespace with single spaces, trims both ends, and caps length at 64 runes. If the sanitized result is empty, the display name defaults to `host_id`.
- **Client default name**: `ClientConfig.HostName` lets callers pass an explicit display name. When omitted, `TunnelListener` uses `os.Hostname()` with any trailing `.local` suffix removed and sanitized.
- **Directory entry**: Each connected host is stored in memory as a `hostEntry` (`id`, `name`, `connectedAt`, `mux`). `connectedAt` uses UTC truncated to whole seconds so standard RFC3339 parsers on Go, Swift, and JavaScript decode it consistently.
- **Response schema**: `GET /api/hosts` returns `{"hosts":[{"id":"...","name":"...","path":"/h/...","connected_at":"2026-11-26T06:54:55Z"}]}` with `Content-Type: application/json` and `Cache-Control: no-store`. When zero hosts are online, `hosts` encodes as `[]`.
- **Ordering**: Entries are sorted deterministically by `name` ascending, then `id` ascending.
- **Status codes on `/api/hosts`**:

| Condition | Status | Headers |
| --- | --- | --- |
| Relay `Secret` is empty | `404 Not Found` | `Content-Type: application/json` |
| HTTP method is not `GET` | `405 Method Not Allowed` | `Allow: GET` |
| Missing or invalid Bearer token | `401 Unauthorized` | `WWW-Authenticate: Bearer` |
| Authorized `GET` | `200 OK` | `Content-Type: application/json`, `Cache-Control: no-store` |

### Code

- `internal/directrelay/server.go`: Add `HostInfo`, `HostNameHeader`, `SanitizeHostName`, `Server.Hosts`, and `Server.handleHosts`. Upgrade `Server.hosts` from `map[string]*Mux` to `map[string]*hostEntry` with atomic replacement and stale-entry guards on disconnect.
- `internal/directrelay/client.go`: Add optional `ClientConfig.HostName`, `RelayRegisterURLWithName`, `RelayHostsURL`, `ErrDiscoveryDisabled`, `ErrUnauthorized`, and `DiscoverHosts(ctx, relayURL, relayToken, tlsPin)`.
- `cmd/windrex-relay/main.go`: Clarify `-secret` help text and print whether `/api/hosts` discovery is enabled or disabled at startup without printing the secret value.

## Alternatives

- **Expose `/api/hosts` without authentication when `-secret` is unset**: Rejected because any internet scanner reaching a self-hosted relay could enumerate connected host identifiers and hostnames.
- **Store host pairing tokens on the relay for zero-click pairing**: Rejected because a stateless relay must not hold host credentials or widen its blast radius.
- **Require a new registration endpoint path**: Rejected because keeping `/relay/host` with optional `name` metadata preserves full wire compatibility with existing hosts.

## Security

- `/api/hosts` is fail-closed: when `Server.Secret` is empty, the endpoint returns `404 Not Found` for every HTTP method.
- Token checks use `subtle.ConstantTimeCompare` on the `Authorization: Bearer <token>` header.
- Responses include `Cache-Control: no-store` and contain only discovery metadata (`id`, `name`, `path`, `connected_at`), never host access tokens or the relay secret.
- `DiscoverHosts` verifies the relay leaf certificate SHA-256 digest when `tlsPin` is set, refuses HTTP redirects so the Bearer token cannot leak to another origin, and caps response body reads at 1 MiB.
- Display names are sanitized and bounded to 64 runes before storage and logging to prevent control-character injection and memory abuse.

## Migration

- Existing hosts that dial `/relay/host?id=<host_id>` without `name` or `X-WindRex-Host-Name` continue to connect identically and appear with `name` equal to `host_id`.
- Existing `windrex-relay` deployments without `-secret` see no behavioral change except that `/api/hosts` explicitly returns `404 Not Found`.
- `RelayRegisterURL(relayBase, hostID)` and `NewServer(secret, logf)` keep their existing signatures.

## Tests

- Disabled discovery (`404`): `TestDiscoveryDisabledWithoutSecret` in `internal/directrelay/relay_test.go` verifies `/api/hosts` returns `404` on `GET` and `POST` and `DiscoverHosts` returns `ErrDiscoveryDisabled` when `Secret` is empty.
- Auth and methods (`401` / `405`): `TestDiscoveryAuthAndMethods` verifies missing, wrong, and non-Bearer credentials return `401` with `WWW-Authenticate: Bearer`, non-GET returns `405` with `Allow: GET`, and valid auth returns `{"hosts":[]}` with `Cache-Control: no-store`.
- Live lifecycle and sorting: `TestDiscoveryLifecycleAndSorting` registers multiple hosts, checks deterministic sorting by `name` then `id`, checks immediate removal on disconnect, and checks atomic metadata replacement on re-registration without stale disconnect eviction.
- Backward compatibility and sanitization: `TestDiscoveryBackwardCompatAndHeader` verifies legacy hosts omitting display name fall back to `host_id`, `X-WindRex-Host-Name` works as a fallback, and control characters or overlong names are sanitized.
- Client helper and TLS pinning: `TestDiscoverHostsClientHelper` verifies `DiscoverHosts` succeeds with matching pin and token and fails on pin mismatch, invalid pin syntax, wrong token, or disabled discovery.
- CLI output hygiene: `cmd/windrex-relay/main_test.go` verifies startup guidance reports discovery status without echoing the secret.

## Phases

1. Relay server `/api/hosts`, optional registration display name metadata, `DiscoverHosts` helper, and `windrex-relay` startup notes (Issue #45).
2. Desktop and CLI client discovery UX built on `DiscoverHosts`.
3. iOS Remote Hosts discovery UI built on `GET /api/hosts`.
