package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"gorex/internal/rex"
	"gorex/locales"
)

func testPNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: 46, G: 111, B: 208, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestLinkPreviewMatrix checks every category of the DSK-011 rule matrix:
// HTTP(S) web/doc/image URLs, schemeless web links, other URL schemes,
// local & remote plain paths and OSC 8 file:// URIs, previewable vs external
// formats, and Option+Cmd bypass.
func TestLinkPreviewMatrix(t *testing.T) {
	dir := t.TempDir()
	localFile := filepath.Join(dir, "docs.example.com", "guide.md")
	if err := os.MkdirAll(filepath.Dir(localFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localFile, []byte("# local"), 0o600); err != nil {
		t.Fatal(err)
	}

	localPane := &Pane{info: rex.SessionInfo{Dir: dir}}
	remotePane := &Pane{info: rex.SessionInfo{Dir: "/srv/app"}, host: newHostConn(savedHost{ID: "h1", Name: "office-mac"})}

	cases := []struct {
		name          string
		pane          *Pane
		link          string
		forceExternal bool
		wantDest      linkDest
		wantKind      previewKind
		wantURL       string
		wantLine      int
	}{
		// HTTP(S) always goes to the default browser (web pages, docs, direct image URLs).
		{name: "https webpage local", pane: localPane, link: "https://example.com/overview", wantDest: linkDestBrowser, wantURL: "https://example.com/overview"},
		{name: "https online doc remote", pane: remotePane, link: "https://docs.example.com/guide.md", wantDest: linkDestBrowser, wantURL: "https://docs.example.com/guide.md"},
		{name: "https image url local", pane: localPane, link: "https://cdn.example.com/diagram.png", wantDest: linkDestBrowser, wantURL: "https://cdn.example.com/diagram.png"},
		{name: "http image url remote", pane: remotePane, link: "http://example.com/chart.webp", wantDest: linkDestBrowser, wantURL: "http://example.com/chart.webp"},
		{name: "schemeless web remote", pane: remotePane, link: "docs.example.com/guide.md", wantDest: linkDestBrowser, wantURL: "https://docs.example.com/guide.md"},
		{name: "schemeless web local missing", pane: localPane, link: "support.github.com/tickets", wantDest: linkDestBrowser, wantURL: "https://support.github.com/tickets"},
		// Local file wins over schemeless web address when the file exists locally.
		{name: "local file beats schemeless web", pane: localPane, link: "docs.example.com/guide.md", wantDest: linkDestPreview, wantKind: previewMarkdown},
		// Other external schemes.
		{name: "mailto link", pane: localPane, link: "mailto:alice@example.com", wantDest: linkDestExternal, wantURL: "mailto:alice@example.com"},
		// Images: plain & OSC 8 file://.
		{name: "local png", pane: localPane, link: "./shot.png", wantDest: linkDestPreview, wantKind: previewImage},
		{name: "local jpeg uppercase", pane: localPane, link: "PHOTO.JPG", wantDest: linkDestPreview, wantKind: previewImage},
		{name: "remote webp", pane: remotePane, link: "out/fig.webp", wantDest: linkDestPreview, wantKind: previewImage},
		{name: "osc8 file svg", pane: localPane, link: "file:///tmp/diagram.svg", wantDest: linkDestPreview, wantKind: previewImage},
		{name: "local png bypass", pane: localPane, link: "./shot.png", forceExternal: true, wantDest: linkDestExternal, wantKind: previewImage},
		// Markdown: plain & OSC 8 file://.
		{name: "local md", pane: localPane, link: "./README.md", wantDest: linkDestPreview, wantKind: previewMarkdown},
		{name: "remote osc8 md", pane: remotePane, link: "file://office-mac/srv/app/spec.markdown", wantDest: linkDestPreview, wantKind: previewMarkdown},
		{name: "local md bypass", pane: localPane, link: "./README.md", forceExternal: true, wantDest: linkDestExternal, wantKind: previewMarkdown},
		// Text / code / config / logs with :line:col.
		{name: "local go with line col", pane: localPane, link: "src/main.go:42:7", wantDest: linkDestPreview, wantKind: previewText, wantLine: 42},
		{name: "local json", pane: localPane, link: "config.json", wantDest: linkDestPreview, wantKind: previewText},
		{name: "remote log", pane: remotePane, link: "logs/build.log:108", wantDest: linkDestPreview, wantKind: previewText, wantLine: 108},
		{name: "extensionless Makefile", pane: localPane, link: "Makefile", wantDest: linkDestPreview, wantKind: previewText},
		// PDFs, folders, and unsupported binaries -> external default app.
		{name: "local pdf", pane: localPane, link: "./report.pdf", wantDest: linkDestExternal, wantKind: previewNone},
		{name: "osc8 remote pdf", pane: remotePane, link: "file://office-mac/srv/app/report.pdf", wantDest: linkDestExternal, wantKind: previewNone},
		{name: "local folder", pane: localPane, link: "docs/", wantDest: linkDestExternal, wantKind: previewNone},
		{name: "local zip", pane: localPane, link: "archive.zip", wantDest: linkDestExternal, wantKind: previewNone},
		{name: "remote docx", pane: remotePane, link: "proposal.docx", wantDest: linkDestExternal, wantKind: previewNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyLink(tc.pane, tc.link, tc.forceExternal)
			if got.dest != tc.wantDest {
				t.Errorf("dest = %v, want %v (plan=%+v)", got.dest, tc.wantDest, got)
			}
			if got.previewKind != tc.wantKind {
				t.Errorf("previewKind = %v, want %v", got.previewKind, tc.wantKind)
			}
			if tc.wantURL != "" && got.url != tc.wantURL {
				t.Errorf("url = %q, want %q", got.url, tc.wantURL)
			}
			if tc.wantLine != 0 && got.line != tc.wantLine {
				t.Errorf("line = %d, want %d", got.line, tc.wantLine)
			}
		})
	}
}

// TestLinkPreviewUnsafePaths verifies that Windows device, pipe, and redirector
// paths are refused before touching the filesystem (docs/lessons.md).
func TestLinkPreviewUnsafePaths(t *testing.T) {
	bad := []string{
		`\\.\pipe\rex`,
		`\\?\C:\secret.txt`,
		`//./pipe/rex`,
		`//?/C:/secret.txt`,
		`\??\C:\secret.txt`,
		`\\;LanmanRedirector\host\share\a.txt`,
		`//;LanmanRedirector/host/share/a.txt`,
	}
	var opened []string
	a := &App{openURLHook: func(u string) { opened = append(opened, u) }}
	p := &Pane{info: rex.SessionInfo{Dir: t.TempDir()}}
	for _, path := range bad {
		if !isUnsafeLocalPath(path) {
			t.Errorf("isUnsafeLocalPath(%q) = false, want true", path)
		}
		a.preview = nil
		if !a.openLinkWith(p, path, false) {
			t.Errorf("openLinkWith(%q) = false, want true (blocked error state)", path)
		}
		if a.preview == nil || a.preview.state != previewError || a.preview.title != locales.PreviewUnsafePathTitle() {
			t.Errorf("openLinkWith(%q) preview = %+v, want blocked error state", path, a.preview)
		}
	}
	if len(opened) != 0 {
		t.Errorf("unsafe paths opened externally: %q", opened)
	}
}

func TestLinkPreviewFitImageSize(t *testing.T) {
	w, h := fitImageSize(1600, 800, 400, 400)
	if w != 400 || h != 200 {
		t.Errorf("wide fit = (%v, %v), want (400, 200)", w, h)
	}
	w, h = fitImageSize(400, 1200, 400, 300)
	if w != 100 || h != 300 {
		t.Errorf("tall fit = (%v, %v), want (100, 300)", w, h)
	}
	w, h = fitImageSize(120, 80, 400, 400)
	if w != 120 || h != 80 {
		t.Errorf("small image enlarged to (%v, %v), want (120, 80)", w, h)
	}
}

// TestLinkPreviewLocalFlow exercises local link opening across images,
// Markdown, plain text, OSC 8 file:// links, PDFs, binaries, folders,
// oversized/binary/corrupt/empty/missing files, and Option+Cmd bypass.
func TestLinkPreviewLocalFlow(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "diagram.png")
	mdPath := filepath.Join(dir, "my report.md")
	txtPath := filepath.Join(dir, "app.go")
	emptyPath := filepath.Join(dir, "empty.txt")
	bigPath := filepath.Join(dir, "huge.log")
	binPath := filepath.Join(dir, "fake.log")
	badPNGPath := filepath.Join(dir, "broken.png")
	pdfPath := filepath.Join(dir, "spec.pdf")
	zipPath := filepath.Join(dir, "bundle.zip")
	subDir := filepath.Join(dir, "docs")

	if err := os.WriteFile(pngPath, testPNGBytes(t, 64, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte("# Title\n\nHello preview.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(txtPath, []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bigPath, bytes.Repeat([]byte("a"), previewMaxTextBytes+64), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPNGPath, []byte("not a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipPath, []byte("PK\x03\x04"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatal(err)
	}

	var opened []string
	a := &App{openURLHook: func(u string) { opened = append(opened, u) }}
	p := &Pane{info: rex.SessionInfo{Dir: dir}}

	// 1. HTTP(S) image URL goes to browser, never Side Preview.
	if !a.openLinkWith(p, "https://example.com/chart.png", false) {
		t.Fatal("https image url not taken")
	}
	if a.preview != nil || !slices.Equal(opened, []string{"https://example.com/chart.png"}) {
		t.Fatalf("https image url: preview=%v opened=%q", a.preview != nil, opened)
	}

	// 2. Local PNG opens in Side Preview with decoded image.
	if !a.openLinkWith(p, "diagram.png", false) {
		t.Fatal("diagram.png not taken")
	}
	if a.preview == nil || a.preview.state != previewReady || a.preview.kind != previewImage || a.preview.image == nil || a.preview.imgW != 64 || a.preview.imgH != 32 {
		t.Fatalf("diagram.png preview = %+v", a.preview)
	}

	// 3. OSC 8 file:// Markdown with space in name opens in Side Preview.
	mdURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(mdPath)}).String()
	if !a.openLinkWith(p, mdURL, false) {
		t.Fatal("osc8 md not taken")
	}
	if a.preview == nil || a.preview.state != previewReady || a.preview.kind != previewMarkdown || !strings.Contains(a.preview.text, "Hello preview.") {
		t.Fatalf("osc8 md preview = %+v", a.preview)
	}

	// 4. Plain text/code with :line:col opens in Side Preview with line badge.
	if !a.openLinkWith(p, "app.go:2:5", false) {
		t.Fatal("app.go:2:5 not taken")
	}
	if a.preview == nil || a.preview.state != previewReady || a.preview.kind != previewText || a.preview.line != 2 {
		t.Fatalf("app.go preview = %+v", a.preview)
	}

	// 5. Empty file shows ready empty state with localPath for external handoff.
	if !a.openLinkWith(p, "empty.txt", false) {
		t.Fatal("empty.txt not taken")
	}
	if a.preview == nil || a.preview.state != previewReady || !a.preview.empty || a.preview.localPath != emptyPath {
		t.Fatalf("empty.txt preview = %+v", a.preview)
	}

	// 6. Oversized file shows fallback state offering Open in Default App.
	if !a.openLinkWith(p, "huge.log", false) {
		t.Fatal("huge.log not taken")
	}
	if a.preview == nil || a.preview.state != previewFallback || a.preview.title != locales.PreviewTooLargeTitle() || a.preview.localPath != bigPath {
		t.Fatalf("huge.log preview = %+v", a.preview)
	}

	// 7. Binary file with text extension shows fallback state offering Open in Default App.
	if !a.openLinkWith(p, "fake.log", false) {
		t.Fatal("fake.log not taken")
	}
	if a.preview == nil || a.preview.state != previewFallback || a.preview.title != locales.PreviewBinaryTitle() || a.preview.localPath != binPath {
		t.Fatalf("fake.log preview = %+v", a.preview)
	}

	// 8. Corrupt image shows fallback state offering Open in Default App.
	if !a.openLinkWith(p, "broken.png", false) {
		t.Fatal("broken.png not taken")
	}
	if a.preview == nil || a.preview.state != previewFallback || a.preview.title != locales.PreviewImageFailedTitle() || a.preview.localPath != badPNGPath {
		t.Fatalf("broken.png preview = %+v", a.preview)
	}

	// 9. Missing file (plain and OSC 8 file://) shows error state without external open.
	missingURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, "nope.md"))}).String()
	for _, link := range []string{"nope.md", missingURL} {
		if !a.openLinkWith(p, link, false) {
			t.Fatalf("%s not taken", link)
		}
		if a.preview == nil || a.preview.state != previewError || a.preview.title != locales.PreviewNotFoundTitle() || a.preview.localPath != "" {
			t.Fatalf("%s preview = %+v", link, a.preview)
		}
	}

	// 10. PDF, unsupported binary, and directory open directly in system default app.
	a.preview = nil
	opened = nil
	pdfURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(pdfPath)}).String()
	for _, link := range []string{"spec.pdf", pdfURL, "bundle.zip", "docs/"} {
		if !a.openLinkWith(p, link, false) {
			t.Fatalf("%s not taken", link)
		}
		if a.preview != nil {
			t.Fatalf("%s unexpectedly opened Side Preview", link)
		}
	}
	wantExternal := []string{fileURL(pdfPath), fileURL(pdfPath), fileURL(zipPath), fileURL(subDir)}
	if !slices.Equal(opened, wantExternal) {
		t.Fatalf("external opens = %q, want %q", opened, wantExternal)
	}

	// 11. Option+Cmd bypass opens previewable Markdown and PNG directly in default app.
	opened = nil
	if !a.openLinkWith(p, "diagram.png", true) || !a.openLinkWith(p, mdURL, true) {
		t.Fatal("bypass click not taken")
	}
	if a.preview != nil || !slices.Equal(opened, []string{fileURL(pngPath), fileURL(mdPath)}) {
		t.Fatalf("bypass: preview=%v opened=%q", a.preview != nil, opened)
	}
}

// TestLinkPreviewUIAndFocusRestoration drives Side Preview end-to-end in
// headless ui.NewTester: opens a Markdown preview and an image preview,
// clicks "Open in Default App", closes via Esc and via the × button, and
// verifies keyboard focus returns cleanly to the originating terminal pane.
func TestLinkPreviewUIAndFocusRestoration(t *testing.T) {
	a, tt := newTestApp(t)
	p := a.tab().Focus
	waitFor(t, tt, "the shell", func() bool { return p.term != nil && strings.Contains(p.term.Text(), testShell.prompt) })

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "notes.md")
	pngPath := filepath.Join(dir, "chart.png")
	if err := os.WriteFile(mdPath, []byte("# Design Summary\n\nAll links follow one matrix.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pngPath, testPNGBytes(t, 120, 60), 0o600); err != nil {
		t.Fatal(err)
	}

	// Open Markdown file in Side Preview.
	if !a.openLink(p, mdPath) {
		t.Fatal("mdPath not taken")
	}
	tt.Frame()
	if a.preview == nil || a.preview.originPane != p {
		t.Fatalf("preview did not open for origin pane: %+v", a.preview)
	}
	for _, want := range []string{"notes.md", "Open in Default App", "Design Summary"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in Side Preview; texts %q", want, tt.Texts())
		}
	}

	// Click "Open in Default App" -> hands off to system default app.
	if err := tt.Click("Open in Default App"); err != nil {
		t.Fatalf("click Open in Default App: %v", err)
	}
	tt.Frame()
	if !slices.Contains(tt.OpenedURLs(), fileURL(mdPath)) {
		t.Fatalf("Open in Default App did not open %q; opened %q", fileURL(mdPath), tt.OpenedURLs())
	}

	// Press Esc -> closes Side Preview and restores focus to the terminal pane.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.preview != nil {
		t.Fatal("Esc did not close Side Preview")
	}
	if a.tab().Focus != p {
		t.Fatal("tab focus did not stay on origin pane after Esc")
	}
	// Verify keyboard input reaches the restored terminal pane immediately.
	tt.Type("echo preview_esc_ok")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "terminal input after Esc", func() bool {
		return strings.Contains(p.term.Text(), "preview_esc_ok")
	})

	// Open PNG image in Side Preview, render in dark & light themes, then close with × button.
	if !a.openLink(p, pngPath) {
		t.Fatal("pngPath not taken")
	}
	tt.SetDark(true)
	tt.Frame()
	if !tt.HasText("chart.png") || !tt.HasText("120 × 60") {
		t.Fatalf("image preview missing metadata; texts %q", tt.Texts())
	}
	tt.SetDark(false)
	tt.Frame()

	if err := tt.Click("Close Preview"); err != nil {
		t.Fatalf("click Close Preview: %v", err)
	}
	tt.Frame()
	if a.preview != nil {
		t.Fatal("Close Preview button did not close Side Preview")
	}
	// Verify keyboard input reaches the restored terminal pane after × button close.
	tt.Type("echo preview_btn_ok")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "terminal input after Close button", func() bool {
		return strings.Contains(p.term.Text(), "preview_btn_ok")
	})

	// Hold Option+Cmd -> bypasses Side Preview and opens directly externally.
	before := len(tt.OpenedURLs())
	tt.HoldModifiers(ui.Cmd | ui.Alt)
	tt.Frame()
	if !a.openLink(p, pngPath) {
		t.Fatal("Option+Cmd click not taken")
	}
	tt.HoldModifiers(0)
	tt.Frame()
	if a.preview != nil {
		t.Fatal("Option+Cmd click opened Side Preview instead of external app")
	}
	if got := tt.OpenedURLs()[before:]; !slices.Equal(got, []string{fileURL(pngPath)}) {
		t.Fatalf("Option+Cmd opened %q, want [%q]", got, fileURL(pngPath))
	}
}

// TestLinkPreviewRemoteFlowAndErrors verifies remote previewable files download,
// receive macOS quarantine, render inside Side Preview with the remote host
// badge, and hand off via Open in Default App; non-previewable PDFs open
// externally; and closing during a download prevents stale reopening.
func TestLinkPreviewRemoteFlowAndErrors(t *testing.T) {
	a, tt := newTestApp(t)
	there := testDir(t)
	t.Cleanup(func() { os.RemoveAll(there) })
	if err := os.WriteFile(filepath.Join(there, "summary.md"), []byte("# Remote Report\n\nDownloaded cleanly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(there, "spec.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, proxy, p := remotePane(t, a, tt, there)
	downloads := downloadsHere(t)

	// 1. Remote Markdown downloads into Downloads, quarantines, and opens Side Preview.
	x := clicked(t, a, tt, p, "summary.md")
	if !x.ok {
		t.Fatalf("remote summary.md download failed: %+v", x)
	}
	gotPath := filepath.Join(downloads, "summary.md")
	if !quarantined(t, gotPath) {
		t.Errorf("%s not quarantined", gotPath)
	}
	tt.Frame()
	if a.preview == nil || a.preview.state != previewReady || a.preview.hostLabel != "office-mac" || a.preview.localPath != gotPath {
		t.Fatalf("remote preview = %+v", a.preview)
	}
	for _, want := range []string{"summary.md", "office-mac", "Remote Report"} {
		if !tt.HasText(want) {
			t.Fatalf("remote Side Preview missing %q; texts %q", want, tt.Texts())
		}
	}
	if len(tt.OpenedURLs()) != 0 {
		t.Fatalf("remote previewable file opened externally too early: %q", tt.OpenedURLs())
	}
	if err := tt.Click("Open in Default App"); err != nil {
		t.Fatalf("click Open in Default App: %v", err)
	}
	tt.Frame()
	if !slices.Equal(tt.OpenedURLs(), []string{fileURL(gotPath)}) {
		t.Fatalf("remote Open in Default App opened %q, want [%q]", tt.OpenedURLs(), fileURL(gotPath))
	}
	a.closePreview()
	tt.Frame()

	// 2. Remote PDF downloads, quarantines, and opens directly in default app.
	x = clicked(t, a, tt, p, "spec.pdf")
	if !x.ok {
		t.Fatalf("remote spec.pdf failed: %+v", x)
	}
	pdfGot := filepath.Join(downloads, "spec.pdf")
	if !quarantined(t, pdfGot) {
		t.Errorf("%s not quarantined", pdfGot)
	}
	if a.preview != nil || !slices.Contains(tt.OpenedURLs(), fileURL(pdfGot)) {
		t.Fatalf("remote pdf: preview=%v opened=%q", a.preview != nil, tt.OpenedURLs())
	}

	// 3. Remote missing file shows error state in Side Preview.
	x = clicked(t, a, tt, p, "missing.md")
	if x.err == "" {
		t.Fatalf("remote missing.md succeeded unexpectedly: %+v", x)
	}
	tt.Frame()
	if a.preview == nil || a.preview.state != previewError || a.preview.title != locales.PreviewRemoteFailedTitle() {
		t.Fatalf("remote missing preview = %+v", a.preview)
	}
	a.closePreview()

	// 4. Closing Side Preview while a remote download is in flight does not reopen it when done.
	gate := make(chan struct{})
	proxy.mu.Lock()
	proxy.gate = gate
	proxy.mu.Unlock()
	if !a.openLink(p, "summary.md") {
		t.Fatal("summary.md second click not taken")
	}
	if a.preview == nil || a.preview.state != previewLoading {
		t.Fatalf("expected loading Side Preview, got %+v", a.preview)
	}
	a.closePreview()
	close(gate)
	last := listOf(p)[len(listOf(p))-1]
	waitFor(t, tt, "second summary.md download", func() bool { return last.ok || last.err != "" })
	tt.Frame()
	if a.preview != nil {
		t.Fatalf("closed preview reopened after download finished: %+v", a.preview)
	}
}
