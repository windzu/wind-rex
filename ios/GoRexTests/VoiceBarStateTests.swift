import CoreGraphics
import XCTest

/// Unit tests for `VoiceBarState`: dual-mode Voice Bar / Terminal Keys
/// switching, short-tap keyboard raise, press-and-hold dictation, and
/// slide-up-to-cancel gesture transitions.
nonisolated final class VoiceBarStateTests: XCTestCase {
    func testDefaultModeIsVoiceWhenKeyboardHidden() {
        let state = VoiceBarState()
        XCTAssertEqual(state.mode, .voice)
        XCTAssertEqual(state.phase, .idle)
        XCTAssertFalse(state.keyboardVisible)
        XCTAssertFalse(state.pinnedKeysMode)
        XCTAssertFalse(state.isHoldingToTalk)
    }

    func testShortTapRaisesKeyboardAndSwitchesToKeysWithoutPinning() {
        var state = VoiceBarState()
        XCTAssertEqual(state.pressBegan(), [])
        XCTAssertEqual(state.phase, .pressing)

        // Small thumb drift within tap tolerance still counts as a short tap.
        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 4, y: -6)), [])
        let actions = state.pressEnded(elapsed: 0.12, translation: CGPoint(x: 4, y: -6))
        XCTAssertEqual(actions, [.hapticLight, .raiseKeyboard])
        XCTAssertEqual(state.mode, .keys)
        XCTAssertEqual(state.phase, .idle)
        XCTAssertTrue(state.keyboardVisible)
        XCTAssertFalse(state.pinnedKeysMode)

        // Dismissing the soft keyboard returns cleanly to Voice Bar.
        XCTAssertEqual(state.setKeyboardVisible(false), [])
        XCTAssertEqual(state.mode, .voice)
    }

    func testShortTapWithExcessiveDragDoesNotRaiseKeyboard() {
        var state = VoiceBarState()
        XCTAssertEqual(state.pressBegan(), [])
        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 0, y: -28)), [])
        let actions = state.pressEnded(elapsed: 0.10, translation: CGPoint(x: 0, y: -28))
        XCTAssertEqual(actions, [])
        XCTAssertEqual(state.mode, .voice)
        XCTAssertEqual(state.phase, .idle)
        XCTAssertFalse(state.keyboardVisible)
    }

    func testReleaseAtHoldThresholdBeforeTimerDoesNotRaiseKeyboard() {
        var state = VoiceBarState()
        XCTAssertEqual(state.pressBegan(), [])
        let actions = state.pressEnded(elapsed: VoiceBarState.holdThreshold)
        XCTAssertEqual(actions, [])
        XCTAssertEqual(state.mode, .voice)
        XCTAssertEqual(state.phase, .idle)
    }

    func testHoldPastThresholdStartsDictatingAndReleaseCommitsWithoutRaisingKeyboard() {
        var state = VoiceBarState()
        XCTAssertEqual(state.pressBegan(), [])
        XCTAssertEqual(state.holdThresholdReached(), [.hapticLight, .startDictation])
        XCTAssertEqual(state.phase, .speaking)
        XCTAssertTrue(state.isHoldingToTalk)
        XCTAssertEqual(state.mode, .voice)
        XCTAssertFalse(state.keyboardVisible)

        // Minor movement within cancel threshold stays in speaking state.
        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 12, y: -25)), [])
        XCTAssertEqual(state.phase, .speaking)

        // Release commits dictation and stays in Voice Bar without raising keyboard.
        XCTAssertEqual(state.pressEnded(elapsed: 1.4, translation: CGPoint(x: 12, y: -25)), [.commitDictation])
        XCTAssertEqual(state.phase, .idle)
        XCTAssertEqual(state.mode, .voice)
        XCTAssertFalse(state.keyboardVisible)
    }

    func testHoldAndSlideUpPastCancelThresholdEntersCancellingAndReleaseCancels() {
        var state = VoiceBarState()
        state.pressBegan()
        XCTAssertEqual(state.holdThresholdReached(), [.hapticLight, .startDictation])
        XCTAssertEqual(state.phase, .speaking)

        // At exact threshold (50 pt) stays speaking; past 50 pt enters cancelling.
        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 0, y: -50)), [])
        XCTAssertEqual(state.phase, .speaking)

        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 0, y: -51)), [.hapticCancelWarning])
        XCTAssertEqual(state.phase, .cancelling)
        XCTAssertTrue(state.isHoldingToTalk)

        // Dragging further upward does not re-emit the cancel warning haptic.
        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 5, y: -90)), [])
        XCTAssertEqual(state.phase, .cancelling)

        // Releasing in cancelling state cancels dictation and does NOT commit.
        XCTAssertEqual(state.pressEnded(elapsed: 1.8, translation: CGPoint(x: 5, y: -90)), [.cancelDictation])
        XCTAssertEqual(state.phase, .idle)
        XCTAssertEqual(state.mode, .voice)
    }

    func testSlidingBackDownFromCancellingResumesSpeakingAndReleaseCommits() {
        var state = VoiceBarState()
        state.pressBegan()
        state.holdThresholdReached()
        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 0, y: -70)), [.hapticCancelWarning])
        XCTAssertEqual(state.phase, .cancelling)

        // Slide back below the cancel threshold -> resumes speaking with light haptic.
        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 0, y: -30)), [.hapticLight])
        XCTAssertEqual(state.phase, .speaking)

        // Staying in speaking emits no duplicate haptic.
        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 0, y: -10)), [])
        XCTAssertEqual(state.phase, .speaking)

        // Normal release commits.
        XCTAssertEqual(state.pressEnded(elapsed: 2.0, translation: CGPoint(x: 0, y: -10)), [.commitDictation])
        XCTAssertEqual(state.phase, .idle)
    }

    func testFastUpwardSwipeBeforeHoldThresholdDoesNotStartDictation() {
        var state = VoiceBarState()
        state.pressBegan()
        XCTAssertEqual(state.dragChanged(translation: CGPoint(x: 0, y: -65)), [])
        XCTAssertEqual(state.holdThresholdReached(), [])
        XCTAssertEqual(state.phase, .idle)
        XCTAssertEqual(state.mode, .voice)
    }

    func testGestureCancelAndExternalDictationEndResetCleanly() {
        var state = VoiceBarState()
        // Cancelling before hold threshold emits no dictation cancel.
        state.pressBegan()
        XCTAssertEqual(state.pressCancelled(), [])
        XCTAssertEqual(state.phase, .idle)

        // Cancelling while speaking emits cancelDictation.
        state.pressBegan()
        state.holdThresholdReached()
        XCTAssertEqual(state.pressCancelled(), [.cancelDictation])
        XCTAssertEqual(state.phase, .idle)

        // Cancelling while in cancelling state also emits cancelDictation.
        state.pressBegan()
        state.holdThresholdReached()
        state.dragChanged(translation: CGPoint(x: 0, y: -80))
        XCTAssertEqual(state.pressCancelled(), [.cancelDictation])
        XCTAssertEqual(state.phase, .idle)

        // External dictation end resets phase without extra actions.
        state.pressBegan()
        state.holdThresholdReached()
        state.dictationEnded()
        XCTAssertEqual(state.phase, .idle)
        XCTAssertEqual(state.pressEnded(elapsed: 1.0), [])
    }

    func testKeyboardShowHideAndManualModeToggleTransitions() {
        var state = VoiceBarState()
        XCTAssertEqual(state.mode, .voice)

        // Soft keyboard appears (e.g., user tapped terminal) -> auto-activates keys mode.
        XCTAssertEqual(state.setKeyboardVisible(true), [])
        XCTAssertEqual(state.mode, .keys)
        XCTAssertFalse(state.pinnedKeysMode)

        // Soft keyboard hides -> returns cleanly to voice bar since keys mode was not pinned.
        XCTAssertEqual(state.setKeyboardVisible(false), [])
        XCTAssertEqual(state.mode, .voice)

        // User manually toggles to Terminal Keys mode while keyboard is hidden.
        XCTAssertEqual(state.toggleMode(), [])
        XCTAssertEqual(state.mode, .keys)
        XCTAssertTrue(state.pinnedKeysMode)

        // Raising and dismissing keyboard while pinned keeps keys mode active.
        XCTAssertEqual(state.setKeyboardVisible(true), [])
        XCTAssertEqual(state.mode, .keys)
        XCTAssertEqual(state.setKeyboardVisible(false), [])
        XCTAssertEqual(state.mode, .keys)
        XCTAssertTrue(state.pinnedKeysMode)

        // Tapping left toggle again returns to voice bar.
        XCTAssertEqual(state.toggleMode(), [])
        XCTAssertEqual(state.mode, .voice)
        XCTAssertFalse(state.pinnedKeysMode)

        // Tapping left toggle while soft keyboard is visible dismisses keyboard and returns to voice bar.
        XCTAssertEqual(state.setKeyboardVisible(true), [])
        XCTAssertEqual(state.mode, .keys)
        XCTAssertEqual(state.toggleMode(), [.dismissKeyboard])
        XCTAssertEqual(state.mode, .voice)
        XCTAssertFalse(state.keyboardVisible)
        XCTAssertFalse(state.pinnedKeysMode)
    }

    func testKeyboardAppearingOrTogglingDuringDictationCancelsActiveSession() {
        var state = VoiceBarState()
        state.pressBegan()
        state.holdThresholdReached()
        XCTAssertEqual(state.setKeyboardVisible(true), [.cancelDictation])
        XCTAssertEqual(state.phase, .idle)
        XCTAssertEqual(state.mode, .keys)

        // Pressing while in keys mode is ignored.
        XCTAssertEqual(state.pressBegan(), [])
        XCTAssertEqual(state.phase, .idle)

        // Toggle while speaking cancels active dictation and switches to keys mode.
        state.setKeyboardVisible(false)
        state.pressBegan()
        state.holdThresholdReached()
        XCTAssertEqual(state.toggleMode(), [.cancelDictation])
        XCTAssertEqual(state.phase, .idle)
        XCTAssertEqual(state.mode, .keys)
    }
}
