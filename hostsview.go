package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"gorex/internal/directrelay"
	"gorex/internal/rex"
	"gorex/locales"
)

// The Remote Hosts page of the settings window: the hosts this machine
// connects to over Direct TLS or Relay, their state, and the sheet that adds one.

// hostsUI is the page's state; the main thread owns it.
var hostsUI struct {
	adding bool // the sheet shows
	// The sheet's fields. The address carries a Bearer token: its field hides
	// it, and it is forgotten once the host is saved or the sheet closes.
	address, name, autoName string
	formErr                 func() string // what is wrong, said when drawn
	stage                   addStage
	addingFleet             bool               // true while checking a #r= Relay Fleet URL
	cancel                  context.CancelFunc // stops the attempt under way
	removing                string             // the id of the host Remove asks about
	renaming                string             // the id of the host whose alias is edited
	alias                   string             // the alias as it is typed
	aliasFocus              bool               // the alias field had the focus
	aliasTaken              string             // the alias refused, another host's
}

type addStage int

const (
	addForm       addStage = iota // the fields
	addConnecting                 // trying the address
)

// openSettingsAt opens the settings window at a section.
func (a *App) openSettingsAt(id string) {
	a.openSettings()
	a.showSection(id)
	if w := settingsUI.win; w != nil {
		w.Update(func() {})
	}
}

// openAddHost opens the sheet that adds a host, its fields empty.
func (a *App) openAddHost() {
	cancelAddHost()
	page := &hostsUI
	page.adding, page.addingFleet, page.address, page.name, page.autoName = true, false, "", "", ""
	page.formErr = nil
}

// cancelAddHost stops an attempt under way and goes back to the fields.
func cancelAddHost() {
	page := &hostsUI
	if page.cancel != nil {
		page.cancel()
		page.cancel = nil
	}
	page.stage = addForm
	page.addingFleet = false
}

// closeAddHost closes the sheet, forgetting the address.
func closeAddHost() {
	cancelAddHost()
	page := &hostsUI
	page.adding, page.addingFleet, page.address, page.name, page.autoName, page.formErr = false, false, "", "", "", nil
}

// sameDirectHost reports whether a and b are Direct TLS / Relay pairing URLs
// pointing at the same host endpoint (ignoring fragment token or display name).
func sameDirectHost(a, b string) bool {
	if !rex.IsDirectTarget(a) || !rex.IsDirectTarget(b) {
		return false
	}
	ta, erra := rex.ParseDirectTarget(a)
	tb, errb := rex.ParseDirectTarget(b)
	return erra == nil && errb == nil && ta.BaseURL == tb.BaseURL
}

// validateHost returns what is wrong with a host to add, "" if nothing.
func validateHost(address, name string, hosts []*hostConn) string {
	address, name = strings.TrimSpace(address), strings.TrimSpace(name)
	if directrelay.IsFleetURL(address) {
		if _, err := directrelay.ParseFleetURL(address); err != nil {
			if errors.Is(err, directrelay.ErrInvalidFleetPin) {
				return locales.HostsBadDirectPin()
			}
			return locales.HostsBadFleetAddress()
		}
		return ""
	}
	direct := rex.IsDirectTarget(address)
	if direct {
		t, err := rex.ParseDirectTarget(address)
		if err != nil {
			if errors.Is(err, rex.ErrDirectInvalidPin) {
				return locales.HostsBadDirectPin()
			}
			return locales.HostsBadDirectAddress()
		}
		if name == "" {
			name = t.DefaultName()
		}
	}
	switch {
	case address == "":
		return locales.HostsEnterAddress()
	case !direct && !validAddress(address):
		return locales.HostsBadDirectAddress()
	case name == "":
		return locales.HostsEnterName()
	}
	for _, h := range hosts {
		if h.Address == address || sameDirectHost(h.Address, address) {
			return locales.HostsAlreadySaved()
		}
	}
	if nameTaken(name, nil, hosts) {
		return locales.HostsNameTaken()
	}
	return ""
}

// nameTaken tells whether a host of hosts but h goes by name, as its name
// or its alias.
func nameTaken(name string, h *hostConn, hosts []*hostConn) bool {
	return slices.ContainsFunc(hosts, func(o *hostConn) bool {
		return o != h && (strings.EqualFold(o.Name, name) || o.Alias != "" && strings.EqualFold(o.Alias, name))
	})
}

// submitHost checks the sheet's fields, and tries the host (or Relay Fleet
// URL): it is saved once it answers.
func (a *App) submitHost() {
	page := &hostsUI
	if page.stage != addForm {
		return
	}
	address, name, hosts := strings.TrimSpace(page.address), strings.TrimSpace(page.name), a.hosts
	if directrelay.IsFleetURL(address) {
		target, err := directrelay.ParseFleetURL(address)
		if err != nil {
			if errors.Is(err, directrelay.ErrInvalidFleetPin) {
				page.formErr = locales.HostsBadDirectPin
			} else {
				page.formErr = locales.HostsBadFleetAddress
			}
			return
		}
		if name == "" {
			name = target.DefaultLabel()
			page.name = name
		}
		ctx, cancel := context.WithCancel(context.Background())
		page.stage, page.addingFleet, page.cancel, page.formErr = addConnecting, true, cancel, nil
		update := a.settingsUpdate()
		go func() {
			list, derr := discoverRelayHosts(ctx, target.RelayURL, target.Secret, target.Pin)
			update(func() {
				if ctx.Err() != nil {
					return
				}
				page.cancel = nil
				cancel()
				if derr != nil {
					page.stage, page.addingFleet, page.formErr = addForm, false, fleetErrorFunc(derr)
					return
				}
				closeAddHost()
				a.upsertRelay(target, name, list)
			})
		}()
		return
	}
	if name == "" && rex.IsDirectTarget(address) {
		if t, err := rex.ParseDirectTarget(address); err == nil {
			name = t.DefaultName()
			page.name = name
		}
	}
	if validateHost(address, name, hosts) != "" {
		page.formErr = func() string { return validateHost(address, name, hosts) }
		return
	}
	s := savedHost{ID: newHostID(), Name: name, Address: address}
	ctx, cancel := context.WithCancel(context.Background())
	page.stage, page.cancel, page.formErr = addConnecting, cancel, nil
	// The connection test is the settings window's: it goes on, and saves the
	// host, though the terminal window closes.
	update := a.settingsUpdate()
	report := func(fn func()) {
		update(func() {
			if ctx.Err() == nil {
				fn()
			}
		})
	}
	go pairHost(ctx, s, report, func() {
		page.cancel = nil
		cancel()
		closeAddHost()
		a.addHost(s)
	})
}

// settingsUpdate returns how the settings window's work changes the state:
// on the main thread, through the settings window, drawing the terminal
// window too when it is open. Call it on the main thread.
func (a *App) settingsUpdate() func(func()) {
	set := settingsUI.win
	return func(fn func()) {
		if set == nil {
			a.post(fn) // tests: no window
			return
		}
		set.Update(func() {
			fn()
			if w := a.win; w != nil {
				w.Update(func() {})
			}
		})
	}
}

// pairHost tests connecting to a host to add: when it answers, done runs;
// otherwise the sheet returns to the form with the error message.
func pairHost(ctx context.Context, s savedHost, report func(func()), done func()) {
	var logged failureLog
	c, _, why, _ := connectHost(ctx, s.Address)
	switch {
	case c != nil:
		c.Close()
		report(done)
	case ctx.Err() != nil:
		return
	default:
		logged.log(s.Name, why)
		report(func() {
			hostsUI.stage, hostsUI.formErr, hostsUI.cancel = addForm, why, nil
		})
	}
}

func (a *App) hostsSettings(c *ui.Context, k *colors) {
	page := &hostsUI
	a.ensureRelaysLoaded()
	settingsGroup(c, k, "", locales.HostsNote(), "", func() {
		if len(a.hosts) == 0 {
			groupRow(c, k, nil).Children(func() {
				ui.Text(c, locales.HostsNone()).FontSize(fontBody).TextColor(k.textFaint)
			})
		}
		for _, h := range a.hosts {
			a.hostRow(c, k, h)
		}
	})
	ui.Row(c).Children(func() {
		if ui.Button(c, locales.HostsAdd()).Clicked() {
			a.openAddHost()
		}
	})
	a.relaysSettings(c, k)
	if page.adding {
		a.addHostSheet(c, k)
	}
}

// hostRow is a saved host: its icon, a menu that chooses it, its state,
// Retry while it is not connected, and Remove, which asks first.
func (a *App) hostRow(c *ui.Context, k *colors, h *hostConn) {
	page := &hostsUI
	label, detail, up := hostStatus(h)
	sessions := a.sessionsOn(h)
	groupRow(c, k, h.ID).Gap(8).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			// Its icon is the menu that chooses it.
			iconButton(c.Key("icon"), k, h.remoteGlyph(), locales.HostsIcon()+", "+h.label(), 28, 18).Shrink(0).Tooltip(locales.HostsIcon()).
				Menu(func(m *ui.Menu) { a.hostIconMenu(m, h) })
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				if page.renaming == h.ID {
					a.hostAliasField(c, k, h)
				} else {
					ui.Text(c, h.label()).FontSize(fontBody).FontWeight(600).TextColor(k.text).SingleLine().Ellipsis("…")
				}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(7, 7).Radius(capsule(7)).Background(hostDot(k, h)).Shrink(0)
					ui.Text(c, label).FontSize(fontFootnote).FontWeight(500).TextColor(k.textMuted)
					if up && h.hello.Host.Model != "" {
						ui.Text(c, h.hello.Host.Model).FontSize(fontFootnote).TextColor(k.textFaint).SingleLine().Ellipsis("…").MinWidth(0)
					}
				})
			})
			ui.Text(c, locales.HostsSessions(sessions)).FontSize(fontFootnote).TextColor(k.textFaint).Shrink(0)
			if !up && ui.Button(c.Key("retry"), locales.HostsRetry()).Clicked() {
				h.poke()
			}
			// Another host's alias, being edited, is kept first, as
			// moving the focus away keeps it.
			if ui.Button(c.Key("rename"), locales.HostsRename()).Disabled(page.renaming == h.ID).Clicked() &&
				(page.renaming == "" || a.commitHostRename()) {
				page.renaming, page.alias = h.ID, h.label()
			}
			if ui.Button(c.Key("remove"), locales.HostsRemove()).Disabled(page.removing == h.ID).Clicked() {
				page.removing = h.ID
			}
		})
		// Said until the alias is typed again.
		if page.renaming == h.ID && page.aliasTaken != "" && page.alias == page.aliasTaken {
			ui.Text(c, locales.HostsNameTaken()).FontSize(fontSubhead).TextColor(k.danger).LineHeight(1.3)
		}
		rowHint(c, k, detail)
		if up {
			// How the link runs goes under the state, once measured;
			// asking starts here, off the UI thread, while the row shows.
			a.hostDiag(c, k, h)
		}
		if page.removing != h.ID {
			return
		}
		ui.Column(c).Gap(8).Padding(8, 10).Radius(radiusM).Background(k.hover).Children(func() {
			ui.Text(c, locales.HostsRemoveAsk(h.label())).FontSize(fontSubhead).TextColor(k.text).LineHeight(1.4)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c.Key("remove-yes"), locales.HostsRemove()).Clicked() {
					page.removing = ""
					a.removeHost(h)
				}
				if ui.Button(c.Key("remove-no"), locales.RemoteCancel()).Clicked() {
					page.removing = ""
				}
			})
		})
	})
}

// hostDiag draws how the link to a connected host runs: the transport
// as a tag, then the host address and the link's numbers, each once
// measured. Asking starts here, off the UI thread, while the row shows.
// Call it on the main thread.
func (a *App) hostDiag(c *ui.Context, k *colors, h *hostConn) {
	a.refreshHostPath(h)
	tag := locales.HostsDiagTagDirect()
	var segs []string
	if h.routeKind == rex.RouteLANDirect {
		tag = locales.HostsDiagTagLANDirect()
		if h.routeHost != "" {
			segs = append(segs, h.routeHost)
		}
	} else if rex.IsDirectTarget(h.Address) {
		if t, err := rex.ParseDirectTarget(h.Address); err == nil {
			if t.IsRelay {
				tag = locales.HostsDiagTagRelay()
			}
			segs = append(segs, t.Host)
		}
	}
	if h.connectMs > 0 {
		segs = append(segs, locales.HostsDiagConnect(h.connectMs))
	}
	if h.pingMs >= 0 {
		segs = append(segs, locales.HostsDiagPing(h.pingMs))
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Radius(capsule(9)).Background(k.panelSel).Padding(2, 6).Children(func() {
			ui.Text(c, tag).FontSize(fontCaption).FontWeight(600).TextColor(k.accent)
		})
		if len(segs) > 0 {
			ui.Text(c, strings.Join(segs, " · ")).FontSize(fontFootnote).TextColor(k.textFaint).SingleLine().Ellipsis("…")
		}
	})
}

// hostPathFresh is how long a host's measured ping keeps; an older one,
// or none, is asked again while the row shows.
var hostPathFresh = 30 * time.Second

// hostDiagTimeout bounds a ping measurement.
const hostDiagTimeout = 8 * time.Second

// hostPing measures the control round trip to the host; tests replace it.
var hostPing = func(c *rex.Client, timeout time.Duration) (time.Duration, error) {
	return c.Ping(timeout)
}

// refreshHostPath pings h's control connection, not while the last measure
// is fresh nor while an ask is under way; the answer lands on the main
// thread. Call it on the main thread.
func (a *App) refreshHostPath(h *hostConn) {
	cl := h.current()
	if cl == nil || h.pathAsking || time.Since(h.pathAt) < hostPathFresh {
		return
	}
	h.pathAsking = true
	update := a.settingsUpdate()
	go func() {
		ping := -1
		if d, err := hostPing(cl, hostDiagTimeout); err == nil {
			ping = int(d.Milliseconds())
		}
		update(func() {
			h.pathAsking = false
			// Asked, answered or not: another comes once this is stale.
			h.pathAt = time.Now()
			if h.current() != cl {
				return // the answer is of a connection gone since
			}
			if ping >= 0 {
				h.pingMs = ping
			}
		})
	}()
}

// hostAliasField edits the name h shows by here, as tabs are renamed:
// Enter or moving the focus away keeps it, Escape goes back, and an empty
// alias, or the host's own name, shows that name again. An alias another
// host goes by keeps the field open, and says so.
func (a *App) hostAliasField(c *ui.Context, k *colors, h *hostConn) {
	page := &hostsUI
	in := textField(c, k, &page.alias, fieldCompact).Placeholder(h.Name).WidthPercent(100).AutoFocus()
	renameKeys(in, page.alias, &page.aliasFocus, func(keep bool) {
		if keep {
			a.commitHostRename()
		} else {
			closeHostRename()
		}
	})
}

// commitHostRename keeps the alias being edited, and closes its field;
// one another host goes by keeps the field open, and is false.
func (a *App) commitHostRename() bool {
	page := &hostsUI
	if h := a.hostByID(page.renaming); h != nil && !a.setHostAlias(h, page.alias) {
		page.aliasTaken, page.aliasFocus = page.alias, false
		return false
	}
	closeHostRename()
	return true
}

// closeHostRename closes the alias field, keeping nothing of it.
func closeHostRename() {
	page := &hostsUI
	page.renaming, page.alias, page.aliasFocus, page.aliasTaken = "", "", false, ""
}

// setHostAlias gives h the alias it shows by in every window; its
// connection and its terminals stay. An alias another host goes by is
// refused, and false.
func (a *App) setHostAlias(h *hostConn, alias string) bool {
	alias = strings.TrimSpace(alias)
	if alias == h.Name {
		alias = ""
	}
	if alias == h.Alias {
		return true
	}
	if alias != "" && nameTaken(alias, h, a.hosts) {
		return false
	}
	h.mu.Lock()
	h.Alias = alias
	h.mu.Unlock()
	a.saveHosts()
	if a.st != nil {
		a.st.redraw()
	}
	return true
}

// hostDot is the color of a host's state.
func hostDot(k *colors, h *hostConn) ui.Color {
	switch h.state {
	case hostConnected:
		return k.busy
	case hostFailed:
		return k.danger
	}
	return k.attention
}

// addHostSheet is the sheet that adds a remote host by its Direct TLS or
// Self-Hosted Relay pairing URL.
func (a *App) addHostSheet(c *ui.Context, k *colors) {
	page := &hostsUI
	was := page.adding
	ui.DialogBase(c, &page.adding, func(backdrop, panel ui.Element) {
		dialogEnter(c, backdrop, panel)
		backdrop.Background(k.backdrop)
		dialogShadow(glassPanel(panel.Width(440).Padding(20).Gap(12).Radius(radiusXXL), k).Border(1, k.panelBorder)).Role(ui.RoleDialog).Label(locales.HostsAddTitle())
		ui.Text(c, locales.HostsAddTitle()).FontSize(fontTitle).FontWeight(700).TextColor(k.text)
		switch page.stage {
		case addConnecting:
			msg := locales.HostsConnectingTo(strings.TrimSpace(page.name))
			if page.addingFleet {
				msg = locales.HostsFleetChecking(strings.TrimSpace(page.name))
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(7, 7).Radius(capsule(7)).Background(k.attention)
				ui.Text(c, msg).FontSize(fontBody).TextColor(k.textMuted)
			})
		default:
			ui.Text(c, locales.HostsAddNote()).FontSize(fontSubhead).TextColor(k.textMuted).LineHeight(1.3)
			field := func(label string, input func() ui.Element) bool {
				changed := false
				ui.Column(c).Gap(4).Children(func() {
					ui.Text(c, label).FontSize(fontSubhead).FontWeight(600).TextColor(k.textMuted)
					if input().Changed() {
						page.formErr = nil
						changed = true
					}
				})
				return changed
			}
			if field(locales.HostsAddress(), func() ui.Element {
				return ui.TextInput(c.Key("address"), &page.address).Password().Font(monoFamily()).
					Placeholder(locales.HostsAddressPlaceholder()).Label(locales.HostsAddress()).AutoFocus()
			}) {
				if directrelay.IsFleetURL(page.address) {
					if ft, err := directrelay.ParseFleetURL(page.address); err == nil {
						if def := ft.DefaultLabel(); def != "" && (strings.TrimSpace(page.name) == "" || page.name == page.autoName) {
							page.name, page.autoName = def, def
						}
					}
				} else if t, err := rex.ParseDirectTarget(page.address); err == nil {
					if def := t.DefaultName(); def != "" && (strings.TrimSpace(page.name) == "" || page.name == page.autoName) {
						page.name, page.autoName = def, def
					}
				}
			}
			if directrelay.IsFleetURL(page.address) {
				if ft, err := directrelay.ParseFleetURL(page.address); err == nil {
					ui.Text(c, locales.HostsFleetDetected(ft.RelayURL)).FontSize(fontSubhead).TextColor(k.textMuted).Margin(-6, 0, 0, 0).SingleLine().Ellipsis("…")
				}
			} else if rex.IsDirectTarget(page.address) {
				if t, err := rex.ParseDirectTarget(page.address); err == nil {
					hint := locales.HostsDirectDetected(t.DisplayAddress())
					if t.IsRelay {
						hint = locales.HostsRelayDetected(t.DisplayAddress())
					}
					ui.Text(c, hint).FontSize(fontSubhead).TextColor(k.textMuted).Margin(-6, 0, 0, 0).SingleLine().Ellipsis("…")
				}
			}
			if field(locales.HostsName(), func() ui.Element {
				return ui.TextInput(c.Key("name"), &page.name).Placeholder(locales.HostsNamePlaceholder()).Label(locales.HostsName())
			}) {
				page.autoName = ""
			}
			if page.formErr != nil {
				ui.Text(c, page.formErr()).FontSize(fontSubhead).TextColor(k.danger).LineHeight(1.3)
			}
		}
		ui.Row(c).Gap(8).Justify(ui.End).Padding(4, 0, 0, 0).Children(func() {
			if ui.Button(c.Key("cancel"), locales.RemoteCancel()).Clicked() {
				closeAddHost()
			}
			if page.stage == addForm && ui.Button(c.Key("add"), locales.RemoteAdd()).Clicked() {
				a.submitHost()
			}
		})
	})
	// Dismissed by Escape or the backdrop: stop trying.
	if was && !page.adding {
		closeAddHost()
	}
}
