---
name: REL-001
description: "WindRex release pipelines for iOS TestFlight and macOS/Windows desktop packages, covering cloud-managed signing, entitlement verification, notarization, auto-updates and version alignment. Use when you change release workflows, mygo.json release metadata, or ios/scripts/upload."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# REL-001: WindRex release pipelines

Status: phase 1 built (#37). Version policy decided; signing credentials onboarding and first live releases are open.

This document defines WindRex's independent release pipelines for iOS TestFlight (issue #27) and macOS/Windows desktop builds (issue #29). It records the workflow triggers, entitlement checks, secret contracts, auto-updater setup and owner-approved release versioning decisions from `docs/irreversible.md`.

## Problem

WindRex started from GoRex `v0.56.1` with two release gaps. First, `.github/workflows/testflight.yml` only supported manual `workflow_dispatch` on `main`, did not validate required App Store Connect secrets before building frameworks, and did not verify that exported IPA bundles kept their entitlements (`App Group`, `iCloud KVS`, `aps-environment`). Second, WindRex had no dedicated desktop release workflow for macOS and Windows packages, and `mygo.json` lacked explicit `slug`, `publisher` and Windows display metadata while keeping GoRex's inherited versions (`MARKETING_VERSION: "1.1"` in `ios/project.yml` and `"version": "0.56.1"` in `mygo.json`).

## Goal

- Trigger an iOS TestFlight build from a manual dispatch or an `ios-v*` Git tag, using cloud-managed signing so distribution keys stay in Apple's environment or the account holder's runner.
- Verify with `codesign -d --entitlements :-` that both the main iOS app (`wiki.wind.windrex`) and widget extension (`wiki.wind.windrex.widgets`) retain their required entitlements before uploading.
- Sync bilingual `en-US` and `zh-Hans` TestFlight release notes from `ios/WhatToTest.md` after each upload.
- Build WindRex desktop packages (`darwin/arm64` DMG and `windows/amd64` NSIS installer) in `.github/workflows/release-desktop.yml`, with optional signing, macOS notarization, signed auto-update manifests and verified `SHA256SUMS`.
- Keep all signing credentials and Apple identifiers out of the public repository, and apply the approved release versioning policy (`1.1` initial iOS submission track; `desktop-v` tag prefix with monotonic bumps above `0.56.1` for desktop).

## Non-goals

- Bumping `version` in `mygo.json` from `0.56.1` before the Ed25519 updater signing keypair is provisioned and the first WindRex desktop release is tagged (`ios/project.yml` stays at `"1.1"`).
- Committing real Apple Team IDs, App Store Connect App IDs, certificates, provisioning profile UUIDs or updater private keys to the repository.
- Replacing `.github/workflows/release-server.yml`, which remains the standalone pipeline for headless Linux server and relay tarballs.

## Design

### Behavior

#### iOS TestFlight release

1. A maintainer triggers `.github/workflows/testflight.yml` manually on `main` (choosing `destination: upload` or `destination: export`) or pushes an `ios-v*` tag.
2. The workflow validates all required App Store Connect secrets and variables before building `GhosttyVt.xcframework`. On a tag push, it also warns if the tag version disagrees with `MARKETING_VERSION` in `ios/project.yml` (`"1.1"` for the initial submission track).
3. `ios/scripts/upload` queries App Store Connect (`asc builds next-build-number`) for the next monotonic `CURRENT_PROJECT_VERSION` unless `BUILD` is supplied explicitly, archives with `CODE_SIGNING_ALLOWED=NO`, applies ad hoc entitlement signatures to the embedded widget extension and main app, and runs `ios/scripts/verify-entitlements` on the archived `.app`.
4. `xcodebuild -exportArchive` signs with Apple cloud-managed signing (`-allowProvisioningUpdates` with the App Store Connect API key) and either uploads directly or writes `ios/build/export/GoRex.ipa`. Whenever an IPA is written (`destination=export` or local-profile upload), `ios/scripts/verify-entitlements` unpacks `GoRex.ipa` and checks the exported bundle's entitlements again before any upload occurs.
5. On a successful `upload`, the workflow diffs `ios/WhatToTest.md` against the previous successful TestFlight run's commit and calls `ios/scripts/test-notes` to update both `en-US` and `zh-Hans` TestFlight notes.

#### Desktop release and auto-update

1. A maintainer pushes a `desktop-v<version>` tag (WindRex's approved desktop tag prefix, isolated from `server-v*`; `v<version>` also triggers the workflow if a unified tag is ever needed) or runs `.github/workflows/release-desktop.yml` via `workflow_dispatch` with `target: all|macos|windows` and optional `publish: true`.
2. The `validate` job checks that any pushed tag matches `mygo.json`'s `version` and exits early on a mismatch.
3. The `macos` job runs on `vars.WINDREX_DESKTOP_MACOS_RUNNER || 'macos-latest'`. When Developer ID secrets are set, it creates an ephemeral keychain in `$RUNNER_TEMP`, imports the `.p12`, stores notarization credentials (via App Store Connect API key or Apple ID), injects `macos.signingIdentity` and `macos.notarize` into the build-time `mygo.json`, and builds a signed, notarized `darwin/arm64` DMG with `go tool mygo build`. When secrets are absent, it builds an ad-hoc signed `.app` and DMG so maintainers can validate packaging before signing onboarding.
4. The `windows` job runs on `ubuntu-latest` with `nsis` and `osslsigncode`. When `WINDOWS_CERTIFICATE_PFX` is set, it decodes the certificate into `$RUNNER_TEMP` and signs the executable, uninstaller and NSIS setup installer via `go tool mygo build -platform windows/amd64`. When secrets are absent, it produces an unsigned NSIS installer.
5. When `mygo.json` contains `updates.publicKey` (or `vars.WINDREX_UPDATER_PUBLIC_KEY` is configured) and `secrets.MYGO_UPDATER_PRIVATE_KEY` is present, `go tool mygo build` embeds WindRex's update feed and Ed25519 public key into the binaries and writes signed `.tar.gz` archives, binary `.delta` patches (for the previous 3 published versions) and `update-<target>.json` manifests.
6. The `release` job downloads all built platform artifacts, generates and verifies `SHA256SUMS`, and on a tag push or `publish=true` creates or updates a draft GitHub Release pinned to `--target "$GITHUB_SHA"`. It uploads installers, archives, deltas and `SHA256SUMS` first, and uploads `update-*.json` manifests last so installed apps never read a manifest whose archive is missing.
7. When an installed WindRex desktop app installs an update, `main.go` runs `updater.OnInstalled`, which calls `st.upgradeServer()` so the background session server upgrades in place and keeps running shells alive.

### Model

#### iOS entitlements contract

`ios/scripts/verify-entitlements` enforces the following entitlements via `codesign -d --entitlements :-`:

| Bundle | Required entitlement | Expected value |
| --- | --- | --- |
| `GoRex.app` (`wiki.wind.windrex`) | `com.apple.security.application-groups` | Contains `group.wiki.wind.windrex` |
| `GoRex.app` (`wiki.wind.windrex`) | `com.apple.developer.ubiquity-kvstore-identifier` | `<TEAM_ID>.wiki.wind.windrex` (no `$(...)` variables) |
| `GoRex.app` (`wiki.wind.windrex`) | `aps-environment` | `development` or `production` |
| `GoRexWidgets.appex` (`wiki.wind.windrex.widgets`) | `com.apple.security.application-groups` | Contains `group.wiki.wind.windrex` |

#### iOS TestFlight secrets and variables

| Kind | Name | Required | Purpose |
| --- | --- | --- | --- |
| Secret | `ASC_KEY_P8` | Yes | App Store Connect API private key (`.p8`) for cloud signing and `asc` |
| Variable | `ASC_KEY_ID` | Yes | App Store Connect API key ID (10 characters) |
| Variable | `ASC_ISSUER_ID` | Yes | App Store Connect API issuer UUID |
| Variable | `WINDREX_ASC_APP_ID` | Yes | Numeric App Store Connect app ID for WindRex |
| Variable | `WINDREX_APPLE_TEAM_ID` | Yes | 10-character Apple Developer Team ID |
| Variable | `WINDREX_TESTFLIGHT_RUNNER` | No | Runner label override (default `xcode-27`) |
| Variable | `WINDREX_PUSH_RELAY_URL` | No | Live Activity push relay base URL embedded in `Info.plist` |

#### Desktop release secrets and variables

| Kind | Name | Required | Purpose |
| --- | --- | --- | --- |
| Secret | `MYGO_UPDATER_PRIVATE_KEY` | For auto-updates | Ed25519 private key from `go tool mygo keygen` |
| Variable | `WINDREX_UPDATER_PUBLIC_KEY` | For auto-updates (until committed in `mygo.json`) | Base64 Ed25519 public key matching `MYGO_UPDATER_PRIVATE_KEY` |
| Variable | `WINDREX_UPDATER_TAG_PREFIX` | No | Tag prefix override for updater feed (approved default: `desktop-v`) |
| Secret | `MACOS_DEVELOPER_ID_P12` | For macOS signing | Base64-encoded Developer ID Application `.p12` |
| Secret | `MACOS_DEVELOPER_ID_P12_PASSWORD` | For macOS signing | Password for `MACOS_DEVELOPER_ID_P12` |
| Variable | `MACOS_SIGNING_IDENTITY` | For macOS signing | Developer ID Application identity string |
| Secret | `ASC_KEY_P8` (plus `ASC_KEY_ID`, `ASC_ISSUER_ID` vars) | For macOS notarization (Option A) | Shared App Store Connect API key for `notarytool` |
| Secret | `APPLE_ID` and `APPLE_APP_PASSWORD` (plus `WINDREX_APPLE_TEAM_ID` var) | For macOS notarization (Option B) | Apple ID and app-specific password for `notarytool` |
| Variable | `WINDREX_DESKTOP_MACOS_RUNNER` | No | macOS runner label override (default `macos-latest`) |
| Secret | `WINDOWS_CERTIFICATE_PFX` | For Windows signing | Base64-encoded Authenticode `.pfx` certificate |
| Secret | `WINDOWS_CERTIFICATE_PASSWORD` | For Windows signing | Password for `WINDOWS_CERTIFICATE_PFX` |

#### Xcode Cloud readiness checklist (optional alternative)

If the Apple Developer account holder chooses Xcode Cloud instead of GitHub Actions for TestFlight, complete these three steps before onboarding the repository:

1. Add a `ci_scripts/ci_post_clone.sh` script that installs `xcodegen`, builds `ios/Frameworks/GhosttyVt.xcframework`, and runs `xcodegen generate` in `ios/`.
2. Confirm that `wiki.wind.windrex` and `wiki.wind.windrex.widgets` have App Group (`group.wiki.wind.windrex`), iCloud Key-Value Storage and Push Notifications enabled on the Apple Developer portal.
3. Update the App Store Connect initial version record from `1.0` to `1.1` to match `MARKETING_VERSION: "1.1"` in `ios/project.yml` before App Store submission.

#### Desktop auto-updater onboarding

1. Run `go tool mygo keygen` on a trusted machine to generate `mygo-update.key` (secret) and `mygo-update.pub` (public).
2. Store `mygo-update.key` in a password manager and upload it to GitHub Secrets as `MYGO_UPDATER_PRIVATE_KEY`.
3. Add the `updates` block (`github: "windzu/wind-rex"`, `publicKey: "<contents of mygo-update.pub>"`, and `tagPrefix: "desktop-v"`) to `mygo.json` (or set `WINDREX_UPDATER_PUBLIC_KEY` in repository variables while testing; `release-desktop.yml` already defaults `tagPrefix` to `desktop-v`). Do not commit an `updates` block without a real `publicKey` because `mygo` validates `updates.publicKey` on every `mygo dev` and `mygo build` invocation.
4. Bump `mygo.json` `version` monotonically above `0.56.1` (for example `0.57.0`) and add a matching `## <version>` section to `CHANGELOG.md` before tagging `desktop-v<version>` so `mygo build` populates the update dialog notes.

### Code

- `.github/workflows/testflight.yml`: Hardened TestFlight workflow supporting `workflow_dispatch` (`upload` or `export`) and `ios-v*` tag triggers, preflight secret validation, entitlement summary output and bilingual `WhatToTest` sync.
- `ios/scripts/verify-entitlements`: Standalone entitlement verifier using `codesign -d --entitlements :-` and `python3 -I` `plistlib` parsing for both `.app` bundles and `.ipa` archives.
- `ios/scripts/upload`: Calls `scripts/verify-entitlements` on the signed archive `.app` and on exported `build/export/GoRex.ipa` before upload.
- `.github/workflows/release-desktop.yml`: Dedicated desktop release workflow for `darwin/arm64` and `windows/amd64` with optional signing/notarization hooks, default `desktop-v` updater tag prefix, `SHA256SUMS` verification and draft GitHub Release publishing.
- `mygo.json`: Declares WindRex desktop metadata (`name`, `slug`, `identifier`, `publisher`, `copyright`, `macos.dmgTitle`, `windows.displayName`) while keeping `version` at `0.56.1` until the first WindRex desktop release bump (`0.57.0` or higher) once the updater keypair is provisioned; `updates` stays omitted until `updates.publicKey` exists because `mygo` validates it on every run.

## Alternatives

- **Reverting `ios/project.yml` `MARKETING_VERSION` from `"1.1"` to `"1.0"`**: Rejected because TestFlight already holds `1.1 (5)` and `1.1 (6)` builds and reverting would cause downgrade friction; aligning the unsubmitted App Store Connect record from `1.0` to `1.1` keeps existing builds consistent.
- **Using unified `v*` tags across desktop and server releases**: Rejected as the default because desktop and server ship on independent cadences; `desktop-v` isolates desktop auto-update feeds from `server-v*` releases.
- **Resetting `mygo.json` `version` below `0.56.1` (for example `0.1.0`)**: Rejected because existing `0.56.1` builds would not detect a lower semver version as an update.
- **Hardcoding `updates` with a placeholder public key or `tagPrefix`-only object in `mygo.json`**: Rejected because `mygo` validates `updates.publicKey` as a valid base64 Ed25519 key on every `go tool mygo dev` and `go tool mygo build` invocation.
- **Using a Windows runner for Windows desktop builds**: Rejected as the default because `mygo build` needs no cgo and `ubuntu-latest` with `nsis` and `osslsigncode` builds and signs Windows installers faster and cheaper. A Windows runner is only needed for hardware-token or cloud `signCommand` flows.
- **Replacing `xcodebuild -exportArchive` direct upload with `altool` for cloud signing**: Rejected because `ios/scripts/upload` relies on `xcodebuild`'s direct upload error matching to auto-increment colliding build numbers. Adding `destination=export` in `testflight.yml` and checking archive entitlements on every run covers both paths cleanly.

## Security

- Signing credentials (`ASC_KEY_P8`, `MACOS_DEVELOPER_ID_P12`, `WINDOWS_CERTIFICATE_PFX`, `MYGO_UPDATER_PRIVATE_KEY`) live exclusively in GitHub Secrets and are written to `$RUNNER_TEMP` under `umask 077`, then deleted in `if: always()` cleanup steps.
- `testflight.yml` runs only on `refs/heads/main` or `refs/tags/ios-v*` and holds only `contents: read` and `actions: read`.
- `release-desktop.yml` scopes `contents: write` exclusively to the `release` job; platform build jobs run with `contents: read`.
- No real Apple Team IDs, App Store Connect App IDs, certificate hashes, profile UUIDs or host names are committed to workflows, scripts or docs.

## Migration

Both irreversible release decisions from `docs/irreversible.md` are owner-approved (2026-11-26):

### Decision 1 (Approved): iOS first-submission `MARKETING_VERSION` stays `"1.1"`

- **Policy**: Keep `MARKETING_VERSION: "1.1"` in `ios/project.yml` and align App Store Connect's initial App Store record from `1.0` to `1.1`.
- **What it affects**: Matches existing TestFlight `1.1 (5)` and `1.1 (6)` builds cleanly with zero downgrade friction on test devices. WindRex iOS launches publicly at `1.1` and cannot submit a `1.0` version afterward.
- **How to check**: Confirm the App Store Connect version page and `ios/project.yml` both show `1.1`, confirm `CFBundleShortVersionString` in exported IPAs is `1.1`, and confirm `asc builds next-build-number` advances `CURRENT_PROJECT_VERSION` monotonically.
- **If it fails**: Because TestFlight version trains cannot be deleted once uploaded, advance to the next minor version rather than attempting to roll back.

### Decision 2 (Approved): Desktop release tag prefix is `desktop-v` with monotonic bumps above `0.56.1`

- **Policy**: Use `updates.tagPrefix: "desktop-v"` (already the default in `.github/workflows/release-desktop.yml`) and keep `mygo.json` at `"0.56.1"` until the first WindRex desktop release bump (for example `"0.57.0"`) once the Ed25519 updater signing keypair is provisioned.
- **What it affects**: `desktop-v*` isolates desktop auto-update feeds from `server-v*` releases in `.github/workflows/release-server.yml`, so headless server releases never overwrite the desktop updater feed. Monotonic bumps above `0.56.1` ensure existing `0.56.1` installs detect the first WindRex release as newer. `mygo.json` omits `updates` until `updates.publicKey` is generated so `go tool mygo dev` and `go tool mygo build` continue validating locally.
- **How to check**: When provisioning the updater keypair, commit `updates` (`github: "windzu/wind-rex"`, `publicKey: "<mygo-update.pub>"`, `tagPrefix: "desktop-v"`) in `mygo.json`, verify `update-darwin-arm64.json` and `update-windows-amd64.json` on the draft `desktop-v<version>` release, and test an upgrade from version `N` to `N+1` while a local session server keeps a running shell alive.
- **If it fails**: Because published release tags and Ed25519 updater keys cannot be silently replaced for installed clients, publish a higher semver patch rather than deleting or retagging a live release.

## Tests

- Workflow lint: `go run ./tools/verify` runs `actionlint` across `.github/workflows/*.yml` and validates `docs/DOC-001-design-docs.md` and `docs/REL-001-release-pipelines.md`.
- Shell syntax: `bash -n ios/scripts/upload ios/scripts/verify-entitlements`.
- Entitlement gate (pass and known-bad fail): run `ios/scripts/verify-entitlements` on a temporary ad-hoc signed dummy `.app` and `.ipa` with all required entitlements (expect exit 0) and on bundles missing App Group, iCloud KVS, Push or containing unexpanded `$(TeamIdentifierPrefix)` (expect non-zero exit).
- Desktop metadata validation: run `go test . -run TestWorkflowsTargetFork` and verify `mygo.json` parses cleanly with `go tool mygo build` / `jq`.
- End-to-end onboarding verification (after secrets are set): run `testflight.yml` with `destination=export` first, then `destination=upload`; run `release-desktop.yml` via `workflow_dispatch` (`publish=false`), then tag a draft `desktop-v<version>` release and verify upgrade preserves running terminal sessions (`docs/invariants.md`).

## Phases

1. Workflow hardening, entitlement verification script, desktop release pipeline and release documentation (#37).
2. Owner approval and documentation of iOS `1.1` initial submission track and desktop `desktop-v` monotonic versioning policy (this PR).
3. Provision Apple, Windows and Ed25519 signing secrets, commit `updates` (`tagPrefix: "desktop-v"` and `publicKey`) and bump `mygo.json` (for example `0.57.0`), then run the first live TestFlight upload and first signed desktop draft release with in-place server upgrade verification.

## Open questions

1. **TestFlight execution host**: GitHub Actions runner (`testflight.yml`) vs Xcode Cloud. Account holder decides when provisioning signing credentials.
