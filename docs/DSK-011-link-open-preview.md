---
name: DSK-011
description: "Defines the macOS terminal link opening and Side Preview rule matrix for HTTP(S) URLs, local paths, OSC 8 hyperlinks, and remote host downloads. Use when you change terminal link routing, Side Preview rendering, remote file download handoff, or preview focus restoration."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# DSK-011: macOS terminal link opening and side preview rules

Status: built (Refs #42).

This document defines predictable link opening and Side Preview rules for macOS terminal links in WindRex. It extends [`DSK-010`](DSK-010-trim-browser-and-web.md).

## Problem

Agents print web URLs, online documentation links, local paths, remote paths, and OSC 8 titled hyperlinks in terminal panes. On `main`, every `⌘`-clicked file path opens in a macOS external application (`find.go`, `openLink`, and `upload.go`, `download`), while `http(s)://` URLs fall through to the system default browser.

Users cannot tell which clicked link stays inside WindRex and which link switches focus to another application. In addition, local OSC 8 `file://` hyperlinks bypass `openLink` and hand off directly to the OS, and missing local files or offline remote hosts fail silently without feedback.

## Goal

- Publish one explicit rule matrix covering plain paths, URLs, and OSC 8 hyperlinks across local and remote panes.
- Keep all HTTP(S) links, including web pages, online docs, and direct image URLs, in the system default browser, consistent with [`DSK-010`](DSK-010-trim-browser-and-web.md).
- Preview lightweight local and remote images, Markdown files, and plain text, code, or log files inside a Side Preview panel in the current WindRex window.
- Hand off PDFs, folders, and unsupported binary formats directly to the macOS default application, and provide `⌥⌘`-click to bypass Side Preview for any file.
- Include the file name, source path (with remote host badge when remote), a prominent **Open in Default App** action, and **Close** (`Esc` or `×`) that restores keyboard focus to the original terminal pane.
- Show clear error and fallback states when a file is missing, unreadable, oversized, binary, blocked, or fails remote transfer.

## Non-goals

- Embedding a webview or fetching remote `http(s)://` image URLs inside WindRex. Embedded browser panes were removed in [`DSK-010`](DSK-010-trim-browser-and-web.md).
- Building a native PDF page renderer inside MyGo. PDFs open cleanly in the system default PDF app (Preview.app on macOS).
- Changing terminal inline image protocols (Kitty, Sixel, iTerm2). Inline terminal images render directly in the terminal grid and are separate from clicked links.
- Changing iOS or web client link handling.

## Design

### Behavior

1. **Trigger gestures**: On macOS, `⌘`-click (`Ctrl`-click on Linux and Windows) follows the default rule matrix below. Holding Option (`⌥⌘`-click) bypasses Side Preview and opens any local or downloaded remote file directly in the system default application.
2. **HTTP(S) links**: All `http://`, `https://`, and schemeless web links (such as `docs.example.com/guide`) open in the system default browser, whether they point to web pages, online documentation, or direct image URLs (such as `https://example.com/chart.png`). WindRex never performs background HTTP fetches or embeds a webview for clicked web links.
3. **Side Preview for previewable files**: Clicking a local or remote image, Markdown document, or plain text/code/log file opens a right-hand Side Preview panel beside the active tab's panes inside the same window. The terminal remains visible while the user reads the preview.
4. **Remote file preview flow**: On a remote host that supports file transfer, clicking a previewable file immediately opens Side Preview in a downloading state with the remote host badge, remote path, live progress bar, and Cancel button. The file downloads to `~/Downloads`, receives the macOS `com.apple.quarantine` attribute (`quarantine`), and then renders inside Side Preview.
5. **External handoff and non-previewable formats**: PDFs, folders, and unsupported binary formats bypass Side Preview and open in the macOS default application (Preview.app for PDFs, Finder for local folders). Inside Side Preview, clicking **Open in Default App** hands off the local or downloaded file to the system default application via `Services.OpenURL` (`mygo.Shell.OpenExternal` / `NSWorkspace`).
6. **Focus restoration**: Opening Side Preview moves keyboard focus to the preview panel so `Esc` closes it immediately. Clicking the `×` button or pressing `Esc` closes Side Preview and sets `App.focusReq` back to the originating terminal pane (`originPane`), restoring terminal typing without an extra click. If the user clicks back into the terminal while Side Preview stays open, `Esc` goes to the terminal program instead of closing the preview.
7. **Error and fallback states**: Missing or unreadable local files, blocked device/pipe paths, oversized previewable files, binary files masquerading with text extensions, corrupt images, offline remote hosts, and remote transfer failures all display a clear message in Side Preview. When a readable file exists on disk (oversized, binary, or image decode error), Side Preview offers **Open in Default App**.

### Rule Matrix

| Link or Content Type | Plain or OSC 8 | Local Session (`⌘`-click) | Remote Session (`⌘`-click) | Bypass (`⌥⌘`-click) |
| --- | --- | --- | --- | --- |
| Web pages and online docs (`http://`, `https://`, schemeless host/path) | Both | System default browser | System default browser | System default browser |
| HTTP(S) direct image URLs (`https://example.com/a.png`) | Both | System default browser | System default browser | System default browser |
| Other external URL schemes (`mailto:`, etc.) | Both | System default handler | System default handler | System default handler |
| Images (`.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`, `.bmp`, `.svg`) up to 20 MB | Plain path or `file://` | WindRex Side Preview (`ui.Image`, aspect ratio preserved) | Download to `~/Downloads` (quarantined), then WindRex Side Preview | Local: Preview.app / default app. Remote: download, then default app |
| Markdown (`.md`, `.markdown`, `.mdx`) up to 2 MB | Plain path or `file://` | WindRex Side Preview (`markdown.View`) | Download to `~/Downloads` (quarantined), then WindRex Side Preview | Local: default app. Remote: download, then default app |
| Plain text, code, config, logs (`.txt`, `.log`, `.json`, `.yaml`, `.yml`, `.toml`, `.csv`, `.go`, `.ts`, `.py`, `.rs`, `.swift`, `.diff`, etc.) up to 2 MB | Plain path or `file://` | WindRex Side Preview (scrollable monospace view, optional `:line` badge) | Download to `~/Downloads` (quarantined), then WindRex Side Preview | Local: default app. Remote: download, then default app |
| PDF documents (`.pdf`) | Plain path or `file://` | System default app (macOS Preview.app) | Download to `~/Downloads` (quarantined), then system default app | Same (system default app) |
| Folders / directories | Plain path or `file://` | System default app (Finder) | Transfer list error (`Folders are not downloaded yet`) | Local: Finder. Remote: error |
| Unsupported binary formats (`.zip`, `.dmg`, `.docx`, `.mp4`, etc.) | Plain path or `file://` | System default app | Download to `~/Downloads` (quarantined), then system default app | Same (system default app) |
| Oversized previewable files (> 20 MB image or > 2 MB text/Markdown) | Plain path or `file://` | WindRex Side Preview size-limit state with **Open in Default App** | Download to `~/Downloads`, then Side Preview size-limit state with **Open in Default App** | System default app directly |
| Missing or unreadable local file | Plain path or `file://` | WindRex Side Preview error state with path and reason | Not applicable | Same error state |
| Remote host offline, unsupported, or transfer failed | Plain path or `file://` | Not applicable | WindRex Side Preview error state naming host and cause | Same error state |

### Model

- `linkDest` classifies every clicked link into one of four outcomes: `linkDestNone`, `linkDestBrowser`, `linkDestExternal`, or `linkDestPreview`.
- `previewKind` classifies previewable files into `previewImage`, `previewMarkdown`, or `previewText`.
- `classifyLink` (`link_preview.go`) parses explicit URLs (`http`, `https`, `file`, and other schemes), schemeless web paths (`webURL`), and plain file paths with optional `:line:col` suffixes (`parseFileLink`). On local panes, an existing local file takes precedence over a schemeless web path, preserving existing `find.go` behavior.
- `linkPreview` (`link_preview.go`) holds the active window's Side Preview state: originating pane (`originPane`), display name and path, optional remote host label and line number, local file path once available, state (`previewLoading`, `previewReady`, `previewFallback`, `previewError`), decoded `ui.ImageSource` (scaled to at most 2048 px per side via `ui.DecodeBitmapScaled` or `ui.ParseSVG`), text content, `markdown.Options`, and focus request flag (`wantFocus`).
- Safe caps bound memory and UI work: `previewMaxImageBytes` is 20 MB and `previewMaxTextBytes` is 2 MB. Text files containing NUL bytes (`0x00`) or invalid binary sequences switch to the fallback state with **Open in Default App**.

### Code

- `link_preview.go`: Implements `classifyLink`, `parseFileLink`, `previewKindForPath`, `isUnsafeLocalPath`, `fitImageSize`, `App.openLinkWith`, `App.openLocalPreview`, `App.openRemotePreview`, `App.loadPreviewFile`, `App.closePreview`, `App.openPreviewExternal`, and `App.linkPreviewPanel`.
- `find.go`: Routes `App.openLink` to `App.openLinkWith(p, link, a.mods&ui.Alt != 0)` and guards `isLink` with `isUnsafeLocalPath`.
- `upload.go`: Extends remote download via `App.downloadWith(p, plan)` so previewable remote files open Side Preview immediately in the loading state and populate it on completion (checking `a.preview == pv` so a stale download never overwrites a newer preview or reopens a closed preview). Non-previewable files and `⌥⌘`-clicks continue opening externally upon completion.
- `app.go`: Adds `mods ui.Modifiers` and `preview *linkPreview` to `App`.
- `view.go`: Captures `a.mods = c.Modifiers()` per frame and places `App.linkPreviewPanel` beside the active tab's panes inside `App.panes` so both top-tab and sidebar layouts share one drawer placement.
- `locales/en.json`, `locales/zh-Hans.json`, `locales/catalog_gen.go`: Adds localized Side Preview labels and error messages.

## Alternatives

- **Always open files in external apps and add a separate preview shortcut**: Rejected because Agent outputs frequently contain Markdown reports, screenshots, diffs, and logs where context-switching out of WindRex breaks flow. Previewing safe formats in-window by default while offering one-click **Open in Default App** and `⌥⌘`-click external bypass gives fast in-context inspection without trapping the user.
- **Render PDFs via an embedded webview or `qlmanage -p`**: Rejected because embedded webviews were removed in [`DSK-010`](DSK-010-trim-browser-and-web.md), and `qlmanage -p` is a macOS debugging tool rather than a stable user viewer. Opening PDFs in the system default app (Preview.app) is reliable, native, and matches macOS user expectations.
- **Fetch `http(s)://` image URLs inside Side Preview**: Rejected because remote URLs may require browser cookies, authentication, or redirects, and fetching arbitrary URLs from terminal output introduces SSRF and privacy risks. Routing all HTTP(S) links to the default browser keeps the rule simple and safe.

## Security

- **Untrusted device and pipe paths**: `isUnsafeLocalPath` rejects Windows device, pipe, and redirector forms (`\\.\`, `\\?\`, `//./`, `//?/`, `\??\`, `\\;`, `//;`) and checks `rex.VolumePath` before any `os.Stat` or `os.ReadFile` call.
- **Remote downloads keep macOS quarantine**: Every file downloaded from a remote host still runs through `quarantine` before Side Preview loads it or hands it off to an external app.
- **Markdown preview link safety**: Links clicked inside a Markdown Side Preview only open `http`, `https`, and `mailto` URLs externally (`ui.SafeToOpen`), or resolve relative local files back through `openLinkWith`, blocking arbitrary schemes such as `javascript:`.
- **Bounded reads**: Side Preview checks file size before reading and caps image reads at 20 MB (decoding bitmaps scaled to 2048 px max side) and text/Markdown reads at 2 MB.

## Tests

- **Rule classification and safety** (`link_preview_test.go`): Unit tests for `classifyLink`, `parseFileLink`, `previewKindForPath`, `isUnsafeLocalPath`, and `fitImageSize` across HTTP(S) web/doc/image URLs, schemeless web links, plain and OSC 8 `file://` local and remote paths, `:line:col` suffixes, PDFs, folders, binaries, `⌥⌘`-click bypass, and unsafe device/pipe paths.
- **Local and remote preview flows** (`link_preview_test.go`, `find_test.go`, `upload_test.go`): Tests covering local image, Markdown, text, oversized, binary, empty, and missing files; remote previewable download + quarantine + Side Preview render; remote PDF/binary download + external handoff; remote offline, unsupported host, and transfer error states; and `⌥⌘`-click external bypass.
- **UI interaction and focus restoration** (`link_preview_test.go`): Headless `ui.NewTester` test opening Side Preview from a terminal pane, verifying filename, path, and content render, clicking **Open in Default App** (confirming `OpenedURLs`), closing via `Esc` and via the `×` button, and verifying keyboard focus returns to the originating terminal pane so typed input reaches the shell.
- **Design system and catalog invariants** (`docs/invariants.md` item 9): `TestDesignTokens`, `TestGlassInk`, `TestCatalogsAgree`, and `TestNoThreeDots`.
