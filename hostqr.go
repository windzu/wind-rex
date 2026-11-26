package main

import (
	"net/url"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"rsc.io/qr"

	"gorex/internal/rexapi"
	"gorex/locales"
)

// The settings window owns this pairing QR dialog.
type hostPairQR struct {
	host, address            string
	image                    ui.ImageSource
	open, copied, copyFailed bool
	local                    bool
	// directTLS is this machine's native TLS direct / self-hosted relay
	// pairing code (rexapi.DirectConfig.PairURL). It carries the Bearer
	// pairing token in its fragment, so the token stays inside the QR image
	// and concealed clipboard writes, never shown on screen or logged.
	directTLS bool
	// fleetAddress and fleetImage hold the one-scan Relay Fleet URL (#r=...)
	// when RelayURL, RelaySecret, and RelayTrust are configured. When present,
	// tab 0 is Relay Fleet and tab 1 is This Host.
	fleetAddress string
	fleetImage   ui.ImageSource
	// tab is the code the dialog shows when a second code is offered:
	// for LAN + web: 0 = this address, 1 = web page;
	// for Direct/Relay + Fleet: 0 = Relay Fleet, 1 = This Host.
	tab int
	// webImage is the code for the page at rexapi.WebOrigin, opened with
	// this listener's address, certificate hash and pairing token
	// (rexapi.LANConfig.WebPairURL); nil when the listener has no leg to
	// put in it. It holds a credential, so the page keeps the image and
	// says where it leads, never the link itself. webTrusted is whether
	// this computer lets that page in (rexapi.LoadWeb).
	webURL                     string
	webImage                   ui.ImageSource
	webTrusted, webAllowFailed bool
	webOpenFailed              bool
	webErr                     string
}

// localRexPair is this computer's local network Rex pairing address, for a
// phone on the same network. It comes from the settings the page's poll reads.
func localRexPair() string {
	return remoteUI.lan.PairURL(remoteUI.lanStatus)
}

func (a *App) openLocalPairQR() {
	address := localRexPair()
	_, image := mobilePairQR(address)
	if image != nil {
		name, _, _ := a.hostIdentity()
		page := &hostPairQR{host: name, address: address, image: image, open: true, local: true}
		// The page's code, and whether this computer lets the page in. A
		// listener that is off, holds no token or has no WebTransport leg
		// gives no link: the page could not reach it anyway.
		if web := remoteUI.lan.WebPairURL(remoteUI.lanStatus, rexapi.WebOrigin); web != "" {
			page.webURL = web
			page.webImage = qrImage(web)
			page.webTrusted = trustsWebOrigin(rexapi.StateDir(), rexapi.WebOrigin)
		}
		remoteUI.pairQR = page
	}
}

// directPairURL returns this machine's active Direct TLS / Self-Hosted Relay
// pairing URL when direct/relay access is enabled and ready.
func (a *App) directPairURL() string {
	if !directReady(remoteUI.direct, remoteUI.directStatus) {
		return ""
	}
	name, _, _ := a.hostIdentity()
	return remoteUI.direct.PairURL(remoteUI.directStatus, name)
}

// directFleetPairURL returns this machine's one-scan Relay Fleet URL (#r=...)
// when direct/relay access is ready and RelayTrust + RelaySecret are enabled.
func (a *App) directFleetPairURL() string {
	if !directReady(remoteUI.direct, remoteUI.directStatus) {
		return ""
	}
	return remoteUI.direct.FleetPairURL("")
}

// openDirectPairQR opens the pairing QR dialog for direct TLS / self-hosted
// relay access. When Relay Fleet access is active, it also builds the #r=
// Fleet QR and shows a two-tab switcher (Relay Fleet vs This Host). Secrets
// stay inside the QR images and concealed clipboard writes only.
func (a *App) openDirectPairQR() {
	link := a.directPairURL()
	if link == "" {
		return
	}
	img := qrImage(link)
	if img == nil {
		return
	}
	name, _, _ := a.hostIdentity()
	displayHost := remoteUI.direct.ResolvedHostName(name)
	page := &hostPairQR{
		host:      displayHost,
		address:   link,
		image:     img,
		open:      true,
		directTLS: true,
	}
	if fleetLink := a.directFleetPairURL(); fleetLink != "" {
		if fleetImg := qrImage(fleetLink); fleetImg != nil {
			page.fleetAddress = fleetLink
			page.fleetImage = fleetImg
		}
	}
	remoteUI.pairQR = page
}

// openWebLink opens a page in the browser. A test replaces it, as the test
// backend opens none.
var openWebLink = mygo.Shell.OpenExternal

// trustsWebOrigin tells whether this computer lets the page at origin in
// (rexapi.LoadWeb), which the pair dialog's web tab adds when it does not.
func trustsWebOrigin(dir, origin string) bool {
	c, err := rexapi.LoadWeb(dir)
	if err != nil {
		return false
	}
	for _, o := range c.Origins {
		if o == origin {
			return true
		}
	}
	return false
}

// mobilePairQR returns the code for a Rex pairing address, and the
// address to show and copy: a bare http(s) origin, or the local network
// listener's with its pairing token, http://IP:PORT/#k=TOKEN
// (rexapi.LANConfig.PairURL). Anything else is no code.
func mobilePairQR(address string) (string, ui.ImageSource) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery {
		return "", nil
	}
	if u.Fragment == "" {
		if u.Path != "" {
			return "", nil
		}
	} else if u.Path != "/" || !strings.HasPrefix(u.Fragment, "k=") || len(u.Fragment) == len("k=") {
		return "", nil
	}
	img := qrImage(address)
	if img == nil {
		return "", nil
	}
	return address, img
}

// qrImage is s as a QR code; nil when it doesn't fit one.
func qrImage(s string) ui.ImageSource {
	code, err := qr.Encode(s, qr.M)
	if err != nil {
		return nil
	}
	img, err := ui.DecodeImage(code.PNG())
	if err != nil {
		return nil
	}
	return img
}

// pairQRCode draws a pairing code: a scanner expects the white behind it,
// inside the panel's corner radius.
func pairQRCode(c *ui.Context, img ui.ImageSource, label string) {
	ui.Row(c).Justify(ui.Center).Margin(2, 0, 2, 0).Children(func() {
		ui.Column(c).Padding(10).Radius(radiusL).Background(ui.RGB(255, 255, 255)).Children(func() {
			ui.Image(c, img).Size(240, 240).Label(label)
		})
	})
}

func hostPairQRSheet(c *ui.Context, k *colors, page *hostPairQR) {
	fleet := page.directTLS && page.fleetImage != nil
	showingFleet := fleet && page.tab == 0

	title := locales.HostsRexQRTitle(page.host)
	if page.directTLS {
		if showingFleet {
			title = locales.RemoteDirectFleetQRTitle()
		} else {
			title = locales.RemoteDirectTLSQRTitle(page.host)
		}
	}
	// The web tab's code opens a browser, not Rex: it says so.
	if page.webImage != nil && page.tab == 1 {
		title = locales.HostsPairWebTitle(page.host)
	}
	ui.DialogBase(c, &page.open, func(backdrop, panel ui.Element) {
		dialogEnter(c, backdrop, panel)
		// The backdrop dims the window; the glass is the panel's, as in
		// the other sheets over the app.
		backdrop.Background(k.backdrop)
		dialogShadow(glassPanel(panel.Width(380).Padding(20).Gap(12).Radius(radiusXXL), k).
			Border(1, k.panelBorder)).
			Role(ui.RoleDialog).Label(title)
		ui.Text(c, title).FontSize(fontTitle).FontWeight(700).TextColor(k.text)
		// A listener with a web link or Relay Fleet offers both codes via
		// a two-tab segmented switcher.
		web := page.webImage != nil
		if fleet {
			if ui.Segmented(c, &page.tab, locales.RemoteDirectQRTabFleet(), locales.RemoteDirectQRTabHost()).Label(title).Changed() {
				page.copied, page.copyFailed = false, false
			}
		} else if web {
			if ui.Segmented(c, &page.tab, locales.HostsPairCodeAddress(), locales.HostsPairCodePage()).Label(title).Changed() {
				page.copied, page.copyFailed = false, false
			}
		}
		activeAddr := page.address
		activeImg := page.image
		if showingFleet {
			activeAddr = page.fleetAddress
			activeImg = page.fleetImage
		}
		switch {
		case web && page.tab == 1:
			ui.Text(c, locales.HostsPairWebHint(rexapi.WebOrigin)).FontSize(fontCallout).TextColor(k.textMuted).LineHeight(1.35)
			// The browser this dialog runs beside is the usual one, so
			// it opens there; the code is for a phone on the network.
			if page.webTrusted {
				ui.Text(c, locales.HostsPairWebTrusted()).FontSize(fontSubhead).TextColor(k.textMuted).LineHeight(1.3)
			} else {
				ui.Text(c, locales.HostsPairWebUntrusted()).FontSize(fontSubhead).TextColor(k.textMuted).LineHeight(1.3)
				if ui.Button(c.Key("trust-web"), locales.HostsPairWebTrust(rexapi.WebOrigin)).Clicked() {
					if _, err := rexapi.AllowWebOrigin(rexapi.StateDir(), rexapi.WebOrigin); err != nil {
						page.webAllowFailed, page.webErr = true, err.Error()
					} else {
						page.webTrusted, page.webAllowFailed = true, false
					}
				}
			}
			if page.webAllowFailed {
				ui.Text(c, locales.HostsPairWebTrustFailed(page.webErr)).FontSize(fontSubhead).TextColor(k.danger).LineHeight(1.3)
			}
			if ui.Button(c.Key("open-web"), locales.HostsPairWebOpen()).Clicked() {
				if err := openWebLink(page.webURL); err != nil {
					page.webOpenFailed, page.webErr = true, err.Error()
				} else {
					page.webOpenFailed = false
				}
			}
			if page.webOpenFailed {
				ui.Text(c, locales.HostsPairWebOpenFailed(page.webErr)).FontSize(fontSubhead).TextColor(k.danger).LineHeight(1.3)
			}
			ui.Text(c, locales.HostsPairWebScan()).FontSize(fontSubhead).TextColor(k.textMuted).LineHeight(1.3)
			pairQRCode(c, page.webImage, title)
		case activeImg != nil:
			hint := locales.HostsLocalLanRexQRHint()
			if showingFleet {
				hint = locales.RemoteDirectFleetQRHint()
			} else if page.directTLS {
				hint = locales.RemoteDirectTLSQRHint()
			}
			ui.Text(c, hint).FontSize(fontCallout).TextColor(k.textMuted).LineHeight(1.35)
			pairQRCode(c, activeImg, title)
			// The pairing token / relay secret stays in the code and the copy, off the screen.
			shown, _, _ := strings.Cut(activeAddr, "#")
			ui.Text(c, shown).Font(monoFamily()).FontSize(fontSubhead).TextColor(k.text).SingleLine().Label(shown)
		default:
			ui.Text(c, locales.HostsRexQRUnavailable()).FontSize(fontCallout).TextColor(k.textMuted).LineHeight(1.35)
		}
		if page.copyFailed {
			ui.Text(c, locales.HostsRexQRCopyFailed()).FontSize(fontSubhead).TextColor(k.danger).LineHeight(1.3)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Margin(8, 0, 0, 0).Children(func() {
			if ui.Button(c.Key("close"), locales.HostsRexQRClose()).Clicked() {
				page.open = false
			}
			// The web tab's code holds the token too: a copy of the other
			// address's link would be a different code than the one shown.
			if activeImg != nil && activeAddr != "" && !(web && page.tab == 1) {
				label := locales.HostsRexQRCopy()
				if page.copied {
					label = locales.HostsRexQRCopied()
				}
				if ui.Button(c.Key("copy"), label).Clicked() {
					var opts mygo.ClipboardOptions
					if page.directTLS || strings.Contains(activeAddr, "#") {
						opts.Concealed = true
					}
					err := mygo.Clipboard.Write(transfer.TextData(activeAddr), opts)
					page.copied, page.copyFailed = err == nil, err != nil
				}
			}
		})
	})
	if !page.open {
		remoteUI.pairQR = nil
	}
}
