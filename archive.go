package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gorex/internal/layout"
	"gorex/internal/rex"
	"gorex/locales"
)

// Desktop tab archiving (docs/DSK-013-archive-tabs.md): archived tabs stay in
// their host's layout.json so their PTY sessions and VT scrollback keep
// running untouched, while the desktop persists the archived set in a
// standalone <GOREX_DIR>/archived.json overlay.

const archiveVersion = 1

// archivedStore is the on-disk shape of archived.json.
type archivedStore struct {
	Version int             `json:"version"`
	Tabs    []archivedEntry `json:"tabs"`
}

// archivedEntry is one archived tab persisted in archived.json.
type archivedEntry struct {
	ID         string     `json:"id"`
	Host       string     `json:"host,omitempty"`
	HostName   string     `json:"hostName,omitempty"`
	SpaceID    string     `json:"spaceId"`
	SpaceName  string     `json:"spaceName,omitempty"`
	ProjectDir string     `json:"projectDir,omitempty"`
	ArchivedAt time.Time  `json:"archivedAt"`
	Title      string     `json:"title,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	Program    string     `json:"program,omitempty"`
	Tab        layout.Tab `json:"tab"`
}

// archiveState describes the live status of an archived tab on its host.
type archiveState int

const (
	archiveStateLive archiveState = iota
	archiveStateExited
	archiveStateOffline
)

func archivePath() string { return filepath.Join(rex.Dir(), "archived.json") }

// archiveHostID is the host identifier stored in archived.json: "" for this
// machine, h.ID for a remote host (matching layout.Node.Host).
func archiveHostID(h *hostConn) string {
	if h == nil {
		return ""
	}
	return h.ID
}

// archiveKey uniquely identifies an archived tab on its host.
func archiveKey(hostID, tabID string) string { return hostID + "\x00" + tabID }

// loadArchived reads archived.json once per store, ignoring a missing file.
func (st *store) loadArchived() {
	if st == nil || st.archivedLoaded {
		return
	}
	st.archivedLoaded = true
	b, err := os.ReadFile(archivePath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("gorex archive: reading %s: %v", archivePath(), err)
		}
		return
	}
	var disk archivedStore
	if err := json.Unmarshal(b, &disk); err != nil {
		log.Printf("gorex archive: decoding %s: %v", archivePath(), err)
		return
	}
	seen := map[string]bool{}
	st.archived = nil
	for _, e := range disk.Tabs {
		if e.ID == "" {
			continue
		}
		k := archiveKey(e.Host, e.ID)
		if seen[k] {
			continue
		}
		seen[k] = true
		st.archived = append(st.archived, e)
	}
}

// saveArchived writes st.archived atomically to <GOREX_DIR>/archived.json with
// 0600 permissions.
func (st *store) saveArchived() {
	if st == nil {
		return
	}
	tabs := st.archived
	if tabs == nil {
		tabs = []archivedEntry{}
	}
	b, err := json.MarshalIndent(archivedStore{Version: archiveVersion, Tabs: tabs}, "", "  ")
	if err != nil {
		log.Printf("gorex archive: encoding: %v", err)
		return
	}
	if err := os.MkdirAll(rex.Dir(), 0o700); err != nil {
		log.Printf("gorex archive: mkdir: %v", err)
		return
	}
	if err := writeFileAtomic(archivePath(), b, 0o600); err != nil {
		log.Printf("gorex archive: saving: %v", err)
	}
}

// archivedIndex returns the index of (hostID, tabID) in st.archived, or -1.
func (st *store) archivedIndex(hostID, tabID string) int {
	if st == nil || tabID == "" {
		return -1
	}
	for i := range st.archived {
		if st.archived[i].Host == hostID && st.archived[i].ID == tabID {
			return i
		}
	}
	return -1
}

// isArchived reports whether tabID on hostID is currently archived.
func (st *store) isArchived(hostID, tabID string) bool {
	return st.archivedIndex(hostID, tabID) >= 0
}

// splitArchived partitions a server layout into visible active tabs and
// archived tabs for workspace w on host h, refreshing cached archive metadata
// from the server's authoritative tab snapshot.
func (st *store) splitArchived(h *hostConn, w *Workspace, l savedLayout) (visible savedLayout, archived []savedTab) {
	if st == nil || len(st.archived) == 0 {
		return l, nil
	}
	hostID := archiveHostID(h)
	activeID := ""
	if l.Active >= 0 && l.Active < len(l.Tabs) {
		activeID = l.Tabs[l.Active].ID
	}
	visible = savedLayout{Tabs: []savedTab{}}
	dirty := false
	for _, t := range l.Tabs {
		idx := st.archivedIndex(hostID, t.ID)
		if idx < 0 {
			if t.ID != "" && t.ID == activeID {
				visible.Active = len(visible.Tabs)
			}
			visible.Tabs = append(visible.Tabs, t)
			continue
		}
		archived = append(archived, t)
		e := &st.archived[idx]
		// Refresh cached tab snapshot and workspace provenance from the server.
		if w != nil {
			if e.SpaceID != w.ID || e.SpaceName != w.Name || e.ProjectDir != w.Dir {
				e.SpaceID, e.SpaceName, e.ProjectDir = w.ID, w.Name, w.Dir
				dirty = true
			}
		}
		e.Tab = cloneSavedTab(t)
	}
	if len(visible.Tabs) == 0 {
		visible.Active = 0
	} else {
		visible.Active = min(max(visible.Active, 0), len(visible.Tabs)-1)
	}
	if dirty {
		st.saveArchived()
	}
	return visible, archived
}

// cloneSavedTab deep-copies a savedTab so desktop state never shares node
// pointers with st.base or archived.json.
func cloneSavedTab(t savedTab) savedTab {
	b, _ := json.Marshal(t)
	var out savedTab
	_ = json.Unmarshal(b, &out)
	return out
}

// serverLayout returns w's layout with its archived tabs merged back at their
// relative positions in baseWS so diffLayout emits zero tab.close and zero
// spurious tab.set commands for archived tabs.
func (w *Workspace) serverLayout(baseWS *savedWorkspace) savedLayout {
	vis := w.savedLayout()
	if len(w.archived) == 0 {
		return vis
	}
	archByID := make(map[string]savedTab, len(w.archived))
	for _, t := range w.archived {
		archByID[t.ID] = t
	}
	// Prefer baseWS's exact tab copies so Arrangement() matches byte-for-byte.
	if baseWS != nil {
		for _, bt := range baseWS.Tabs {
			if _, ok := archByID[bt.ID]; ok {
				archByID[bt.ID] = bt
			}
		}
	}
	visIDs := make(map[string]bool, len(vis.Tabs))
	for _, t := range vis.Tabs {
		visIDs[t.ID] = true
	}
	// Bucket each archived tab by how many surviving visible tabs precede it
	// in baseWS, preserving its exact slot without triggering outOfOrder moves.
	buckets := make([][]savedTab, len(vis.Tabs)+1)
	placed := make(map[string]bool, len(w.archived))
	if baseWS != nil {
		before := 0
		for _, bt := range baseWS.Tabs {
			if visIDs[bt.ID] {
				before++
				continue
			}
			if at, ok := archByID[bt.ID]; ok && !placed[bt.ID] {
				slot := min(before, len(vis.Tabs))
				buckets[slot] = append(buckets[slot], at)
				placed[bt.ID] = true
			}
		}
	}
	for _, t := range w.archived {
		if !placed[t.ID] {
			buckets[len(vis.Tabs)] = append(buckets[len(vis.Tabs)], archByID[t.ID])
			placed[t.ID] = true
		}
	}
	merged := make([]savedTab, 0, len(vis.Tabs)+len(w.archived))
	for i := range vis.Tabs {
		merged = append(merged, buckets[i]...)
		merged = append(merged, vis.Tabs[i])
	}
	merged = append(merged, buckets[len(vis.Tabs)]...)
	active := 0
	if len(vis.Tabs) > 0 {
		vi := min(max(vis.Active, 0), len(vis.Tabs)-1)
		activeID := vis.Tabs[vi].ID
		if j := slices.IndexFunc(merged, func(t savedTab) bool { return t.ID == activeID }); j >= 0 {
			active = j
		}
	} else if baseWS != nil && len(merged) > 0 {
		active = min(max(baseWS.Active, 0), len(merged)-1)
	}
	return savedLayout{Active: active, Tabs: merged}
}

// baseWorkspace returns the host's last-known savedWorkspace for w, or nil.
func (st *store) baseWorkspace(w *Workspace) *savedWorkspace {
	if st == nil || w == nil {
		return nil
	}
	if w.host == nil {
		return st.base.Space(w.ID)
	}
	if w.host.lay == nil {
		return nil
	}
	return w.host.lay.base.Space(w.ID)
}

// canArchiveTab reports whether active tab t can be archived right now.
func (a *App) canArchiveTab(t *Tab) bool {
	return a.st != nil && a.ws != nil && !a.ws.offline() && t != nil && t.UID != "" && slices.Contains(a.tabs, t)
}

// archivedCount returns how many tabs are currently in the archive.
func (a *App) archivedCount() int {
	if a.st == nil {
		return 0
	}
	return len(a.st.archived)
}

// archivedEntries returns archived entries sorted newest-first.
func (st *store) archivedEntries() []archivedEntry {
	if st == nil || len(st.archived) == 0 {
		return nil
	}
	out := slices.Clone(st.archived)
	slices.SortStableFunc(out, func(x, y archivedEntry) int {
		if !x.ArchivedAt.Equal(y.ArchivedAt) {
			if x.ArchivedAt.After(y.ArchivedAt) {
				return -1
			}
			return 1
		}
		return strings.Compare(y.ID, x.ID)
	})
	return out
}

// openArchiveDialog opens the Archived tabs dialog.
func (a *App) openArchiveDialog() {
	a.archiveOpen = true
	a.archiveQuery = ""
	a.archiveDeleteKey = ""
	a.focusReq = nil
}

// archiveActiveTab archives the window's currently active tab.
func (a *App) archiveActiveTab() {
	if t := a.tab(); t != nil {
		a.archiveTab(t)
	}
}

// archiveTab archives tab t from the window's current workspace without ending
// any of its PTY sessions on its host.
func (a *App) archiveTab(t *Tab) {
	if !a.canArchiveTab(t) {
		return
	}
	st, w := a.st, a.ws
	st.loadArchived()
	// Flush pending local changes (e.g. a rename or split just made on t)
	// before detaching t so the host server and base keep the latest state.
	st.save()
	saved := a.savedTab(t)
	if baseWS := st.baseWorkspace(w); baseWS != nil {
		if bi := slices.IndexFunc(baseWS.Tabs, func(x savedTab) bool { return x.ID == t.UID }); bi >= 0 {
			saved = cloneSavedTab(baseWS.Tabs[bi])
		}
	}
	name, detail := a.tabLabel(t)
	progName := ""
	if t.Focus != nil {
		progName = t.Focus.program().Name
	}
	entry := archivedEntry{
		ID:         t.UID,
		Host:       archiveHostID(w.host),
		HostName:   a.hostName(w.host),
		SpaceID:    w.ID,
		SpaceName:  w.Name,
		ProjectDir: w.Dir,
		ArchivedAt: time.Now().UTC(),
		Title:      name,
		Detail:     detail,
		Program:    progName,
		Tab:        cloneSavedTab(saved),
	}
	st.upsertArchived(entry)
	if !slices.ContainsFunc(w.archived, func(x savedTab) bool { return x.ID == saved.ID }) {
		w.archived = append(w.archived, saved)
	}
	if a.closeAsk.tab == t {
		a.closeAsk = closeAsk{}
	}
	if a.closeOthers == t {
		a.closeOthers, a.closeOthersOpen = nil, false
	}
	if a.revealed == t {
		a.revealed = nil
	}
	// Detach UI panes WITHOUT killing host PTY sessions.
	a.closePanes([]*Tab{t})
	a.removeArchivedActiveTab(t)
	st.syncBadge()
	st.redraw()
}

// removeArchivedActiveTab removes an already-archived tab from a.tabs without
// closing the window if it was the last active tab.
func (a *App) removeArchivedActiveTab(t *Tab) {
	i := slices.Index(a.tabs, t)
	if i < 0 {
		return
	}
	chosen := a.active == i
	a.tabs = slices.Delete(a.tabs, i, i+1)
	if a.active > i || a.active >= len(a.tabs) {
		a.active = max(a.active-1, 0)
	}
	if chosen {
		same := func(o *Tab) bool { return o.host() == t.host() }
		if j := slices.IndexFunc(a.tabs[i:], same); j >= 0 {
			a.active = i + j
		} else {
			for j, o := range slices.Backward(a.tabs[:i]) {
				if same(o) {
					a.active = j
					break
				}
			}
		}
	}
	if a.renaming == t {
		a.renaming = nil
	}
	if len(a.tabs) == 0 {
		a.active, a.focusReq = 0, nil
		if next := a.st.freeAfter(a.ws); next != nil {
			a.show(next)
		}
		a.changed()
		return
	}
	if nt := a.tab(); nt != nil {
		a.focusReq = nt.Focus
	}
	a.changed()
}

// archiveWorkspaceTab archives tab tabID from workspace w, whether w is shown
// in this window, another window, or currently hidden in the sidebar.
func (a *App) archiveWorkspaceTab(w *Workspace, tabID string) {
	if a.st == nil || w == nil || w.offline() || tabID == "" {
		return
	}
	if o := w.app; o != nil {
		if t := o.tabByUID(tabID); t != nil {
			o.archiveTab(t)
		}
		return
	}
	idx := slices.IndexFunc(w.layout.Tabs, func(t savedTab) bool { return t.ID == tabID })
	if idx < 0 {
		return
	}
	st := a.st
	st.loadArchived()
	saved := cloneSavedTab(w.layout.Tabs[idx])
	infos, _, _ := st.listOf(w.host)
	proj := ""
	if w.Dir != "" {
		proj = w.Name
	}
	name, detail, prog := savedTabFaceIn(saved, infos, proj)
	entry := archivedEntry{
		ID:         saved.ID,
		Host:       archiveHostID(w.host),
		HostName:   a.hostName(w.host),
		SpaceID:    w.ID,
		SpaceName:  w.Name,
		ProjectDir: w.Dir,
		ArchivedAt: time.Now().UTC(),
		Title:      name,
		Detail:     detail,
		Program:    prog.Name,
		Tab:        cloneSavedTab(saved),
	}
	st.upsertArchived(entry)
	// Drop any kept live UI panes for this workspace without killing sessions.
	w.unkeep()
	w.layout.Tabs = slices.Delete(w.layout.Tabs, idx, idx+1)
	if w.layout.Active > idx || w.layout.Active >= len(w.layout.Tabs) {
		w.layout.Active = max(w.layout.Active-1, 0)
	}
	if !slices.ContainsFunc(w.archived, func(x savedTab) bool { return x.ID == saved.ID }) {
		w.archived = append(w.archived, saved)
	}
	st.syncBadge()
	st.redraw()
}

// upsertArchived inserts or replaces an entry in st.archived and saves.
func (st *store) upsertArchived(entry archivedEntry) {
	if idx := st.archivedIndex(entry.Host, entry.ID); idx >= 0 {
		st.archived[idx] = entry
	} else {
		st.archived = append(st.archived, entry)
	}
	st.saveArchived()
}

// removeArchivedEntry deletes (hostID, tabID) from st.archived and saves.
func (st *store) removeArchivedEntry(hostID, tabID string) bool {
	idx := st.archivedIndex(hostID, tabID)
	if idx < 0 {
		return false
	}
	st.archived = slices.Delete(st.archived, idx, idx+1)
	st.saveArchived()
	return true
}

// dropWorkspaceArchived removes all archived entries belonging to workspace w
// (called when a project is explicitly closed).
func (st *store) dropWorkspaceArchived(w *Workspace) {
	if st == nil || w == nil {
		return
	}
	hostID := archiveHostID(w.host)
	before := len(st.archived)
	st.archived = slices.DeleteFunc(st.archived, func(e archivedEntry) bool {
		return e.Host == hostID && e.SpaceID == w.ID
	})
	w.archived = nil
	if len(st.archived) != before {
		st.saveArchived()
	}
}

// archivedHost returns the hostConn for hostID, nil for local or unknown.
func (a *App) archivedHost(hostID string) (*hostConn, bool) {
	if hostID == "" {
		return nil, true
	}
	h := a.hostByID(hostID)
	return h, h != nil
}

// archivedEntryStatus classifies an archived entry as live, exited, or offline,
// and resolves its latest display title, detail, and program tile.
func (a *App) archivedEntryStatus(e archivedEntry) (state archiveState, title, detail string, prog program) {
	title, detail = e.Title, e.Detail
	if e.Program != "" {
		prog = programOf(e.Program)
	} else {
		var savedTitle string
		savedTitle, prog = savedTabLabel(e.Tab)
		if title == "" {
			title = savedTitle
		}
	}
	h, ok := a.archivedHost(e.Host)
	if !ok {
		return archiveStateOffline, cmp.Or(title, "Shell"), detail, prog
	}
	if h != nil && (h.lay == nil || !h.lay.live || h.current() == nil) {
		return archiveStateOffline, cmp.Or(title, "Shell"), detail, prog
	}
	if a.st == nil {
		return archiveStateExited, cmp.Or(title, "Shell"), detail, prog
	}
	infos, _, listed := a.st.listOf(h)
	if !listed {
		return archiveStateOffline, cmp.Or(title, "Shell"), detail, prog
	}
	liveTab, liveWS := a.st.findArchivedTab(h, e.ID)
	tab := e.Tab
	if liveTab != nil {
		tab = *liveTab
	}
	hasLive := false
	for _, r := range tab.Roots() {
		for _, n := range layout.Leaves(r) {
			if n.Kind == noteKind {
				hasLive = true
				continue
			}
			if n.SID == "" {
				continue
			}
			in, ok := infos[n.SID]
			if ok && !in.Exited {
				hasLive = true
			} else if !ok && liveTab != nil {
				// The host's authoritative layout.json still holds this tab;
				// st.infos may simply not have polled the new session yet.
				hasLive = true
			}
		}
	}
	if hasLive || liveTab != nil {
		proj := e.SpaceName
		if liveWS != nil && liveWS.Dir != "" {
			proj = liveWS.Name
		} else if e.ProjectDir == "" {
			proj = ""
		}
		liveName, liveDetail, liveProg := savedTabFaceIn(tab, infos, proj)
		if liveName != "" {
			title, detail, prog = liveName, liveDetail, liveProg
		}
		if hasLive {
			return archiveStateLive, cmp.Or(title, "Shell"), detail, prog
		}
	}
	return archiveStateExited, cmp.Or(title, "Shell"), detail, prog
}

// archivedEntryRunning reports whether any pane of archived entry e currently
// runs a foreground program on its host.
func (a *App) archivedEntryRunning(e archivedEntry) bool {
	if a.st == nil {
		return false
	}
	h, ok := a.archivedHost(e.Host)
	if !ok {
		return false
	}
	infos, _, listed := a.st.listOf(h)
	if !listed {
		return false
	}
	tab := e.Tab
	if liveTab, _ := a.st.findArchivedTab(h, e.ID); liveTab != nil {
		tab = *liveTab
	}
	for _, r := range tab.Roots() {
		for _, n := range layout.Leaves(r) {
			if n.SID == "" {
				continue
			}
			in, ok := infos[n.SID]
			if !ok || in.Exited {
				continue
			}
			p := &Pane{SID: n.SID, info: in}
			if p.running() {
				return true
			}
		}
	}
	return false
}

// findArchivedTab locates a server-present archived tab by ID on host h.
func (st *store) findArchivedTab(h *hostConn, tabID string) (*savedTab, *Workspace) {
	if st == nil || tabID == "" {
		return nil, nil
	}
	for _, w := range st.workspaces {
		if w.host != h {
			continue
		}
		for i := range w.archived {
			if w.archived[i].ID == tabID {
				return &w.archived[i], w
			}
		}
	}
	return nil, nil
}

// resolveArchivedTarget finds the workspace on host h where entry e should be
// restored: its original SpaceID, else matching ProjectDir, else No Project.
func (st *store) resolveArchivedTarget(h *hostConn, e archivedEntry) *Workspace {
	if st == nil {
		return nil
	}
	if w := st.byID(e.SpaceID); w != nil && w.host == h {
		return w
	}
	if e.ProjectDir != "" {
		for _, w := range st.workspaces {
			if w.host == h && w.Dir != "" && layout.SameDir(w.Dir, e.ProjectDir) {
				return w
			}
		}
	}
	if w := st.noProjectOf(h); w != nil {
		return w
	}
	if h == nil {
		return st.addWorkspace()
	}
	return nil
}

// restoreArchivedTab restores archived tab (hostID, tabID) back into its
// original project or No Project list, re-attaching to live host sessions
// (with full server scrollback) or recreating the saved split layout if all
// sessions exited while archived.
func (a *App) restoreArchivedTab(hostID, tabID string) bool {
	if a.st == nil {
		return false
	}
	st := a.st
	idx := st.archivedIndex(hostID, tabID)
	if idx < 0 {
		return false
	}
	entry := st.archived[idx]
	h, ok := a.archivedHost(entry.Host)
	if !ok {
		return false
	}
	if h != nil && (h.lay == nil || !h.lay.live || h.current() == nil) {
		return false
	}
	liveTab, srcW := st.findArchivedTab(h, entry.ID)
	state, _, _, _ := a.archivedEntryStatus(entry)
	dstW := st.resolveArchivedTarget(h, entry)
	if dstW == nil {
		dstW = srcW
	}
	if dstW == nil || dstW.offline() {
		return false
	}
	st.removeArchivedEntry(hostID, tabID)
	a.archiveOpen = false
	a.archiveDeleteKey = ""

	if liveTab != nil && srcW != nil && state == archiveStateLive {
		restored := cloneSavedTab(*liveTab)
		srcW.archived = slices.DeleteFunc(srcW.archived, func(t savedTab) bool { return t.ID == entry.ID })
		// When srcW != dstW (the project was recreated under a new ID),
		// removing from srcW.archived and appending to dstW moves the tab
		// on the next save.
		setRowOpen(openKey(dstW.host, dstW.ID), true)
		if o := dstW.app; o != nil {
			l := o.snapshot()
			l.Tabs = append(l.Tabs, restored)
			l.Active = len(l.Tabs) - 1
			o.reconcile(l)
			if o != a {
				o.front()
			}
			o.changed()
		} else {
			dstW.unkeep()
			dstW.layout.Tabs = append(dstW.layout.Tabs, restored)
			dstW.layout.Active = len(dstW.layout.Tabs) - 1
			a.showWorkspaceTab(dstW, len(dstW.layout.Tabs)-1)
		}
		st.save()
		st.syncBadge()
		st.redraw()
		return true
	}

	// All PTY sessions exited while archived: drop any stale entry from
	// srcW.archived first so it cannot resurface, then recreate the saved tab &
	// splits in its original project/directory with resume hints.
	if liveTab != nil && srcW != nil {
		srcW.archived = slices.DeleteFunc(srcW.archived, func(t savedTab) bool { return t.ID == entry.ID })
		st.save()
	}
	setRowOpen(openKey(dstW.host, dstW.ID), true)
	a.recreateArchivedTab(dstW, entry)
	st.syncBadge()
	st.redraw()
	return true
}

// recreateArchivedTab starts a fresh tab in w mirroring entry.Tab's name,
// working directories, split structure, and resume hints when the archived
// tab's original host sessions exited while archived.
func (a *App) recreateArchivedTab(w *Workspace, entry archivedEntry) {
	if w == nil || w.offline() {
		return
	}
	if w != a.ws {
		a.showWorkspace(w)
		if w.app != a {
			return
		}
	}
	leaves := layout.Leaves(entry.Tab.Root)
	firstDir := w.Dir
	if len(leaves) > 0 && leaves[0].Dir != "" {
		firstDir = leaves[0].Dir
	}
	if firstDir == "" {
		firstDir = a.homeOf(w.host)
	}
	tabName := entry.Tab.Name
	a.askOn(w.host, rex.LayoutCommand{Op: "tab.create", Space: w.ID, Dir: firstDir, Name: tabName, Focus: true}, func(res rex.LayoutCommand) {
		if res.Code != "" || res.New == "" {
			return
		}
		if len(leaves) > 0 {
			a.applyArchivedLeafMeta(w.host, res.New, leaves[0])
		}
		if entry.Tab.Root != nil && !entry.Tab.Root.IsLeaf() {
			a.recreateArchivedSplit(w, res.New, entry.Tab.Root)
		}
	})
}

// recreateArchivedSplit replays a saved binary split tree onto anchorSID on w.
func (a *App) recreateArchivedSplit(w *Workspace, anchorSID string, n *layout.Node) {
	if n == nil || n.IsLeaf() || n.A == nil || n.B == nil {
		return
	}
	rightLeaf := layout.FirstLeaf(n.B)
	dir := w.Dir
	paneName := ""
	if rightLeaf != nil {
		dir = cmp.Or(rightLeaf.Dir, dir)
		paneName = rightLeaf.Name
	}
	a.askOn(w.host, rex.LayoutCommand{
		Op: "pane.split", Space: w.ID, SID: anchorSID,
		Vertical: n.Vertical, Dir: dir, Name: paneName,
	}, func(res rex.LayoutCommand) {
		if res.Code != "" || res.New == "" {
			return
		}
		if rightLeaf != nil {
			a.applyArchivedLeafMeta(w.host, res.New, rightLeaf)
		}
		a.recreateArchivedSplit(w, anchorSID, n.A)
		a.recreateArchivedSplit(w, res.New, n.B)
	})
}

// applyArchivedLeafMeta applies a saved leaf's pane name and resume hint once
// its recreated pane is present in the window.
func (a *App) applyArchivedLeafMeta(h *hostConn, sid string, saved *layout.Node) {
	if saved == nil || sid == "" {
		return
	}
	p := a.paneOn(h, sid)
	if p == nil {
		return
	}
	if saved.Name != "" && p.Name == "" {
		p.Name = saved.Name
	}
	if saved.Prog != "" && p.resume == nil {
		p.resume = resumeHintFor(cmp.Or(p.info.Shell, "sh"), saved)
	}
}

// deleteArchivedTab permanently deletes an archived tab and ends any still-live
// PTY sessions on its host by emitting tab.close through diffLayout.
func (a *App) deleteArchivedTab(hostID, tabID string) {
	if a.st == nil {
		return
	}
	st := a.st
	st.removeArchivedEntry(hostID, tabID)
	a.archiveDeleteKey = ""
	h, _ := a.archivedHost(hostID)
	removedLive := false
	for _, w := range st.workspaces {
		if w.host != h {
			continue
		}
		before := len(w.archived)
		w.archived = slices.DeleteFunc(w.archived, func(t savedTab) bool { return t.ID == tabID })
		if len(w.archived) != before {
			removedLive = true
		}
	}
	if removedLive {
		st.save()
	}
	st.syncBadge()
	st.redraw()
}

// archivedWorkspacePanes counts total panes across w's archived tabs.
func archivedWorkspacePanes(w *Workspace) int {
	if w == nil {
		return 0
	}
	n := 0
	for _, t := range w.archived {
		for _, r := range t.Roots() {
			n += len(layout.Leaves(r))
		}
	}
	return n
}

// archivedPaneCount counts panes in a savedTab.
func archivedPaneCount(t layout.Tab) int {
	n := 0
	for _, r := range t.Roots() {
		n += len(layout.Leaves(r))
	}
	return max(n, 1)
}

// archivedOrigin formats "<Host> · <Project>" (or "<Project>" for local when
// single-host, or "No Project") for display in the Archived dialog.
func (a *App) archivedOrigin(e archivedEntry) string {
	proj := e.SpaceName
	if e.ProjectDir == "" {
		proj = locales.ProjectsNoProject()
	} else if a.st != nil {
		if w := a.st.byID(e.SpaceID); w != nil && w.Name != "" {
			proj = w.Name
		}
	}
	if e.Host == "" && len(a.hosts) == 0 {
		return proj
	}
	host := e.HostName
	if e.Host == "" {
		host = a.hostName(nil)
	} else if h := a.hostByID(e.Host); h != nil {
		host = a.hostName(h)
	}
	if host == "" {
		return proj
	}
	if proj == "" {
		return host
	}
	return host + " · " + proj
}
