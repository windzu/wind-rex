---
name: IOS-009
description: "WindRex's app icon and its source artwork. Use when updating the desktop, iOS, or Web icon assets."
metadata:
  created: "2026-11-19"
  updated: "2026-11-24"
---

# IOS-009: WindRex app icons

Status: the WindRex mark replaces the inherited artwork.

## Design

WindRex uses a rounded W in one stroke. Its last stroke extends horizontally
like a terminal cursor. The primary palette puts a warm white mark on dark
green ink. The mark keeps the same shape on desktop, iOS, and Web. Its
strokes and gaps must remain legible at 16 px.

The iOS picker offers WindRex, Forest, and Paper. Forest has a green
background. Paper uses the inverse of the primary palette. Both use the
same mark. The iOS alternate identifiers `AppIcon-Spectrum` and
`AppIcon-Classic` stay the same so an installed app can resolve its
previous choice. The launch screen and the terminal's connecting indicator
use the new mark too.

## Source and output

Edit `assets/brand/windrex.svg` to change the primary artwork. Run
`go run ./tools/mkicon` from the repository root to rebuild the assets.
The generator reads its background rectangle and the cubic paths of
the mark. It produces these files:

- `resources/icon.png` and `resources/icon-dev.png`. MyGo makes the
  platform installers and icons from these files. The development app
  uses a purple background to distinguish it in the Dock.
- The mark layer and background of each iOS Icon Composer bundle.
  The system clips the icon on the Home Screen. The picker previews and
  launch image come from the same source. The launch image is centered at
  144 pt on `LaunchBackground`, which matches the host list. The terminal
  connecting indicator uses the transparent mark from `BrandMark.imageset`.
- The Web favicon, PWA icons and Apple touch icon in `web/pwa`.
  The maskable icons and Apple touch icon have an opaque background.

`go run ./tools/mkicon -web` rebuilds only the Web assets.

## iOS behavior

Settings shows the three variants and marks the one in use. The picker
passes nil to `setAlternateIconName` for the default icon. Passing
`"AppIcon"` can prevent further changes on iOS 26.1. The app keeps the
previous selection and shows the error if iOS refuses a change. On devices
where `supportsAlternateIcons` is false, the rows are disabled. The picker
clips its square previews to a continuous corner of 22.37% of their size.

`ios/project.yml` names the primary bundle in
`ASSETCATALOG_COMPILER_APPICON_NAME` and lists both alternate identifiers
in `ASSETCATALOG_COMPILER_ALTERNATE_APPICON_NAMES`. Keep the list, the
bundle names, and `AppIcon.alternateName` in agreement. The build uses
`ASSETCATALOG_COMPILER_INCLUDE_ALL_APPICON_ASSETS` so both alternates are
available to the system.

The system draws notification icons. iOS may show a cached copy of the icon
after an update. A notification already in Notification Center keeps its icon.

## Check

Build the desktop package and open it on a Mac. Check the Dock at normal and
small sizes. Build the iOS app with XcodeGen 2.45.1 or later and a version
of Xcode that supports the checked-in Icon Composer bundles. Check the
Home Screen and the icon picker on a simulator. Build the Web app and
check the browser tab and PWA icon.
