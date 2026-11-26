---
name: DSK-010
description: "Removes the desktop embedded browser pane with backward-compatible legacy layout migration, and converges the web client to a lean local or self-hosted static fallback. Use when you change legacy layout loading, desktop pane types, or web client hosting and packaging."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# DSK-010: Remove desktop embedded browser and converge web client

Status: phase 1 built (Refs #13). Phase 2 (iOS bundled ASR removal and Hotspot Share review) not built.

This document defines the desktop and web scope of Issue #13. It extends [`DSK-002`](DSK-002-pane-drop-zones.md), [`DSK-006`](DSK-006-empty-states.md) and [`REF-007`](REF-007-web-client.md).

## Problem

WindRex focuses on managing, connecting and resuming terminal sessions across desktop and iOS. The repository also carried two extra stacks inherited from upstream GoRex:

- **Desktop embedded browser panes** (`browser.go`, `browserHeader`, `browserBody`, `cmdNewBrowser`, `cmdSplitBrowser`, `canvasMenu`, `debug.go`, `locales/*.json`): maintain a native `mygo.WebView` host, custom navigation bar, URL policy, focus-hover heuristics and layout sync for a feature outside the core terminal workflow. External web links already open in the system browser through `mygo.Shell.OpenExternal`.
- **Hosted web deployment and PWA app shell** (`web/wrangler.jsonc`, `web/r2-upload.mjs`, `.github/workflows/web.yml`, `ci.yml` `web-r2` job, `web/sw.js`, `web/manifest.webmanifest`, `web/src/app.ts`): maintain Cloudflare Workers and R2 upload pipelines, Service Worker precaching, install prompts and update banners even though WindRex runs no public hosted web service.

## Goal

- Remove all desktop UI, command palette, canvas menu, debug hook, icon and locale entries for embedded browser panes.
- Restore every surviving terminal and note pane, split tree and floating pane from legacy saved layouts that still contain browser nodes (`Node.URL != ""`).
- Retire Cloudflare Workers deployment, R2 snapshot uploads, PWA installation UI and Service Worker complexity from `web/` while keeping `web/` working as a lean local or self-hosted static browser terminal fallback.
- Document the remaining `web/` couplings with `ios/` so Wave 2 can evaluate iOS Hotspot Share without breaking `ios/**` in this wave.

## Non-goals

- Removing iOS bundled speech recognition models (`SenseVoice`, `Fun-ASR-Nano`, `Qwen3-ASR`, `FluidAudio`, `SherpaOnnx`) or iOS Hotspot Share (`ios/share`, `ios/GoRex/Share/*`). That work belongs to Wave 2 on `ios/**`.
- Removing `Node.URL` from `internal/layout/layout.go` or changing `internal/rex` / `internal/rexapi` wire schemas. Saved journals (`layout.jsonl`) and mixed-version clients must keep parsing existing records cleanly (`docs/irreversible.md`).
- Removing opt-in `WINDREX_WEB_ORIGIN` LAN pairing (`hostqr.go`, `internal/rexapi/web.go`). It defaults to empty and stays hidden unless a self-hoster sets `WINDREX_WEB_ORIGIN`.

## Design

### Behavior

1. **Desktop panes**: Desktop tabs and splits host terminal panes and Markdown note panes only. **New Browser Tab**, **Split with Browser** and the canvas menu's **New Browser** item no longer appear. External links continue opening in the system browser.
2. **Legacy layout recovery**: When the desktop opens or reconciles a saved layout containing legacy browser panes (`URL != ""`), those browser nodes are silently omitted instead of creating broken empty terminals. Any sibling terminal or note pane in the same split expands into the split's slot, floating terminals survive (and promote if the tab's only tiled pane was a browser), browser-only tabs are skipped, and a workspace whose tabs were all browser-only falls back to a fresh default terminal tab.
3. **Converged web client**: `web/` remains a standalone multi-host browser terminal (`libghostty-vt` WASM) that developers or self-hosters run locally (`npm run dev`) or build statically (`npm run build`). It no longer advertises PWA app installation, Service Worker offline caching or in-app update checks, and it no longer carries Cloudflare Workers or R2 deployment scripts or workflows.

### Web Surface Inventory

| Area | Decision in Phase 1 | Reason |
| --- | --- | --- |
| Core browser terminal (`web/src/`, `libghostty-vt` WASM, themes, split panes, transports) | Keep | Useful lightweight fallback for local or self-hosted browser access with no local daemon. |
| Cloudflare Workers & R2 deploy (`web/wrangler.jsonc`, `web/r2-upload.mjs`, `npm run deploy`, `.github/workflows/web.yml`, `ci.yml` `web-r2`) | Remove | WindRex publishes no hosted Cloudflare Worker or R2 web mirror. |
| PWA install & Service Worker (`web/sw.js`, `web/manifest.webmanifest`, `web/src/app.ts`, About install/update rows) | Remove | Native desktop and iOS apps are the installed clients; removing the SW and PWA shell eliminates caching edge cases and update-banner state. Tailnet downloads use the existing Blob download path (`saveBlob`). |
| Hotspot Share hooks (`web/src/share.ts`, `files.json` in `web/build.mjs`) | Keep for now | Still consumed by `ios/share` and `ios/GoRex/Share/WebClientStore.swift`. Re-evaluate in Wave 2 when `ios/**` is in scope. |
| Opt-in `WINDREX_WEB_ORIGIN` pairing (`hostqr.go`, `internal/rexapi/web.go`) | Keep | Empty by default (zero UI clutter); still allows self-hosted web pairing when configured. |

### Model and Code

- **Desktop removal**: Delete `browser.go` and `browser_test.go`. Remove `Pane.browser`, `App.pageHover`, `browserProgram`, `cmdNewBrowser`, `cmdSplitBrowser`, `browserHeader`, `browserBody`, `addressKey`, canvas `"browser"` creation, `url`/`webshot` debug commands and unused browser locale keys from `state.go`, `view.go`, `app.go`, `commands.go`, `canvas.go`, `closeconfirm.go`, `teleport.go`, `workspace.go`, `programs.go`, `sidebar.go`, `debug.go`, `locales/en.json`, `locales/zh-Hans.json` and generated `locales/catalog_gen.go`.
- **Legacy migration hook**: In `App.reconcile` (`state.go`), keep an explicit `case s.URL != "": return nil` inside `load` before the `default:` terminal fallback so legacy browser nodes never attach as empty sessions. Existing `load` split-collapse, float-skip, `promoteFloat` and empty-tab checks handle every tree shape cleanly.
- **Web convergence**: Delete `web/wrangler.jsonc`, `web/r2-upload.mjs`, `web/sw.js`, `web/manifest.webmanifest`, `web/src/app.ts` and `.github/workflows/web.yml`. Remove the `web-r2` job from `.github/workflows/ci.yml` and the `deploy` script from `web/package.json`. Remove Service Worker and PWA wiring from `web/build.mjs`, `web/dev.mjs`, `web/_headers`, `web/index.html`, `web/src/main.ts`, `web/src/files.ts`, `web/src/settings.ts`, `web/src/glass.ts` and `web/locales/*.json`.

## Alternatives

- **Delete `web/` completely in Phase 1**: Rejected because `ios/share` and `ios/GoRex/Share/WebClientStore.swift` still depend on `web/dist` and `files.json`, and `ios/**` is out of scope for Wave 1. Keeping `web/` as a lean static fallback preserves self-hosted browser access while cutting deployment and PWA overhead immediately.
- **Remove `Node.URL` from `internal/layout`**: Rejected under `docs/irreversible.md` because `layout.jsonl` files and older clients still carry `url` fields on disk and over the wire.

## Migration

Existing `layout.jsonl` files and older servers require no manual migration:

- `internal/layout.Parse` still decodes legacy `"url"` fields without error.
- Desktop `App.reconcile` drops `s.URL != ""` leaves on load, preserving every terminal session, note pane, split and float in the same tab or workspace.
- On the next layout save, the desktop writes the cleaned layout without browser panes.
- No `settings.json` keys were added or removed by this change.

## Tests

- **Legacy layout recovery** (`TestLegacyBrowserLayoutMigration` in `reconcile_test.go`, covering `docs/invariants.md` items 1 and 8): loads saved layouts with tiled terminal+browser splits, nested browser splits, floating browser panes beside floating notes, tiled browser + floating terminal promotion, browser-only tabs beside terminal tabs, and browser-only workspaces; verifies surviving terminal sessions attach and restore cleanly.
- **Desktop UI & catalog rules**: `TestDesignTokens`, `TestGlassInk`, `TestCatalogsAgree`, `TestNoThreeDots`, `TestEmptyNoTab`, `TestCanvasMenuPlacesBox`, `TestRestoreFocusesSavedFloat`.
- **Web client**: `cd web && npm run typecheck && npm test` verifies TypeScript types, i18n catalogs (`web/i18n.mjs`) and all unit tests after retiring `app.ts` and PWA/SW code.
- **Pre-push verification**: `go run ./tools/verify` checks `gofmt`, design docs, `actionlint`, `go vet` and cross-builds for `linux`, `windows`, `darwin`, `golangci-lint`, and web typecheck/tests.

## Phases

1. **Desktop embedded browser removal and web hosting/PWA convergence**: this PR (Refs #13).
2. **iOS bundled ASR removal and Hotspot Share evaluation**: follow-up PR on `ios/**` after Swift 6 warning cleanup lands.
