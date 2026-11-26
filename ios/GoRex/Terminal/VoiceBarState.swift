import CoreGraphics
import Foundation

/// Active single-row layout mode of the iPhone bottom bar (`KeyBar`).
nonisolated enum VoiceBarMode: Equatable, Sendable {
    /// WeChat-style wide Hold-to-Talk capsule with left Keys toggle and right Enter.
    case voice
    /// Compact single-row terminal shortcut keys (`Esc`, `Tab`, modifiers, stick, `↵`, keyboard).
    case keys
}

/// Press-and-hold / slide-up-to-cancel gesture phase for the Voice Bar center capsule.
nonisolated enum VoicePressPhase: Equatable, Sendable {
    /// No touch active on the voice capsule.
    case idle
    /// Touch began; waiting to distinguish a short tap (< 200 ms) from hold-to-talk (≥ 200 ms).
    case pressing
    /// Hold threshold reached; actively dictating, release commits without raising the keyboard.
    case speaking
    /// Finger slid upward past the cancel threshold; release cancels without inserting.
    case cancelling
}

/// Side-effect actions emitted by `VoiceBarState` for `KeyBar` and `TerminalScreen` to perform.
nonisolated enum VoiceBarAction: Equatable, Sendable {
    case raiseKeyboard
    case dismissKeyboard
    case startDictation
    case commitDictation
    case cancelDictation
    case hapticLight
    case hapticCancelWarning
}

/// Pure dual-mode and hold-to-talk / slide-up-to-cancel state machine for the
/// iPhone single-row bottom bar (`KeyBar`).
///
/// - Default mode is `.voice` while the soft keyboard is hidden.
/// - A short tap (< `holdThreshold` with drag ≤ `tapMaxDragDistance`) raises
///   the soft keyboard and switches to `.keys` without pinning it.
/// - Pressing and holding for at least `holdThreshold` (200 ms) starts
///   dictation with a light haptic; releasing commits and inserts text without
///   raising the keyboard.
/// - Dragging upward past `cancelDragThreshold` (50 pt) while speaking enters
///   `.cancelling` with a haptic; releasing discards the transcript, while
///   sliding back down resumes `.speaking`.
/// - Showing the soft keyboard switches to `.keys`; dismissing it returns to
///   `.voice` unless the user explicitly pinned `.keys` via the left toggle.
nonisolated struct VoiceBarState: Equatable, Sendable {
    /// Minimum press duration before hold-to-talk begins dictation.
    static let holdThreshold: TimeInterval = 0.20
    /// Upward vertical drag distance (in points) required to enter the cancel zone.
    static let cancelDragThreshold: CGFloat = 50
    /// Maximum finger movement (in points) allowed for a release before `holdThreshold` to count as a tap.
    static let tapMaxDragDistance: CGFloat = 18

    private(set) var keyboardVisible: Bool
    private(set) var pinnedKeysMode: Bool
    private(set) var phase: VoicePressPhase = .idle
    private var maxPressDrag: CGFloat = 0
    private var lastTranslationY: CGFloat = 0

    /// Visible bar mode: `.keys` while the soft keyboard is shown or manually pinned, else `.voice`.
    var mode: VoiceBarMode {
        (keyboardVisible || pinnedKeysMode) ? .keys : .voice
    }

    /// True while dictation is actively held (`.speaking` or `.cancelling`).
    var isHoldingToTalk: Bool {
        phase == .speaking || phase == .cancelling
    }

    init(keyboardVisible: Bool = false, pinnedKeysMode: Bool = false) {
        self.keyboardVisible = keyboardVisible
        self.pinnedKeysMode = pinnedKeysMode
    }

    /// Touch down on the center Voice Bar capsule.
    @discardableResult
    mutating func pressBegan() -> [VoiceBarAction] {
        guard mode == .voice, phase == .idle else { return [] }
        phase = .pressing
        maxPressDrag = 0
        lastTranslationY = 0
        return []
    }

    /// Hold timer fired after `holdThreshold` while the finger is still down.
    @discardableResult
    mutating func holdThresholdReached() -> [VoiceBarAction] {
        guard phase == .pressing else { return [] }
        // Ignore a fast upward swipe (such as a home-indicator gesture) that
        // already crossed the cancel zone before the hold threshold elapsed.
        if -lastTranslationY > Self.cancelDragThreshold {
            phase = .idle
            maxPressDrag = 0
            lastTranslationY = 0
            return []
        }
        phase = .speaking
        return [.hapticLight, .startDictation]
    }

    /// Touch moved relative to touch-down origin (`translation.y < 0` is upward).
    @discardableResult
    mutating func dragChanged(translation: CGPoint) -> [VoiceBarAction] {
        let distance = hypot(translation.x, translation.y)
        lastTranslationY = translation.y
        switch phase {
        case .idle:
            return []
        case .pressing:
            maxPressDrag = max(maxPressDrag, distance)
            return []
        case .speaking:
            if -translation.y > Self.cancelDragThreshold {
                phase = .cancelling
                return [.hapticCancelWarning]
            }
            return []
        case .cancelling:
            if -translation.y <= Self.cancelDragThreshold {
                phase = .speaking
                return [.hapticLight]
            }
            return []
        }
    }

    /// Touch lifted normally after `elapsed` seconds from touch down.
    @discardableResult
    mutating func pressEnded(elapsed: TimeInterval, translation: CGPoint = .zero) -> [VoiceBarAction] {
        let drag = max(maxPressDrag, hypot(translation.x, translation.y))
        let current = phase
        phase = .idle
        maxPressDrag = 0
        lastTranslationY = 0

        switch current {
        case .idle:
            return []
        case .pressing:
            if elapsed < Self.holdThreshold && drag <= Self.tapMaxDragDistance {
                keyboardVisible = true
                return [.hapticLight, .raiseKeyboard]
            }
            return []
        case .speaking:
            return [.commitDictation]
        case .cancelling:
            return [.cancelDictation]
        }
    }

    /// Touch gesture cancelled or interrupted by the system.
    @discardableResult
    mutating func pressCancelled() -> [VoiceBarAction] {
        let wasActive = isHoldingToTalk
        phase = .idle
        maxPressDrag = 0
        lastTranslationY = 0
        return wasActive ? [.cancelDictation] : []
    }

    /// Dictation stopped externally (permission denied, audio error, interruption, pane switch).
    mutating func dictationEnded() {
        phase = .idle
        maxPressDrag = 0
        lastTranslationY = 0
    }

    /// Left button tapped to toggle between Voice Bar and Terminal Keys mode.
    /// If the soft keyboard is currently visible, dismisses it and returns to Voice Bar.
    @discardableResult
    mutating func toggleMode() -> [VoiceBarAction] {
        var actions: [VoiceBarAction] = []
        if isHoldingToTalk {
            actions.append(.cancelDictation)
        }
        phase = .idle
        maxPressDrag = 0
        lastTranslationY = 0

        if keyboardVisible {
            pinnedKeysMode = false
            keyboardVisible = false
            actions.append(.dismissKeyboard)
        } else {
            pinnedKeysMode.toggle()
        }
        return actions
    }

    /// Soft keyboard visibility changed. Auto-switches to `.keys` while shown
    /// and returns cleanly to `.voice` on dismiss unless `.keys` was manually pinned.
    @discardableResult
    mutating func setKeyboardVisible(_ visible: Bool) -> [VoiceBarAction] {
        keyboardVisible = visible
        guard visible else { return [] }
        let wasActive = isHoldingToTalk
        if phase != .idle {
            phase = .idle
            maxPressDrag = 0
            lastTranslationY = 0
        }
        return wasActive ? [.cancelDictation] : []
    }
}
