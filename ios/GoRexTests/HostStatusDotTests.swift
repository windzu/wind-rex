import XCTest

nonisolated final class HostStatusDotTests: XCTestCase {
    @MainActor func testAppearanceByStatus() {
        XCTAssertEqual(HostStatusDot.appearance(for: .online), .solidGreen)
        XCTAssertEqual(HostStatusDot.appearance(for: .connecting), .amberRing)
        XCTAssertEqual(HostStatusDot.appearance(for: .offline("unreachable")), .grayRing)
    }

    @MainActor func testAccessibilityLabelByStatus() {
        XCTAssertEqual(HostStatusDot.accessibilityText(for: .online), "Online")
        XCTAssertEqual(HostStatusDot.accessibilityText(for: .connecting), "Connecting…")
        XCTAssertEqual(HostStatusDot.accessibilityText(for: .offline("unreachable")), "Offline")
    }
}
