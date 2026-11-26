---
name: IOS-024
description: "WindRex keeps keyboard-free voice input on iPhone after removing bundled ASR runtimes by replacing FluidAudio and SherpaOnnx with Apple's iOS 26 SpeechAnalyzer behind KeyBar's non-focusing mic, with an optional cloud transcription backend. Use when you change iOS dictation, KeyBar voice interaction, or speech permissions."
metadata:
  created: "2026-11-26"
  updated: "2026-11-26"
---

# IOS-024: Voice input on iPhone without raising the keyboard

Status: proposal.

This design doc researches keyboard-free voice input for the iPhone terminal (issue #21) after WindRex removes its self-managed ASR runtimes and model downloads (issue #13). It extends [`IOS-013`](IOS-013-key-bar-enter.md) and coordinates with [`IOS-008`](IOS-008-icloud-sync.md) and [`IOS-003`](IOS-003-web-share.md).

## Problem

WindRex currently provides keyboard-free voice input on iPhone through `Dictation` (`ios/GoRex/Terminal/Dictation.swift`), `TerminalView.dictate(said:)` (`ios/GoRex/Terminal/TerminalView.swift`), and `Mic` / `VoiceCapsule` (`ios/GoRex/Views/TerminalScreen.swift`). That stack depends on two native speech runtimes (`FluidAudio` and `SherpaOnnx` in `ios/project.yml`), `Archive.swift` (`TarBz2` + `-lbz2`), and manual downloads of 241 MB to 1.01 GB models (SenseVoice Small, Fun-ASR-Nano, and Qwen3-ASR 0.6B) from Hugging Face, hf-mirror.com, or GitHub releases. Issue #13 plans to remove these self-managed ASR models and runtimes to reduce build, binary, and maintenance overhead.

When those runtimes go away, the iPhone terminal loses its keyboard-free voice entry point. Users want to speak into the terminal while the soft keyboard stays hidden, without switching to another app or pasting by hand. Issue #21 asks whether WindRex can trigger the user's active third-party input method (such as Doubao Input Method) while the keyboard is hidden, and asks for a concrete comparison of feasible paths if iOS forbids that.

There is also a small UI placement note in current code. On iPhone without a hardware keyboard (`TerminalView.hasBar == true`), `Mic` sits in the right capsule of the bottom `KeyBar`, not in the top navigation bar. On iPad or with a hardware keyboard (`hasBar == false`), `Mic` sits in the top navigation bar (`barTrailing` in `TerminalScreen`). Both placements work without raising the soft keyboard, but current `Mic` only supports tap-to-start and tap-to-finish, not press-and-hold.

## Goal

- Preserve voice input on iPhone while the soft keyboard remains hidden and `TerminalView.isFirstResponder` stays `false`.
- Remove all third-party ASR dependencies (`FluidAudio`, `SherpaOnnx`, `Archive.swift`, and `-lbz2`) so the app carries zero bundled or self-hosted ASR model bloat, aligning with issue #13.
- Support hold-to-talk (press and hold to speak, release to insert, drag to cancel) while keeping tap-to-talk for longer utterances.
- Insert Chinese and mixed Chinese-English text at the current terminal cursor without sending Enter automatically, and insert nothing when the user cancels.
- Document the exact iOS platform rules that govern third-party IMEs (including Doubao Input Method) and UIKit first-responder focus.

## Non-goals

- Shipping or downloading open-source ASR model weights (SenseVoice, Fun-ASR-Nano, Qwen3-ASR, or bundled Whisper weights). Issue #13 removes self-managed model downloads.
- Supporting iOS versions earlier than 26.0. `ios/project.yml` already sets `deploymentTarget.iOS` to `26.0`.
- Auto-submitting commands with a trailing Enter. Users review the inserted text and press `KeyBar`'s Enter key ([`IOS-013`](IOS-013-key-bar-enter.md)) to run it.
- Changing how third-party keyboards work once the user manually raises the soft keyboard. `TerminalView+TextInput.swift` already supports IME composition and dictation when `TerminalView` is first responder.

## Design

### Platform findings: third-party IMEs and UIKit focus

Research against Apple's UIKit and Keyboard Extension architecture shows that a host app cannot invoke an active third-party keyboard's voice input while the soft keyboard is hidden.

- **Keyboard Extension sandbox isolation**: A third-party input method (such as Doubao Input Method) runs as an isolated `UIInputViewController` extension process owned by the system, not by the host app. UIKit provides no public API to query which third-party keyboard is active, launch a Keyboard Extension without presenting the soft keyboard, or invoke a button inside that extension.
- **Passive `UITextInput` callbacks**: `TerminalView` conforms to `UITextInput` (`ios/GoRex/Terminal/TerminalView+TextInput.swift`) and implements `insertDictationResult(_:)`. Apple's `UITextInput` dictation callbacks (`insertDictationResult(_:)`, `dictationRecordingDidEnd()`, `dictationRecognitionFailed()`) are strictly passive receivers. They fire only after the user raises the keyboard and starts dictation inside the system keyboard. No public `UITextInput` or `UIResponder` method starts dictation programmatically.
- **Doubao Input Method constraints**: Doubao Input Method's press-and-hold voice input runs inside its Keyboard Extension (using Allow Full Access for network recognition or container app handoff) and writes text through `UITextDocumentProxy.insertText(_:)` only while the keyboard is raised and `TerminalView` is first responder. Opening Doubao's main app via URL scheme leaves WindRex and provides no standard callback to insert text back into WindRex's terminal without app switching and manual paste.
- **Why WindRex's `KeyBar` works without raising the keyboard**: `TerminalHost` (`ios/GoRex/Terminal/TerminalView.swift`) adds `TerminalView.bar` (`KeyBar`) as a direct subview pinned to `keyboardLayoutGuide.topAnchor`, not as `TerminalView.inputAccessoryView`. An `inputAccessoryView` only appears while a responder holds focus, whereas a direct subview pinned to `keyboardLayoutGuide.topAnchor` stays docked above the home indicator when `TerminalView.isFirstResponder` is `false`. Tapping or holding `KeyBar.mic` never calls `TerminalView.becomeFirstResponder()`. When transcription finishes, `TerminalView.typeDictation()` calls `TerminalView.insert(_:)`, which passes UTF-8 bytes directly to `ghostty_surface_text` (using bracketed paste if enabled) and streams them through `FDReader` to `TerminalSession.type(_:)` without needing UIKit first-responder status.

### Behavior

WindRex keeps `KeyBar`'s non-focusing microphone dock on iPhone (and `barTrailing` when `hasBar == false`) and replaces its recognition backend with Apple's native iOS 26 `SpeechAnalyzer` stack, keeping existing optional `Polish` cleanup and allowing an optional cloud ASR backend in Phase 2.

1. While the soft keyboard is hidden, `KeyBar` stays visible above the home indicator with `[stick, mic, keyboard]` in its right capsule and `Enter` on the first page of its middle capsule ([`IOS-013`](IOS-013-key-bar-enter.md)).
2. **Hold-to-talk**: The user presses and holds `Mic` for 250 ms or longer. The mic gives a light haptic tap, `KeyBar.setVoice(active: true)` morphs the middle and right capsules into `VoiceCapsule`, and live audio capture begins without raising the keyboard. While holding, `VoiceCapsule` shows the audio level meter and live streaming text (settled text in primary ink, volatile hypothesis in secondary ink). Releasing the finger stops capture, finalizes the transcript, runs optional `Polish`, and inserts the text at the terminal cursor with no trailing Enter. Sliding the finger left onto the `xmark` cancel target (or upward past a 48 pt cancel threshold) highlights cancel, and releasing cancels recording and inserts nothing.
3. **Tap-to-talk (retained for long prompts)**: A short tap under 250 ms locks recording open in `VoiceCapsule`. The user speaks freely, taps the red finish dot to commit, or taps `xmark` to cancel. On iPad or with a hardware keyboard (`hasBar == false`), `Mic` in `barTrailing` keeps the floating `Heard` preview overlay at the bottom.
4. **First-use permission and asset check**: On the first mic use, WindRex requests microphone permission (`AVAudioApplication.requestRecordPermission`) and speech recognition permission (`SFSpeechRecognizer.requestAuthorization`). If Apple's on-device locale model for the selected language is not yet installed, WindRex installs it via `AssetInventory.assetInstallationRequest(supporting:)` and shows download progress in `Mic` using the existing circular progress ring.
5. **Locale and technical term accuracy**: By default, WindRex selects the `Speech` locale matching the user's preferred language (`zh-CN` for Simplified Chinese, `en-US` for English) and exposes a compact language picker in Settings › Terminal › Dictation (`Auto`, `zh-CN`, `en-US`, plus installed system locales). On every start, WindRex reads the visible terminal screen via `Polish.words(in:program:)` and passes those tokens as `AnalysisContext.contextualStrings` to `DictationTranscriber` so English CLI commands, file paths, and identifiers are biased even under `zh-CN`. If the user configures a `Polish` API key (`ios/GoRex/Terminal/Polish.swift`), WindRex runs the existing 3-second LLM cleanup pass before insertion.
6. **Manual keyboard fallback**: Tapping the terminal or `KeyBar.keyboard` calls `TerminalView.becomeFirstResponder()` as today, raising the user's chosen keyboard (including Doubao Input Method) whenever the user prefers that IME's built-in voice input.

### Model

The dictation pipeline splits into a small protocol so the UI and audio lifecycle stay independent of the recognition backend:

- **`DictationEngine` protocol**: Accepts a stream of audio buffers and contextual vocabulary words, emits `AsyncStream<Dictation.Said>` updates (`phrases` for finalized ranges and `open` for volatile hypotheses), and returns the final transcript on `finish()` or discards work on `cancel()`.
- **Phase 1 engine (`AppleSpeechEngine`)**: Uses Apple's iOS 26 `Speech` framework: `SpeechAnalyzer` paired with `DictationTranscriber` (selected as primary module because `DictationTranscriber` targets short interactive utterances and supports `AnalysisContext.contextualStrings`, whereas `SpeechTranscriber` currently lacks custom vocabulary injection). When `DictationTranscriber` does not support a chosen locale, the engine falls back to `SpeechTranscriber` for that locale. `SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith:)` drives the `AVAudioConverter` format in `Recording`, eliminating manual Silero VAD segmentation.
- **Phase 2 optional engine (`CloudASREngine`)**: Uses the existing `Recording` (`AVAudioEngine`, 16 kHz mono PCM encoded to lightweight WAV or M4A in memory) and sends one multipart `POST {base}/audio/transcriptions` request to an OpenAI-compatible endpoint (such as OpenAI `gpt-4o-transcribe`, Groq `whisper-large-v3-turbo`, or a self-hosted Whisper server), passing `Polish.words(in:program:)` in the `prompt` field. Because batch upload produces no mid-speech hypotheses, `VoiceCapsule` shows the live audio meter while recording and a spinner while transcribing.
- **State rules**: Only one dictation session runs at a time. Leaving the terminal screen, switching panes ([`IOS-006`](IOS-006-live-activities.md)), entering background, or receiving `AVAudioSession.interruptionNotification` calls `TerminalView.stopDictating()`, which cancels capture and inserts nothing. Audio session deactivation uses `.notifyOthersOnDeactivation` so background audio and hotspot web share ([`IOS-003`](IOS-003-web-share.md)) recover cleanly.

### Code

- `ios/GoRex/Terminal/Dictation.swift`: Replace `FluidAudio`, `SherpaOnnxC`, `Model`, `Sherpa`, `Listening`, and Hugging Face downloaders with `DictationEngine`, `AppleSpeechEngine` (`SpeechAnalyzer` + `DictationTranscriber` + `AssetInventory`), and locale management. Keep `Dictation.Said`, `Dictation.joined(_:)`, `Recording`, `Turns`, and `Meter`.
- `ios/GoRex/Terminal/TerminalView.swift`: Keep `TerminalHost` anchoring `KeyBar` to `keyboardLayoutGuide.topAnchor` without `becomeFirstResponder()`. Update `dictate(said:)` to collect `Polish.words(in: screen, program:)` at start and pass them to `DictationEngine`. Keep `typeDictation()` committing through `insert(_:)` with no trailing Enter and optional `Polish.saved` pass.
- `ios/GoRex/Views/TerminalScreen.swift`: Upgrade `Mic` and `KeyBar.mic` gesture handling to support press-and-hold (start on touch-down/long-press, release to commit, drag to cancel) alongside short tap-to-talk, keeping `VoiceCapsule` and `Heard`.
- `ios/GoRex/Views/SettingsView.swift`: Replace the SenseVoice / Fun-ASR-Nano / Qwen3-ASR download rows with a dictation language selector (and Phase 2 optional Cloud ASR endpoint toggle/fields alongside `Polish`).
- `ios/GoRex/Terminal/Polish.swift`: Keep unchanged as the optional post-recognition cleanup pass and reuse `Polish.words(in:program:)` to populate `AnalysisContext.contextualStrings`.
- `ios/project.yml` and `ios/GoRex/InfoPlist.xcstrings`: Remove `FluidAudio`, `SherpaOnnx`, `Archive.swift`, and `-lbz2` (coordinated with issue #13). Add `NSSpeechRecognitionUsageDescription` (English and `zh-Hans`) and update `NSMicrophoneUsageDescription` so it accurately reflects on-device Apple Speech (and optional user-configured cloud service).

## Alternatives

| Option | Keyboard stays hidden | App size / build impact | Offline support | Latency | Mixed CN/EN tech terms | Permissions | iOS version |
| --- | --- | --- | --- | --- | --- | --- | --- |
| **0. Trigger active 3rd-party IME (Doubao) while hidden** | Impossible on iOS | Zero | N/A | N/A | N/A | Blocked by sandbox | Not possible |
| **A. Apple iOS 26 `SpeechAnalyzer` + `DictationTranscriber` behind `KeyBar` (Chosen, Phase 1)** | Yes | Zero binary bloat, OS-managed assets | Yes (after system asset download) | Very low (streaming volatile + final) | Good with `contextualStrings`, strong with optional `Polish` | Microphone + Speech Recognition | iOS 26.0 (already minimum) |
| **B. `AVAudioEngine` + Cloud `/v1/audio/transcriptions` behind `KeyBar` (Optional, Phase 2)** | Yes | Zero binary bloat | No (network + API key) | Medium (upload + batch RTT, ~0.8 to 2.5 s) | Best (Whisper/GPT-4o + screen vocabulary `prompt`) | Microphone only | iOS 26.0 |
| **C. `KeyBar` non-focusing UI without `becomeFirstResponder()` (Adopted as UI layer)** | Yes | Zero (already in `TerminalHost`) | Depends on engine (A or B) | UI adds 0 ms | Depends on engine (A or B) | Depends on engine | iOS 26.0 |
| **D. Raise soft keyboard to use Doubao IME (Manual fallback)** | No (~40% screen covered) | Zero | Depends on IME | Low | Very strong (Doubao cloud model) | None in WindRex (IME Full Access) | Any iOS |
| **E. Legacy `SFSpeechRecognizer` (`requiresOnDeviceRecognition`)** | Yes | Zero | Partial (60 s cap, older acoustic model) | Low | Weaker than iOS 26 `SpeechAnalyzer` | Microphone + Speech Recognition | iOS 13+ |

- **Option 0 (Invoke Doubao or active IME with keyboard hidden)**: Rejected because iOS provides no public API to run a Keyboard Extension without presenting the soft keyboard, and `UITextInput` dictation callbacks are passive.
- **Option B alone (Cloud ASR only)**: Rejected as the sole default because it fails offline, requires every user to supply and pay for a third-party API key, and sends raw voice audio off-device. It fits well as an opt-in Phase 2 backend for power users who want maximum mixed Chinese-English technical accuracy.
- **Option D alone (Remove `KeyBar` mic and rely on raising the soft keyboard)**: Rejected because raising the keyboard hides almost half of the terminal output and adds extra open/dismiss taps for short voice prompts. It remains available automatically whenever the user taps the terminal or `KeyBar.keyboard`.
- **Option E (Legacy `SFSpeechRecognizer`)**: Rejected because WindRex already targets iOS 26.0, where `SpeechAnalyzer` and `DictationTranscriber` provide modern actor-based streaming, no 1-minute session cap, and `AssetInventory` management without deprecated callback plumbing.

## Security

- **Permissions**: Phase 1 requires `NSMicrophoneUsageDescription` (already present) and adds `NSSpeechRecognitionUsageDescription` to `ios/project.yml` and `ios/GoRex/InfoPlist.xcstrings`. Without `NSSpeechRecognitionUsageDescription`, iOS terminates the app when requesting speech authorization.
- **On-device boundary**: With Phase 1 defaults (`AppleSpeechEngine` and no `Polish` API key), audio stays on device inside Apple's local `SpeechAnalyzer` model and no transcript or terminal text leaves the phone.
- **Opt-in external services**: Both `Polish` and Phase 2 `CloudASREngine` are strictly opt-in and inactive until the user enters an API key. API keys live in iCloud Keychain with `kSecAttrSynchronizable` ([`IOS-008`](IOS-008-icloud-sync.md)). `Polish.words(in:program:)` already filters out hex hashes and long digit-bearing tokens so API keys visible on screen are not sent as vocabulary hints.
- **Prompt injection and command safety**: Dictated text is never auto-submitted with Enter (`TerminalView.insert(_:)` inserts text only). `Polish` wraps recognized text in `<dictation>` tags and rejects refusals or runaway expansions via `Polish.accepts(_:for:)`.

## Migration

- Removing `FluidAudio`, `SherpaOnnx`, `Archive.swift`, and `-lbz2` drops two external dependencies and stops all Hugging Face / GitHub model downloads.
- On first launch after upgrade, WindRex removes any leftover `Application Support/FluidAudio` directory (which previously held up to ~2.5 GB of SenseVoice, Silero VAD, Fun-ASR-Nano, or Qwen3-ASR weights) and clears the obsolete `dictationModel` `UserDefaults` key.
- Existing `Polish` settings (`polishBase`, `polishModel`, and the iCloud Keychain API key) remain intact and continue working with `AppleSpeechEngine`.

## Tests

- **Unit tests (`ios/GoRexTests/DictationTests.swift`)**: Verify `Dictation.Said` joining for Chinese, English, and mixed sentences, gesture state transitions (hold-to-talk commit, slide-to-cancel abort, short-tap lock), cancellation dropping any in-flight recognition, and screen vocabulary extraction (`PolishTests.swift`).
- **UI and focus checks**: Verify in iOS Simulator and on a real iPhone that activating `KeyBar.mic` never calls `becomeFirstResponder()`, leaves `TerminalView.isFirstResponder == false`, and keeps `KeyBar` docked at the bottom with no keyboard frame change.
- **Real-iPhone verification checklist (issue #21 acceptance criteria)**:
  1. Confirm on a real iPhone with Doubao Input Method active that hiding the keyboard leaves no Doubao voice entry accessible without raising the keyboard, and record exact reproduction steps in issue #21.
  2. Dictate pure Simplified Chinese (`zh-CN`) via `KeyBar.mic` with the keyboard hidden and confirm text appears at the terminal cursor with no automatic Enter.
  3. Dictate mixed Chinese and English technical phrases (for example, CLI flags, `git status`, `TerminalView`, file paths) with and without `Polish`, and confirm insertion accuracy and absence of trailing Enter.
  4. Cancel recording (slide-to-cancel during hold-to-talk, or tap `xmark` during tap-to-talk) and confirm zero bytes are sent to the terminal.
  5. Repeat with the soft keyboard raised using Doubao Input Method's built-in voice button to confirm the manual fallback continues to insert via `TerminalView+TextInput.swift`.
- **Invariants**: No item in `docs/invariants.md` names dictation or `KeyBar`. Existing `InputDocumentTests` remain green for the keyboard fallback path.

## Phases

1. **Phase 1 — Apple `SpeechAnalyzer` + hold-to-talk `KeyBar` mic**: Replace `FluidAudio` and `SherpaOnnx` with `AppleSpeechEngine` (`SpeechAnalyzer` + `DictationTranscriber` + `AnalysisContext.contextualStrings`), add hold-to-talk with slide-to-cancel alongside tap-to-talk, add `NSSpeechRecognitionUsageDescription`, and clean up old `Application Support/FluidAudio` files (coordinated with issue #13).
2. **Phase 2 — Optional Cloud ASR backend**: Add an opt-in OpenAI-compatible `/v1/audio/transcriptions` backend (`CloudASREngine`) behind `DictationEngine` for users who want cloud Whisper / GPT-4o accuracy on heavy Chinese-English technical speech without raising the keyboard.

## Open questions

1. **Real-device `zh-CN` mixed-language quality on iOS 26**: How well does `DictationTranscriber` with `zh-CN` and `AnalysisContext.contextualStrings` recognize mixed Chinese-English coding terms on a real iPhone in mainland China, and how fast does `AssetInventory` download the system model there? Decider: maintainer, after testing a Phase 1 build on a physical iPhone.
2. **Phase 2 timing**: Should the optional OpenAI-compatible `/v1/audio/transcriptions` backend ship immediately after Phase 1 or wait until real-device Phase 1 accuracy is measured? Decider: maintainer.
3. **Hold-to-talk vs horizontal gesture ergonomics**: Should slide-to-cancel on `KeyBar.mic` trigger on dragging left (toward `VoiceCapsule`'s `xmark`), dragging up (away from the home indicator), or either? Decider: maintainer, during Phase 1 prototype review.
