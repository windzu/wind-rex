import Foundation
import UIKit

/// Settings › Tailscale, as UserDefaults keeps them (docs/IOS-001-tailnet.md).
enum TailnetSettings {
    static let onKey = "tailnet"
    static let controlKey = "tailnetControl"
    static let nameKey = "tailnetName"

    /// Whether the built-in Tailscale is on.
    static var isOn: Bool { UserDefaults.standard.bool(forKey: onKey) }

    /// The control server's URL; empty for Tailscale's own.
    static var controlURL: String { UserDefaults.standard.string(forKey: controlKey) ?? "" }

    /// The device's name; empty until makeName.
    static var name: String { UserDefaults.standard.string(forKey: nameKey) ?? "" }

    /// Gives the device a default name, once: from then on it is kept.
    static func makeName() {
        if name.isEmpty { UserDefaults.standard.set(defaultName(), forKey: nameKey) }
    }

    /// <model>-rex- and four random letters, as iphone-rex-kqzt. iOS gives
    /// an app only the model — since iOS 16, the own name needs an
    /// entitlement Apple grants case by case — so the model names the
    /// device and the letters keep two phones of a model apart. With the
    /// entitlement granted, the own name ("Alice's iPhone" as
    /// alices-iphone-...) is what this returns.
    static func defaultName(device: String = UIDevice.current.name) -> String {
        var b = ""
        // "Alice's iPhone" reads better as alices-iphone than alice-s-iphone.
        for r in device.lowercased().replacingOccurrences(of: "'s", with: "s").replacingOccurrences(of: "’s", with: "s").unicodeScalars {
            switch r {
            case "a"..."z", "0"..."9":
                b.unicodeScalars.append(r)
            default:
                if !b.isEmpty && !b.hasSuffix("-") { b += "-" }
            }
        }
        var base = b.trimmingCharacters(in: CharacterSet(charactersIn: "-"))
        if base.count > 54 {
            base = String(base.prefix(54)).trimmingCharacters(in: CharacterSet(charactersIn: "-"))
        }
        let letters = String((0..<4).map { _ in "abcdefghijklmnopqrstuvwxyz".randomElement()! })
        return base.isEmpty ? "rex-\(letters)" : "\(base)-rex-\(letters)"
    }
}

/// Single activation policy choke point deciding whether WindRex's in-process
/// Tailscale or tailcat engine is needed (docs/NET-005-direct-relay.md, Phase 3).
/// When all hosts use direct TLS, a self-hosted relay, or LAN (`.system`) and
/// Built-in Tailscale is off, the engine stays dormant on cold start,
/// foreground resume, and network changes.
enum TailnetActivationPolicy {
    /// Whether any non-demo host is paired over tailcat.
    static func needsTailcat(hosts: [HostConfig]) -> Bool {
        hosts.contains { $0.demo != true && $0.tailcat != nil }
    }

    /// Whether Built-in Tailscale is enabled in Settings or any non-demo,
    /// non-tailcat host uses `.tailnet` transport.
    static func needsTailscale(hosts: [HostConfig], tailnetEnabled: Bool) -> Bool {
        if tailnetEnabled { return true }
        return hosts.contains { $0.demo != true && $0.tailcat == nil && $0.transport == .tailnet }
    }

    /// Whether the in-process Tailnet/tailcat engine should initialize or wake
    /// on lifecycle transitions.
    static func needsEngine(hosts: [HostConfig], tailnetEnabled: Bool) -> Bool {
        needsTailscale(hosts: hosts, tailnetEnabled: tailnetEnabled) || needsTailcat(hosts: hosts)
    }
}
