import AVFoundation
import Speech
import XCTest
import os

/// Tests for native iOS 26 dictation: transcript accumulation, locale
/// resolution, permission evaluation, screen vocabulary extraction, legacy
/// artifact cleanup, audio capture helpers, and session lifecycle.
nonisolated final class DictationTests: XCTestCase {
    private nonisolated final class FakeSession: DictationSession, @unchecked Sendable {
        let said: AsyncStream<Dictation.Said>
        let heard: Task<String, Error>
        private let saidCont: AsyncStream<Dictation.Said>.Continuation
        private let finishGate: AsyncStream<String>.Continuation
        private let lock = OSAllocatedUnfairLock(initialState: (inputs: 0, cancelled: false))

        var inputCount: Int { lock.withLock { $0.inputs } }
        var isCancelled: Bool { lock.withLock { $0.cancelled } }

        init() {
            let (saidStream, saidCont) = AsyncStream.makeStream(of: Dictation.Said.self, bufferingPolicy: .bufferingNewest(8))
            let (gateStream, gateCont) = AsyncStream.makeStream(of: String.self)
            self.said = saidStream
            self.saidCont = saidCont
            self.finishGate = gateCont
            self.heard = Task<String, Error> {
                defer { saidCont.finish() }
                for await result in gateStream {
                    try Task.checkCancellation()
                    return result
                }
                try Task.checkCancellation()
                return ""
            }
        }

        func emit(_ value: Dictation.Said) {
            saidCont.yield(value)
        }

        func resolve(with text: String) {
            finishGate.yield(text)
            finishGate.finish()
        }

        func add(_ input: AnalyzerInput) {
            lock.withLock { $0.inputs += 1 }
        }

        func finish() async throws -> String {
            finishGate.finish()
            return try await heard.value
        }

        func cancel() {
            lock.withLock { $0.cancelled = true }
            finishGate.finish()
            heard.cancel()
        }
    }

    /// Volatile hypotheses replace `open` while finalized results append to
    /// `phrases`, joining Chinese, English, and mixed sentences cleanly.
    func testTranscriptAccumulatorVolatileAndFinal() {
        var acc = TranscriptAccumulator()
        XCTAssertEqual(acc.said, Dictation.Said())
        XCTAssertEqual(acc.finalText, "")

        var s = acc.apply(text: "打开", isFinal: false)
        XCTAssertEqual(s.phrases, [])
        XCTAssertEqual(s.open, "打开")
        XCTAssertEqual(s.text, "打开")

        s = acc.apply(text: "打开 TerminalView", isFinal: true)
        XCTAssertEqual(s.phrases, ["打开 TerminalView"])
        XCTAssertEqual(s.open, "")
        XCTAssertEqual(s.settled, "打开 TerminalView")

        // Whitespace-only final results do not pollute phrases.
        s = acc.apply(text: "   \n ", isFinal: true)
        XCTAssertEqual(s.phrases, ["打开 TerminalView"])

        s = acc.apply(text: "然后运行测试", isFinal: false)
        XCTAssertEqual(s.text, "打开 TerminalView 然后运行测试")
        XCTAssertEqual(s.settled, "打开 TerminalView")

        s = acc.apply(text: "然后运行 go test。", isFinal: true)
        XCTAssertEqual(s.phrases, ["打开 TerminalView", "然后运行 go test。"])
        XCTAssertEqual(s.open, "")
        XCTAssertEqual(acc.finalText, "打开 TerminalView 然后运行 go test。")
    }

    /// Preferred locale resolves explicit choices directly and maps system
    /// preferred languages cleanly under `.auto`.
    func testPreferredLocaleResolution() {
        XCTAssertEqual(Dictation.preferredLocale(choice: .zhCN, preferredLanguages: ["en-US"]).identifier, "zh-CN")
        XCTAssertEqual(Dictation.preferredLocale(choice: .enUS, preferredLanguages: ["zh-Hans-CN"]).identifier, "en-US")
        XCTAssertEqual(Dictation.preferredLocale(choice: .auto, preferredLanguages: ["zh-Hans-CN"]).identifier, "zh-CN")
        XCTAssertEqual(Dictation.preferredLocale(choice: .auto, preferredLanguages: ["zh-Hant-TW"]).identifier, "zh-TW")
        XCTAssertEqual(Dictation.preferredLocale(choice: .auto, preferredLanguages: ["en"]).identifier, "en-US")
        XCTAssertEqual(Dictation.preferredLocale(choice: .auto, preferredLanguages: ["en-GB"]).identifier, "en-GB")
        XCTAssertEqual(
            Dictation.preferredLocale(choice: .auto, preferredLanguages: [], current: Locale(identifier: "ja-JP")).identifier,
            "ja-JP"
        )
    }

    /// Selected language preference persists in UserDefaults.
    @MainActor func testLocaleOptionPersists() {
        let d = Dictation.shared
        let was = d.localeOption
        defer { d.localeOption = was }

        d.localeOption = .zhCN
        XCTAssertEqual(UserDefaults.standard.string(forKey: Dictation.localeKey), "zh-CN")
        d.localeOption = .enUS
        XCTAssertEqual(UserDefaults.standard.string(forKey: Dictation.localeKey), "en-US")
        d.localeOption = .auto
        XCTAssertEqual(UserDefaults.standard.string(forKey: Dictation.localeKey), "auto")
    }

    /// Microphone and speech permissions evaluate every denied, undetermined,
    /// and granted combination deterministically.
    func testEvaluateAccessStates() {
        XCTAssertEqual(Dictation.evaluateAccess(mic: .granted, speech: .authorized), .granted)
        XCTAssertEqual(
            Dictation.evaluateAccess(mic: .undetermined, speech: .notDetermined),
            .needsRequest(mic: true, speech: true)
        )
        XCTAssertEqual(
            Dictation.evaluateAccess(mic: .granted, speech: .notDetermined),
            .needsRequest(mic: false, speech: true)
        )
        XCTAssertEqual(
            Dictation.evaluateAccess(mic: .undetermined, speech: .authorized),
            .needsRequest(mic: true, speech: false)
        )

        if case .denied(let title, _) = Dictation.evaluateAccess(mic: .denied, speech: .authorized) {
            XCTAssertFalse(title.isEmpty)
        } else {
            XCTFail("expected mic denied")
        }

        if case .denied(let title, _) = Dictation.evaluateAccess(mic: .granted, speech: .denied) {
            XCTAssertFalse(title.isEmpty)
        } else {
            XCTFail("expected speech denied")
        }

        if case .denied(let title, _) = Dictation.evaluateAccess(mic: .granted, speech: .restricted) {
            XCTAssertFalse(title.isEmpty)
        } else {
            XCTFail("expected speech restricted")
        }
    }

    /// Screen vocabulary extraction passes CLI commands and identifiers while
    /// excluding hex hashes and secret-like tokens.
    func testContextualVocabularyExtraction() {
        let screen = """
        $ git checkout feat/ios-native-speech
        commit a1b2c3d4e5f60718293a4b5c6d7e8f90
        Editing ios/GoRex/Terminal/Dictation.swift and SpeechAnalyzer
        """
        let words = Dictation.contextualVocabulary(screen: screen, program: "claude")
        XCTAssertEqual(words.first, "claude")
        XCTAssertTrue(words.contains("SpeechAnalyzer"))
        XCTAssertTrue(words.contains("Dictation.swift"))
        XCTAssertTrue(words.contains("git"))
        XCTAssertFalse(words.contains("a1b2c3d4e5f60718293a4b5c6d7e8f90"))
    }

    /// Startup cleanup deletes leftover `Application Support/FluidAudio` files
    /// and clears the obsolete `dictationModel` preference safely.
    func testCleanLegacyArtifactsRemovesFluidAudioAndModelDefault() throws {
        let fm = FileManager.default
        let appSupport = fm.temporaryDirectory.appending(path: "gorex-app-support-\(UUID().uuidString)", directoryHint: .isDirectory)
        let fluidDir = appSupport.appending(path: "FluidAudio", directoryHint: .isDirectory)
        try fm.createDirectory(at: fluidDir, withIntermediateDirectories: true)
        try Data("stub".utf8).write(to: fluidDir.appending(path: "model.bin"))
        defer { try? fm.removeItem(at: appSupport) }

        let suite = "wiki.wind.windrex.tests.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        defaults.set("senseVoice", forKey: Dictation.legacyModelKey)

        Dictation.cleanLegacyArtifacts(appSupport: appSupport, defaults: defaults)
        XCTAssertFalse(fm.fileExists(atPath: fluidDir.path))
        XCTAssertNil(defaults.object(forKey: Dictation.legacyModelKey))

        // Running again when already clean is a safe no-op.
        Dictation.cleanLegacyArtifacts(appSupport: appSupport, defaults: defaults)
        XCTAssertNil(defaults.object(forKey: Dictation.legacyModelKey))
    }

    /// Mock session lifecycle verifies streaming updates, final commit, and
    /// cancellation discarding work.
    func testSessionLifecycleFinishAndCancel() async throws {
        let session = FakeSession()
        session.emit(Dictation.Said(phrases: [], open: "检查"))
        session.emit(Dictation.Said(phrases: ["检查 git status"], open: ""))

        let buf = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: Recording.format, frameCapacity: 160))
        buf.frameLength = 160
        session.add(AnalyzerInput(buffer: buf))
        XCTAssertEqual(session.inputCount, 1)

        session.resolve(with: "检查 git status")
        let final = try await session.finish()
        XCTAssertEqual(final, "检查 git status")

        let cancelled = FakeSession()
        cancelled.cancel()
        XCTAssertTrue(cancelled.isCancelled)
        do {
            _ = try await cancelled.finish()
            XCTFail("expected cancellation error")
        } catch is CancellationError {
        }
    }

    /// The level shown is the newest the microphone gave, taken once.
    func testMeterGivesTheNewestLevelOnce() {
        let meter = Meter()
        var count: UInt64 = 0
        XCTAssertNil(meter.take(after: &count))
        meter.set(0.1)
        meter.set(0.2)
        XCTAssertEqual(meter.take(after: &count), 0.2)
        XCTAssertNil(meter.take(after: &count))
        meter.set(0)
        XCTAssertEqual(meter.take(after: &count), 0)
        XCTAssertNil(meter.take(after: &count))
    }

    /// A stop asked for while the microphone starts wins: it runs after
    /// the start, which is told it ran.
    func testStopDuringStartRunsAfterIt() {
        let queue = DispatchQueue(label: "test.turns")
        let turns = Turns(on: queue)
        let log = OSAllocatedUnfairLock(initialState: [String]())
        let starting = DispatchSemaphore(value: 0)
        let go = DispatchSemaphore(value: 0)
        let done = expectation(description: "done")
        turns.start({
            starting.signal()
            go.wait()
            log.withLock { $0.append("start") }
        }, done: { r in
            XCTAssertEqual(try? r.get(), true)
            done.fulfill()
        })
        starting.wait()
        let stopped = expectation(description: "stopped")
        turns.stop {
            log.withLock { $0.append("stop") }
            stopped.fulfill()
        }
        go.signal()
        wait(for: [done, stopped], timeout: 10)
        XCTAssertEqual(log.withLock { $0 }, ["start", "stop"])
    }

    /// A stop asked for before the start begins passes the start over; a
    /// start that throws tells why.
    func testStopBeforeStartPassesItOver() {
        let queue = DispatchQueue(label: "test.turns")
        let turns = Turns(on: queue)
        let log = OSAllocatedUnfairLock(initialState: [String]())
        let busy = DispatchSemaphore(value: 0)
        queue.async { busy.wait() }
        let done = expectation(description: "done")
        turns.start({ log.withLock { $0.append("start") } }, done: { r in
            XCTAssertEqual(try? r.get(), false)
            done.fulfill()
        })
        let stopped = expectation(description: "stopped")
        turns.stop {
            log.withLock { $0.append("stop") }
            stopped.fulfill()
        }
        busy.signal()
        wait(for: [done, stopped], timeout: 10)
        XCTAssertEqual(log.withLock { $0 }, ["stop"])

        let failed = expectation(description: "failed")
        Turns(on: queue).start({ throw CancellationError() }, done: { r in
            if case .failure(let e) = r { XCTAssertTrue(e is CancellationError) } else { XCTFail("\(r)") }
            failed.fulfill()
        })
        wait(for: [failed], timeout: 10)
    }

    /// The microphone's 48 kHz converts to 16 kHz into independent owned
    /// buffers safe to pass into `SpeechAnalyzer` as `AnalyzerInput`.
    func testConverterProducesIndependentOwnedBuffers() throws {
        let source = try XCTUnwrap(AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48_000, channels: 1, interleaved: false))
        let converter = try XCTUnwrap(Recording.Converter(from: source, to: Recording.format))
        let input = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: source, frameCapacity: 4_800))
        input.frameLength = 4_800
        for i in 0..<4_800 { input.floatChannelData![0][i] = sin(Float(i) * 0.05) }

        let first = try XCTUnwrap(converter.convertBuffer(input))
        let second = try XCTUnwrap(converter.convertBuffer(input))
        XCTAssertFalse(first === second)
        XCTAssertEqual(first.format, Recording.format)
        var total = Int(first.frameLength + second.frameLength)
        for _ in 0..<3 {
            total += Int(try XCTUnwrap(converter.convertBuffer(input)).frameLength)
        }
        XCTAssertEqual(Double(total), 8_000, accuracy: 200)
        XCTAssertNotNil(converter.convertToInput(input))
    }

    /// English phrases take a space; Chinese and Japanese take none between
    /// them, and keep one beside Latin words.
    func testPhrasesJoinAsWritten() {
        XCTAssertEqual(Dictation.joined(["Hello there.", "How are you?"]), "Hello there. How are you?")
        XCTAssertEqual(Dictation.joined(["今天天气很好。", "我们出去吧。"]), "今天天气很好。我们出去吧。")
        XCTAssertEqual(Dictation.joined(["こ ん に ち は"]), "こんにちは")
        XCTAssertEqual(Dictation.joined(["我在用 Claude Code 写代码。"]), "我在用 Claude Code 写代码。")
        XCTAssertEqual(Dictation.joined([]), "")
        XCTAssertEqual(Dictation.joined(["", " "]), "")
    }

    /// What is said so far: the phrases for good, then the guess at the one
    /// open, written as one; what is for good begins it.
    func testSaidShowsTheOpenPhraseAfterThoseForGood() {
        var said = Dictation.Said()
        XCTAssertEqual(said.text, "")
        said.open = "Hello"
        XCTAssertEqual(said.text, "Hello")
        XCTAssertEqual(said.settled, "")
        said.open = ""
        said.phrases = ["Hello there."]
        XCTAssertEqual(said.text, "Hello there.")
        said.open = "How are"
        XCTAssertEqual(said.text, "Hello there. How are")
        XCTAssertEqual(said.settled, "Hello there.")
        said = Dictation.Said(phrases: ["今天天气很好。"], open: "我们出")
        XCTAssertEqual(said.text, "今天天气很好。我们出")
        XCTAssertEqual(said.settled, "今天天气很好。")
        said = Dictation.Said(phrases: ["我在用"], open: "Claude Code")
        XCTAssertEqual(said.text, "我在用 Claude Code")
        for s in [Dictation.Said(phrases: ["A.", "今天"], open: "好"), Dictation.Said(phrases: ["こ ん"], open: "に ち は")] {
            XCTAssertTrue(s.text.hasPrefix(s.settled), "\(s)")
        }
    }

    /// The level shown: -50 dB and below is none, -10 dB and above full;
    /// it rises at once and falls by half.
    func testLevelRisesAtOnceAndFallsByHalf() {
        XCTAssertEqual(Dictation.level(of: 0, after: 0), 0)
        XCTAssertEqual(Dictation.level(of: 0.001, after: 0), 0, accuracy: 0.001)
        XCTAssertEqual(Dictation.level(of: 0.01, after: 0), 0.25, accuracy: 0.001)
        XCTAssertEqual(Dictation.level(of: 0.1, after: 0), 0.75, accuracy: 0.001)
        XCTAssertEqual(Dictation.level(of: 1, after: 0), 1)
        XCTAssertEqual(Dictation.level(of: 0, after: 0.8), 0.4, accuracy: 0.001)
        XCTAssertEqual(Dictation.level(of: 0.1, after: 0.5), 0.75, accuracy: 0.001)
    }
}
