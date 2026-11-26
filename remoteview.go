package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"gorex/internal/rexapi"
	"gorex/locales"
)

// The Server page's remote access and local API controls: Direct TLS /
// Self-Hosted Relay access, Connect iPhone QR card, and the Local API & LAN
// listener.

// remoteUI is the Server page's state; the main thread owns it.
var remoteUI struct {
	stop   chan struct{} // closed to stop the refresh; non-nil while it runs
	err    func() string // what went wrong with the last clipboard copy, said when drawn
	copied string        // what was copied just now
	// lan is the gateway's listener on the local network, as its files
	// say (rexapi.LoadLAN, LoadLANStatus); lanListen and lanAdd are what
	// is typed in its fields, and lanErr what was wrong with the last.
	lan       rexapi.LANConfig
	lanStatus rexapi.LANStatus
	lanListen string
	lanEdited bool
	lanAdd    string
	lanErr    string
	focusLAN  bool
	// direct is the gateway's native TLS direct listener and outbound
	// stateless relay settings (rexapi.LoadDirect, LoadDirectStatus),
	// shared on disk with "gorex rexapi direct".
	direct            rexapi.DirectConfig
	directStatus      rexapi.DirectStatus
	directHostName    string
	directRelayURL    string
	directRelaySecret string
	directRelayPin    string
	directPublicURL   string
	directListen      string
	directEdited      bool
	directErr         string
	focusDirect       bool
	pairQR            *hostPairQR
}

// showSection chooses a settings section and refreshes its Rex listener
// status while shown.
func (a *App) showSection(id string) {
	if id != "keyboard" && shortcutsUI.recording != nil {
		a.stopRecording()
	}
	if settingsUI.section == id {
		return
	}
	stopRemotePoll()
	remoteUI.pairQR = nil
	settingsUI.section = id
	stopServerInfoPoll()
	if id == "server" {
		fetchLAN()
		fetchDirect()
		a.startRemotePoll()
		startServerInfoPoll()
	}
	if id == "hosts" {
		a.loadRelaysUI()
	}
}

func stopRemotePoll() {
	if remoteUI.stop != nil {
		close(remoteUI.stop)
	}
	page := &remoteUI
	page.stop, page.err, page.copied = nil, nil, ""
	page.focusLAN = false
	page.direct, page.directStatus = rexapi.DirectConfig{}, rexapi.DirectStatus{}
	page.directHostName = ""
	page.directRelayURL, page.directRelaySecret, page.directRelayPin = "", "", ""
	page.directPublicURL, page.directListen = "", ""
	page.directEdited, page.directErr, page.focusDirect = false, "", false
}

func (a *App) startRemotePoll() {
	win := settingsUI.win
	if remoteUI.stop != nil || win == nil {
		return
	}
	stop := make(chan struct{})
	remoteUI.stop = stop
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			win.Update(func() {
				select {
				case <-stop:
					return
				default:
					fetchLAN()
					fetchDirect()
				}
			})
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
}

// fetchLAN reads the local network listener's settings and state, which
// the server's gateway follows.
func fetchLAN() {
	dir := rexapi.StateDir()
	c, _ := rexapi.LoadLAN(dir)
	remoteUI.lan = c
	if !remoteUI.lanEdited {
		remoteUI.lanListen = c.Listen
	}
	remoteUI.lanStatus = rexapi.LoadLANStatus(dir)
}

// fetchDirect reads the direct TLS / self-hosted relay settings and runtime
// status from disk, keeping the GUI in sync with "gorex rexapi direct".
func fetchDirect() {
	dir := rexapi.StateDir()
	c, _ := rexapi.LoadDirect(dir)
	remoteUI.direct = c
	if !remoteUI.directEdited {
		remoteUI.directHostName = c.HostName
		remoteUI.directRelayURL = c.RelayURL
		remoteUI.directRelaySecret = c.RelaySecret
		remoteUI.directRelayPin = c.RelayCertPin
		remoteUI.directPublicURL = c.PublicURL
		remoteUI.directListen = c.Listen
	}
	remoteUI.directStatus = rexapi.LoadDirectStatus(dir)
}

// saveDirect writes direct.json for the server's gateway to pick up within a
// second. It preserves CLI-owned fields (Token, HostID, CertFile, KeyFile)
// from disk so a concurrent CLI rotation or custom cert is not clobbered.
func saveDirect(c rexapi.DirectConfig) bool {
	dir := rexapi.StateDir()
	if cur, err := rexapi.LoadDirect(dir); err == nil {
		cur.Enabled = c.Enabled
		cur.HostName = strings.TrimSpace(c.HostName)
		cur.RelayURL = strings.TrimSpace(c.RelayURL)
		cur.RelaySecret = strings.TrimSpace(c.RelaySecret)
		cur.RelayCertPin = strings.TrimSpace(c.RelayCertPin)
		cur.RelayTrust = c.RelayTrust
		cur.PublicURL = strings.TrimSpace(c.PublicURL)
		cur.Listen = strings.TrimSpace(c.Listen)
		c = cur
	} else {
		c.HostName = strings.TrimSpace(c.HostName)
		c.RelayURL = strings.TrimSpace(c.RelayURL)
		c.RelaySecret = strings.TrimSpace(c.RelaySecret)
		c.RelayCertPin = strings.TrimSpace(c.RelayCertPin)
		c.PublicURL = strings.TrimSpace(c.PublicURL)
		c.Listen = strings.TrimSpace(c.Listen)
		c.Token = ""
		c.HostID = ""
	}
	if c.PublicURL != "" && c.Listen == "" {
		c.Listen = fmt.Sprintf(":%d", rexapi.DirectPort)
	}
	if err := rexapi.SaveDirect(dir, c); err != nil {
		remoteUI.directErr = err.Error()
		return false
	}
	remoteUI.directErr = ""
	// Read back: SaveDirect populates Token and HostID when missing.
	if saved, err := rexapi.LoadDirect(dir); err == nil {
		c = saved
	}
	remoteUI.direct = c
	remoteUI.directHostName = c.HostName
	remoteUI.directRelayURL = c.RelayURL
	remoteUI.directRelaySecret = c.RelaySecret
	remoteUI.directRelayPin = c.RelayCertPin
	remoteUI.directPublicURL = c.PublicURL
	remoteUI.directListen = c.Listen
	remoteUI.directEdited = false
	return true
}

// rotateDirectToken generates a new 256-bit pairing token in direct.json so
// the gateway disconnects existing direct/relay clients and issues a new QR.
func rotateDirectToken() bool {
	dir := rexapi.StateDir()
	cur, err := rexapi.LoadDirect(dir)
	if err != nil {
		remoteUI.directErr = err.Error()
		return false
	}
	cur.Token = rexapi.NewLANToken()
	if err := rexapi.SaveDirect(dir, cur); err != nil {
		remoteUI.directErr = err.Error()
		return false
	}
	remoteUI.directErr = ""
	if saved, err := rexapi.LoadDirect(dir); err == nil {
		cur = saved
	}
	remoteUI.direct = cur
	return true
}

// directReady reports whether Direct TLS or self-hosted relay access is
// active and ready to pair with a QR code.
func directReady(c rexapi.DirectConfig, st rexapi.DirectStatus) bool {
	if !c.Enabled || c.Token == "" || st.Error != "" {
		return false
	}
	if c.RelayURL != "" && c.HostID != "" {
		return st.RelayConnected && st.RelayError == ""
	}
	if c.PublicURL != "" {
		return st.Listening != ""
	}
	return false
}

// directState returns the human-readable status label and indicator color for
// the Direct / Relay Access section.
func directState(c rexapi.DirectConfig, st rexapi.DirectStatus, k *colors) (string, ui.Color) {
	switch {
	case !c.Enabled:
		return locales.RemoteDirectOff(), k.exited
	case st.Error != "":
		return locales.RemoteDirectError(st.Error), k.danger
	case c.RelayURL != "" && st.RelayError != "":
		return locales.RemoteDirectRelayError(st.RelayError), k.danger
	case c.RelayURL != "" && st.RelayConnected:
		return locales.RemoteDirectRelayConnected(), k.busy
	case st.Listening != "":
		return locales.RemoteDirectListening("https://" + st.Listening), k.busy
	case c.RelayURL != "":
		return locales.RemoteDirectRelayConnecting(), k.attention
	case c.PublicURL == "" && c.Listen == "":
		return locales.RemoteDirectUnconfigured(), k.attention
	default:
		return locales.RemoteDirectWaiting(), k.attention
	}
}

// connectPhoneCard is the above-the-fold iPhone pairing entry on Settings ›
// Server (docs/IOS-023-pairing-entry.md). It reflects Direct TLS / Self-Hosted
// Relay readiness (with Local Network fallback) so the user can open the
// WindRex pairing QR code immediately without scrolling.
func (a *App) connectPhoneCard(c *ui.Context, k *colors) {
	page := &remoteUI
	label, dot := directState(page.direct, page.directStatus, k)
	ready := directReady(page.direct, page.directStatus)
	lan := localRexPair()
	if !ready && !page.direct.Enabled && lan != "" {
		label, dot = locales.RemoteLanListening("http://"+page.lanStatus.Listening), k.busy
	}
	settingsGroup(c, k, "", "", "", func() {
		groupRow(c, k, "connect-phone").Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(40, 40).Radius(radiusM).Background(k.accent).Center().Shrink(0).Children(func() {
					glyphIcon(c, "globe", 22, ui.RGB(255, 255, 255))
				})
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, locales.RemoteConnectPhoneTitle()).FontSize(fontTitle).FontWeight(600).TextColor(k.text)
					statusLine(c, k, dot, label)
				})
				switch {
				case ready:
					if ui.PrimaryButton(c.Key("direct-phone-qr"), locales.RemoteDirectRelayQR()).Clicked() {
						a.openDirectPairQR()
					}
				case lan != "":
					if ui.PrimaryButton(c.Key("lan-phone-qr"), locales.HostsRexQR()).Clicked() {
						a.openLocalPairQR()
					}
				}
			})
			switch {
			case ready:
				rowHint(c, k, locales.RemoteConnectPhoneReady())
			case !page.direct.Enabled && lan != "":
				rowHint(c, k, locales.RemoteConnectPhoneReady())
			case !page.direct.Enabled:
				rowHint(c, k, locales.RemoteConnectPhoneOff())
			}
			if page.err != nil {
				rowError(c, k, page.err())
			}
		})
	})
}

// directSettings renders the Direct / Relay Access group on Settings › Server
// (docs/NET-005-direct-relay.md): enable switch, live status, QR and token
// rotation actions, self-hosted relay fields, and direct TLS fields.
func (a *App) directSettings(c *ui.Context, k *colors) {
	page := &remoteUI
	conf, st := page.direct, page.directStatus
	label, dot := directState(conf, st, k)
	ready := directReady(conf, st)
	settingsGroup(c, k, locales.RemoteDirectTitle(), "", locales.RemoteDirectNote(), func() {
		on := conf.Enabled
		groupRow(c, k, "direct-switch").Gap(10).Children(func() {
			apiLine(c, k, locales.RemoteDirectSwitch(), false, func() {
				statusLine(c, k, dot, label).Selectable()
			}, func() {
				if ui.Switch(c.Key("direct-enable"), &on).Label(locales.RemoteDirectSwitch()).Changed() {
					next := conf
					next.Enabled = on
					if on && page.directEdited {
						next.HostName = page.directHostName
						next.RelayURL = page.directRelayURL
						next.RelaySecret = page.directRelaySecret
						next.RelayCertPin = page.directRelayPin
						next.PublicURL = page.directPublicURL
						next.Listen = page.directListen
					}
					saveDirect(next)
				}
			})
			// Not dimmed: its color is its meaning.
			if page.directErr != "" {
				rowError(c, k, page.directErr)
			}
			if ready || conf.Token != "" {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if ready {
						if ui.PrimaryButton(c.Key("direct-qr-open"), locales.RemoteDirectRelayQR()).Clicked() {
							a.openDirectPairQR()
						}
					}
					if conf.Token != "" {
						if ui.Button(c.Key("direct-rotate"), locales.RemoteDirectRotateToken()).Clicked() {
							rotateDirectToken()
						}
					}
				})
			}
		})
		if st.CertPin != "" {
			factRow(c, k, locales.RemoteDirectCertPin(), st.CertPin)
		}
		defaultName, _, _ := a.hostIdentity()
		groupRow(c, k, "direct-relay").Gap(10).Children(func() {
			rowLine(c, k, locales.RemoteDirectHostName(), func() {
				if ui.TextInput(c.Key("direct-host-name"), &page.directHostName).Placeholder(defaultName).
					Label(locales.RemoteDirectHostName()).Width(260).Changed() {
					page.directEdited, page.directErr = true, ""
				}
			})
			rowLine(c, k, locales.RemoteDirectRelayURL(), func() {
				if ui.TextInput(c.Key("direct-relay-url"), &page.directRelayURL).Placeholder("https://relay.example.com:7443").
					Label(locales.RemoteDirectRelayURL()).Font(monoFamily()).Width(260).Changed() {
					page.directEdited, page.directErr = true, ""
				}
			})
			rowLine(c, k, locales.RemoteDirectRelayToken(), func() {
				if ui.TextInput(c.Key("direct-relay-secret"), &page.directRelaySecret).Password().Placeholder(locales.RemoteDirectOptional()).
					Label(locales.RemoteDirectRelayToken()).Font(monoFamily()).Width(260).Changed() {
					page.directEdited, page.directErr = true, ""
				}
			})
			rowLine(c, k, locales.RemoteDirectRelayPin(), func() {
				if ui.TextInput(c.Key("direct-relay-pin"), &page.directRelayPin).Placeholder(locales.RemoteDirectOptional()).
					Label(locales.RemoteDirectRelayPin()).Font(monoFamily()).Width(260).Changed() {
					page.directEdited, page.directErr = true, ""
				}
			})
			trust := conf.RelayTrust
			apiLine(c, k, locales.RemoteDirectRelayTrust(), false, func() {
				rowHint(c, k, locales.RemoteDirectRelayTrustNote())
			}, func() {
				if ui.Switch(c.Key("direct-relay-trust"), &trust).Label(locales.RemoteDirectRelayTrust()).Changed() {
					next := conf
					next.RelayTrust = trust
					if page.directEdited {
						next.HostName = page.directHostName
						next.RelayURL = page.directRelayURL
						next.RelaySecret = page.directRelaySecret
						next.RelayCertPin = page.directRelayPin
						next.PublicURL = page.directPublicURL
						next.Listen = page.directListen
					}
					saveDirect(next)
				}
			})
			rowHint(c, k, locales.RemoteDirectRelayNote())
		})
		groupRow(c, k, "direct-tls").Gap(10).Children(func() {
			rowLine(c, k, locales.RemoteDirectPublicURL(), func() {
				if ui.TextInput(c.Key("direct-public-url"), &page.directPublicURL).Placeholder("https://203.0.113.10:7443").
					Label(locales.RemoteDirectPublicURL()).Font(monoFamily()).Width(260).Changed() {
					page.directEdited, page.directErr = true, ""
				}
			})
			rowLine(c, k, locales.RemoteDirectListen(), func() {
				if ui.TextInput(c.Key("direct-listen"), &page.directListen).Placeholder(":7443").
					Label(locales.RemoteDirectListen()).Font(monoFamily()).Width(160).Changed() {
					page.directEdited, page.directErr = true, ""
				}
			})
			rowHint(c, k, locales.RemoteDirectTLSNote())
		})
		actionsRow := groupRow(c, k, "direct-actions")
		if page.focusDirect {
			page.focusDirect = false
			actionsRow.ScrollIntoView()
		}
		actionsRow.Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				if ui.Button(c.Key("direct-save"), locales.RemoteLanSave()).
					Label(locales.RemoteDirectTitle() + ": " + locales.RemoteLanSave()).
					Disabled(!page.directEdited).Clicked() {
					next := conf
					next.HostName = page.directHostName
					next.RelayURL = page.directRelayURL
					next.RelaySecret = page.directRelaySecret
					next.RelayCertPin = page.directRelayPin
					next.PublicURL = page.directPublicURL
					next.Listen = page.directListen
					saveDirect(next)
				}
			})
		})
	})
}

// saveLAN writes the listener's settings, which the server's gateway
// applies within a second. The page owns only the switch, the address
// and the networks: the token and the legacy list are taken from the
// file as it is now, never from the page's copy, which a gorex rexapi lan
// token rotate or legacy add may have left behind.
func saveLAN(c rexapi.LANConfig) bool {
	dir := rexapi.StateDir()
	if cur, err := rexapi.LoadLAN(dir); err == nil {
		cur.Enabled, cur.Listen, cur.Allow = c.Enabled, c.Listen, c.Allow
		c = cur
	} else {
		c.Token = "" // SaveLAN keeps the one on disk, or makes one
	}
	if err := rexapi.SaveLAN(dir, c); err != nil {
		remoteUI.lanErr = err.Error()
		return false
	}
	remoteUI.lanErr = ""
	// Read back: SaveLAN gives the settings their pairing token.
	if saved, err := rexapi.LoadLAN(dir); err == nil {
		c = saved
	}
	remoteUI.lan = c
	return true
}

// localAPI is the server page's group of Rex's Local API: the gateway's
// revisions and version, the primary endpoint's URL to copy, each other
// listener's address and whom it lets in, then the listener of Rex's apps on the local network with its
// controls, which shows off too and is not listed again among the others.
func (a *App) localAPI(c *ui.Context, k *colors) {
	info, lan := settingsUI.apiInfo, remoteUI.lan
	settingsGroup(c, k, locales.RemoteApiTitle(), "", locales.RemoteApiNote(), func() {
		if settingsUI.apiErr != "" {
			apiRow(c, k, locales.RemoteApiUrl(), false, func() {
				ui.Text(c, locales.HostApiOff()).FontSize(fontFootnote).TextColor(k.textFaint)
			}, nil)
		} else if settingsUI.apiLoaded {
			factRow(c, k, locales.HostApiTitle(), locales.HostApiRevisions(info.APIRevision, info.ServerRevision, info.MinRevision))
			factRow(c, k, locales.HostApiVersion(), info.RexVersion)
		}
		for _, l := range info.Listeners {
			if lanListener(l, lan) {
				continue
			}
			if l.Primary {
				apiRow(c, k, locales.RemoteApiUrl(), false, func() {
					ui.Text(c, l.Endpoint).Font(monoFamily()).FontSize(fontFootnote).TextColor(k.textFaint).SingleLine().Ellipsis("…").
						Tooltip(l.Endpoint).Selectable()
				}, func() {
					if iconButton(c.Key("copy-api"), k, "copy", copyLabel("api"), 26, 15).Tooltip(copyLabel("api")).Clicked() {
						copyText("api", l.Endpoint)
					}
				})
				continue
			}
			addr := l.Endpoint
			if len(l.Addresses) > 0 {
				addr = l.Addresses[0]
			}
			label, open := admissionLabel(l)
			apiRow(c, k, addr, true, func() {
				switch {
				case open:
					statusLine(c, k, k.attention, label)
				case label != "":
					ui.Text(c, label).FontSize(fontFootnote).TextColor(k.textFaint)
				}
			}, nil)
		}
		a.lanRow(c, k)
	})
}

// lanRow is the Local API group's row of Rex's apps on the local network:
// the switch and its state, the address it listens on and the networks
// let in.
func (a *App) lanRow(c *ui.Context, k *colors) {
	page := &remoteUI
	conf, st := page.lan, page.lanStatus
	label, dot := locales.RemoteLanOff(), k.exited
	switch {
	case conf.Enabled && st.Error != "":
		label, dot = locales.RemoteLanError(st.Error), k.danger
	case conf.Enabled && st.Listening != "":
		label, dot = locales.RemoteLanListening("http://"+st.Listening), k.busy
	case conf.Enabled:
		label, dot = locales.RemoteLanWaiting(), k.attention
	}
	on := conf.Enabled
	row := groupRow(c, k, "lan").Gap(10)
	if page.focusLAN {
		page.focusLAN = false
		row.ScrollIntoView()
	}
	row.Children(func() {
		apiLine(c, k, locales.RemoteLanSwitch(), false, func() {
			statusLine(c, k, dot, label).Selectable()
		}, func() {
			if ui.Switch(c.Key("lan"), &on).Label(locales.RemoteLanSwitch()).Changed() {
				next := conf
				next.Enabled = on
				if on {
					next = next.WithDefaults()
					if next.Listen == "" {
						page.lanErr = locales.RemoteLanNoAddress()
						return
					}
					page.lanListen, page.lanEdited = next.Listen, false
				}
				saveLAN(next)
			}
		})
		// Not dimmed: its color is its meaning.
		if page.lanErr != "" {
			rowError(c, k, page.lanErr)
		}
		if lan := localRexPair(); lan != "" {
			ui.Row(c).Children(func() {
				if ui.Button(c.Key("local-rex-qr"), locales.HostsRexQR()).Clicked() {
					a.openLocalPairQR()
				}
			})
		} else if conf.Enabled && st.Running && st.Error == "" && st.Listening != "" && len(conf.Allow) != 0 {
			rowHint(c, k, locales.RemoteLanPairAddress())
		}
		more := ui.Column(c).Gap(10)
		if !conf.Enabled {
			dimmed(more, 0.5)
		}
		more.Children(func() {
			rowLine(c, k, locales.RemoteLanListenOn(), func() {
				if ui.TextInput(c.Key("lan-listen"), &page.lanListen).Placeholder("192.168.1.20:7297").
					Label(locales.RemoteLanAddress()).Font(monoFamily()).Width(200).Changed() {
					page.lanEdited = true
				}
				if ui.Button(c.Key("lan-save"), locales.RemoteLanSave()).Disabled(!page.lanEdited).Clicked() {
					next := conf
					next.Listen = strings.TrimSpace(page.lanListen)
					if saveLAN(next) {
						page.lanEdited = false
					}
				}
			})
			ui.Text(c, locales.RemoteLanNetworks()).FontSize(fontBody).FontWeight(500).TextColor(k.text)
			if len(conf.Allow) == 0 {
				rowHint(c, k, locales.RemoteLanNoNetworks())
			}
			for _, n := range conf.Allow {
				ui.Row(c.Key("net-" + n)).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, n).Font(monoFamily()).FontSize(fontCallout).TextColor(k.text).Grow(1).MinWidth(0)
					if ui.Button(c.Key("rm"), locales.RemoteLanRemove()).Clicked() {
						next := conf
						next.Allow = slices.DeleteFunc(slices.Clone(conf.Allow), func(x string) bool { return x == n })
						saveLAN(next)
					}
				})
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("lan-add"), &page.lanAdd).Placeholder(locales.RemoteLanAddPlaceholder()).Grow(1)
				if ui.Button(c.Key("lan-add-btn"), locales.RemoteLanAdd()).Disabled(strings.TrimSpace(page.lanAdd) == "").Clicked() {
					p, err := rexapi.ParseNetwork(page.lanAdd)
					if err != nil {
						page.lanErr = err.Error()
						return
					}
					next := conf
					if !slices.Contains(next.Allow, p.String()) {
						next.Allow = append(slices.Clone(next.Allow), p.String())
					}
					if saveLAN(next) {
						page.lanAdd = ""
					}
				}
			})
		})
		rowHint(c, k, locales.RemoteLanNote())
	})
}

// apiRow is a row of the Local API group: its line, as apiLine draws it.
func apiRow(c *ui.Context, k *colors, title string, mono bool, under, control func()) {
	groupRow(c, k, title).Children(func() { apiLine(c, k, title, mono, under, control) })
}

// apiLine is a Local API row's first line: its title, in the terminal's
// font when mono, what under shows below it, and control at the right.
func apiLine(c *ui.Context, k *colors, title string, mono bool, under, control func()) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			t := ui.Text(c, title).FontSize(fontBody).FontWeight(500).TextColor(k.text).SingleLine().Ellipsis("…")
			if mono {
				t.Font(monoFamily()).Selectable()
			}
			under()
		})
		if control != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(control)
		}
	})
}

// lanListener is that l is the local network's listener, which lanRow
// shows. An anonymous one at its address while it is off is --listen's.
func lanListener(l rexapi.Listener, lan rexapi.LANConfig) bool {
	return l.Transport == "tcp" && l.Admission == rexapi.AdmissionAnonymous && lan.Enabled && l.Endpoint == lan.Listen
}

// admissionLabel says whom a listener lets in, "" when the gateway does
// not say, and a value it does not know as it is. open is an anonymous
// TCP listener: one that lets in anyone who reaches it.
func admissionLabel(l rexapi.Listener) (label string, open bool) {
	switch l.Admission {
	case rexapi.AdmissionPeer:
		return locales.RemoteApiPeer(), false
	case rexapi.AdmissionAnonymous:
		return locales.RemoteApiAnyone(), l.Transport == "tcp"
	}
	return l.Admission, false
}

// serverMessage is an error of the server without its package prefix.
func serverMessage(err error) string {
	s := strings.TrimPrefix(err.Error(), "rex: ")
	if s == "" {
		return locales.RemoteSomethingWrong()
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// monoFamily is the font of keys and addresses: the terminal's.
func monoFamily() string {
	if len(ghostty.Families) > 0 {
		return ghostty.Families[0]
	}
	return "JetBrains Mono"
}

// copyText copies s, and says so on the button named by what for a while.
// It does not clear the clipboard afterwards.
func copyText(what, s string) { copyWith(what, s, mygo.ClipboardOptions{}) }

func copyWith(what, s string, opts mygo.ClipboardOptions) {
	if err := mygo.Clipboard.Write(transfer.TextData(s), opts); err != nil {
		msg := err.Error()
		remoteUI.err = func() string { return locales.RemoteCopyFailed(msg) }
		return
	}
	remoteUI.copied = what
	win := settingsUI.win
	go func() {
		time.Sleep(1500 * time.Millisecond)
		win.Update(func() {
			if remoteUI.copied == what {
				remoteUI.copied = ""
			}
		})
	}()
}

// copyLabel is a copy button's label.
func copyLabel(what string) string {
	if remoteUI.copied == what {
		return locales.RemoteCopied()
	}
	return locales.RemoteCopy()
}

// rowError says what went wrong, under a row's first line.
func rowError(c *ui.Context, k *colors, msg string) {
	ui.Text(c, msg).FontSize(fontFootnote).TextColor(k.danger).LineHeight(1.4).MaxWidth(settingsHintW)
}

// statusLine is a dot of a state's color and its label.
func statusLine(c *ui.Context, k *colors, dot ui.Color, label string) ui.Element {
	var text ui.Element
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(7, 7).Radius(capsule(7)).Background(dot).Shrink(0)
		text = ui.Text(c, label).FontSize(fontFootnote).FontWeight(500).TextColor(k.textMuted).MinWidth(0)
	})
	return text
}
