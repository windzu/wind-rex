---
name: NET-009
description: "Removes built-in Tailscale, tailcat, Tailnet.xcframework, and iOS Hotspot Share across iOS, desktop, server, and web in two phases so WindRex connects only over Direct TLS, self-hosted windrex-relay, and LAN. Use when you change host transport migration, iOS HostStore legacy decoding, iOS build pipelines, or Phase 2 Go and web Tailscale removal."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# NET-009: Remove built-in Tailscale and Hotspot Share

Status: built — phase 1 (iOS removal, PR #56) and phase 2 (Go, desktop, server, and web removal, Fixes #46).

This doc replaces the removed `IOS-001` (built-in tailnet), `IOS-002` (tailcat), `IOS-003` (Hotspot Share), `DSK-005` (desktop tailnet), `NET-002` (host links), `NET-004` (public relay), `WEB-002` (web Tailscale), and `WEB-006` (web tailcat) designs, and supersedes the Tailscale coexistence sections of [`NET-005`](NET-005-direct-relay.md), [`NET-007`](NET-007-desktop-direct-client.md), [`IOS-012`](IOS-012-background-and-network-changes.md), [`IOS-023`](IOS-023-pairing-entry.md), and [`DSK-010`](DSK-010-trim-browser-and-web.md).

## Problem

WindRex now supports Direct TLS and self-hosted `windrex-relay` across iOS ([`NET-005`](NET-005-direct-relay.md)), desktop ([`NET-007`](NET-007-desktop-direct-client.md)), and headless servers ([`NET-006`](NET-006-headless-server.md)). Carrying the inherited built-in Tailscale, tailcat, and iOS Hotspot Share stacks adds binary size, Go/`gomobile` build coupling, maintenance overhead, and extra network complexity:

- On iOS, `Tailnet.xcframework` (`ios/tailnet` and `ios/share`) compiles a Go userspace WireGuard stack and HTTP proxy through `gomobile`, adds tens of megabytes to every build, requires `setup-go` and framework caching in iOS CI, and maintains separate Settings, pairing, diagnostics, and Hotspot Share surfaces.
- On desktop, server, and web (Phase 2), `internal/tailnode`, `internal/pairaddr`, `internal/rexapi/tailnet.go`, `internal/rexapi/tailcat.go`, `web/tailnet/`, and `tailscale.com` module dependencies keep a second transport and pairing stack alongside Direct TLS and `windrex-relay`.

## Goal

- Phase 1 (iOS): Remove `Tailnet.xcframework`, `ios/tailnet/`, `ios/share/`, `ios/scripts/build-tailnet`, all Swift Tailnet/tailcat/Hotspot Share code, UI, and tests, and Go build steps from iOS workflows so iOS links only `GhosttyVt.xcframework` and `GhosttyKit`.
- Phase 1 (iOS): Tolerantly decode pre-#46 `UserDefaults["hosts"]` JSON so every saved Direct TLS, stateless relay, and LAN host (and its Keychain token in `LANTokens`) survives upgrade intact while unroutable tailcat (`*.tailcat.invalid`) and built-in `.tailnet` records are dropped cleanly.
- Phase 2 (Go/Desktop/Server/Web): Remove `internal/tailnode`, `internal/pairaddr`, `web/tailnet/`, desktop/server Tailscale and tailcat listeners/clients/UI, `system_tailnet*.go`, and `tailscale.com` / `tailcat` dependencies from `go.mod` while keeping `github.com/tailscale/peercred` for Unix socket peer verification.

## Non-goals

- Removing `GhosttyVt.xcframework` or the `windrex://` URL scheme on iOS (widgets use `windrex://open` and `windrex://inbox`).
- Blocking users who run the external system Tailscale VPN app from entering a reachable HTTP(S) host URL manually. WindRex uses standard OS networking (`URLSession` and Go `net/http`) and does not inspect or block OS routes.
- Touching `go.mod`, desktop Go code, `internal/rex*`, or `web/` in Phase 1. Those belong exclusively to Phase 2.

## Design

### Behavior

#### Phase 1: iOS

1. **Settings**: `Settings › Tailscale` and `Settings › Share on Hotspot` no longer appear. `Settings › Remote Hosts` manages Direct TLS, self-hosted relay, and LAN hosts only.
2. **Pairing**: Scanning a QR code from the empty state (`HostListView`) or `Settings › Remote Hosts` accepts Direct TLS (`https://203.0.113.10:7443/#k=<token>&p=<pin>&n=alice-laptop`), stateless relay (`https://relay.example.com:7443/h/<host_id>/#k=<token>&p=<pin>&n=alice-laptop`), and LAN (`http://192.0.2.10:7297/#k=<token>`) codes. Legacy `rexgo://tailcat?...` and `windrex://tailcat?...` codes return the standard invalid-code guidance pointing to `Settings › Server`.
3. **Manual host editing**: `HostForm` edits server URL, icon, name, and startup command without any Built-in Tailscale toggle or IP address tailcat pairing sheet. Editing an existing host's name, icon, or command keeps its stored `lanToken` and `tlsPin`.
4. **Foreground recovery and network changes**: Returning from background probes active `URLSession` control and data sockets directly (`probeOrResume`) and signals `Host.foregroundReady`. `NetworkPath` resumes active or retrying real hosts on interface or gateway changes without any proxy skip branch.
5. **Diagnostics and Developer Mode**: `DiagnosticsView` shows failure reason, host metadata, and `ConnectionLog` lines without Tailscale disco pings. `DeveloperModeOverlay` and `NetworkDiagnostics` report OS interfaces, IPv4/IPv6 support, and VPN presence without built-in Tailscale state.
6. **Upgrade migration**: On launch, `HostStore.init` reads `UserDefaults["hosts"]` element by element. Direct TLS, relay, LAN, and plain OS-routed hosts decode and connect with their Keychain `LANTokens` intact. Unroutable tailcat (`tailcat != nil`, `*.tailcat.invalid`, or `transport == "tailcat"`) and built-in Tailscale (`transport == "tailnet"`) records are dropped and the cleaned list is saved back.

#### Phase 2: Go, Desktop, Server, and Web

1. Remove `internal/tailnode`, `internal/pairaddr`, `tools/remotectl`, `web/tailnet/`, `web/tailcat/`, `web/jsbridge/`, `system_tailnet*.go`, and all Tailscale/tailcat code in `internal/rexapi`, `internal/rex`, `internal/cli`, `cmd/windrex-server`, desktop settings/host dialogs, and `web/src/`.
2. Tolerantly load desktop `hosts.json` (`internal/hosts/hosts.go`): filter out legacy `tailscale:`, `tc...`/`tcat://`, and `net:` records on `Load` while keeping all `https://` Direct TLS and relay hosts intact; ignore unused `remote.json`/`tailscale.json` files on disk.
3. Preserve pre-#46 `MachineID()` stability in `internal/rex/remote.go` using stdlib `crypto/ecdh` X25519 reading/writing `remote-client.json` (`privkey:...` format) with zero `tailscale.com` imports.
4. Drop `tailscale.com`, `github.com/tailscale/tailcat`, `github.com/tailscale/wireguard-go`, and `golang.org/x/mobile/cmd/{gobind,gomobile}` from `go.mod`, keeping only `github.com/tailscale/peercred` for Unix domain socket peer credential checks on `rexapi.sock`.
5. Remove Go/WASM tailnet/tailcat build steps from `web/build.mjs` and `web/dev.mjs` so building `web/` has zero Go dependency.
6. Stripped `linux/amd64` `windrex-server` shrinks from 31 MB to 17 MB (`-45%`).

### Model

#### Approved irreversible decisions (`docs/irreversible.md`)

All three irreversible decisions were approved by the owner before implementation:

| Decision | Choice | Scope | What it affects | Verification | If rollback is needed |
| --- | --- | --- | --- | --- | --- |
| Decision A (iOS `UserDefaults["hosts"]`) | Option 1: Tolerant decode + drop unroutable tailcat/tailnet records | Phase 1 | Legacy tailcat (`*.tailcat.invalid`) and built-in `.tailnet` entries disappear on first launch after upgrade, while every Direct TLS, relay, and LAN host and its Keychain `LANTokens` entry stays intact. | Mixed pre-#46 JSON unit tests in `HostTests` verify Direct, relay, and LAN hosts decode, keep Keychain tokens, and connect to `FakeRex`, while `.tailnet`, `.tailcat`, `*.tailcat.invalid`, and malformed entries are dropped cleanly. | Re-pair affected hosts over Direct TLS, relay, or LAN via QR code. No saved Direct/Relay/LAN data is lost. |
| Decision B (iOS Hotspot Share) | Option 1: Remove `ios/share/` and `HotspotShare` with `Tailnet.xcframework` | Phase 1 | Removes the Personal Hotspot web-share server from iOS so the iOS app has zero Go and `gomobile` dependencies. | `xcodegen generate` and `xcodebuild test` pass with only `GhosttyVt.xcframework` present (`Tailnet.xcframework` absent). | Restore from Git history or introduce a native Swift HTTP server in a future PR if Hotspot Share is ever needed again. |
| Decision C (`system_tailnet*.go` and `peercred`) | Option 1: Remove `system_tailnet*.go`, keep `github.com/tailscale/peercred` | Phase 2 | Removes OS Tailscale interface probing from desktop/server while preserving `peercred` Unix socket authentication on local session servers. | Phase 2 `go test` and cross-builds (`linux`, `windows`, `darwin`) verify local Unix socket auth and zero `tailscale.com` imports. | Re-add lightweight interface inspection if needed without re-introducing `tailscale.com`. |

#### Tolerant iOS host record decoding

`HostConfig` omits `transport` and `tailcat` from `CodingKeys` and implements `init(from:)` so extra legacy JSON keys never throw. `HostConfig.decodeSaved(_:)` decodes the JSON array one element at a time using a private `SavedHostRecord` wrapper:

- Drops records where `demo == true`.
- Drops records with a non-null legacy `"tailcat"` field, a `*.tailcat.invalid` hostname, or a legacy `"transport"` of `"tailcat"` or `"tailnet"`.
- Skips malformed non-conforming elements via an `IgnoredJSON` decoder without failing the surrounding array.
- Preserves every Direct TLS (`https://...` with optional `tlsPin`), relay (`https://.../h/<host_id>`), LAN (`http://...`), and `"transport": "system"` host in original order.

### Code

#### Phase 1 (this PR)

- Deleted: `ios/tailnet/`, `ios/share/`, `ios/scripts/build-tailnet`, `ios/GoRex/Tailnet/`, `ios/GoRex/Views/TailscaleView.swift`, `ios/GoRex/Share/`, `ios/GoRex/Views/HotspotShareView.swift`, `ios/GoRexTests/{ProxyTests,ShareHostsTests,ShareTests,TailnetActivationPolicyTests}.swift`, `ios/GoRexUITests/TailscaleSettingsTests.swift`, `internal/rex/pairaddr_rexgo_test.go`, `docs/IOS-001-tailnet.md`, `docs/IOS-002-tailcat.md`, `docs/IOS-003-web-share.md`.
- Refactored iOS app: `ios/project.yml`, `ios/GoRex/GoRexApp.swift`, `ios/GoRex/Rex/Host.swift`, `ios/GoRex/Rex/HostPairing.swift`, `ios/GoRex/Rex/NetworkDiagnostics.swift`, `ios/GoRex/Rex/NetworkPath.swift`, `ios/GoRex/Rex/BytesStream.swift`, `ios/GoRex/Rex/DataStream.swift`, `ios/GoRex/Terminal/TerminalSession.swift`, `ios/GoRex/Views/SettingsView.swift`, `ios/GoRex/Views/RemoteHostsView.swift`, `ios/GoRex/Views/HostListView.swift`, `ios/GoRex/Views/DeveloperModeOverlay.swift`, `ios/GoRex/Views/DiagnosticsView.swift`, `ios/GoRex/Views/FeaturesView.swift`, `ios/GoRex/Views/TerminalScreen.swift`, `ios/GoRex/Localizable.xcstrings`.
- Refactored iOS scripts, workflows, and verification: `ios/scripts/test-notes`, `ios/scripts/upload`, `ios/WhatToTest.md`, `.github/workflows/ios-test.yml`, `.github/workflows/testflight.yml`, `tools/verify/main.go`.
- Updated tests: `ios/GoRexTests/{HostTests,PairingTests,HostPairingTests,DeveloperModeTests,NetworkPathTests,LANTokenTests,WidgetTests,ConnectionTests}.swift`.

#### Phase 2 (Go, desktop, server, and web)

- Deleted directories: `internal/tailnode/`, `internal/pairaddr/`, `tools/remotectl/`, `web/tailnet/`, `web/tailcat/`, `web/jsbridge/`.
- Deleted files: `internal/rex/{builtin_tailnet*,system_tailnet*,ownrelay*,pairaddr*,firewall*,remote_ops,remote_tailcat_test,remote_gateway_test}.go`, `internal/rexapi/{tailnet*,tailcat*,tailscale_methods,desktop*}.go`, `remotetailnet.go`, `remotetailnet_test.go`, `reachable_test.go`, `web/src/{tailnet,tailengine,tailstore,tailcat,catengine,catstore,guide}.ts`, `web/test/{tailcat,tailstore}.test.ts`, `docs/{DSK-005-tailnet,NET-002-host-links,NET-004-public-relay,WEB-002-tailscale,WEB-006-tailcat}.md`.
- Refactored: `internal/hosts/hosts.go`, `internal/rex/`, `internal/rexapi/`, `internal/cli/`, `cmd/windrex-server/`, desktop `remoteview.go`, `hostqr.go`, `hosts.go`, `hostsview.go`, `settingsview.go`, `state.go`, `app.go`, `workspace.go`, `adoptions.go`, `main.go`, `locales/{en,zh-Hans}.json`, `locales/catalog_gen.go`, `web/build.mjs`, `web/dev.mjs`, `web/_headers`, `web/index.html`, `web/locales/{en,zh-Hans}.json`, `web/src/`, `tools/verify/`, and `go.mod`.

## Alternatives

- **Keep `Tailnet.xcframework` only for iOS Hotspot Share**: Rejected under Decision B because keeping Go/`gomobile` for a single secondary feature preserves the entire framework build pipeline and binary footprint.
- **Keep `Transport` enum with `.tailnet` and `.tailcat` cases as dead stubs**: Rejected because dead transport states complicate every connection and UI check. Explicit tolerant decoding in `HostConfig.decodeSaved` handles upgrades cleanly without dead runtime branches.
- **Ship iOS and Go removal in one giant PR**: Rejected because splitting Phase 1 (iOS) and Phase 2 (Go/Desktop/Server/Web) keeps reviews, CI signals, and rollback boundaries small and clean.

## Security

- Removing `ios/tailnet` and `ios/share` eliminates the loopback SOCKS5 proxy listener, the Personal Hotspot HTTP server, and embedded Tailscale node keys on iOS.
- Direct TLS and stateless relay connections continue requiring a 256-bit constant-time Bearer token (stored in Keychain `LANTokens`) and optional leaf SHA-256 certificate pinning (`PinnedTLSSession`).
- No new listeners, entitlements, or credentials are introduced.

## Migration

- Existing iOS installations upgrading from pre-#46 builds decode `UserDefaults["hosts"]` through `HostConfig.decodeSaved(_:)`.
- Every Direct TLS, relay, and LAN host keeps its `id`, `name`, `url`, `icon`, `command`, `tlsPin`, and Keychain `lanToken` untouched.
- Legacy tailcat (`*.tailcat.invalid` / `"tailcat": {...}`) and built-in Tailscale (`"transport": "tailnet"`) records are dropped on first load, and the cleaned list is written back to `UserDefaults["hosts"]`.
- `windrex://open` and `windrex://inbox` widget deep links continue working unchanged.

## Tests

- Tolerant pre-#46 host migration (`HostTests.testLegacyTailnetAndTailcatRecordsDroppedWhileDirectRelayLANSurvive` and `HostTests.testLegacyMigrationPreservesKeychainTokensAndConnects`): feed mixed pre-#46 JSON containing Direct TLS (with `tlsPin`), relay (`/h/<host_id>`), LAN, `"transport": "system"`, `"transport": "tailnet"`, `"tailcat": {...}`, bare `*.tailcat.invalid`, `demo: true`, and malformed elements; verify Direct/Relay/LAN hosts survive, keep their `LANTokens` in Keychain, persist the cleaned list, and connect live to `FakeRex`.
- Pairing and URL links (`PairingTests`, `HostPairingTests`, `WidgetTests`): verify Direct TLS, relay, and LAN pairing codes work, legacy `rexgo://tailcat` and `windrex://tailcat` codes return `.invalid`, and `windrex://open` / `windrex://inbox` keep working.
- Foreground recovery, network path, and Developer Mode (`HostTests`, `DirectRelayTests`, `NetworkPathTests`, `DeveloperModeTests`): verify probe-or-resume, network-change reconnects, and network summary output with zero Tailnet hooks.
- Build and CI checks: `xcodegen generate` and `xcodebuild test` on iOS Simulator with only `GhosttyVt.xcframework` present (0 repo Swift 6 warnings), `./ios/scripts/test-notes check origin/main`, `bash -n ios/scripts/test-notes ios/scripts/upload`, and `go run ./tools/verify` (design docs, `actionlint`, Go vet/build/lint/test for `tools/verify`, and iOS `xcodebuild`).
- `docs/invariants.md`: Phase 1 touches iOS client transport/config only and does not modify session server invariants; Phase 2 must preserve invariants 1 through 9 when touching desktop and server code.

## Phases

1. **Phase 1 — iOS removal**: Remove built-in Tailscale, tailcat, `Tailnet.xcframework`, `ios/tailnet/`, `ios/share/`, Hotspot Share UI, iOS CI Go/Tailnet steps, and obsolete iOS docs; add tolerant pre-#46 host migration (PR #56, Refs #46).
2. **Phase 2 — Go, desktop, server, and web removal**: Remove `internal/tailnode`, `internal/pairaddr`, `tools/remotectl`, `system_tailnet*.go`, desktop/server Tailscale/tailcat transports, `web/tailnet/`, `web/tailcat/`, `web/jsbridge/`, and `tailscale.com` / `tailcat` / `wireguard-go` / `gomobile` dependencies while keeping `github.com/tailscale/peercred` (this PR, Fixes #46).
