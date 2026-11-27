---
name: DSK-013
description: "Desktop users can archive and restore tabs (project, No Project, local, and remote) while keeping live PTY sessions running on their hosts. Use when changing tab archiving, archived.json persistence, or the Archived dialog."
metadata:
  created: "2026-11-27"
  updated: "2026-11-27"
---

# DSK-013: Desktop tab archive and restore

Status: built (this PR).

This doc defines how the desktop client archives tabs out of the daily sidebar list and restores them later while preserving tab names, project assignments, split layouts, and live host terminal sessions. It extends [`MOD-002`](MOD-002-projects.md), [`DSK-006`](DSK-006-empty-states.md), and [`DSK-007`](DSK-007-sidebar-style.md).

## Problem

The desktop sidebar fills up with tabs that a user does not need in their daily working set right now, but still wants to recover later with their project context, split layout, and running shell or coding agent intact. The existing Close Tab action ends the underlying PTY sessions on the host.

## Goal

- Provide an Archive action on every desktop session row (project, No Project, local, and remote): a hover trailing Archive icon button on sidebar rows, an Archive Tab item in tab context menus, and Command Palette commands (`Archive Tab`, `Archived Tabs…`).
- Keep archived tabs' PTY sessions and 8 MiB server-side VT scrollback running on their host without sending `tab.close` or killing any process.
- Persist archived state across desktop restarts in a desktop-local `<GOREX_DIR>/archived.json` file without altering `layout.json`, `layout.jsonl`, `settings.json`, `hosts.json`, session IDs, or the `rex` wire protocol.
- Keep archived tabs completely quiet: excluded from active tab counts, project busy dots (`MOD-002`), attention dots, desktop notifications, Cmd+J jumps, and the macOS Dock badge.
- Provide a zero-clutter Archived entry in the sidebar footer opening a Glass Dialog (`DESIGN.md`) to inspect, restore, or permanently delete archived tabs.

## Non-goals

- Changing `internal/rex` wire protocol, `layout.json` schema, or requiring upgrades to existing remote `windrex-server` binaries.
- Syncing desktop archive state to iOS (`WindRex Go`) or the Web client in this slice.
- Writing raw terminal scrollback plaintext to disk in `archived.json`.

## Design

### Persistence (`archived.json` overlay)

Each host's `rex.Server` remains the single source of truth for its `layout.json` and PTY lifecycle. The desktop client stores archived tabs in `<GOREX_DIR>/archived.json` (written atomically with `0600` permissions), keyed by `(hostID, tabID)` where `hostID` is `""` for this machine and the `hosts.json` ID for a remote host, and `tabID` is the lasting `layout.Tab.ID`:

```json
{
  "version": 1,
  "tabs": [
    {
      "id": "019...",
      "host": "",
      "hostName": "This Mac",
      "spaceId": "abc123",
      "spaceName": "wind-rex",
      "projectDir": "/Users/alice/code/wind-rex",
      "archivedAt": "2026-11-27T16:00:00Z",
      "title": "dev",
      "detail": "~/code/wind-rex",
      "program": "zsh",
      "tab": { "id": "019...", "root": { "sid": "..." } }
    }
  ]
}
```

- **Read path (`takeLayout`, `takeHost`, `loaded`, `reconcileOn`)**: When the desktop takes a host's `layout.Store`, `splitArchived` partitions each workspace's `Tabs` into visible tabs (`w.layout.Tabs` / `a.tabs`) and archived tabs (`w.archived`), refreshing cached `archivedEntry.Tab` metadata from the host's authoritative layout.
- **Write path (`st.saved`, `st.savedOn`, `serverLayout`)**: Before `diffLayout(base, now)` runs, `serverLayout` merges `w.archived` back at each archived tab's relative position in `base` using `base`'s exact `savedTab` struct. As a result, `diffLayout` emits zero `tab.close` and zero spurious `tab.set` commands for archived tabs, `placeOrphans()` never resurrects archived sessions into No Project, and `restoreLost` continues to recover archived sessions across server crashes.
- **Backward compatibility**: Older desktop, iOS, Web, and remote Linux `windrex-server` builds ignore `archived.json` completely. Deleting `archived.json` cleanly unarchives every live tab with zero session loss.

### Lifecycle

1. **Archive**:
   - Never asks for close confirmation because archiving is non-destructive and reversible.
   - Detaches local UI panes via `disposePane(p, false)` (never `kill = true`), preserving every PTY and its server-side VT scrollback on the local or remote host.
   - Works on both active-workspace rows (`sidebarTab`) and non-shown expanded-workspace rows (`workspaceTabs` / `spaceTab`). Disabled on offline hosts (`w.offline()`).
   - Remaining active tabs in the workspace keep their exact relative order.
   - If the last active tab of a workspace is archived, the window switches to another workspace with active tabs (`freeAfter(w)`). If no active tabs remain in any workspace, the window stays open showing the `emptyNoTab` empty state (`DSK-006`) instead of closing the window. Opening an empty No Project workspace that still holds archived tabs does not auto-spawn a surprise shell.
2. **Restore**:
   - **Live tab on host (normal case)**: Removes the entry from `archived.json`, moves the `savedTab` from `w.archived` back into its original workspace (matching `SpaceID`, then `ProjectDir`, then No Project), reconciles/attaches live terminals to the still-running `SID`s (replaying full server scrollback and split/float layout), expands the project row, and selects the restored tab.
   - **Exited while archived**: If the host pruned the tab because its process exited while archived, Restore recreates the tab and its saved split tree in the original project/directory and attaches `resumeHintFor` (`Prog`/`Args`/`AgentSession`, e.g., `claude --continue`) to restored panes.
   - **Offline host**: Restore is disabled and dimmed with an offline indicator until the host reconnects.
3. **Permanent Delete & Close Project**:
   - Deleting an entry from the Archived dialog removes it from `archived.json` and `w.archived` and saves, so `diffLayout` emits `tab.close` to end its PTY sessions cleanly. If a program is still running in the archived tab, the dialog asks for inline confirmation (`Cancel` / `End & Delete`) first.
   - `Close Project…` includes archived panes in its confirmation count and clears that project's archived entries on confirmation.
4. **Silence while archived**:
   - Because `a.tabs` and `w.layout.Tabs` exclude archived tabs, `Tab.status`, `store.status`, `store.workingStatus` (collapsed-project busy dots), `store.hiddenNeeds`, `store.needCount` (Dock badge), `App.needing`, and desktop notifications automatically ignore archived tabs.

### UI/UX (`DESIGN.md`)

- **Session rows**: Every `sidebarTab` (including single-tab workspaces) and non-offline `spaceTab` shows a trailing `archive` icon button (`assets/icons/archive.svg`, SF Symbol `archivebox` on macOS) on hover. Both `tabMenu` and `spaceTab` context menus include `Archive Tab`.
- **Archived entry**: Sidebar footer gains a compact `archive` icon button beside New Project, displaying a muted count when archived tabs exist. Clicking it (or invoking `Archived Tabs…` in the Command Palette) opens a `ui.DialogBase` Glass Dialog (`glassPanel`, `radiusXXL`, padding `20`, `dialogShadow`, `k.backdrop`, `dialogEnter`).
- **Glass contrast**: All text and secondary details inside the Archived dialog use `k.text` and `k.textMuted` (never `k.textFaint`, verified by `TestGlassInk`).

## Alternatives

- **Server-side `layout.Store.Archived` + `tab.archive`/`tab.restore` wire commands**: Rejected for this slice because it alters both `layout.json`/`layout.jsonl` and the `rex` wire protocol (`docs/irreversible.md`), and would block remote archiving until every remote `windrex-server` in the fleet is upgraded.
- **Inline collapsible Archived section in the sidebar**: Rejected because a 236 DIP sidebar cannot cleanly display tab title, host/project provenance, split count, live/offline state, Restore, and Delete without crowding `No Project` and remote hosts.

## Migration

None. `archived.json` is created on first archive and absent files load as an empty archive list.

## Tests

- `archive_test.go`: atomic persistence across simulated desktop restart, live local and remote PTY survival + re-attach with scrollback, remaining tab order preservation, archiving the last active tab without closing the window, quiet busy/attention/Dock badge aggregation (including collapsed-project `workingStatus`), exited-tab layout recreation, permanent delete + running confirmation, and headless `ui.NewTester` coverage of hover buttons, context menus, footer entry, and the Archived dialog.
- Real-window Light and Dark screenshots via `GOREX_DEBUG` showing the sidebar hover Archive affordance and the Archived dialog.
- Touches `docs/invariants.md` items 1, 6, 8, and 9 (`TestDesignTokens`, `TestGlassInk`).
