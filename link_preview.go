package main

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"gorex/internal/rex"
	"gorex/locales"
)

// linkDest is where a clicked terminal link goes (docs/DSK-011-link-open-preview.md).
type linkDest int

const (
	linkDestNone linkDest = iota
	// linkDestBrowser opens HTTP(S) web pages, online docs, image URLs, and
	// schemeless web links in the system default browser.
	linkDestBrowser
	// linkDestExternal hands off files (PDFs, folders, unsupported binaries,
	// or Option+Cmd bypass) and non-HTTP schemes to the system default app.
	linkDestExternal
	// linkDestPreview opens lightweight images, Markdown, and text/code/log
	// files inside WindRex's Side Preview panel.
	linkDestPreview
)

// previewKind identifies the renderer used inside WindRex's Side Preview.
type previewKind int

const (
	previewNone previewKind = iota
	previewImage
	previewMarkdown
	previewText
)

// previewState is the lifecycle state of a Side Preview panel.
type previewState int

const (
	previewLoading previewState = iota
	previewReady
	previewFallback
	previewError
)

const (
	// previewMaxImageBytes caps images decoded into Side Preview (20 MB).
	previewMaxImageBytes = 20 << 20
	// previewMaxTextBytes caps Markdown and plain-text files read into Side Preview (2 MB).
	previewMaxTextBytes = 2 << 20
	// previewMaxImageSide bounds bitmap decoding so huge photos stay light in memory.
	previewMaxImageSide = 2048
	// previewMinW and previewMaxW bound the Side Preview panel width.
	previewMinW = 320
	previewMaxW = 480
)

// linkPlan is the resolved routing decision for a single terminal link click.
type linkPlan struct {
	dest        linkDest
	previewKind previewKind
	url         string
	rawPath     string
	line        int
	remote      bool
	isFileURL   bool
}

// linkPreview holds the active Side Preview drawer state for a window.
type linkPreview struct {
	originPane *Pane
	hostLabel  string
	name       string
	// displayPath is shown in the header subtitle: the remote path for remote
	// files, or the resolved local path for local files.
	displayPath string
	// localPath is the local file on disk (resolved path for local files, or
	// downloaded path in Downloads for remote files). When non-empty, "Open in
	// Default App" hands it off via Services.OpenURL.
	localPath string
	line      int

	kind  previewKind
	state previewState

	// Loading state for remote downloads.
	transfer *fileTransfer

	// Ready state payloads.
	image    ui.ImageSource
	imgW     int
	imgH     int
	text     string
	fileSize int64
	empty    bool
	md       markdown.Options
	mdTheme  *markdown.Theme
	mdKey    themeKey

	// Fallback or error messaging.
	title  string
	detail string

	// wantFocus moves keyboard focus into the Side Preview on the next frame
	// so Esc dismisses it immediately.
	wantFocus bool
}

var imagePreviewExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".bmp":  true,
	".svg":  true,
}

var markdownPreviewExts = map[string]bool{
	".md":       true,
	".markdown": true,
	".mdx":      true,
}

var textPreviewExts = map[string]bool{
	".txt":          true,
	".log":          true,
	".json":         true,
	".jsonl":        true,
	".yaml":         true,
	".yml":          true,
	".toml":         true,
	".csv":          true,
	".tsv":          true,
	".xml":          true,
	".ini":          true,
	".conf":         true,
	".cfg":          true,
	".env":          true,
	".sh":           true,
	".bash":         true,
	".zsh":          true,
	".fish":         true,
	".go":           true,
	".mod":          true,
	".sum":          true,
	".ts":           true,
	".tsx":          true,
	".js":           true,
	".jsx":          true,
	".mjs":          true,
	".cjs":          true,
	".py":           true,
	".rs":           true,
	".swift":        true,
	".c":            true,
	".h":            true,
	".cc":           true,
	".cpp":          true,
	".hpp":          true,
	".java":         true,
	".kt":           true,
	".rb":           true,
	".php":          true,
	".sql":          true,
	".css":          true,
	".scss":         true,
	".html":         true,
	".htm":          true,
	".diff":         true,
	".patch":        true,
	".dockerfile":   true,
	".makefile":     true,
	".gitignore":    true,
	".editorconfig": true,
}

var textPreviewNames = map[string]bool{
	"makefile":   true,
	"dockerfile": true,
	"readme":     true,
	"license":    true,
	"changelog":  true,
}

// previewKindForPath reports whether path's extension or basename maps to a
// Side Preview renderer. PDFs, directories, and unsupported binaries return
// previewNone so they open in the system default application.
func previewKindForPath(path string) previewKind {
	if path == "" || strings.HasSuffix(path, "/") || strings.HasSuffix(path, `\`) {
		return previewNone
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case imagePreviewExts[ext]:
		return previewImage
	case markdownPreviewExts[ext]:
		return previewMarkdown
	case textPreviewExts[ext]:
		return previewText
	case ext == "":
		base := strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, "/")))
		if textPreviewNames[base] {
			return previewText
		}
	}
	return previewNone
}

// isUnsafeLocalPath reports whether path uses a Windows device, pipe, or
// redirector prefix that must never reach os.Stat or os.Open.
func isUnsafeLocalPath(path string) bool {
	if path == "" {
		return false
	}
	norm := strings.ReplaceAll(path, "/", `\`)
	for _, prefix := range []string{`\\.\`, `\\?\`, `\??\`, `\\;`} {
		if strings.HasPrefix(norm, prefix) {
			return true
		}
	}
	if _, ok := rex.VolumePath(path); !ok {
		return true
	}
	return false
}

// splitPathLine strips an optional :line or :line:col suffix from path and
// returns the cleaned path and 1-based line number (0 when absent).
func splitPathLine(path string) (string, int) {
	if m := linkLine.FindStringSubmatch(path); m != nil {
		line, _ := strconv.Atoi(strings.TrimPrefix(m[2], ":"))
		return m[1], line
	}
	return path, 0
}

// parseFileLink parses either a plain file path (with optional :line:col) or
// an OSC 8 file:// URI into a path, optional line number, and whether it came
// from a file:// URL.
func parseFileLink(link string) (path string, line int, isFileURL bool, ok bool) {
	if u, err := url.Parse(link); err == nil && strings.EqualFold(u.Scheme, "file") {
		p := u.Path
		if len(p) > 2 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		if p == "" {
			return "", 0, true, false
		}
		p, line = splitPathLine(p)
		return p, line, true, p != ""
	}
	if strings.Contains(link, "://") {
		return "", 0, false, false
	}
	p, line := splitPathLine(link)
	return p, line, false, p != ""
}

// classifyLink decides where a clicked terminal link should go according to
// the DSK-011 rule matrix.
func classifyLink(p *Pane, link string, forceExternal bool) linkPlan {
	link = strings.TrimSpace(link)
	if link == "" {
		return linkPlan{}
	}
	remote := p != nil && p.host != nil

	// 1. Explicit URL with scheme (http, https, file, mailto, etc.).
	if u, err := url.Parse(link); err == nil && u.Scheme != "" {
		scheme := strings.ToLower(u.Scheme)
		// Guard against Windows drive letters like C:\dir\file being parsed as a 1-letter scheme.
		if len(scheme) > 1 {
			switch scheme {
			case "http", "https":
				return linkPlan{dest: linkDestBrowser, url: link, remote: remote}
			case "file":
				raw, line, _, ok := parseFileLink(link)
				if !ok {
					return linkPlan{}
				}
				return classifyFilePlan(raw, line, remote, true, forceExternal)
			default:
				if strings.Contains(link, "://") || scheme == "mailto" {
					return linkPlan{dest: linkDestExternal, url: link, remote: remote}
				}
			}
		}
	}

	// 2. Schemeless web address (e.g. docs.example.com/guide).
	// On a local pane, an existing local file at the same relative path wins.
	if web, isWeb := webURL(link); isWeb {
		if remote {
			return linkPlan{dest: linkDestBrowser, url: web, remote: true}
		}
		if _, exists := panePath(p, link); !exists {
			return linkPlan{dest: linkDestBrowser, url: web, remote: false}
		}
	}

	// 3. Plain file path.
	raw, line, isFileURL, ok := parseFileLink(link)
	if !ok {
		return linkPlan{}
	}
	return classifyFilePlan(raw, line, remote, isFileURL, forceExternal)
}

func classifyFilePlan(raw string, line int, remote, isFileURL, forceExternal bool) linkPlan {
	kind := previewKindForPath(raw)
	dest := linkDestExternal
	if kind != previewNone && !forceExternal {
		dest = linkDestPreview
	}
	return linkPlan{
		dest:        dest,
		previewKind: kind,
		rawPath:     raw,
		line:        line,
		remote:      remote,
		isFileURL:   isFileURL,
	}
}

// openExternal opens u in the system default application (browser for HTTP(S),
// default viewer for file://).
func (a *App) openExternal(u string) {
	if u == "" {
		return
	}
	if a.openURLHook != nil {
		a.openURLHook(u)
		return
	}
	a.services.OpenURL(u)
}

// openLinkWith executes the DSK-011 rule matrix for link clicked in pane p.
// When forceExternal is true (Option+Cmd click), previewable files bypass
// Side Preview and hand off directly to the system default application.
func (a *App) openLinkWith(p *Pane, link string, forceExternal bool) bool {
	plan := classifyLink(p, link, forceExternal)
	switch plan.dest {
	case linkDestNone:
		return false
	case linkDestBrowser:
		a.openExternal(plan.url)
		return true
	case linkDestExternal:
		if plan.url != "" {
			a.openExternal(plan.url)
			return true
		}
	}

	// Remote file path or remote file:// hyperlink.
	if plan.remote {
		return a.openRemoteFileLink(p, plan)
	}

	// Local file path or local file:// hyperlink.
	return a.openLocalFileLink(p, plan)
}

func (a *App) openLocalFileLink(p *Pane, plan linkPlan) bool {
	if isUnsafeLocalPath(plan.rawPath) {
		a.showPreviewError(p, "", filepath.Base(plan.rawPath), plan.rawPath, plan.line,
			locales.PreviewUnsafePathTitle(), locales.PreviewUnsafePathDetail())
		return true
	}
	resolved, ok := resolvePath(p, plan.rawPath)
	if !ok {
		return false
	}
	if isUnsafeLocalPath(resolved) {
		a.showPreviewError(p, "", filepath.Base(resolved), resolved, plan.line,
			locales.PreviewUnsafePathTitle(), locales.PreviewUnsafePathDetail())
		return true
	}

	fi, err := os.Stat(resolved)
	if err != nil {
		title := locales.PreviewUnreadableTitle()
		if errors.Is(err, fs.ErrNotExist) {
			title = locales.PreviewNotFoundTitle()
		}
		a.showPreviewError(p, "", filepath.Base(resolved), resolved, plan.line, title, shortFSError(err))
		return true
	}

	if fi.IsDir() || plan.dest == linkDestExternal {
		a.openExternal(fileURL(resolved))
		return true
	}

	a.openLocalPreview(p, resolved, plan.previewKind, plan.line)
	return true
}

func (a *App) openRemoteFileLink(p *Pane, plan linkPlan) bool {
	hostLabel := p.host.label()
	remotePath := hostPath(p.info.Dir, plan.rawPath)
	name := pathBaseAny(remotePath)

	if !transfersFiles(p) {
		a.showPreviewError(p, hostLabel, name, remotePath, plan.line,
			locales.PreviewRemoteFailedTitle(), locales.PreviewRemoteUnsupported(hostLabel))
		return true
	}
	if p.host.current() == nil {
		a.showPreviewError(p, hostLabel, name, remotePath, plan.line,
			locales.PreviewRemoteFailedTitle(), locales.PreviewRemoteOffline(hostLabel))
		return true
	}
	return a.downloadWith(p, plan)
}

func pathBaseAny(path string) string {
	trimmed := strings.TrimRight(path, `/\`)
	if trimmed == "" {
		return path
	}
	return trimmed[strings.LastIndexAny(trimmed, `/\`)+1:]
}

func shortFSError(err error) string {
	if pe := (*fs.PathError)(nil); errors.As(err, &pe) && pe.Err != nil {
		return pe.Err.Error()
	}
	return err.Error()
}

// openLocalPreview opens the Side Preview panel for a local file and loads its
// content into memory subject to safe size caps.
func (a *App) openLocalPreview(p *Pane, path string, kind previewKind, line int) {
	pv := &linkPreview{
		originPane:  p,
		name:        filepath.Base(path),
		displayPath: path,
		localPath:   path,
		line:        line,
		kind:        kind,
		wantFocus:   true,
	}
	a.loadPreviewFile(pv, path)
	a.preview = pv
	a.focusReq = nil
	a.changed()
}

// openRemotePreview opens the Side Preview panel in loading state for an
// in-flight remote download and returns the preview handle so download
// completion can populate it if still active.
func (a *App) openRemotePreview(p *Pane, hostLabel, name, remotePath string, kind previewKind, line int, x *fileTransfer) *linkPreview {
	pv := &linkPreview{
		originPane:  p,
		hostLabel:   hostLabel,
		name:        name,
		displayPath: remotePath,
		line:        line,
		kind:        kind,
		state:       previewLoading,
		transfer:    x,
		wantFocus:   true,
	}
	a.preview = pv
	a.focusReq = nil
	a.changed()
	return pv
}

// showPreviewError opens the Side Preview panel with a clear error message.
func (a *App) showPreviewError(p *Pane, hostLabel, name, displayPath string, line int, title, detail string) {
	a.preview = &linkPreview{
		originPane:  p,
		hostLabel:   hostLabel,
		name:        cmp.Or(name, displayPath),
		displayPath: displayPath,
		line:        line,
		state:       previewError,
		title:       title,
		detail:      detail,
		wantFocus:   true,
	}
	a.focusReq = nil
	a.changed()
}

// loadPreviewFile reads localPath into pv, enforcing size caps, binary checks,
// and image decoding. It leaves pv in previewReady, previewFallback, or
// previewError state.
func (a *App) loadPreviewFile(pv *linkPreview, localPath string) {
	pv.localPath = localPath
	pv.transfer = nil
	fi, err := os.Stat(localPath)
	if err != nil {
		title := locales.PreviewUnreadableTitle()
		if errors.Is(err, fs.ErrNotExist) {
			title = locales.PreviewNotFoundTitle()
		}
		pv.state, pv.title, pv.detail = previewError, title, shortFSError(err)
		pv.localPath = ""
		return
	}
	pv.fileSize = fi.Size()

	switch pv.kind {
	case previewImage:
		if fi.Size() > previewMaxImageBytes {
			pv.state = previewFallback
			pv.title = locales.PreviewTooLargeTitle()
			pv.detail = locales.PreviewTooLargeDetail(byteSize(fi.Size()), byteSize(previewMaxImageBytes))
			return
		}
		data, err := os.ReadFile(localPath)
		if err != nil {
			pv.state, pv.title, pv.detail = previewError, locales.PreviewUnreadableTitle(), shortFSError(err)
			return
		}
		img, err := decodePreviewImage(data, localPath)
		if err != nil {
			pv.state, pv.title, pv.detail = previewFallback, locales.PreviewImageFailedTitle(), err.Error()
			return
		}
		w, h := ui.ImageSize(img)
		pv.image, pv.imgW, pv.imgH = img, int(w), int(h)
		pv.state = previewReady

	case previewMarkdown, previewText:
		if fi.Size() > previewMaxTextBytes {
			pv.state = previewFallback
			pv.title = locales.PreviewTooLargeTitle()
			pv.detail = locales.PreviewTooLargeDetail(byteSize(fi.Size()), byteSize(previewMaxTextBytes))
			return
		}
		data, err := os.ReadFile(localPath)
		if err != nil {
			pv.state, pv.title, pv.detail = previewError, locales.PreviewUnreadableTitle(), shortFSError(err)
			return
		}
		if len(data) == 0 {
			pv.empty = true
			pv.state = previewReady
			return
		}
		if looksBinary(data) {
			pv.state = previewFallback
			pv.title = locales.PreviewBinaryTitle()
			pv.detail = locales.PreviewBinaryDetail()
			return
		}
		pv.text = string(data)
		if pv.kind == previewMarkdown {
			pv.md.OnLink = func(dest string) {
				a.openPreviewMarkdownLink(pv, dest)
			}
			// Only resolve relative Markdown images on disk for local previews;
			// remote Markdown files live in ~/Downloads where sibling files are unrelated.
			if pv.hostLabel == "" {
				pv.md.LoadImage = markdown.FileImages(filepath.Dir(localPath))
			} else {
				pv.md.LoadImage = nil
			}
		}
		pv.state = previewReady

	default:
		pv.state = previewFallback
		pv.title = locales.PreviewBinaryTitle()
		pv.detail = locales.PreviewBinaryDetail()
	}
}

func decodePreviewImage(data []byte, path string) (ui.ImageSource, error) {
	if strings.EqualFold(filepath.Ext(path), ".svg") {
		svg, err := ui.ParseSVG(data)
		if err != nil {
			return nil, err
		}
		return svg, nil
	}
	bmp, err := ui.DecodeBitmapScaled(data, previewMaxImageSide)
	if err == nil {
		return bmp, nil
	}
	return ui.DecodeImage(data)
}

// looksBinary reports whether data looks like binary content rather than UTF-8 text.
func looksBinary(data []byte) bool {
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	return !utf8.Valid(data)
}

func (a *App) openPreviewMarkdownLink(pv *linkPreview, dest string) {
	dest = strings.TrimSpace(dest)
	if dest == "" || strings.HasPrefix(dest, "#") {
		return
	}
	if ui.SafeToOpen(dest) {
		a.openExternal(dest)
		return
	}
	// Reject any non-HTTP(S)/mailto URI scheme (including javascript:, data:, file:).
	if u, err := url.Parse(dest); err == nil && len(u.Scheme) > 1 {
		return
	}
	// Strip optional #anchor from a relative file path.
	dest, _, _ = strings.Cut(dest, "#")
	dest = strings.TrimSpace(dest)
	if pv == nil || dest == "" {
		return
	}
	if pv.originPane != nil && pv.originPane.host != nil {
		if target := resolveRemoteRelative(pv.displayPath, dest); target != "" {
			a.openLinkWith(pv.originPane, target, false)
		}
		return
	}
	if pv.localPath == "" {
		return
	}
	base := filepath.Dir(pv.localPath)
	rel := filepath.Join(base, filepath.FromSlash(dest))
	a.openLinkWith(pv.originPane, rel, false)
}

// resolveRemoteRelative resolves a relative Markdown link dest against the
// remote directory of displayPath without ever leaking a local Downloads path.
func resolveRemoteRelative(displayPath, dest string) string {
	if dest == "" {
		return ""
	}
	// Preserve absolute or home-relative remote links as-is.
	if strings.ContainsRune(`~/\`, rune(dest[0])) || (len(dest) > 1 && dest[1] == ':') {
		return dest
	}
	dir := remotePathDir(displayPath)
	if dir == "" {
		return dest
	}
	if strings.HasPrefix(dir, "/") {
		return path.Clean(dir + "/" + strings.ReplaceAll(dest, `\`, "/"))
	}
	return hostPath(dir, strings.ReplaceAll(dest, "/", `\`))
}

func remotePathDir(p string) string {
	trimmed := strings.TrimRight(p, `/\`)
	idx := strings.LastIndexAny(trimmed, `/\`)
	if idx < 0 {
		return ""
	}
	if idx == 0 {
		return trimmed[:1]
	}
	return trimmed[:idx]
}

// openPreviewExternal hands the currently previewed local file to the system
// default application.
func (a *App) openPreviewExternal() {
	if a.preview == nil || a.preview.localPath == "" {
		return
	}
	a.openExternal(fileURL(a.preview.localPath))
}

// closePreview dismisses the Side Preview panel and restores keyboard focus to
// the originating terminal pane (or the active tab's focused pane).
func (a *App) closePreview() {
	pv := a.preview
	if pv == nil {
		return
	}
	a.preview = nil
	target := pv.originPane
	t := a.tab()
	if target == nil || target.closed || (t != nil && target.Tab != t) {
		if t != nil {
			target = t.Focus
		} else {
			target = nil
		}
	}
	if target != nil && !target.closed {
		if target.Tab != nil {
			target.Tab.setFocus(target)
		}
		a.focusReq = target
	}
	a.changed()
}

// fitImageSize scales an image of natural dimensions (natW, natH) to fit
// within (maxW, maxH) while preserving aspect ratio and never enlarging.
func fitImageSize(natW, natH, maxW, maxH float32) (w, h float32) {
	if natW <= 0 || natH <= 0 || maxW <= 0 || maxH <= 0 {
		return 0, 0
	}
	scale := float32(1)
	if natW > maxW {
		scale = min(scale, maxW/natW)
	}
	if natH > maxH {
		scale = min(scale, maxH/natH)
	}
	return natW * scale, natH * scale
}

// previewPanelWidth computes a balanced Side Preview width that leaves at
// least panesMinW for the terminal panes beside it.
func previewPanelWidth(winW float32) float32 {
	if winW <= 0 {
		return 380
	}
	w := min(max(winW*0.38, previewMinW), previewMaxW)
	avail := winW - panesMinW - gap*3
	if avail < w {
		w = max(avail, 240)
	}
	return w
}

// previewMarkdownTheme returns the Markdown theme for Side Preview using only
// k.text and k.textMuted (never k.textFaint) so ink stays readable on glass.
func (pv *linkPreview) previewMarkdownTheme(k *colors) *markdown.Theme {
	key := themeKey{fontBody, k.text, k.textMuted, k.textMuted, k.accent, k.panelBorder}
	if pv.mdTheme != nil && pv.mdKey == key {
		return pv.mdTheme
	}
	th := &markdown.Theme{
		FontSize: fontBody, Text: k.text, Strong: k.text, Muted: k.textMuted, Link: k.accent,
		Code: k.text, CodeBackground: k.hover,
		QuoteBar: k.panelBorder, QuoteText: k.textMuted, Rule: k.panelBorder,
		CodeBlockBackground: k.hover, CodeBlockBorder: k.panelBorder,
		TableBorder: k.panelBorder, TableHeader: k.hover, Checkbox: k.accent,
	}
	for i := range th.Headings {
		th.Headings[i].Color = k.text
	}
	th.Headings[5].Color = k.textMuted
	pv.mdTheme, pv.mdKey = th, key
	return th
}

// linkPreviewPanel draws the right-hand Side Preview panel beside the panes.
func (a *App) linkPreviewPanel(c *ui.Context, k *colors) {
	pv := a.preview
	if pv == nil {
		return
	}
	winW, winH := c.Size()
	panelW := previewPanelWidth(winW)
	r := a.cardR()
	panel := ui.Column(c.Key("link-preview")).Width(panelW).Shrink(0).MinHeight(0).
		Radius(r).Border(1, k.panelBorder).Clip().
		Focusable().FocusRing(false).Role(ui.RoleRegion).Label(pv.name)
	glassPanel(panel, k)
	raisedShadow(panel, k)
	if pv.wantFocus {
		panel.Focus()
		pv.wantFocus = false
	}
	panel.OnShortcut(0, ui.KeyEscape, func() { a.closePreview() })

	panel.Children(func() {
		a.linkPreviewHeader(c, k, pv)
		a.linkPreviewBody(c, k, pv, panelW, winH)
	})
}

func (a *App) linkPreviewHeader(c *ui.Context, k *colors, pv *linkPreview) {
	ui.Column(c).Shrink(0).Padding(10, 12).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(k.panelBorder).Children(func() {
		// Top row: icon, file name, optional remote host / line badge, close button.
		ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
			glyphIcon(c, previewHeaderIcon(pv), 15, k.textMuted)
			ui.Text(c, pv.name).FontSize(fontCallout).FontWeight(600).TextColor(k.text).
				SingleLine().Ellipsis("…").Grow(1).MinWidth(0).Label(pv.name)
			if pv.hostLabel != "" {
				ui.Row(c).Padding(2, 6).Radius(radiusS).Background(k.hover).Shrink(0).Children(func() {
					ui.Text(c, pv.hostLabel).FontSize(fontCaption).FontWeight(500).TextColor(k.textMuted).
						SingleLine().Label(pv.hostLabel)
				})
			}
			if pv.line > 0 {
				badge := locales.PreviewLineBadge(pv.line)
				ui.Row(c).Padding(2, 6).Radius(radiusS).Background(k.hover).Shrink(0).Children(func() {
					ui.Text(c, badge).FontSize(fontCaption).FontWeight(500).TextColor(k.textMuted).
						SingleLine().Label(badge)
				})
			}
			closeBtn := iconButton(c, k, "x", locales.PreviewClose(), 26, 14).
				Tooltip(locales.PreviewClose() + "  Esc")
			if closeBtn.Clicked() {
				a.later(c, a.closePreview)
			}
		})
		// Second row: full path + prominent "Open in Default App" button.
		ui.Row(c).Gap(8).AlignItems(ui.Center).MinWidth(0).Children(func() {
			ui.Text(c, pv.displayPath).FontSize(fontFootnote).TextColor(k.textMuted).
				SingleLine().Ellipsis("…").Grow(1).MinWidth(0).Tooltip(pv.displayPath).Label(pv.displayPath)
			if pv.localPath != "" {
				if previewOpenButton(c, k).Clicked() {
					a.openPreviewExternal()
				}
			}
		})
	})
}

func previewHeaderIcon(pv *linkPreview) string {
	switch {
	case pv.state == previewError || pv.state == previewFallback:
		return "circle-alert"
	case pv.state == previewLoading:
		return "cloud-download"
	case pv.kind == previewMarkdown:
		return "sticky-note"
	case pv.kind == previewText:
		return "file-code"
	default:
		return "eye"
	}
}

// previewOpenButton draws the prominent "Open in Default App" button.
func previewOpenButton(c *ui.Context, k *colors) ui.Element {
	label := locales.PreviewOpenDefault()
	b := ui.Row(c.Key("preview-open-default")).Height(26).Padding(0, 10).Gap(6).
		Radius(radiusM).AlignItems(ui.Center).Shrink(0).
		Cursor(ui.CursorPointer).Role(ui.RoleButton).Label(label).Tooltip(label)
	bg := k.accent
	if b.Pressed() {
		bg = k.accentPressed
	} else if b.Hovered() {
		bg = k.accentHover
	}
	b.Background(bg)
	fg := accentText(c.Theme().Dark)
	b.Children(func() {
		glyphIcon(c, "square-arrow-out-up-right", 13, fg)
		ui.Text(c, label).FontSize(fontSubhead).FontWeight(500).TextColor(fg).SingleLine()
	})
	return b
}

func (a *App) linkPreviewBody(c *ui.Context, k *colors, pv *linkPreview, panelW, winH float32) {
	switch pv.state {
	case previewLoading:
		a.linkPreviewLoadingBody(c, k, pv)
	case previewError, previewFallback:
		a.linkPreviewStatusBody(c, k, pv)
	case previewReady:
		a.linkPreviewReadyBody(c, k, pv, panelW, winH)
	}
}

func (a *App) linkPreviewLoadingBody(c *ui.Context, k *colors, pv *linkPreview) {
	ui.Column(c).Grow(1).MinHeight(0).Padding(20).Gap(12).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		glyphIcon(c, "cloud-download", 24, k.textMuted)
		ui.Text(c, locales.PreviewDownloading(pv.hostLabel)).FontSize(fontBody).FontWeight(600).
			TextColor(k.text).TextAlign(ui.Center)
		if x := pv.transfer; x != nil {
			ui.Box(c).Width(220).Height(4).Radius(capsule(4)).Background(k.pressed).Draw(func(pt *ui.Painter, b ui.Rect) {
				if x.size > 0 {
					b.W *= float32(x.done) / float32(x.size)
					pt.Fill(b, k.accent, 2)
				}
			})
			progress := "…"
			if x.size > 0 {
				progress = locales.PaneTransferProgress(byteSize(x.done), byteSize(x.size))
			}
			ui.Text(c, progress).FontSize(fontFootnote).TextColor(k.textMuted)
			if x.cancel != nil && ui.Button(c.Key("preview-cancel"), locales.PaneTransferCancel()).Clicked() {
				x.cancel()
				a.later(c, a.closePreview)
			}
		}
	})
}

func (a *App) linkPreviewStatusBody(c *ui.Context, k *colors, pv *linkPreview) {
	ui.Column(c).Grow(1).MinHeight(0).Padding(20).Gap(10).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		iconCol := k.danger
		if pv.state == previewFallback {
			iconCol = k.attention
		}
		glyphIcon(c, "circle-alert", 24, iconCol)
		ui.Text(c, pv.title).FontSize(fontTitle).FontWeight(600).TextColor(k.text).TextAlign(ui.Center)
		if pv.detail != "" {
			ui.Text(c, pv.detail).FontSize(fontBody).TextColor(k.textMuted).TextAlign(ui.Center).LineHeight(1.35).MaxWidth(320)
		}
		if pv.localPath != "" {
			ui.Row(c).Margin(6, 0, 0, 0).Children(func() {
				if ui.PrimaryButton(c.Key("preview-status-open"), locales.PreviewOpenDefault()).Clicked() {
					a.openPreviewExternal()
				}
			})
		}
	})
}

func (a *App) linkPreviewReadyBody(c *ui.Context, k *colors, pv *linkPreview, panelW, winH float32) {
	if pv.empty {
		ui.Column(c).Grow(1).MinHeight(0).Padding(20).Gap(10).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.PreviewEmptyFile()).FontSize(fontBody).TextColor(k.textMuted).TextAlign(ui.Center)
		})
		return
	}

	sc := ui.Scroll(c.Key("preview-scroll")).Grow(1).MinHeight(0)
	sc.Children(func() {
		switch pv.kind {
		case previewImage:
			maxW := max(panelW-24, 160)
			maxH := max(winH-180, 180)
			w, h := fitImageSize(float32(pv.imgW), float32(pv.imgH), maxW, maxH)
			ui.Column(c).Padding(12).Gap(10).AlignItems(ui.Center).Children(func() {
				if pv.image != nil && w > 0 && h > 0 {
					ui.Image(c, pv.image).Size(w, h).Radius(radiusS).Label(pv.name)
				}
				meta := byteSize(pv.fileSize)
				if pv.imgW > 0 && pv.imgH > 0 {
					meta = fmt.Sprintf("%d × %d · %s", pv.imgW, pv.imgH, meta)
				}
				ui.Text(c, meta).FontSize(fontFootnote).TextColor(k.textMuted)
			})
		case previewMarkdown:
			pv.md.Theme = pv.previewMarkdownTheme(k)
			ui.Column(c).Padding(12, 14).Children(func() {
				markdown.View(c, pv.text, &pv.md)
			})
		case previewText:
			ui.Column(c).Padding(12, 14).Gap(8).Children(func() {
				ui.Text(c, pv.text).Font(monoFamily()).FontSize(fontSubhead).TextColor(k.text).LineHeight(1.4)
			})
		}
	})
}
