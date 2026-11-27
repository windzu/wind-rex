package main

import (
	"encoding/json"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"gorex/internal/layout"
	"gorex/internal/rex"
	"gorex/locales"
)

// TestArchiveServerLayoutPreservesBaseOrder verifies serverLayout merges
// archived tabs back at their relative base positions so diffLayout never
// emits tab.close or spurious tab.set commands for archived tabs.
func TestArchiveServerLayoutPreservesBaseOrder(t *testing.T) {
	mkTab := func(id, sid string) savedTab {
		return savedTab{ID: id, Root: &savedNode{SID: sid}}
	}
	tA, tB, tC := mkTab("tab-a", "s1"), mkTab("tab-b", "s2"), mkTab("tab-c", "s3")
	baseWS := &savedWorkspace{
		ID:     "ws1",
		Name:   "proj",
		Layout: savedLayout{Active: 0, Tabs: []savedTab{tA, tB, tC}},
	}
	baseStore := savedStore{Version: layout.Version, Sessions: []savedWorkspace{*baseWS}, Windows: []string{"ws1"}}

	// Case 1: tB archived, active tabs [tA, tC] unchanged -> 0 commands.
	w := &Workspace{ID: "ws1", Name: "proj", layout: savedLayout{Active: 0, Tabs: []savedTab{tA, tC}}, archived: []savedTab{tB}}
	got := w.serverLayout(baseWS)
	if len(got.Tabs) != 3 || got.Tabs[0].ID != "tab-a" || got.Tabs[1].ID != "tab-b" || got.Tabs[2].ID != "tab-c" {
		t.Fatalf("case 1 tabs = %+v", got.Tabs)
	}
	nowStore := savedStore{Version: layout.Version, Sessions: []savedWorkspace{{ID: "ws1", Name: "proj", Layout: got}}, Windows: []string{"ws1"}}
	if cmds := diffLayout(baseStore, nowStore); len(cmds) != 0 {
		t.Fatalf("case 1 emitted unexpected diffLayout commands: %+v", cmds)
	}

	// Case 2: active tab tA closed while tB is archived -> tab.close(tab-a), zero ops touching tab-b.
	w.layout = savedLayout{Active: 0, Tabs: []savedTab{tC}}
	got = w.serverLayout(baseWS)
	if len(got.Tabs) != 2 || got.Tabs[0].ID != "tab-b" || got.Tabs[1].ID != "tab-c" {
		t.Fatalf("case 2 tabs = %+v", got.Tabs)
	}
	nowStore.Sessions[0].Layout = got
	cmds := diffLayout(baseStore, nowStore)
	closedA := false
	for _, c := range cmds {
		if c.Tab == "tab-b" || (c.TabLayout != nil && c.TabLayout.ID == "tab-b") {
			t.Fatalf("case 2 touched archived tab-b: %+v", c)
		}
		if c.Op == "tab.close" {
			if c.Tab != "tab-a" {
				t.Fatalf("case 2 unexpected tab.close: %+v", c)
			}
			closedA = true
		}
	}
	if !closedA {
		t.Fatalf("case 2 missing tab.close(tab-a): %+v", cmds)
	}

	// Case 3: all tabs archived -> 0 commands.
	w.layout = savedLayout{Active: 0, Tabs: []savedTab{}}
	w.archived = []savedTab{tA, tB, tC}
	got = w.serverLayout(baseWS)
	nowStore.Sessions[0].Layout = got
	if cmds := diffLayout(baseStore, nowStore); len(cmds) != 0 {
		t.Fatalf("case 3 emitted unexpected commands: %+v", cmds)
	}
}

// TestArchiveProjectAndNoProjectOrderPreserved archives tabs in both a project
// and No Project (including a background hidden workspace), verifying remaining
// tab order is preserved and underlying PTY sessions stay alive on the host.
func TestArchiveProjectAndNoProjectOrderPreserved(t *testing.T) {
	a, tt := newTestApp(t)
	old := prefs
	t.Cleanup(func() { prefs = old })
	prefs = settings{TabPlacement: "left", Animation: "off"}

	noProj := a.ws
	a.setTabName(a.tabs[0], "scratch-1")
	a.newTab(os.TempDir())
	waitFor(t, tt, "second no-project tab", func() bool { return len(a.tabs) == 2 && a.tabs[1].Focus != nil })
	a.setTabName(a.tabs[1], "scratch-2")
	scratch1ID := a.tabs[0].UID
	scratch1SID := a.tabs[0].Focus.SID
	a.save()
	a.st.out.wait(time.Second)

	projDir := appTempDir(t, a)
	a.createProject(nil, projDir, "acme-web")
	waitFor(t, tt, "project acme-web", func() bool { return a.ws != noProj && a.ws.Dir != "" && len(a.tabs) == 1 })
	proj := a.ws
	a.setTabName(a.tabs[0], "alpha")
	a.newProjectTab()
	waitFor(t, tt, "beta tab", func() bool { return len(a.tabs) == 2 && a.tabs[1].Focus != nil })
	a.setTabName(a.tabs[1], "beta")
	a.newProjectTab()
	waitFor(t, tt, "gamma tab", func() bool { return len(a.tabs) == 3 && a.tabs[2].Focus != nil })
	a.setTabName(a.tabs[2], "gamma")
	a.save()
	a.st.out.wait(time.Second)

	beta := a.tabs[1]
	betaSID := beta.Focus.SID

	// Archive middle project tab "beta".
	a.archiveTab(beta)
	tt.Frame()
	if len(a.tabs) != 2 || a.tabs[0].Name != "alpha" || a.tabs[1].Name != "gamma" {
		t.Fatalf("remaining project tabs = %q, want [alpha gamma]", tabNames(a.tabs))
	}
	if !slices.Contains(sessionIDs(t, a.client), betaSID) {
		t.Fatalf("archived project tab session %s was killed", betaSID)
	}

	// Verify archived.json exists with 0600 permissions on unix.
	stInfo, err := os.Stat(archivePath())
	if err != nil {
		t.Fatalf("archived.json missing: %v", err)
	}
	if runtime.GOOS != "windows" && stInfo.Mode().Perm() != 0o600 {
		t.Fatalf("archived.json perm = %o, want 0600", stInfo.Mode().Perm())
	}

	// Archive No Project's first tab while still viewing acme-web.
	a.archiveWorkspaceTab(noProj, scratch1ID)
	tt.Frame()
	if a.ws != proj {
		t.Fatalf("archiving background workspace tab switched workspace to %v", a.ws.Name)
	}
	if noProj.tabCount() != 1 || noProj.layout.Tabs[0].Name != "scratch-2" {
		t.Fatalf("remaining No Project tabs = %+v, want [scratch-2]", noProj.layout.Tabs)
	}
	if !slices.Contains(sessionIDs(t, a.client), scratch1SID) {
		t.Fatalf("archived No Project session %s was killed", scratch1SID)
	}

	// Save layout and confirm the server still retains all archived sessions.
	a.st.saveNow()
	live := sessionIDs(t, a.client)
	for _, sid := range []string{betaSID, scratch1SID} {
		if !slices.Contains(live, sid) {
			t.Fatalf("session %s disappeared after save", sid)
		}
	}
}

// TestArchivePersistAcrossDesktopRestartAndRestoreSplitHistory verifies an
// archived multi-pane split tab survives a desktop restart (reloaded from
// archived.json) and restores back to its original project with its live split
// sessions and VT scrollback intact.
func TestArchivePersistAcrossDesktopRestartAndRestoreSplitHistory(t *testing.T) {
	a, tt := newTestApp(t)
	old := prefs
	t.Cleanup(func() { prefs = old })
	prefs = settings{TabPlacement: "left", Animation: "off"}

	projDir := appTempDir(t, a)
	a.createProject(nil, projDir, "acme-api")
	waitFor(t, tt, "acme-api", func() bool { return a.ws.Dir != "" && len(a.tabs) == 1 && a.tabs[0].Focus != nil })
	projID := a.ws.ID

	a.newProjectTab()
	waitFor(t, tt, "second tab", func() bool { return len(a.tabs) == 2 && a.tabs[1].Focus != nil })
	a.setTabName(a.tabs[1], "deploy")
	a.split(false)
	waitFor(t, tt, "split pane", func() bool { return len(a.tabs[1].panes()) == 2 })

	deployTab := a.tabs[1]
	deployID := deployTab.UID
	panes := deployTab.panes()
	sid1, sid2 := panes[0].SID, panes[1].SID

	// Print a unique marker into pane 1 so it lives in server VT scrollback.
	if err := a.client.Input(sid1, []byte("echo ARCHIVE_SCROLLBACK_42\r")); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, tt, "scrollback marker", func() bool {
		capRes, err := a.client.Capture(sid1, 0)
		return err == nil && strings.Contains(string(capRes.Screen), "ARCHIVE_SCROLLBACK_42")
	})

	a.archiveTab(deployTab)
	a.st.saveNow()
	if len(a.tabs) != 1 {
		t.Fatalf("active tabs after archive = %d, want 1", len(a.tabs))
	}

	// Simulate closing and reopening the desktop client against the same
	// GOREX_DIR and running session server.
	a.detach()
	st2 := &store{client: a.client, started: true}
	st2.use(a.client, nil)
	st2.loadArchived()
	open := st2.load()
	if len(st2.archived) != 1 || st2.archived[0].ID != deployID {
		t.Fatalf("reopened archived entries = %+v, want [%s]", st2.archived, deployID)
	}
	proj2 := st2.byID(projID)
	if proj2 == nil {
		t.Fatalf("project %s missing after reopen", projID)
	}
	if proj2.tabCount() != 1 {
		t.Fatalf("reopened project active tab count = %d, want 1 (archived tab leaked)", proj2.tabCount())
	}
	a2 := &App{st: st2, client: a.client}
	st2.apps = []*App{a2}
	startW := proj2
	if len(open) > 0 && open[0] != nil {
		startW = open[0]
	}
	a2.show(startW)
	tt2 := ui.NewTester(a2.view, 1000, 620)
	tt2.Frame()

	// Restore the archived tab and verify project, split layout, original SIDs,
	// and live scrollback history.
	if !a2.restoreArchivedTab("", deployID) {
		t.Fatal("restoreArchivedTab returned false")
	}
	waitFor(t, tt2, "restored split tab", func() bool {
		return a2.ws == proj2 && a2.tab() != nil && a2.tab().UID == deployID && len(a2.tab().panes()) == 2
	})
	restoredPanes := a2.tab().panes()
	if restoredPanes[0].SID != sid1 || restoredPanes[1].SID != sid2 {
		t.Fatalf("restored SIDs = [%s %s], want [%s %s]", restoredPanes[0].SID, restoredPanes[1].SID, sid1, sid2)
	}
	waitFor(t, tt2, "replayed scrollback in terminal", func() bool {
		return restoredPanes[0].term != nil && strings.Contains(restoredPanes[0].term.Text(), "ARCHIVE_SCROLLBACK_42")
	})
	if len(st2.archived) != 0 {
		t.Fatalf("archived list not empty after restore: %+v", st2.archived)
	}
}

// TestArchiveRemoteLiveSessionStaysAliveAndRestores verifies archiving and
// restoring a multi-pane tab on a remote host keeps all remote PTYs running
// with zero tab.close sent and restores the exact remote sessions.
func TestArchiveRemoteLiveSessionStaysAliveAndRestores(t *testing.T) {
	a, tt, sock, remote := projectsApp(t)
	dir := testDir(t)
	res, err := remote.Command(rex.LayoutCommand{Op: "project.create", Dir: dir, Name: "remote-svc"})
	if err != nil || res.Code != "" {
		t.Fatalf("project.create on remote: %+v %v", res, err)
	}
	h := a.addHost(savedHost{ID: "h1", Name: "alice-server", Address: sock})
	waitFor(t, tt, "remote project", func() bool { w := a.st.byID(res.Space); return w != nil && w.host == h && !w.offline() })
	w := a.st.byID(res.Space)
	a.showWorkspace(w)
	waitFor(t, tt, "remote tab shown", func() bool { return a.ws == w && len(a.tabs) == 1 && a.tabs[0].Focus != nil })

	a.newProjectTab()
	waitFor(t, tt, "second remote tab", func() bool { return len(a.tabs) == 2 && a.active == 1 && a.tab().Focus != nil })
	a.setTabName(a.tabs[1], "worker")
	a.split(false)
	waitFor(t, tt, "remote split", func() bool { return len(a.tabs[1].panes()) == 2 })

	worker := a.tabs[1]
	workerID := worker.UID
	rsid1, rsid2 := worker.panes()[0].SID, worker.panes()[1].SID
	if err := remote.Input(rsid1, []byte("echo REMOTE_ARCHIVE_OK\r")); err != nil {
		t.Fatalf("remote input: %v", err)
	}
	waitFor(t, tt, "remote output", func() bool {
		capRes, err := remote.Capture(rsid1, 0)
		return err == nil && strings.Contains(string(capRes.Screen), "REMOTE_ARCHIVE_OK")
	})

	a.archiveTab(worker)
	a.st.saveNow()
	if h.lay != nil {
		h.lay.out.wait(time.Second)
	}
	tt.Frame()
	if len(a.tabs) != 1 {
		t.Fatalf("remote active tabs = %d, want 1", len(a.tabs))
	}
	remoteLive := sessionIDs(t, remote)
	if !slices.Contains(remoteLive, rsid1) || !slices.Contains(remoteLive, rsid2) {
		t.Fatalf("remote sessions killed on archive: live=%v want %s,%s", remoteLive, rsid1, rsid2)
	}
	noHostLeaves(t, a)

	if !a.restoreArchivedTab(h.ID, workerID) {
		t.Fatal("restoreArchivedTab failed for remote tab")
	}
	waitFor(t, tt, "remote tab restored", func() bool {
		return a.ws == w && a.tab() != nil && a.tab().UID == workerID && len(a.tab().panes()) == 2
	})
	if a.tab().panes()[0].SID != rsid1 || a.tab().panes()[1].SID != rsid2 {
		t.Fatalf("restored remote SIDs = [%s %s], want [%s %s]", a.tab().panes()[0].SID, a.tab().panes()[1].SID, rsid1, rsid2)
	}
	waitFor(t, tt, "remote scrollback replayed", func() bool {
		return a.tab().panes()[0].term != nil && strings.Contains(a.tab().panes()[0].term.Text(), "REMOTE_ARCHIVE_OK")
	})
}

// TestArchiveLastActiveTabKeepsWindowOpenAndQuiet verifies archiving the last
// active tab keeps the window open on emptyNoTab and suppresses busy dots,
// attention dots, and Dock badges while archived.
func TestArchiveLastActiveTabKeepsWindowOpenAndQuiet(t *testing.T) {
	a, tt := newTestApp(t)
	old := prefs
	t.Cleanup(func() { prefs = old })
	prefs = settings{TabPlacement: "left", Animation: "off"}

	p := a.tabs[0].Focus
	now := time.Now()
	p.info.Statuses = []rex.ProgramStatus{
		{ID: "w1", State: "working", Updated: now},
		{ID: "b1", State: "blocked", Updated: now},
	}
	a.st.infos = map[string]rex.SessionInfo{p.SID: p.info}
	a.syncBadge()
	if a.st.workingStatus(a.ws) == nil || a.st.status(a.ws) == nil || testBadge.Load() == 0 {
		t.Fatalf("expected active busy/blocked status and badge before archive")
	}

	a.archiveTab(a.tabs[0])
	tt.Frame()
	if len(a.tabs) != 0 {
		t.Fatalf("expected 0 active tabs, got %d", len(a.tabs))
	}
	if !tt.HasText(locales.EmptyNoTab()) {
		t.Fatalf("expected emptyNoTab empty state, got texts %q", tt.Texts())
	}
	if a.st.workingStatus(a.ws) != nil {
		t.Fatalf("archived tab leaked into workingStatus")
	}
	if a.st.status(a.ws) != nil {
		t.Fatalf("archived tab leaked into workspace status")
	}
	if testBadge.Load() != 0 {
		t.Fatalf("archived tab leaked into Dock badge: %d", testBadge.Load())
	}
}

// TestArchiveExitedSessionRecreatesLayoutWithResumeHint verifies restoring an
// archived tab whose host sessions exited recreates its split layout and
// attaches a resume hint for its previously running program.
func TestArchiveExitedSessionRecreatesLayoutWithResumeHint(t *testing.T) {
	a, tt := newTestApp(t)
	old := prefs
	t.Cleanup(func() { prefs = old })
	prefs = settings{TabPlacement: "left", Animation: "off"}

	projDir := appTempDir(t, a)
	a.createProject(nil, projDir, "agent-proj")
	waitFor(t, tt, "agent-proj", func() bool { return a.ws.Dir != "" && len(a.tabs) == 1 })
	proj := a.ws

	a.newProjectTab()
	waitFor(t, tt, "second tab", func() bool { return len(a.tabs) == 2 })
	a.setTabName(a.tabs[1], "claude-work")
	a.split(false)
	waitFor(t, tt, "split", func() bool { return len(a.tabs[1].panes()) == 2 })

	tab := a.tabs[1]
	tabID := tab.UID
	sids := []string{tab.panes()[0].SID, tab.panes()[1].SID}
	a.archiveTab(tab)
	a.st.saveNow()

	// Stamp a saved program on the archived entry's first leaf and kill the
	// underlying sessions to simulate exiting while archived.
	idx := a.st.archivedIndex("", tabID)
	if idx < 0 {
		t.Fatal("archived entry missing")
	}
	leaves := layout.Leaves(a.st.archived[idx].Tab.Root)
	leaves[0].Prog = "claude"
	leaves[0].Args = []string{"claude"}
	a.st.saveArchived()

	for _, sid := range sids {
		_ = a.client.Kill(sid)
	}
	waitFor(t, tt, "archived tab pruned by server", func() bool {
		if raw, err := a.st.read(); err == nil {
			a.st.reconcile(layout.Parse(raw))
		}
		refresh(a)
		state, _, _, _ := a.archivedEntryStatus(a.st.archived[0])
		return state == archiveStateExited && len(proj.archived) == 0
	})

	if !a.restoreArchivedTab("", tabID) {
		t.Fatal("restoreArchivedTab returned false for exited tab")
	}
	waitFor(t, tt, "recreated split tab", func() bool {
		return a.ws == proj && len(a.tabs) == 2 && a.tabs[1].Name == "claude-work" && len(a.tabs[1].panes()) == 2
	})
	first := a.tabs[1].panes()[0]
	if first.resume == nil || first.resume.cont != "claude --continue" {
		t.Fatalf("recreated pane resume = %+v, want claude --continue", first.resume)
	}
}

// TestArchivePermanentDeleteEndsSessionAndConfirmsWhenRunning verifies
// permanent delete from the Archived dialog asks before ending a running
// program and emits tab.close to end the host PTY on confirmation.
func TestArchivePermanentDeleteEndsSessionAndConfirmsWhenRunning(t *testing.T) {
	a, tt := newTestApp(t)
	old := prefs
	t.Cleanup(func() { prefs = old })
	prefs = settings{TabPlacement: "left", Animation: "off"}

	a.newTab(os.TempDir())
	waitFor(t, tt, "second tab", func() bool { return len(a.tabs) == 2 && a.tabs[1].Focus != nil })
	a.setTabName(a.tabs[1], "runner")
	runnerSID := a.tabs[1].Focus.SID
	a.archiveTab(a.tabs[1])
	a.st.saveNow()

	// Simulate an active program running inside the archived session.
	a.st.infos = map[string]rex.SessionInfo{
		runnerSID: {ID: runnerSID, Program: "python3", Idle: false, Statuses: []rex.ProgramStatus{{ID: "w", State: "working", Updated: time.Now()}}},
	}
	a.openArchiveDialog()
	tt.Frame()

	delLabel := locales.ArchiveDelete() + " runner"
	click(t, tt, delLabel)
	tt.Frame()
	if !tt.HasText(locales.ArchiveDeleteConfirm()) {
		t.Fatalf("expected inline running delete confirmation, texts=%q", tt.Texts())
	}
	if !slices.Contains(sessionIDs(t, a.client), runnerSID) {
		t.Fatalf("session ended before confirmation")
	}

	click(t, tt, locales.ArchiveDeleteConfirm())
	tt.Frame()
	a.st.out.wait(time.Second)
	waitFor(t, tt, "deleted archived session ended on server", func() bool {
		return len(a.st.archived) == 0 && !slices.Contains(sessionIDs(t, a.client), runnerSID)
	})
}

// TestArchiveUIHoverContextMenuAndDialog exercises the desktop sidebar hover
// Archive button, right-click context menu, background workspace spaceTab
// Archive action, sidebar footer Archived button, and Restore inside the
// Archived dialog via ui.NewTester.
func TestArchiveUIHoverContextMenuAndDialog(t *testing.T) {
	a, tt := newTestApp(t)
	old := prefs
	t.Cleanup(func() { prefs = old })
	prefs = settings{TabPlacement: "left", Animation: "off"}

	projDir := appTempDir(t, a)
	a.createProject(nil, projDir, "ui-proj")
	waitFor(t, tt, "ui-proj", func() bool { return a.ws.Dir != "" && len(a.tabs) == 1 })
	proj := a.ws
	a.setTabName(a.tabs[0], "first-tab")
	a.newProjectTab()
	waitFor(t, tt, "second tab", func() bool { return len(a.tabs) == 2 })
	a.setTabName(a.tabs[1], "second-tab")
	a.save()
	a.st.out.wait(time.Second)
	tt.Frame()

	// 1. Hover active sidebar row and click trailing Archive button.
	row, ok := tt.Find("second-tab ")
	if !ok {
		t.Fatalf("second-tab row not found; texts=%q", tt.Texts())
	}
	tt.Move(row.X+20, row.Y+row.H/2)
	tt.Frame()
	archBtn, ok := tt.Find(locales.CmdArchiveTab())
	if !ok {
		t.Fatalf("hover Archive button missing on active sidebar row")
	}
	tt.ClickAt(archBtn.X+archBtn.W/2, archBtn.Y+archBtn.H/2)
	tt.Frame()
	waitFor(t, tt, "second-tab archived", func() bool { return len(a.tabs) == 1 && len(a.st.archived) == 1 })

	// 2. Right-click active sidebar row and choose Archive Tab from context menu.
	row1, ok := tt.Find("first-tab ")
	if !ok {
		t.Fatalf("first-tab row not found; texts=%q", tt.Texts())
	}
	tt.RightClick("first-tab ")
	tt.Frame()
	tt.ChooseMenuItem(locales.CmdArchiveTab())
	tt.Frame()
	waitFor(t, tt, "first-tab archived via menu", func() bool {
		return len(a.st.archived) == 2 && (a.ws != proj || len(a.tabs) == 0)
	})
	_ = row1

	// 3. Open Archived dialog via sidebar footer button and restore "second-tab".
	click(t, tt, locales.SidebarArchived())
	tt.Frame()
	if !a.archiveOpen || !tt.HasText(locales.ArchiveDialogTitle()) {
		t.Fatalf("Archived dialog did not open; texts=%q", tt.Texts())
	}
	if !tt.HasText("second-tab") || !tt.HasText("first-tab") {
		t.Fatalf("Archived dialog missing entries; texts=%q", tt.Texts())
	}
	click(t, tt, locales.ArchiveRestore()+" second-tab")
	tt.Frame()
	waitFor(t, tt, "second-tab restored from dialog", func() bool {
		return !a.archiveOpen && a.ws == proj && len(a.tabs) == 1 && a.tabs[0].Name == "second-tab" && len(a.st.archived) == 1
	})

	// 4. Switch to No Project so ui-proj is an expanded background project,
	// hover its background spaceTab row ("second-tab"), and click its trailing
	// Archive button without switching away from No Project.
	noProj := a.st.noProjectOf(nil)
	if noProj == nil {
		t.Fatalf("expected No Project workspace")
	}
	a.showWorkspace(noProj)
	setRowOpen(openKey(proj.host, proj.ID), true)
	tt.Frame()
	bgRow, ok := tt.Find("second-tab ")
	if !ok {
		t.Fatalf("background spaceTab row not found; texts=%q", tt.Texts())
	}
	tt.Move(bgRow.X+20, bgRow.Y+bgRow.H/2)
	tt.Frame()
	bgArch, ok := tt.Find(locales.CmdArchiveTab())
	if !ok {
		t.Fatalf("hover Archive button missing on background spaceTab row")
	}
	tt.ClickAt(bgArch.X+bgArch.W/2, bgArch.Y+bgArch.H/2)
	tt.Frame()
	waitFor(t, tt, "background spaceTab archived", func() bool {
		return a.ws == noProj && len(proj.layout.Tabs) == 0 && len(a.st.archived) == 2
	})

	// 5. Verify JSON written to archived.json is valid and has both entries.
	raw, err := os.ReadFile(archivePath())
	if err != nil {
		t.Fatalf("reading archived.json: %v", err)
	}
	var disk archivedStore
	if err := json.Unmarshal(raw, &disk); err != nil || len(disk.Tabs) != 2 {
		t.Fatalf("unexpected archived.json: %s (%v)", raw, err)
	}
}

func tabNames(tabs []*Tab) []string {
	out := make([]string, len(tabs))
	for i, t := range tabs {
		out[i] = t.Name
	}
	return out
}
