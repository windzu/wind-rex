package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"gorex/locales"
)

// archivedFooterButton draws the subtle Archived entry in the sidebar footer:
// an archive icon alone when empty, or a compact icon + count capsule when
// archived tabs exist.
func (a *App) archivedFooterButton(c *ui.Context, k *colors) {
	n := a.archivedCount()
	label := locales.SidebarArchived()
	tipText := label
	if n > 0 {
		tipText = locales.SidebarArchivedCount(n)
	}
	if n == 0 {
		if iconButtonWith(c.Key("sidebar-archived"), k, "archive", label, 30, 16, a.archiveOpen, nil).Tooltip(tipText).Clicked() {
			a.later(c, a.openArchiveDialog)
		}
		return
	}
	b := ui.Row(c.Key("sidebar-archived")).Height(30).Padding(0, 8).Gap(4).AlignItems(ui.Center).
		Radius(capsule(30)).Cursor(ui.CursorPointer).Role(ui.RoleButton).Label(label).Tooltip(tipText)
	col := k.iconMuted
	switch {
	case b.Pressed():
		b.Background(k.pressed)
		col = k.text
	case b.Hovered() || a.archiveOpen:
		b.Background(k.hover)
		col = k.text
	}
	if fadeColors {
		b.Transition(hoverFade)
	}
	b.Children(func() {
		glyphIcon(c, "archive", 15, col).Shrink(0)
		ui.Text(c, fmt.Sprint(n)).FontSize(fontFootnote).FontWeight(500).TextColor(col).SingleLine().Shrink(0)
	})
	if b.Clicked() {
		a.later(c, a.openArchiveDialog)
	}
}

// archiveDialog draws the Glass Dialog listing archived tabs with Restore and
// permanent Delete actions (docs/DSK-013-archive-tabs.md). All text on glass
// uses k.text or k.textMuted (never k.textFaint, checked by TestGlassInk).
func (a *App) archiveDialog(c *ui.Context, k *colors) {
	if !a.archiveOpen || a.st == nil {
		return
	}
	was := a.archiveOpen
	entries := a.st.archivedEntries()
	_, winH := c.Size()
	maxH := max(winH-80, 260)
	ui.DialogBase(c, &a.archiveOpen, func(backdrop, panel ui.Element) {
		dialogEnter(c, backdrop, panel)
		backdrop.Background(k.backdrop)
		dialogShadow(glassPanel(panel.Width(500).MaxHeight(maxH).Padding(20).Radius(radiusXXL), k).Border(1, k.panelBorder)).
			Role(ui.RoleDialog).Label(locales.ArchiveDialogTitle())
		ui.Column(c).Gap(12).MinHeight(0).Children(func() {
			// Header
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
				glyphIcon(c, "archive", 16, k.textMuted).Shrink(0)
				ui.Text(c, locales.ArchiveDialogTitle()).FontSize(fontTitle).FontWeight(700).TextColor(k.text).SingleLine()
				if len(entries) > 0 {
					ui.Text(c, fmt.Sprint(len(entries))).FontSize(fontCaption).FontWeight(600).TextColor(k.textMuted).
						Padding(2, 6).Radius(radiusS).Background(k.hover).SingleLine().Shrink(0)
				}
				ui.Spacer(c)
				if iconButton(c.Key("archive-close"), k, "x", locales.ArchiveClose(), 26, 14).Tooltip(locales.ArchiveClose()).Clicked() {
					a.archiveOpen = false
				}
			})

			// Filter field when the archive list grows beyond a few items.
			if len(entries) > 4 {
				textField(c.Key("archive-search"), k, &a.archiveQuery, fieldCompact).
					Placeholder(locales.ArchiveSearchPlaceholder()).Shrink(0)
			}

			query := strings.ToLower(strings.TrimSpace(a.archiveQuery))
			var shown []archivedEntry
			for _, e := range entries {
				if query == "" || a.archiveMatches(e, query) {
					shown = append(shown, e)
				}
			}

			if len(shown) == 0 {
				msg := locales.ArchiveEmpty()
				note := locales.ArchiveEmptyHint()
				if len(entries) > 0 && query != "" {
					msg = locales.ArchiveNoMatches(a.archiveQuery)
					note = ""
				}
				ui.Column(c).Padding(24, 12).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Text(c, msg).FontSize(fontBody).FontWeight(500).TextColor(k.textMuted).SingleLine()
					if note != "" {
						ui.Text(c, note).FontSize(fontFootnote).TextColor(k.textMuted)
					}
				})
				return
			}

			ui.Scroll(c.Key("archive-list")).MinHeight(0).Shrink(1).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					now := time.Now()
					for _, e := range shown {
						a.archiveDialogRow(c, k, e, now)
					}
				})
			})
		})
	})
	if was && !a.archiveOpen {
		a.archiveDeleteKey = ""
		if t := a.tab(); t != nil && a.focusReq == nil {
			a.focusReq = t.Focus
		}
	}
}

func (a *App) archiveMatches(e archivedEntry, query string) bool {
	_, title, detail, _ := a.archivedEntryStatus(e)
	hay := strings.ToLower(title + " " + detail + " " + a.archivedOrigin(e))
	return strings.Contains(hay, query)
}

// archiveDialogRow draws one archived tab row inside the Archived dialog.
func (a *App) archiveDialogRow(c *ui.Context, k *colors, e archivedEntry, now time.Time) {
	key := archiveKey(e.Host, e.ID)
	state, title, _, prog := a.archivedEntryStatus(e)
	origin := a.archivedOrigin(e)
	panes := archivedPaneCount(e.Tab)
	metaParts := []string{origin}
	if panes > 1 {
		metaParts = append(metaParts, locales.ArchivePaneCount(panes))
	}
	if !e.ArchivedAt.IsZero() {
		when, _ := ago(e.ArchivedAt, now)
		metaParts = append(metaParts, when)
	}
	meta := strings.Join(metaParts, " · ")

	row := ui.Row(c.Key("arch-row-"+key)).Padding(8, 10).Gap(10).AlignItems(ui.Center).Radius(radiusM)
	if row.Hovered() {
		row.Background(k.hover)
	}
	row.Children(func() {
		ui.Box(c).Size(tileSlot, tileH+4).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
			drawTile(p, ui.Rect{X: r.X + 1, Y: r.Y + 2, W: tileW, H: tileH}, prog, true)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(fontBody).FontWeight(500).TextColor(k.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				switch state {
				case archiveStateLive:
					ui.Box(c).Size(6, 6).Radius(capsule(6)).Background(k.busy).Shrink(0).Tooltip(locales.ArchiveStatusLive())
				case archiveStateOffline:
					ui.Text(c, locales.ProjectsOfflineLabel()).FontSize(fontCaption).TextColor(k.textMuted).SingleLine().Shrink(0)
				case archiveStateExited:
					ui.Text(c, locales.ArchiveStatusExited()).FontSize(fontCaption).TextColor(k.textMuted).SingleLine().Shrink(0)
				}
			})
			ui.Text(c, meta).FontSize(fontFootnote).TextColor(k.textMuted).SingleLine().Ellipsis("…").MinWidth(0)
		})

		if a.archiveDeleteKey == key {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
				if cancelButton(c.Key("cancel-del"), locales.SessionCancel()).Clicked() {
					a.archiveDeleteKey = ""
				}
				if dangerButton(c.Key("confirm-del"), k, locales.ArchiveDeleteConfirm()).Clicked() {
					hostID, tabID := e.Host, e.ID
					a.later(c, func() { a.deleteArchivedTab(hostID, tabID) })
				}
			})
			return
		}

		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			restoreLabel := locales.ArchiveRestore()
			rb := ui.Row(c.Key("restore-"+key)).Height(28).Padding(0, 10).Gap(4).AlignItems(ui.Center).
				Radius(radiusM).Border(1, k.panelBorder).Role(ui.RoleButton).Label(restoreLabel + " " + title)
			if state == archiveStateOffline {
				dimmed(rb, 0.45)
				rb.Tooltip(locales.ArchiveRestoreOffline())
			} else {
				rb.Cursor(ui.CursorPointer).Tooltip(restoreLabel)
				if rb.Pressed() {
					rb.Background(k.pressed)
				} else if rb.Hovered() {
					rb.Background(k.hover)
				}
			}
			rb.Children(func() {
				glyphIcon(c, "rotate-ccw", 12, k.text).Shrink(0)
				ui.Text(c, restoreLabel).FontSize(fontSubhead).FontWeight(500).TextColor(k.text).SingleLine()
			})
			if state != archiveStateOffline && rb.Clicked() {
				hostID, tabID := e.Host, e.ID
				a.later(c, func() { a.restoreArchivedTab(hostID, tabID) })
			}

			delLabel := locales.ArchiveDelete()
			del := iconButton(c.Key("delete-"+key), k, "trash-2", delLabel+" "+title, 28, 13).Tooltip(delLabel)
			if del.Clicked() {
				entry := e
				if a.archivedEntryRunning(entry) {
					a.archiveDeleteKey = key
				} else {
					a.later(c, func() { a.deleteArchivedTab(entry.Host, entry.ID) })
				}
			}
		})
	})
}
