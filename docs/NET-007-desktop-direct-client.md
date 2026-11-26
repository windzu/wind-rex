---
name: NET-007
description: "Desktop WindRex connects to remote hosts directly over Direct TLS or a self-hosted windrex-relay using an authenticated WebSocket tunnel to the host session server. Use when you change desktop Direct TLS or relay client connections, /api/v1/rex-tunnel, TLS certificate pinning for desktop clients, or Direct / Relay host entry in Settings."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# NET-007: Desktop Direct TLS and relay client

Status: built (#44).

This doc adds Direct TLS and self-hosted relay client support to the desktop app and CLI. It extends [`NET-005`](NET-005-direct-relay.md) so a desktop machine connects to remote macOS or Linux hosts without built-in Tailscale or tailcat.

## Problem

[`NET-005`](NET-005-direct-relay.md) added a Direct TLS listener and stateless `windrex-relay` tunnel on the host, plus desktop server settings to configure and share pairing URLs. However, the desktop client (`hosts.go`, `hostsview.go`, and `internal/rex/remote.go`) can only connect to remote hosts over built-in Tailscale or tailcat.

Users who run headless Linux or macOS hosts behind Direct TLS or `windrex-relay` cannot add those hosts from a desktop WindRex window. Supporting Direct TLS and relay as a desktop client transport is required before built-in Tailscale can be removed.

## Goal

- A desktop WindRex client connects to any remote host exposing Direct TLS or `windrex-relay` by pasting its pairing URL in Settings › Remote Hosts › Add Remote Host.
- The connection carries the native `rex` session protocol over an authenticated WebSocket tunnel so terminals, projects, layouts, uploads, and Teleport work identically to other remote hosts.
- Connections strictly enforce the host's 256-bit Bearer token and, when present, leaf TLS certificate SHA-256 pinning.
- The `#k=<token>` secret is never shown in clear text in the UI, host rows, diagnostics, or logs.
- Existing tailcat, Tailscale, and `hosts.json` entries keep working without migration or regression.

## Non-goals

- Removing built-in Tailscale or tailcat from the desktop app. That cleanup happens in a later change once users migrate.
- Automatic host discovery from `windrex-relay`. Relay host listing is tracked separately.
- Exposing the native session tunnel over plain HTTP or LAN listeners.

## Design

### Behavior

1. On the remote host, the user enables Direct TLS or Relay Access in Settings › Server or runs `gorex rexapi direct on` / `gorex rexapi direct relay` to obtain a pairing URL:
   - Direct TLS: `https://203.0.113.10:7443/#k=<token>&p=<sha256>&n=alice-laptop`
   - Self-hosted relay: `https://relay.example.com:7443/h/<host_id>/#k=<token>&p=<sha256>&n=alice-laptop`
2. On the desktop client, the user opens Settings › Remote Hosts and clicks Add Remote Host….
3. The user pastes the pairing URL into the password-masked Address field. WindRex detects the Direct TLS / Relay URL, auto-fills the Name field from `&n=...` (falling back to the URL hostname), hides the tailcat-only Pair as field, and shows the sanitized endpoint without the fragment token.
4. When the user clicks Add, WindRex immediately dials the host, verifies the TLS certificate pin (when `p=` is present), authenticates with the Bearer token, and completes a `rex.Hello` handshake.
5. If authentication fails (wrong or rotated token), the certificate pin does not match, or the host is unreachable, WindRex displays a clear localized error in the dialog and saves nothing.
6. On success, WindRex saves the host to `hosts.json` (`0600`), clears the dialog state, and opens the persistent host connection. The host row shows a `direct` or `relay` badge with the sanitized host address and measured connect/ping times.
7. When the connection drops, the existing host supervisor (`runHost`) reconnects automatically with backoff and reattaches open terminal panes.

### Model

- **Native session tunnel (`GET /api/v1/rex-tunnel`)**: The host's `rexapi.Gateway` exposes a binary WebSocket endpoint exclusively on `transportDirect` and `transportRelay`. Requests from LAN, plain TCP, Unix, Tailscale, or tailcat transports are rejected with `403 Forbidden`.
- **Admission**: Direct and relay admission validates the 256-bit Bearer token in constant time (`subtle.ConstantTimeCompare`). Missing or invalid tokens on `/api/v1/rex-tunnel` fail with `401 Unauthorized` and `WWW-Authenticate: Bearer realm="windrex"`.
- **1:1 stream splicing**: After upgrading the WebSocket, the gateway dials the local `rex` session server Unix socket (`rex.SocketPath()`), wraps the WebSocket as a binary `net.Conn` (`websocket.NetConn`), and splices bytes in both directions. Closing either side, cancelling the request, or stopping the gateway (`g.done`) closes both ends so no goroutines leak.
- **Target parsing and TLS pinning**: `ParseDirectTarget` requires `https://`, a non-empty host, no userinfo, and a non-empty fragment token `#k=`. Optional `#p=` accepts either a 43-character unpadded base64url SHA-256 digest (emitted by `DirectConfig.PairURL` and `windrex-relay`) or a 64-character hex SHA-256 digest. When `#p=` is present, `tls.Config.VerifyPeerCertificate` computes SHA-256 over the leaf DER certificate and compares it in constant time. When `#p=` is absent, standard system CA trust applies.
- **Path prefix preservation**: Any non-empty path prefix such as `/h/<host_id>` is preserved when building `wss://<host><path>/api/v1/rex-tunnel`, matching iOS `URL.apiURL`.
- **Client lifecycle**: `ConnectDirect` returns a standard `*rex.Client` with `keepalive = true`, a redial closure for pane streams (`AttachConn` / `Stream`), and a transport closer that releases idle TLS connections on `Client.Close()`.
- **Redaction**: `SanitizeDirectTarget` removes fragments and `k` parameters. `rex.Scrub` also redacts extracted direct tokens from error strings.

### Code

- `internal/rexapi/direct_tunnel.go`, `internal/rexapi/direct.go`, `internal/rexapi/http.go`, `internal/rexapi/gateway.go`: Register `GET /api/v1/rex-tunnel`, enforce direct/relay transport and Bearer token auth (`401` on bad/missing token), dial `rex.SocketPath()` (or `Options.RexSocket` in tests), and splice binary WebSocket frames with the Unix socket stream.
- `internal/rex/direct_client.go`, `internal/rex/remote.go`: Implement `DirectTarget`, `IsDirectTarget`, `ParseDirectTarget`, `SanitizeDirectTarget`, `ConnectDirect`, `ErrDirectAuth`, `ErrDirectPinMismatch`, and dispatch `rex.ConnectRemote` and `rex.ValidRemoteAddress` for Direct TLS / Relay URLs.
- `hosts.go`, `hostsview.go`, `locales/en.json`, `locales/zh-Hans.json`, `locales/catalog_gen.go`: Auto-detect Direct TLS / Relay URLs in Add Remote Host, auto-fill host name, verify connectivity before saving, map auth and pin errors to localized messages, show `direct` / `relay` badges without leaking `#k=<token>`, and skip `TailcatStatus` probes for direct hosts.

## Alternatives

- **Translate desktop `*rex.Client` calls to Rex API JSON-RPC**: Rejected because the desktop app relies on native `rex` protocol features (`Subscribe`, layout synchronization, file transfer streams, Teleport, and `paneStream` reattach) that differ from the iOS gateway model. Tunneling the native byte stream reuses every existing desktop capability with minimal code.
- **Add separate fields to `hosts.json` for direct URL, token, and pin**: Rejected to avoid changing the `hosts.json` schema (`docs/irreversible.md`). `Host.Address` is already treated as a `0600` secret across the app and CLI.

## Security

- `/api/v1/rex-tunnel` is strictly limited to `transportDirect` and `transportRelay` and gated by the host's 256-bit constant-time Bearer token. It is never exposed on LAN or anonymous TCP listeners.
- Plain `http://` URLs are rejected by `IsDirectTarget` and `ParseDirectTarget`, preventing tokens from traversing unencrypted links.
- Self-signed certificates are accepted only when the leaf DER SHA-256 matches the pinned `#p=` digest in constant time. Invalid pin encodings are rejected during URL parsing.
- Rotating the host's Direct / Relay pairing token closes active direct TLS connections and restarts the relay tunnel, immediately disconnecting desktop clients using the old token.
- The Add Remote Host address input is password-masked, cleared on close, and all logs, errors, and host diagnostics use sanitized addresses without `#k=<token>`.

## Migration

- `hosts.json` keeps its exact existing JSON schema. Existing tailcat and Tailscale hosts continue to load and connect unchanged.
- Older desktop builds ignore unknown `Address` strings without corrupting `hosts.json`.

## Tests

- `internal/rexapi/direct_tunnel_test.go`: Verifies `GET /api/v1/rex-tunnel` over direct TLS and `directrelay.Server` (`/h/<host_id>`), `401` on missing or wrong Bearer token, `403` refusal on LAN and plain TCP listeners, `405` on non-GET, and clean teardown with no goroutine leaks.
- `internal/rex/direct_client_test.go`: Verifies URL parsing (direct, relay `/h/<id>`, base64url and 64-hex pins, invalid pins, missing tokens, `http://` rejection), end-to-end `ConnectRemote` (`Hello`, `List`, `Create`, stream attach, and reconnect), TLS pin mismatch rejection, wrong token rejection, and `SanitizeDirectTarget` / `Scrub` token redaction.
- `hosts_test.go`: Headless `ui.NewTester` tests adding a Direct TLS and Relay host through `hostsview.go`, checking name auto-fill, auth/pin error reporting, `direct`/`relay` badges, and verifying the secret token never appears in rendered UI texts or logs. Also runs `TestDesignTokens`, `TestGlassInk`, `TestCatalogsAgree`, and `TestNoThreeDots` (`docs/invariants.md` item 9).
