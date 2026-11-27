---
name: DSK-012
description: "Desktop Antigravity CLI sessions display their conversation title, show a concise temporary label with project context before a title exists, and move full paths into hover tooltips. Use when changing Antigravity session naming, tab/sidebar tooltips, or gorex skill setup for agy."
metadata:
  created: "2026-11-27"
  updated: "2026-11-27"
---

# DSK-012: Antigravity session naming and path display

Status: built (this PR).

This doc defines how the desktop app names Antigravity CLI (`agy`) sessions across tabs, the sidebar, pane headers, tooltips, and the window title, and how `gorex skill setup` connects Antigravity's official `title` command to WindRex. It extends [`MOD-002`](MOD-002-projects.md) and [`REF-001`](REF-001-cli.md).

## Problem

When a user opens multiple Antigravity CLI (`agy`) sessions on the desktop, every session displays as `Antigravity` followed by its working directory path (`state.go`, `Pane.label`). Sessions inside the same project look identical, and long paths crowd narrow sidebar rows, tabs, and the window title.

By default, Antigravity CLI does not emit OSC 0 or OSC 2 terminal title sequences unless a `title` command is configured in `~/.gemini/antigravity-cli/settings.json`. Without that bridge, WindRex never receives `conversation_title` from Antigravity. Even when a title is emitted, raw formats that include a redundant `Antigravity` prefix or trailing directory path still clutter the label.

## Goal

- Connect Antigravity CLI's official `title` command interface to WindRex via `gorex __internal title --agent agy` and `gorex skill setup --agent agy`, without overwriting an existing user `title` command.
- Show the Antigravity conversation title as the session name across tabs, the sidebar, pane headers, and the window title once available, and keep it synchronized across tab switches, project switches, and server reconnects.
- Before a conversation title exists, show `Antigravity` as a concise temporary name with the project name (or folder basename) as secondary text, never a full or truncated directory path.
- Show the full working path and title details in hover tooltips on sidebar rows, tabs, and pane headers.
- Keep manual WindRex tab names (`Tab.Name`) at highest priority, and keep non-Antigravity sessions unchanged.

## Non-goals

- Reading undocumented or internal Antigravity storage files directly from disk.
- Changing how shells, Claude Code, Codex, Devin, omp, or other non-Antigravity programs name their panes.
- Modifying iOS or Web client title rendering in this slice.

## Design

### Behavior

1. **Title priority for an Antigravity session**:
   - A manual WindRex tab name (`Tab.Name`) always wins.
   - A manual pane name (`Pane.Name`) wins over automatic titles and omits the inline directory path.
   - An Antigravity conversation title from the terminal OSC title (`Pane.title` or `SessionInfo.Title`) is used as the primary session name with empty inline detail.
   - Before a conversation title exists, the primary name is `Antigravity`, and the secondary detail is the WindRex project name (or the working directory's folder name when outside a project, omitted for home or root).
2. **Window title**: When the window shows a named project whose name matches the tab's secondary project detail, `windowTitle` omits the duplicate detail so the window title reads `<Project> — Antigravity` instead of repeating the project name.
3. **Hover tooltips**: Sidebar tab rows (`sidebarTab`, `spaceTab`), top tab strip items (`tabItem`), and pane headers (`paneHeader`) carry a tooltip with the full title, Antigravity conversation title (if overridden by a manual name), and full home-relative working path (`displayDir`) without ellipsis truncation.
4. **Switching and reconnecting**: The session server tracks the latest OSC 2 title in `SessionInfo.Title` and replays `\x1b]2;<title>\x07` in attach snapshots. When a session's server-reported title changes while the client held the previous title, `App.take` synchronizes `Pane.title`. Unshown project tabs resolve titles through `savedTabFaceIn` using the same rule.
5. **Antigravity official title hook**: `gorex skill setup --agent agy` installs a `title` command block in `~/.gemini/antigravity-cli/settings.json` running `gorex __internal title --agent agy`. On each Antigravity state update, Antigravity pipes its JSON state payload to stdin. When `conversation_title` is non-empty, the command prints it on one sanitized line. When `conversation_title` is empty, the command prints `Antigravity` so switching to a new untitled conversation resets the previous OSC 2 title. If the user already configured another `title` command, setup leaves it untouched. `gorex skill remove` removes only GoRex's own `title` block.

### Model

| State | Primary label | Secondary inline detail | Hover tooltip |
| --- | --- | --- | --- |
| Manual tab rename (`Tab.Name`) | `Tab.Name` | empty | `Tab.Name`, optional conversation title, full path |
| Manual pane rename (`Pane.Name`) | `Pane.Name` | empty | `Pane.Name`, optional conversation title, full path |
| Conversation title received | Cleaned conversation title | empty | Conversation title, `Antigravity · <full path>` |
| Before conversation title (in project `P`) | `Antigravity` | `P` | `Antigravity · P`, full path |
| Before conversation title (no project) | `Antigravity` | Folder basename (or empty at home/root) | `Antigravity · <folder>`, full path |

`agyConversationTitle` cleans raw OSC titles for Antigravity panes:

1. Rejects empty strings, shell wrapper titles, and `user@host:path` shell prompts.
2. Strips a redundant leading `Antigravity`, `Antigravity CLI`, `Google Antigravity`, or `agy` prefix (optionally preceded by a leading symbol) when followed by a separator (`—`, `–`, `-`, `:`, `|`, `·`), or treats a bare program name as no title.
3. Strips trailing path segments separated by ` — `, ` – `, ` - `, ` | `, or ` · ` when the trailing part is a path or matches the session directory.
4. Rejects a remaining value that is itself a path or a bare Antigravity program name.

### Code

- `agy_title.go`: `isAgyInfo`, `agyConversationTitle`, `agyProjectDetail`, `displayDir`, `paneTooltip`, and `tabTooltip`.
- `state.go`: `Pane.labelIn`, `Tab.labelIn`, `App.tabLabel`, and title synchronization in `App.take`.
- `sidebar.go`, `projects_sidebar.go`, `tabs.go`, `view.go`, `workspaceview.go`: route desktop tab, sidebar, pane header, and window title rendering through the shared label and tooltip helpers (`savedTabFaceIn`, `App.tabLabel`, `tabTooltip`, `paneTooltip`).
- `internal/cli/sessionstart.go`: dispatch `gorex __internal title --agent agy` and sanitize `conversation_title` from stdin JSON.
- `internal/cli/hooks.go`, `internal/cli/skill.go`: manage the `title` block in `~/.gemini/antigravity-cli/settings.json` for `skill setup`, `skill status`, and `skill remove`.

## Alternatives

- **Pass `--settings` in `internal/rex/agents_unix.go`**: Rejected because older Antigravity CLI versions reject unknown flags and `--settings` would override a user's existing `title` block if the file changed after detection.
- **Print empty stdout when `conversation_title` is absent**: Rejected because Antigravity CLI emits no OSC 2 sequence on empty stdout, leaving a prior conversation's title stuck after `/new`.

## Migration

Existing `~/.gemini/antigravity-cli/settings.json` files without a `title` block receive one on the next `gorex skill setup`. Files with a user-defined `title` block remain untouched. No saved WindRex layout format or wire protocol field changes.

## Tests

- `agy_title_test.go`: unit tests for before-title fallback, conversation title extraction and prefix/path stripping, tab switch and reconnect sync (`App.take` and `savedTabFaceIn`), manual tab and pane rename precedence, window title deduplication, tooltips, and non-Antigravity stability.
- `app_test.go`: updated `TestLabels` expectations for Antigravity plus non-Antigravity checks.
- `internal/cli/skill_test.go`: unit tests for `gorex __internal title --agent agy` (with title, without title, multiline/control characters, invalid JSON) and `skill setup` / `status` / `remove` managing `~/.gemini/antigravity-cli/settings.json` while preserving user `title` commands.
- Headless `ui.NewTester` tests in `agy_title_test.go` and real-window light and dark screenshots via `GOREX_DEBUG`.
- Touches `docs/invariants.md` item 1 (sessions and titles preserved across reconnect and workspace switches).
