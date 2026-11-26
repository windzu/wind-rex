---
name: NET-006
description: "WindRex ships a standalone headless Linux server binary, systemd units, an installer script, and a release workflow for linux/amd64 and linux/arm64 without desktop UI dependencies. Use when you change cmd/windrex-server, packaging/linux, scripts/install-server.sh, or headless server releases."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# NET-006: Standalone headless Linux server package

Status: built.

This doc defines the standalone headless Linux server package (`windrex-server`) and its release, installation, systemd, diagnostic, and in-place upgrade workflows. It builds on [`REF-004`](REF-004-shared-server.md), [`REF-006`](REF-006-session-server.md), and [`NET-005`](NET-005-direct-relay.md).

## Problem

Remote Linux machines that only host terminal sessions currently require the full WindRex desktop executable (`gorex -server`). That desktop binary embeds desktop UI views, shaders, glass plugins, fonts, icons, and locale catalogs (50 MB stripped on `linux/amd64`).

There is also no dedicated Linux server release artifact, no checksum-verified install or upgrade script, and no user-mode systemd unit for personal servers or stateless relay units alongside the server. Operators upgrading a headless host need a clear, repeatable flow that never kills running shells.

## Goal

- Publish standalone `linux/amd64` and `linux/arm64` archives for `windrex-server` (with bundled `libghostty-vt.so`, systemd templates, and `install-server.sh`) and `windrex-relay`, accompanied by a verified `SHA256SUMS` file.
- Provide a single headless binary `cmd/windrex-server` that runs the session server (`internal/rex`), the Rex API gateway (`internal/rexapi`), and the administration CLI (`internal/cli`) with zero X11, Wayland, GTK, or desktop assets.
- Support safe in-place upgrades (`windrex-server server upgrade`) and migration from existing `gorex -server` deployments while preserving running shells, PTYs, data directory state, and paired identities.
- Keep desktop local auto-start and protocol/capability negotiation unchanged so newer clients work with supported older servers and older clients work with newer servers.

## Non-goals

- Removing the embedded session server from the desktop app. The desktop app keeps its one-click local server startup.
- Publishing macOS or Windows headless server packages in this phase. Additional platforms follow after Linux validation.
- Replacing `internal/rex` or `internal/rexapi` protocols, data formats, or pairing keys.
- Adding an interactive login or PAM server. Multi-client isolation follows [`REF-004`](REF-004-shared-server.md).

## Design

### Behavior

1. On a headless Linux machine (`alice-server`), the operator installs `windrex-server` (and optionally `windrex-relay`) with `scripts/install-server.sh`. For a personal account, `--user` installs under `~/.local/lib/windrex` and symlinks `~/.local/bin/windrex-server`. For a shared host, `--system` installs under `/usr/local/lib/windrex` and symlinks `/usr/local/bin/windrex-server`.
2. With `--systemd`, the script installs `windrex-server.service` (and `windrex-relay.service` when `--with-relay` is passed) and reloads systemd. The operator starts the service with `systemctl --user enable --now windrex-server` (or `sudo systemctl enable --now windrex-server` for system mode), or runs `windrex-server server start` directly.
3. To pair iPhone or desktop clients over cellular or WAN, the operator enables direct TLS (`windrex-server rexapi direct on --listen :7443 --public https://203.0.113.10:7443`) or stateless relay mode (`windrex-server rexapi direct relay --url https://relay.example.com:7443 --pin <pin>`), then scans the printed QR code.
4. To inspect health and active transports, the operator runs `windrex-server server status` (or `--json`). The command reports PID, executable path, protocol version, session count, data directory, socket path, and redacted direct, relay, and LAN status.
5. To upgrade a running server, the operator re-runs `scripts/install-server.sh` (or installs a new archive and runs `windrex-server server upgrade` or `systemctl reload windrex-server`). The installer replaces files atomically and hands running PTYs over in place. Active shells and long-running commands continue without interruption, and reconnecting clients resume their screens.

### Model

- **Shared server core**: `cmd/windrex-server` wires the exact `rex.OnServe` hook used by `main.go` to run the embedded `rexapi.Gateway`. It sets `rexapi.RexVersion` to `<version>-windrex` from build-time `-ldflags`.
- **Entrypoint dispatch**: `-server` (including `-resume`, `-handoff-version`, and `-protocol-version`) runs `rex.ServerMain`. `-session-exec` and `-session-read` run `rex.SessionExecMain` so `GOREX_SESSION_USER` works identically. `rexapi` dispatches to `rexapi.Main`. `server` handles `start`, `status`, `upgrade`, and `restart`. All other CLI commands (`ls`, `clients`, `layout`, `new`, `split`, `send`, `capture`, `wait`, `run`, `version`, `-S`) delegate to `cli.Main`.
- **Native terminal library layout**: `mygo/plugins/terminal` loads `libghostty-vt.so` via `purego` from the resolved executable directory (`os.Executable()`). Installing the binary and `libghostty-vt.so` together in `<prefix>/lib/windrex` and symlinking `<prefix>/bin/windrex-server` keeps `bin/` clean and avoids runtime downloads on offline hosts.
- **Cross-binary migration upgrade**: `upgradeRunningServer` allows an in-place handoff without `--force` when `hello.Exe != exe` (such as migrating from `gorex -server` to `windrex-server`), while still requiring the running server to verify `-server -handoff-version` via `probe` before pausing sessions. For the same executable path, it skips identical builds and requires `--force` when the target modification time is not newer.
- **Artifact sizes and runtime dependencies**: Measured with `CGO_ENABLED=0` and `-trimpath -ldflags "-s -w"` after [`NET-009`](NET-009-remove-tailscale.md) removed built-in Tailscale and `tailcat` (previously 31 MB on `linux/amd64`).

| Artifact | Arch | Stripped binary size | Runtime dependencies |
| --- | --- | --- | --- |
| Desktop `gorex` (pre-NET-009 baseline) | `linux/amd64` | 50 MB | `ld-linux`, glibc, `libghostty-vt.so`, desktop display libs when UI starts |
| `windrex-server` | `linux/amd64` | 17 MB | `ld-linux`, glibc, bundled `libghostty-vt.so` (zero X11/Wayland/GTK) |
| `windrex-relay` | `linux/amd64` | 7.9 MB | Statically linked (zero shared library dependencies) |
| `windrex-relay` | `linux/arm64` | 7.4 MB | Statically linked (zero shared library dependencies) |

### Code

- `cmd/windrex-server/main.go`: Headless server entrypoint, hook wiring, `server start|status|upgrade|restart`, and delegation to `rex.ServerMain`, `rex.SessionExecMain`, `rexapi.Main`, and `cli.Main`.
- `cmd/windrex-server/main_test.go`: Unit and end-to-end tests covering help/usage, version, stopped status, secret redaction, detached start, live status, and in-place cross-binary upgrade preserving a live shell.
- `packaging/linux/windrex-server.service` and `packaging/linux/windrex-server.user.service`: System and user systemd units with `ExecReload` bound to `windrex-server server upgrade`.
- `packaging/linux/windrex-relay.service` and `packaging/linux/windrex-relay.user.service`: Optional system and user systemd units for `windrex-relay`.
- `scripts/install-server.sh`: Checksum-verified installer and in-place upgrader supporting `--user`, `--system`, `--prefix`, `--version`, `--with-relay`, `--systemd`, `--archive`, `--checksums`, and `--dry-run`.
- `.github/workflows/release-server.yml`: GitHub Actions workflow building `linux/amd64` and `linux/arm64` tarballs, bundling SHA-256-verified `libghostty-vt.so` from `mygo`'s `mygo-plugin.json`, generating `SHA256SUMS`, and publishing a draft GitHub Release on `server-v*` / `v*` tags or manual dispatch.
- `.golangci.yml`: Allows `os.Exit` in `cmd/` entrypoints alongside `main.go` and `tools/`.

## Alternatives

- **Build tag (`-tags headless`) on root package `main`**: Rejected because root package files embed desktop fonts, icons, and views across dozens of files and would require widespread build-tag splits. A dedicated `cmd/windrex-server` package reuses `internal/rex`, `internal/rexapi`, and `internal/cli` cleanly with zero changes to desktop UI code.
- **Download `libghostty-vt.so` at first server start**: Rejected as the sole path because restricted Linux servers and systemd units without outbound internet or writable cache directories would fail to start. Bundling the SHA-256-verified `.so` beside `windrex-server` makes startup deterministic and offline-safe while keeping `$MYGO_GHOSTTY_VT` as an override.
- **Restart systemd service on upgrade**: Rejected because restarting kills every shell and long-running task. Using atomic file replacement plus `windrex-server server upgrade` (and `ExecReload`) preserves the same PID, listener socket, and PTYs.

## Security

- `scripts/install-server.sh` always downloads `SHA256SUMS` and verifies the SHA-256 digest of every archive before extraction. It never pipes `curl` output into `tar` or a shell.
- `.github/workflows/release-server.yml` verifies the downloaded `libghostty-vt.so` against the exact SHA-256 pinned in `mygo`'s `mygo-plugin.json` before packaging.
- `windrex-server server status` (both human-readable and `--json`) omits secrets: it never prints the direct Bearer token or the relay registration secret.
- Systemd system units run under dedicated unprivileged accounts (`windrex` and `windrex-relay`) with `NoNewPrivileges=true` and `PrivateTmp=true`. `windrex-relay.service` adds `ProtectSystem=strict` and `ProtectHome=true`.
- Authentication and transport security remain unchanged from [`NET-005`](NET-005-direct-relay.md) and [`REF-004`](REF-004-shared-server.md).

## Migration

- Existing hosts running `gorex -server` keep the same `GOREX_DIR` (socket, `layout.jsonl`, `remote.json`, `rexapi/direct.json`, `rexapi/lan.json`, and device keys).
- To migrate a running `gorex -server` without dropping sessions: install `windrex-server`, point the systemd unit's `ExecStart` to `/usr/local/bin/windrex-server -server` (keeping the existing `GOREX_DIR`), run `systemctl daemon-reload`, and run `GOREX_DIR=<dir> windrex-server server upgrade`. The running `gorex` server probes `windrex-server -server -handoff-version`, writes `handoff.json`, and `exec`s `windrex-server -server -resume` in the same PID.
- Paired macOS desktop and iPhone clients reconnect to the same server identity and resume their existing sessions.
- Wire protocol compatibility relies on the existing `ProtocolVersion`, `Hello.Capabilities`, `apiRevision`, and `welcome.features` negotiation. No saved format or wire field changes in this work.

## Tests

- Headless CLI, status, secret redaction, detached start, and cross-binary in-place upgrade: `go test -race ./cmd/windrex-server` (`TestHelpAndUsage`, `TestVersion`, `TestServerStatusStoppedAndSecretRedaction`, `TestServerStartStatusAndInPlaceUpgrade`). Covers `docs/invariants.md` item 1 (in-place upgrade keeps running shells and PIDs).
- Cross-compilation and linting: `go run ./tools/verify` checks `gofmt`, `design docs`, `actionlint` on `.github/workflows/release-server.yml`, `go vet` across `linux`, `windows`, and `darwin`, `go build ./...` across `linux/amd64`, `linux/arm64`, `windows/amd64`, and `darwin/arm64`, `golangci-lint`, and package tests.
- Installer script verification: `bash -n scripts/install-server.sh`, `--dry-run`, offline `--archive` + `--checksums` install into an isolated prefix verifying binary symlinks and systemd units, and a negative test confirming a mismatched SHA-256 fails before extraction.
