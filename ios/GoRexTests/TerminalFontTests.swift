import UIKit
import XCTest

nonisolated final class TerminalFontTests: XCTestCase {
    @MainActor private func makeDefaults() -> UserDefaults {
        let suite = "terminal-font-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        addTeardownBlock { UserDefaults().removePersistentDomain(forName: suite) }
        return defaults
    }

    @MainActor func testDefaultSizeByIdiom() {
        let defaults = makeDefaults()
        XCTAssertEqual(TerminalFont.defaultSize(for: .phone), 10)
        XCTAssertEqual(TerminalFont.defaultSize(for: .pad), 12)
        XCTAssertEqual(TerminalFont.size(in: defaults, idiom: .phone), 10)
        XCTAssertEqual(TerminalFont.size(in: defaults, idiom: .pad), 12)
    }

    @MainActor func testManualOverrideTakesPrecedence() {
        let defaults = makeDefaults()
        defaults.set(14.0, forKey: TerminalFont.sizeKey)
        XCTAssertEqual(TerminalFont.size(in: defaults, idiom: .phone), 14)
        XCTAssertEqual(TerminalFont.size(in: defaults, idiom: .pad), 14)
    }

    @MainActor func testNonPositiveFallsBackToDefault() {
        let defaults = makeDefaults()
        defaults.set(0.0, forKey: TerminalFont.sizeKey)
        XCTAssertEqual(TerminalFont.size(in: defaults, idiom: .phone), 10)
        defaults.set(-4.0, forKey: TerminalFont.sizeKey)
        XCTAssertEqual(TerminalFont.size(in: defaults, idiom: .pad), 12)
    }

    @MainActor func testClampingMatchesRange() {
        XCTAssertEqual(TerminalFont.clamped(4), 8)
        XCTAssertEqual(TerminalFont.clamped(10), 10)
        XCTAssertEqual(TerminalFont.clamped(30), 24)
    }
}
