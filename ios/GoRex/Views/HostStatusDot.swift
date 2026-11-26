import SwiftUI

/// A host's connection status dot: solid green when online, amber ring while
/// connecting, and muted gray ring while offline.
struct HostStatusDot: View {
    let status: Host.Status

    enum Appearance: Equatable {
        case solidGreen
        case amberRing
        case grayRing
    }

    static func appearance(for status: Host.Status) -> Appearance {
        switch status {
        case .online: .solidGreen
        case .connecting: .amberRing
        case .offline: .grayRing
        }
    }

    static func accessibilityText(for status: Host.Status) -> LocalizedStringKey {
        switch status {
        case .online: "Online"
        case .connecting: "Connecting…"
        case .offline: "Offline"
        }
    }

    var body: some View {
        switch Self.appearance(for: status) {
        case .solidGreen:
            Circle()
                .fill(Color.green)
                .frame(width: 8, height: 8)
                .accessibilityLabel(Self.accessibilityText(for: status))
        case .amberRing:
            Circle()
                .strokeBorder(Color.orange, lineWidth: 1.5)
                .frame(width: 8, height: 8)
                .accessibilityLabel(Self.accessibilityText(for: status))
        case .grayRing:
            Circle()
                .strokeBorder(Color.secondary, lineWidth: 1.5)
                .frame(width: 8, height: 8)
                .accessibilityLabel(Self.accessibilityText(for: status))
        }
    }
}
