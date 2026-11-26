package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"gorex/internal/directrelay"
	"gorex/internal/hosts"
	"gorex/internal/rex"
	"gorex/internal/rexapi"
	"gorex/locales"
)

// Relay Fleets discovery on Settings › Remote Hosts
// (docs/NET-010-relay-fleet-discovery.md): saved relays live in
// <GOREX_DIR>/relays.json (0600), query GET /api/hosts with optional TLS
// pinning, exclude this machine's own host_id, and allow 1-click import of
// fleet_auth hosts using DeriveFleetToken.

// discoverRelayHosts queries a relay's GET /api/hosts; tests replace it.
var discoverRelayHosts = directrelay.DiscoverHosts

// localDirectHostID reads this machine's local relay host_id from direct.json
// so the Relay Fleets list never offers to add this machine as a remote host.
// Tests replace it.
var localDirectHostID = func() string {
	c, err := rexapi.LoadDirect(rexapi.StateDir())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(c.HostID)
}

type relayDiscoveryState struct {
	loading bool
	loaded  bool
	err     func() string
	hosts   []directrelay.HostInfo
}

// relaysUI owns the Remote Hosts page's saved relay list and discovery state;
// the main thread owns it.
var relaysUI struct {
	loaded   bool
	relays   []hosts.Relay
	state    map[string]*relayDiscoveryState
	removing string
}

func resetRelaysUI() {
	relaysUI.loaded = false
	relaysUI.relays = nil
	relaysUI.state = nil
	relaysUI.removing = ""
}

func (a *App) ensureRelaysLoaded() {
	if !relaysUI.loaded {
		a.loadRelaysUI()
	}
}

func (a *App) loadRelaysUI() {
	list, err := hosts.LoadRelays(rex.Dir())
	if err != nil {
		log.Printf("gorex: loading relays: %v", err)
	}
	relaysUI.loaded = true
	relaysUI.relays = list
	if relaysUI.state == nil {
		relaysUI.state = map[string]*relayDiscoveryState{}
	}
	for _, r := range list {
		st := relaysUI.state[r.ID]
		if st == nil || (!st.loading && !st.loaded) {
			a.refreshRelay(r)
		}
	}
}

func (a *App) saveRelaysUI() {
	if err := hosts.SaveRelays(rex.Dir(), relaysUI.relays); err != nil {
		log.Printf("gorex: saving relays: %v", err)
	}
}

func (a *App) upsertRelay(target directrelay.FleetTarget, label string, discovered []directrelay.HostInfo) hosts.Relay {
	a.ensureRelaysLoaded()
	if relaysUI.state == nil {
		relaysUI.state = map[string]*relayDiscoveryState{}
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = target.DefaultLabel()
	}
	var saved hosts.Relay
	updated := false
	for i := range relaysUI.relays {
		if strings.EqualFold(strings.TrimRight(relaysUI.relays[i].URL, "/"), target.RelayURL) {
			relaysUI.relays[i].URL = target.RelayURL
			relaysUI.relays[i].Secret = target.Secret
			relaysUI.relays[i].CertPin = target.Pin
			if label != "" {
				relaysUI.relays[i].Label = label
			}
			saved = relaysUI.relays[i]
			updated = true
			break
		}
	}
	if !updated {
		saved = hosts.Relay{
			ID:      newHostID(),
			URL:     target.RelayURL,
			Label:   label,
			Secret:  target.Secret,
			CertPin: target.Pin,
		}
		relaysUI.relays = append(slices.Clone(relaysUI.relays), saved)
	}
	relaysUI.state[saved.ID] = &relayDiscoveryState{
		loaded: true,
		hosts:  slices.Clone(discovered),
	}
	a.saveRelaysUI()
	return saved
}

func (a *App) removeRelay(id string) {
	relaysUI.relays = slices.DeleteFunc(slices.Clone(relaysUI.relays), func(r hosts.Relay) bool { return r.ID == id })
	delete(relaysUI.state, id)
	if relaysUI.removing == id {
		relaysUI.removing = ""
	}
	a.saveRelaysUI()
}

func (a *App) refreshRelay(r hosts.Relay) {
	if relaysUI.state == nil {
		relaysUI.state = map[string]*relayDiscoveryState{}
	}
	st := relaysUI.state[r.ID]
	if st == nil {
		st = &relayDiscoveryState{}
		relaysUI.state[r.ID] = st
	}
	if st.loading {
		return
	}
	st.loading = true
	st.err = nil
	update := a.settingsUpdate()
	go func(relay hosts.Relay) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		list, err := discoverRelayHosts(ctx, relay.URL, relay.Secret, relay.CertPin)
		update(func() {
			cur := relaysUI.state[relay.ID]
			if cur == nil {
				return
			}
			cur.loading = false
			cur.loaded = true
			if err != nil {
				cur.err = fleetErrorFunc(err)
				return
			}
			cur.err = nil
			cur.hosts = list
		})
	}(r)
}

func fleetErrorFunc(err error) func() string {
	switch {
	case errors.Is(err, directrelay.ErrUnauthorized):
		return locales.HostsFleetAuthFailed
	case errors.Is(err, directrelay.ErrDiscoveryDisabled):
		return locales.HostsFleetDisabled
	case errors.Is(err, directrelay.ErrCertPinMismatch):
		return locales.HostsFleetPinMismatch
	default:
		return locales.HostsFleetUnreachable
	}
}

// relayHostAlreadySaved reports whether info on relay r is already present in
// the user's saved hosts (by RelayHostID or matching /h/<host_id> base URL).
func relayHostAlreadySaved(r hosts.Relay, info directrelay.HostInfo, saved []*hostConn) bool {
	wantBase, err := directrelay.RelayClientBaseURL(r.URL, info.ID)
	for _, h := range saved {
		if h.RelayHostID != "" && h.RelayHostID == info.ID {
			if h.RelayID == "" || h.RelayID == r.ID {
				return true
			}
		}
		if err == nil && rex.IsDirectTarget(h.Address) {
			if t, perr := rex.ParseDirectTarget(h.Address); perr == nil && strings.EqualFold(t.BaseURL, wantBase) {
				return true
			}
		}
	}
	return false
}

func uniqueHostName(base string, saved []*hostConn) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "host"
	}
	if !nameTaken(base, nil, saved) {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !nameTaken(cand, nil, saved) {
			return cand
		}
	}
}

// importRelayHost adds a single discovered fleet_auth host using its derived
// HMAC-SHA256 fleet token and starts connecting to it immediately.
func (a *App) importRelayHost(r hosts.Relay, info directrelay.HostInfo) bool {
	if !info.FleetAuth {
		return false
	}
	if localID := localDirectHostID(); localID != "" && info.ID == localID {
		return false
	}
	if relayHostAlreadySaved(r, info, a.hosts) {
		return false
	}
	pairURL, err := directrelay.FleetHostPairURL(r.URL, info.ID, r.Secret, r.CertPin, info.Name)
	if err != nil {
		return false
	}
	name := uniqueHostName(info.Name, a.hosts)
	a.addHost(savedHost{
		ID:          newHostID(),
		Name:        name,
		Address:     pairURL,
		RelayID:     r.ID,
		RelayHostID: info.ID,
	})
	return true
}

// importAllRelayHosts imports and connects every online fleet_auth host on
// relay r that is neither this machine nor already saved.
func (a *App) importAllRelayHosts(r hosts.Relay) int {
	st := relaysUI.state[r.ID]
	if st == nil {
		return 0
	}
	added := 0
	for _, info := range st.hosts {
		if a.importRelayHost(r, info) {
			added++
		}
	}
	return added
}

func (a *App) relaysSettings(c *ui.Context, k *colors) {
	if len(relaysUI.relays) == 0 {
		return
	}
	localID := localDirectHostID()
	settingsGroup(c, k, locales.HostsFleetTitle(), "", locales.HostsFleetNote(), func() {
		for _, r := range relaysUI.relays {
			st := relaysUI.state[r.ID]
			if st == nil {
				st = &relayDiscoveryState{}
			}
			importable := 0
			for _, info := range st.hosts {
				if info.FleetAuth && (localID == "" || info.ID != localID) && !relayHostAlreadySaved(r, info, a.hosts) {
					importable++
				}
			}
			groupRow(c, k, "relay-"+r.ID).Gap(8).Children(func() {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(28, 28).Radius(radiusM).Background(k.hover).Center().Shrink(0).Children(func() {
						glyphIcon(c, "globe", 16, k.accent)
					})
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, r.DisplayName()).FontSize(fontBody).FontWeight(600).TextColor(k.text).SingleLine().Ellipsis("…")
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							dot := k.busy
							statusText := locales.HostsFleetOnlineCount(len(st.hosts))
							switch {
							case st.loading:
								dot = k.attention
								statusText = locales.HostsFleetRefreshing()
							case st.err != nil:
								dot = k.danger
								statusText = locales.HostsFailed()
							}
							ui.Box(c).Size(7, 7).Radius(capsule(7)).Background(dot).Shrink(0)
							ui.Text(c, statusText+" · "+directrelay.SanitizeFleetURL(r.URL)).FontSize(fontFootnote).FontWeight(500).TextColor(k.textMuted).SingleLine().Ellipsis("…").MinWidth(0)
						})
					})
					if importable > 0 {
						if ui.PrimaryButton(c.Key("add-all"), locales.HostsFleetAddAll(importable)).Clicked() {
							a.importAllRelayHosts(r)
						}
					}
					if ui.Button(c.Key("refresh"), locales.HostsFleetRefresh()).Disabled(st.loading).Clicked() {
						a.refreshRelay(r)
					}
					if ui.Button(c.Key("remove"), locales.HostsRemove()).Disabled(relaysUI.removing == r.ID).Clicked() {
						relaysUI.removing = r.ID
					}
				})
				if st.err != nil {
					rowError(c, k, st.err())
				}
				for _, info := range st.hosts {
					isLocal := localID != "" && info.ID == localID
					isSaved := !isLocal && relayHostAlreadySaved(r, info, a.hosts)
					ui.Row(c.Key("host-"+info.ID)).Gap(8).Padding(6, 10).Radius(radiusM).Background(k.hover).AlignItems(ui.Center).Children(func() {
						ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
							ui.Text(c, info.Name).FontSize(fontBody).FontWeight(500).TextColor(k.text).SingleLine().Ellipsis("…")
							ui.Text(c, info.Path).Font(monoFamily()).FontSize(fontCaption).TextColor(k.textFaint).SingleLine().Ellipsis("…")
						})
						switch {
						case isLocal:
							fleetBadge(c, k, locales.HostsFleetThisMachine(), k.textMuted, k.panel)
						case isSaved:
							fleetBadge(c, k, locales.HostsFleetAdded(), k.accent, k.panelSel)
						case !info.FleetAuth:
							fleetBadge(c, k, locales.HostsFleetNeedsQR(), k.textMuted, k.panel)
						default:
							if ui.Button(c.Key("add-host"), locales.HostsFleetAddOne()).Label(locales.HostsFleetAddOne() + " " + info.Name).Clicked() {
								a.importRelayHost(r, info)
							}
						}
					})
				}
				if relaysUI.removing == r.ID {
					ui.Column(c).Gap(8).Padding(8, 10).Radius(radiusM).Background(k.hover).Children(func() {
						ui.Text(c, locales.HostsFleetRemoveAsk(r.DisplayName())).FontSize(fontSubhead).TextColor(k.text).LineHeight(1.4)
						ui.Row(c).Gap(8).Children(func() {
							if ui.Button(c.Key("remove-relay-yes"), locales.HostsRemove()).Clicked() {
								a.removeRelay(r.ID)
							}
							if ui.Button(c.Key("remove-relay-no"), locales.RemoteCancel()).Clicked() {
								relaysUI.removing = ""
							}
						})
					})
				}
			})
		}
	})
}

func fleetBadge(c *ui.Context, k *colors, text string, fg, bg ui.Color) {
	ui.Box(c).Radius(capsule(9)).Background(bg).Padding(2, 8).Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(fontCaption).FontWeight(600).TextColor(fg)
	})
}
