import Foundation
import UIKit

/// Terminal font size defaults and clamping, shared by Ghostty rendering,
/// pinch/keyboard zoom, and Settings so they never drift apart.
enum TerminalFont {
    static let sizeKey = "fontSize"
    static let range: ClosedRange<Double> = 8...24

    /// Default terminal font size for the device idiom: 10 pt on iPhone for
    /// more readable columns per line, 12 pt on iPad.
    static func defaultSize(for idiom: UIUserInterfaceIdiom = UIDevice.current.userInterfaceIdiom) -> Double {
        idiom == .phone ? 10 : 12
    }

    /// Effective font size from `defaults`, falling back to the idiom's default
    /// when unset or non-positive.
    static func size(
        in defaults: UserDefaults = .standard,
        idiom: UIUserInterfaceIdiom = UIDevice.current.userInterfaceIdiom
    ) -> CGFloat {
        let v = defaults.double(forKey: sizeKey)
        return v > 0 ? CGFloat(v) : CGFloat(defaultSize(for: idiom))
    }

    /// Clamps a target font size into Settings' supported range.
    static func clamped(_ size: CGFloat) -> CGFloat {
        min(max(size, CGFloat(range.lowerBound)), CGFloat(range.upperBound))
    }
}
