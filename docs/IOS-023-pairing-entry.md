---
name: IOS-023
description: "First-time iPhone-to-Mac pairing entry on desktop and iOS. Use when changing the Connect iPhone onboarding card, WindRex QR flow, or iOS empty-state pairing."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# IOS-023: Visible First-Time iPhone Pairing Entry

Status: built.

## Problem

Pairing an iPhone running WindRex with a Mac for the first time required hunting through several buried screens (#19):

1. In macOS Settings, the sidebar card read "GoRex Server" and looked like a passive status widget rather than the pairing entry.
2. On the Server page, machine facts and Local API occupied the entire first viewport; remote access ("Let other devices connect") sat below the fold.
3. While remote access was off, no phone-pairing entry or QR action was visible at all. Once turned on, the main pairing button read "Rex Go QR…" while Local API also offered "Rex QR…", creating ambiguity for new users.
4. On iOS with no hosts yet, the empty state promoted Demo Mode and hid QR scanning behind Settings › Remote Hosts › Add Host › Pair via QR Code, with no hint explaining where on the Mac to obtain the QR code.

## Design

### Desktop: Above-the-fold Connect iPhone entry

- Rename the Settings sidebar server card to **WindRex Server** and label the primary phone pairing code **WindRex QR…** (distinguishing the Local API / Tailscale code as **Local / Tailscale QR…**).
- Place a **Connect iPhone** card at the top of `Settings › Server`, always visible above the fold regardless of whether remote access is off, starting, or listening:
  - **Off**: explains the three-step flow (enable remote access, scan in WindRex on iPhone, verify fingerprint and approve here) and offers a single primary action **Turn On & Show QR…**. Clicking it enables remote access and arms `wantPhoneQR` so the **WindRex QR…** sheet opens automatically as soon as the server transitions to listening.
  - **Starting**: shows the starting status and automatic-QR note, with a **Cancel** action to disarm auto-open if desired.
  - **Listening**: shows ready status and a primary **WindRex QR…** button.
  - **Pending requests**: displays incoming pairing requests with device name, fingerprint, **Reject**, and **Approve** directly inside the top card so users never have to scroll down to approve their iPhone.
- Add a **Connect iPhone…** button to the main window's Host popover (`host.go`) that jumps straight to `Settings › Server`.

### iOS: Direct Scan QR to Pair on the empty state

- Extract scanned-code handling into a shared `HostPairing` helper (`ios/GoRex/Rex/HostPairing.swift`) used by both `HostListView` and `RemoteHostsView` so direct TLS/relay (`https://...#k=...&p=...`) and LAN pairing codes follow one code path (tailcat QR handling was removed in [`NET-009`](NET-009-remove-tailscale.md)).
- Update `NoHostsView` (`HostListView.swift`) to:
  - State explicitly where to get the QR code on Mac (`WindRex › Settings › Server › Connect iPhone`).
  - Make **Scan QR to Pair** the primary prominent button, launching `QRScanner` directly from the home screen and adding a direct/relay or LAN host immediately.
  - Keep **Add a Host** and **Try Demo Mode** available directly below.
- Update pairing sheet copy in `RemoteHostsView.swift` to consistently name **WindRex › Settings › Server**.
