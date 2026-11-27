package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorex/internal/layout"
	"gorex/internal/rex"
)

func TestAgyTitleBeforeConversationTitle(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	deepDir := filepath.Join(home, "work", "team", "platform", "services", "wind-rex")
	wantFullDir := filepath.Join("~", "work", "team", "platform", "services", "wind-rex")

	p := &Pane{info: rex.SessionInfo{Shell: "zsh", Program: "agy", Dir: deepDir}}

	// Without project context, fall back to concise brand + folder name (no long path).
	if name, detail := p.label(); name != "Antigravity" || detail != "wind-rex" {
		t.Fatalf("untitled agy label = (%q, %q), want (Antigravity, wind-rex)", name, detail)
	}
	if tip := paneTooltip(p, "", ""); tip != "Antigravity · wind-rex\n"+wantFullDir {
		t.Errorf("untitled agy tooltip = %q, want %q", tip, "Antigravity · wind-rex\n"+wantFullDir)
	}

	// With a named project, prefer the project name as secondary info and keep full path in tooltip.
	if name, detail := p.labelIn("WindRex App"); name != "Antigravity" || detail != "WindRex App" {
		t.Fatalf("project agy label = (%q, %q), want (Antigravity, WindRex App)", name, detail)
	}
	if tip := paneTooltip(p, "", "WindRex App"); tip != "Antigravity · WindRex App\n"+wantFullDir {
		t.Errorf("project agy tooltip = %q, want %q", tip, "Antigravity · WindRex App\n"+wantFullDir)
	}

	// Bare program names or path-only OSC titles fall back to the concise temporary name.
	for _, raw := range []string{
		"Antigravity",
		"agy",
		"Antigravity CLI",
		"Antigravity — " + deepDir,
		"Antigravity — " + wantFullDir,
		"Antigravity: /srv/work/org/wind-rex",
		"alice@devbox:~/work/wind-rex",
		`C:\WINDOWS\system32\cmd.exe - agy`,
	} {
		p.title = raw
		if name, detail := p.labelIn("WindRex App"); name != "Antigravity" || detail != "WindRex App" {
			t.Errorf("raw %q: label = (%q, %q), want (Antigravity, WindRex App)", raw, name, detail)
		}
	}

	// Home directory does not masquerade as a project name.
	pHome := &Pane{info: rex.SessionInfo{Shell: "zsh", Program: "agy", Dir: home}}
	if name, detail := pHome.label(); name != "Antigravity" || detail != "" {
		t.Errorf("home agy label = (%q, %q), want (Antigravity, \"\")", name, detail)
	}
	if tip := paneTooltip(pHome, "", ""); tip != "Antigravity\n~" {
		t.Errorf("home agy tooltip = %q, want %q", tip, "Antigravity\n~")
	}
}

func TestAgyTitleAfterConversationTitle(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	deepDir := filepath.Join(home, "work", "team", "platform", "wind-rex")
	wantFullDir := filepath.Join("~", "work", "team", "platform", "wind-rex")
	in := rex.SessionInfo{Shell: "zsh", Program: "agy", Dir: deepDir}

	cases := []struct {
		raw  string
		want string
	}{
		{"Fix login redirect", "Fix login redirect"},
		{"Antigravity — Fix login redirect", "Fix login redirect"},
		{"Antigravity: Fix login redirect", "Fix login redirect"},
		{"Antigravity - Fix login redirect", "Fix login redirect"},
		{"Antigravity | Fix login redirect", "Fix login redirect"},
		{"✨ Antigravity: Fix login redirect", "Fix login redirect"},
		{"agy — Fix login redirect", "Fix login redirect"},
		{"Fix login redirect — " + deepDir, "Fix login redirect"},
		{"Fix login redirect | " + wantFullDir, "Fix login redirect"},
		{"Antigravity — Fix login redirect — /srv/work/wind-rex", "Fix login redirect"},
		{"优化 Antigravity 会话命名", "优化 Antigravity 会话命名"},
		{"Antigravity title polish", "Antigravity title polish"},
	}
	for _, tc := range cases {
		p := &Pane{info: in, title: tc.raw}
		name, detail := p.labelIn("wind-rex")
		if name != tc.want || detail != "" {
			t.Errorf("raw %q: label = (%q, %q), want (%q, \"\")", tc.raw, name, detail, tc.want)
		}
		tip := paneTooltip(p, "", "wind-rex")
		if !strings.Contains(tip, tc.want) || !strings.Contains(tip, wantFullDir) {
			t.Errorf("raw %q: tooltip %q missing title or full path %q", tc.raw, tip, wantFullDir)
		}
	}

	// Custom project name different from folder basename appears in tooltip too.
	p := &Pane{info: in, title: "Fix login redirect"}
	wantTip := "Fix login redirect\nAntigravity · WindRex App\n" + wantFullDir
	if tip := paneTooltip(p, "", "WindRex App"); tip != wantTip {
		t.Errorf("custom project tooltip = %q, want %q", tip, wantTip)
	}
}

func TestAgyManualRenameWins(t *testing.T) {
	dir := filepath.FromSlash("/srv/work/org/platform/wind-rex")
	p := &Pane{
		SID:   "s1",
		info:  rex.SessionInfo{ID: "s1", Shell: "zsh", Program: "agy", Dir: dir, Title: "Fix login redirect"},
		title: "Fix login redirect",
	}
	tab := &Tab{Name: "Auth Review", Focus: p}

	// Manual WindRex tab rename always wins over automatic conversation title.
	if name, detail := tab.labelIn("wind-rex"); name != "Auth Review" || detail != "" {
		t.Fatalf("manual tab label = (%q, %q), want (Auth Review, \"\")", name, detail)
	}
	if tip := tabTooltip(tab, "wind-rex"); tip != "Auth Review\nFix login redirect\n"+dir {
		t.Errorf("manual tab tooltip = %q, want %q", tip, "Auth Review\nFix login redirect\n"+dir)
	}

	// Saved tab keeps manual name and Antigravity icon.
	st := savedTab{ID: "t1", Name: "Auth Review", Root: &layout.Node{SID: "s1", Dir: dir}}
	infos := map[string]rex.SessionInfo{"s1": p.info}
	if name, detail, prog := savedTabFaceIn(st, infos, "wind-rex"); name != "Auth Review" || detail != "" || prog.Name != "Antigravity" {
		t.Errorf("saved manual tab = (%q, %q, %q)", name, detail, prog.Name)
	}

	// Manual pane rename overrides automatic title without path clutter.
	p.Name = "Backend Fix"
	tab.Name = ""
	if name, detail := tab.labelIn("wind-rex"); name != "Backend Fix" || detail != "" {
		t.Fatalf("manual pane label = (%q, %q), want (Backend Fix, \"\")", name, detail)
	}
	if tip := tabTooltip(tab, "wind-rex"); tip != "Backend Fix\nFix login redirect\n"+dir {
		t.Errorf("manual pane tooltip = %q, want %q", tip, "Backend Fix\nFix login redirect\n"+dir)
	}
}

func TestAgyTitleSurvivesSwitchAndReconnect(t *testing.T) {
	a, tt := newTestApp(t)
	old := prefs
	t.Cleanup(func() { prefs = old })
	prefs = settings{TabPlacement: "left", Animation: "off"}

	space := ""
	deepDir := filepath.Join(appTempDir(t, a), "org", "platform", "wind-rex")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	res := clientCommand(a, rex.LayoutCommand{Op: "project.create", Dir: deepDir, Name: "wind-rex", SpaceName: &space})
	caughtUp(t, a, tt)
	w := a.st.byID(res.Space)
	a.showWorkspace(w)
	waitFor(t, tt, "project first tab", func() bool { return a.ws == w && len(a.tabs) == 1 && a.tabs[0].Focus != nil })
	a.newTab(deepDir)
	waitFor(t, tt, "project second tab", func() bool { return len(a.tabs) == 2 && a.tabs[1].Focus != nil })
	a.newTab(deepDir)
	waitFor(t, tt, "project third tab", func() bool { return len(a.tabs) == 3 && a.tabs[2].Focus != nil })

	p1, p2, p3 := a.tabs[0].Focus, a.tabs[1].Focus, a.tabs[2].Focus
	// Clear any initial shell title read by the test terminal before injecting agy session state.
	p1.title, p2.title, p3.title = "", "", ""
	a.st.infos = map[string]rex.SessionInfo{
		p1.SID: {ID: p1.SID, Shell: "zsh", Program: "agy", Dir: deepDir, Title: "Fix login redirect"},
		p2.SID: {ID: p2.SID, Shell: "zsh", Program: "agy", Dir: deepDir, Title: "Antigravity — Polish sidebar layout"},
		p3.SID: {ID: p3.SID, Shell: "zsh", Program: "agy", Dir: deepDir, Title: "Antigravity"},
	}
	a.apply(nil, a.st.infos)
	tt.Frame()

	// Two conversations in the same project are distinguished by title; untitled uses concise label.
	if n, d := a.tabLabel(a.tabs[0]); n != "Fix login redirect" || d != "" {
		t.Fatalf("tab 1 = (%q, %q)", n, d)
	}
	if n, d := a.tabLabel(a.tabs[1]); n != "Polish sidebar layout" || d != "" {
		t.Fatalf("tab 2 = (%q, %q)", n, d)
	}
	if n, d := a.tabLabel(a.tabs[2]); n != "Antigravity" || d != "wind-rex" {
		t.Fatalf("tab 3 = (%q, %q)", n, d)
	}
	// Window title on untitled tab does not repeat the project name.
	a.selectTab(2)
	if got := a.windowTitle(); got != "wind-rex — Antigravity" {
		t.Errorf("untitled windowTitle = %q, want %q", got, "wind-rex — Antigravity")
	}
	// Window title on titled tab shows conversation title.
	a.selectTab(0)
	if got := a.windowTitle(); got != "wind-rex — Fix login redirect" {
		t.Errorf("titled windowTitle = %q, want %q", got, "wind-rex — Fix login redirect")
	}
	// Rendered texts must not show the long path inline.
	tt.Frame()
	for _, want := range []string{"Fix login redirect", "Polish sidebar layout", "Antigravity"} {
		if !tt.HasText(want) {
			t.Errorf("sidebar missing %q in %q", want, tt.Texts())
		}
	}
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "org/platform/wind-rex") || strings.Contains(txt, `org\platform\wind-rex`) {
			t.Errorf("long path leaked into inline UI text: %q", txt)
		}
	}

	// Switching away to another workspace keeps both conversation titles via savedTabFaceIn.
	a.showWorkspace(a.st.noProjectOf(nil))
	waitFor(t, tt, "no-project workspace", func() bool { return a.ws != w && w.app == nil })
	setRowOpen(openKey(nil, w.ID), true)
	tt.Frame()
	if len(w.layout.Tabs) != 3 {
		t.Fatalf("saved tabs = %d, want 3", len(w.layout.Tabs))
	}
	if n, d, prog := savedTabFaceIn(w.layout.Tabs[0], a.st.infos, w.Name); n != "Fix login redirect" || d != "" || prog.Name != "Antigravity" {
		t.Errorf("saved tab 1 = (%q, %q, %q)", n, d, prog.Name)
	}
	if n, d, prog := savedTabFaceIn(w.layout.Tabs[1], a.st.infos, w.Name); n != "Polish sidebar layout" || d != "" || prog.Name != "Antigravity" {
		t.Errorf("saved tab 2 = (%q, %q, %q)", n, d, prog.Name)
	}
	if n, d, prog := savedTabFaceIn(w.layout.Tabs[2], a.st.infos, w.Name); n != "Antigravity" || d != "wind-rex" || prog.Name != "Antigravity" {
		t.Errorf("saved tab 3 = (%q, %q, %q)", n, d, prog.Name)
	}

	// Switching back restores conversation titles; server updates across reconnect sync p.title.
	a.showWorkspace(w)
	waitFor(t, tt, "back to project", func() bool { return a.ws == w && len(a.tabs) == 3 })
	p1, p3 = a.tabs[0].Focus, a.tabs[2].Focus
	p1.title = "Fix login redirect"
	p3.title = "Antigravity"
	a.st.infos[p1.SID] = rex.SessionInfo{ID: p1.SID, Shell: "zsh", Program: "agy", Dir: deepDir, Title: "Fix login redirect"}
	a.st.infos[p3.SID] = rex.SessionInfo{ID: p3.SID, Shell: "zsh", Program: "agy", Dir: deepDir, Title: "Antigravity"}
	a.take(nil, a.st.infos)

	// Server reports new title after reconnect / conversation rename / /new reset.
	a.st.infos[p1.SID] = rex.SessionInfo{ID: p1.SID, Shell: "zsh", Program: "agy", Dir: deepDir, Title: "Antigravity"}
	a.st.infos[p3.SID] = rex.SessionInfo{ID: p3.SID, Shell: "zsh", Program: "agy", Dir: deepDir, Title: "Add OAuth PKCE"}
	a.take(nil, a.st.infos)

	if n, d := a.tabLabel(a.tabs[0]); n != "Antigravity" || d != "wind-rex" {
		t.Errorf("after /new reset, tab 1 = (%q, %q), want (Antigravity, wind-rex)", n, d)
	}
	if n, d := a.tabLabel(a.tabs[2]); n != "Add OAuth PKCE" || d != "" {
		t.Errorf("after title arrived, tab 3 = (%q, %q), want (Add OAuth PKCE, \"\")", n, d)
	}

	// Manual tab rename overrides live conversation title.
	a.tabs[2].Name = "OAuth Work"
	if n, d := a.tabLabel(a.tabs[2]); n != "OAuth Work" || d != "" {
		t.Errorf("manual rename tab 3 = (%q, %q)", n, d)
	}
	a.selectTab(2)
	if got := a.windowTitle(); got != "wind-rex — OAuth Work" {
		t.Errorf("manual windowTitle = %q, want %q", got, "wind-rex — OAuth Work")
	}
}

func TestAgyNonAgySessionsUnchanged(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(home, "Sites", "rex-snake")
	short := filepath.Join("~", "Sites", "rex-snake")

	// Shell at prompt after agy exited drops agy's title and returns to shell label.
	ended := &Pane{
		info:  rex.SessionInfo{Shell: "zsh", Program: "zsh", Idle: true, Dir: site, Title: "Fix login redirect"},
		title: "Fix login redirect",
	}
	if n, d := ended.labelIn("rex-snake"); n != "zsh" || d != short {
		t.Errorf("ended agy shell = (%q, %q), want (zsh, %q)", n, d, short)
	}

	// Claude Code, Codex, Devin, and vim keep existing behavior.
	claude := &Pane{info: rex.SessionInfo{Shell: "zsh", Program: "claude", Dir: site}, title: "✳ Fix the login bug"}
	if n, d := claude.labelIn("rex-snake"); n != "✳ Fix the login bug" || d != "" {
		t.Errorf("claude = (%q, %q)", n, d)
	}
	codex := &Pane{info: rex.SessionInfo{Shell: "zsh", Program: "codex", Dir: site}, title: ""}
	if n, d := codex.labelIn("rex-snake"); n != "Codex" || d != short {
		t.Errorf("codex untitled = (%q, %q), want (Codex, %q)", n, d, short)
	}
	vim := &Pane{info: rex.SessionInfo{Shell: "zsh", Program: "vim", Dir: site}}
	if n, d := vim.labelIn("rex-snake"); n != "Vim" || d != short {
		t.Errorf("vim = (%q, %q), want (Vim, %q)", n, d, short)
	}
}
