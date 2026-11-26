import Accelerate
import AVFoundation
import Speech
import Synchronization
import UIKit
import os

/// On-device dictation backed by Apple's iOS 26 Speech framework
/// (`SpeechAnalyzer` + `DictationTranscriber`, falling back to
/// `SpeechTranscriber` when a locale is not supported by
/// `DictationTranscriber`), keeping voice input keyboard-free behind `KeyBar`
/// (docs/IOS-024-voice-without-keyboard.md).
@Observable final class Dictation {
    static let shared = Dictation()

    nonisolated static let localeKey = "dictationLocale"
    nonisolated static let legacyModelKey = "dictationModel"

    /// Preferred recognition language configured in Settings › Terminal.
    nonisolated enum LocaleOption: String, CaseIterable, Identifiable, Sendable {
        case auto = "auto"
        case zhCN = "zh-CN"
        case enUS = "en-US"

        var id: String { rawValue }

        var locale: Locale? {
            switch self {
            case .auto: nil
            case .zhCN: Locale(identifier: "zh-CN")
            case .enUS: Locale(identifier: "en-US")
            }
        }

        var title: String {
            switch self {
            case .auto: String(localized: "Automatic")
            case .zhCN: "中文（简体）"
            case .enUS: "English (US)"
            }
        }
    }

    /// Microphone and speech recognition authorization state.
    nonisolated enum AccessState: Equatable, Sendable {
        case granted
        case needsRequest(mic: Bool, speech: Bool)
        case denied(title: String, message: String)
    }

    /// Language preference saved in UserDefaults.
    var localeOption = LocaleOption(rawValue: UserDefaults.standard.string(forKey: localeKey) ?? "") ?? .auto {
        didSet {
            UserDefaults.standard.set(localeOption.rawValue, forKey: Self.localeKey)
        }
    }

    /// The microphone's last levels while the mic is on, 0 to 1, the newest last.
    var levels = [Float](repeating: 0, count: 5)
    /// How much of an Apple Speech on-device asset download is done; nil when idle.
    private(set) var progress: Double?
    /// Latest asset download error shown to the user.
    var error: String?

    private var downloadTask: Task<Bool, Error>?
    private var downloadProgress: Progress?

    private init() {}

    /// Removes legacy third-party ASR model weights (`Application Support/FluidAudio`)
    /// and the obsolete `dictationModel` preference left by older builds.
    nonisolated static func cleanLegacyArtifacts(
        appSupport: URL = URL.applicationSupportDirectory,
        defaults: UserDefaults = .standard
    ) {
        defaults.removeObject(forKey: legacyModelKey)
        let legacyDir = appSupport.appending(path: "FluidAudio")
        if FileManager.default.fileExists(atPath: legacyDir.path) {
            try? FileManager.default.removeItem(at: legacyDir)
        }
    }

    /// Clears the obsolete preference immediately and deletes legacy model
    /// files off the main actor.
    static func cleanLegacyArtifactsInBackground() {
        UserDefaults.standard.removeObject(forKey: legacyModelKey)
        let appSupport = URL.applicationSupportDirectory
        Task.detached(priority: .utility) {
            let legacyDir = appSupport.appending(path: "FluidAudio")
            if FileManager.default.fileExists(atPath: legacyDir.path) {
                try? FileManager.default.removeItem(at: legacyDir)
            }
        }
    }

    /// Resolves the preferred Speech locale from the user's setting or system
    /// language list.
    nonisolated static func preferredLocale(
        choice: LocaleOption,
        preferredLanguages: [String] = Locale.preferredLanguages,
        current: Locale = .current
    ) -> Locale {
        if let explicit = choice.locale { return explicit }
        if let first = preferredLanguages.first, !first.isEmpty {
            let lower = first.lowercased()
            if lower.hasPrefix("zh-hans") || lower == "zh-cn" || lower == "zh" {
                return Locale(identifier: "zh-CN")
            }
            if lower.hasPrefix("zh-hant") || lower == "zh-tw" || lower == "zh-hk" {
                return Locale(identifier: "zh-TW")
            }
            if lower.hasPrefix("en") {
                return Locale(identifier: first.contains("-") || first.contains("_") ? first : "en-US")
            }
            return Locale(identifier: first)
        }
        return current
    }

    /// Extracts screen identifiers and CLI names for `AnalysisContext.contextualStrings`.
    nonisolated static func contextualVocabulary(screen: String, program: String?, limit: Int = 150) -> [String] {
        Polish.words(in: screen, program: program, limit: limit)
    }

    /// Pure access check over microphone and speech recognition authorization.
    nonisolated static func evaluateAccess(
        mic: AVAudioApplication.recordPermission,
        speech: SFSpeechRecognizerAuthorizationStatus
    ) -> AccessState {
        if mic == .denied {
            return .denied(
                title: String(localized: "No Access to the Microphone"),
                message: String(localized: "Allow WindRex to use the microphone in Settings.")
            )
        }
        if speech == .denied || speech == .restricted {
            return .denied(
                title: String(localized: "No Access to Speech Recognition"),
                message: String(localized: "Allow WindRex to use Speech Recognition in Settings.")
            )
        }
        let needMic = mic == .undetermined
        let needSpeech = speech == .notDetermined
        if needMic || needSpeech {
            return .needsRequest(mic: needMic, speech: needSpeech)
        }
        return .granted
    }

    /// Checks and, when undetermined, requests microphone and speech
    /// recognition permissions.
    func requestAccess() async -> AccessState {
        let initial = Self.evaluateAccess(
            mic: AVAudioApplication.shared.recordPermission,
            speech: SFSpeechRecognizer.authorizationStatus()
        )
        guard case .needsRequest(let needMic, let needSpeech) = initial else { return initial }
        if needMic {
            _ = await AVAudioApplication.requestRecordPermission()
        }
        if needSpeech {
            _ = await withCheckedContinuation { (c: CheckedContinuation<SFSpeechRecognizerAuthorizationStatus, Never>) in
                SFSpeechRecognizer.requestAuthorization { c.resume(returning: $0) }
            }
        }
        return Self.evaluateAccess(
            mic: AVAudioApplication.shared.recordPermission,
            speech: SFSpeechRecognizer.authorizationStatus()
        )
    }

    /// Cancels an active OS speech asset download, if any.
    func cancelDownload() {
        downloadProgress?.cancel()
        downloadTask?.cancel()
    }

    /// Prepares an on-device Apple Speech session for the preferred locale,
    /// downloading the OS locale asset via `AssetInventory` if needed.
    /// Returns `nil` when the user cancels an asset download.
    func startSession(vocabulary: [String]) async throws -> (session: any DictationSession, format: AVAudioFormat)? {
        let preferred = Self.preferredLocale(choice: localeOption)
        let transcriber = try await Self.makeTranscriber(for: preferred)
        let installed = try await ensureAssets(for: transcriber)
        guard installed else { return nil }
        let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber.module]) ?? Recording.format
        let session = try await AppleSpeechSession.start(
            transcriber: transcriber,
            format: format,
            vocabulary: vocabulary
        )
        return (session, format)
    }

    /// Chooses `DictationTranscriber` (which supports contextual vocabulary),
    /// falling back to `SpeechTranscriber` when a locale is unavailable there.
    nonisolated private static func makeTranscriber(for preferred: Locale) async throws -> TranscriberModule {
        if let locale = await DictationTranscriber.supportedLocale(equivalentTo: preferred) {
            let module = DictationTranscriber(
                locale: locale,
                contentHints: [.shortForm],
                transcriptionOptions: [.punctuation],
                reportingOptions: [.volatileResults, .frequentFinalization],
                attributeOptions: []
            )
            return .dictation(module)
        }
        if SpeechTranscriber.isAvailable,
           let locale = await SpeechTranscriber.supportedLocale(equivalentTo: preferred) {
            let module = SpeechTranscriber(locale: locale, preset: .progressiveTranscription)
            return .speech(module)
        }
        throw RexError(
            code: "dictation",
            message: String(localized: "On-device speech recognition is not available for this language.")
        )
    }

    /// Ensures the OS on-device speech assets for `transcriber` are installed,
    /// reporting download progress on `Dictation.progress`.
    private func ensureAssets(for transcriber: TranscriberModule) async throws -> Bool {
        let status = await AssetInventory.status(forModules: [transcriber.module])
        switch status {
        case .installed:
            return true
        case .unsupported:
            throw RexError(
                code: "dictation",
                message: String(localized: "On-device speech recognition is not available for this language.")
            )
        case .supported, .downloading:
            guard let request = try await AssetInventory.assetInstallationRequest(supporting: [transcriber.module]) else {
                return true
            }
            error = nil
            progress = request.progress.fractionCompleted
            downloadProgress = request.progress
            let poll = Task { [weak self] in
                while !Task.isCancelled {
                    self?.progress = min(1, max(0, request.progress.fractionCompleted))
                    try? await Task.sleep(for: .milliseconds(100))
                }
            }
            let work = Task { () throws -> Bool in
                try await request.downloadAndInstall()
                return !Task.isCancelled && !request.progress.isCancelled
            }
            downloadTask = work
            defer {
                poll.cancel()
                downloadProgress = nil
                downloadTask = nil
                progress = nil
            }
            do {
                return try await work.value
            } catch is CancellationError {
                return false
            } catch {
                if request.progress.isCancelled || Task.isCancelled { return false }
                self.error = error.localizedDescription
                throw error
            }
        @unknown default:
            return true
        }
    }

    /// Takes the level of samples the microphone heard.
    func hear(_ level: Float) {
        levels = levels.dropFirst() + [Self.level(of: level, after: levels.last ?? 0)]
    }

    /// Shows a meter's levels, each new one once, looking at most 30 times
    /// a second, until cancelled.
    func show(_ meter: Meter) -> Task<Void, Never> {
        Task {
            var count: UInt64 = 0
            while !Task.isCancelled {
                if let rms = meter.take(after: &count) { hear(rms) }
                try? await Task.sleep(for: .seconds(1.0 / 30))
            }
        }
    }

    /// The level to show for samples of an RMS: from -50 dB to -10 dB, as
    /// 0 to 1; up at once, and down by half.
    nonisolated static func level(of rms: Float, after last: Float) -> Float {
        let level = min(1, max(0, (20 * log10(max(rms, 1e-6)) + 50) / 40))
        return max(level, (last + level) / 2)
    }

    /// What is said so far: each phrase recognised for good, and the guess
    /// at the one still open.
    nonisolated struct Said: Equatable, Sendable {
        var phrases: [String] = []
        var open = ""
        /// Whether it is being polished (Polish), the mic off.
        var polishing = false
        /// All of it, written as spoken.
        var text: String { Dictation.joined(phrases + [open]) }
        /// What of text is for good, its beginning.
        var settled: String { Dictation.joined(phrases) }
    }

    /// Phrases, spaced as words are: none between Chinese or Japanese on
    /// both sides, keeping spaces beside Latin words.
    nonisolated static func joined(_ phrases: [String]) -> String {
        var out = ""
        var gap = ""
        for c in phrases.joined(separator: " ") {
            if c == " " { gap.append(c); continue }
            if let last = out.last, unspaced(last), unspaced(c) { gap = "" }
            out += gap
            out.append(c)
            gap = ""
        }
        return out.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Han, kana and full-width punctuation, written without spaces.
    nonisolated private static func unspaced(_ c: Character) -> Bool {
        switch c.unicodeScalars.first?.value ?? 0 {
        case 0x3000...0x30FF, 0x31F0...0x31FF, 0x3400...0x4DBF, 0x4E00...0x9FFF,
             0xF900...0xFAFF, 0xFF00...0xFFEF, 0x20000...0x2FA1F: true
        default: false
        }
    }
}

/// Accumulates streaming volatile and finalized transcription updates into
/// `Dictation.Said`.
nonisolated struct TranscriptAccumulator: Sendable {
    private(set) var said = Dictation.Said()

    @discardableResult
    mutating func apply(text: String, isFinal: Bool) -> Dictation.Said {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if isFinal {
            said.open = ""
            if !trimmed.isEmpty { said.phrases.append(trimmed) }
        } else {
            said.open = trimmed
        }
        return said
    }

    var finalText: String {
        said.text
    }
}

/// Active recognition session consumed by `TerminalView`.
nonisolated protocol DictationSession: AnyObject, Sendable {
    /// Streaming transcript updates as recognition progresses.
    var said: AsyncStream<Dictation.Said> { get }
    /// Final transcript task, resolving once recognition finishes.
    var heard: Task<String, Error> { get }
    /// Feeds converted audio into the recognition session.
    func add(_ input: AnalyzerInput)
    /// Ends audio input, finalizes pending hypotheses, and returns the transcript.
    func finish() async throws -> String
    /// Aborts recognition and discards pending audio/results.
    func cancel()
}

/// Concrete Apple Speech module selected for a session.
nonisolated enum TranscriberModule: Sendable {
    case dictation(DictationTranscriber)
    case speech(SpeechTranscriber)

    var module: any SpeechModule {
        switch self {
        case .dictation(let m): m
        case .speech(let m): m
        }
    }
}

/// Live recognition session powered by `SpeechAnalyzer` + `DictationTranscriber`
/// (or `SpeechTranscriber` fallback).
nonisolated final class AppleSpeechSession: DictationSession, @unchecked Sendable {
    let said: AsyncStream<Dictation.Said>
    let heard: Task<String, Error>

    private let analyzer: SpeechAnalyzer
    private let input: AsyncStream<AnalyzerInput>.Continuation

    private init(
        analyzer: SpeechAnalyzer,
        input: AsyncStream<AnalyzerInput>.Continuation,
        said: AsyncStream<Dictation.Said>,
        heard: Task<String, Error>
    ) {
        self.analyzer = analyzer
        self.input = input
        self.said = said
        self.heard = heard
    }

    /// Starts `SpeechAnalyzer` with optional contextual vocabulary strings and
    /// begins streaming results into `said`.
    static func start(
        transcriber: TranscriberModule,
        format: AVAudioFormat,
        vocabulary: [String]
    ) async throws -> AppleSpeechSession {
        let (inputStream, inputContinuation) = AsyncStream.makeStream(
            of: AnalyzerInput.self,
            bufferingPolicy: .bufferingNewest(64)
        )
        let (saidStream, saidContinuation) = AsyncStream.makeStream(
            of: Dictation.Said.self,
            bufferingPolicy: .bufferingNewest(1)
        )
        let analyzer = SpeechAnalyzer(
            modules: [transcriber.module],
            options: SpeechAnalyzer.Options(priority: .userInitiated, modelRetention: .lingering)
        )
        if !vocabulary.isEmpty {
            let context = AnalysisContext()
            context.contextualStrings[.general] = vocabulary
            try await analyzer.setContext(context)
        }
        try await analyzer.prepareToAnalyze(in: format)
        try await analyzer.start(inputSequence: inputStream)

        let heard = Task<String, Error> {
            defer {
                saidContinuation.finish()
                inputContinuation.finish()
            }
            var acc = TranscriptAccumulator()
            switch transcriber {
            case .dictation(let module):
                for try await result in module.results {
                    try Task.checkCancellation()
                    saidContinuation.yield(acc.apply(text: String(result.text.characters), isFinal: result.isFinal))
                }
            case .speech(let module):
                for try await result in module.results {
                    try Task.checkCancellation()
                    saidContinuation.yield(acc.apply(text: String(result.text.characters), isFinal: result.isFinal))
                }
            }
            return acc.finalText
        }

        return AppleSpeechSession(
            analyzer: analyzer,
            input: inputContinuation,
            said: saidStream,
            heard: heard
        )
    }

    func add(_ input: AnalyzerInput) {
        self.input.yield(input)
    }

    func finish() async throws -> String {
        input.finish()
        try? await analyzer.finalizeAndFinishThroughEndOfInput()
        return try await heard.value
    }

    func cancel() {
        input.finish()
        heard.cancel()
        let analyzer = self.analyzer
        Task { await analyzer.cancelAndFinishNow() }
    }
}

/// The microphone while the mic is on: captures live audio on a dedicated
/// serial queue (`Turns`), converts each tap buffer to the `SpeechAnalyzer`
/// target format, and reports RMS levels for `Meter`.
nonisolated final class Recording: @unchecked Sendable {
    /// Fallback format for `SpeechAnalyzer` (`AnalyzerInput` requires 16-bit
    /// signed integer PCM samples).
    static let format = AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: 16_000, channels: 1, interleaved: false)!
    private static let queue = DispatchQueue(label: "wiki.wind.windrex.recording", qos: .userInitiated)
    private var engine: AVAudioEngine?
    private let turns = Turns(on: Recording.queue)

    /// Starts the microphone on its queue, converting audio into `targetFormat`
    /// `AnalyzerInput` buffers and reporting RMS levels on the audio thread.
    func start(
        format targetFormat: AVAudioFormat = Recording.format,
        onInput: @escaping @Sendable (AnalyzerInput) -> Void,
        onLevel: @escaping @Sendable (Float) -> Void,
        done: @escaping @MainActor @Sendable (Result<Bool, any Error>) -> Void
    ) {
        turns.start({ try self.begin(targetFormat: targetFormat, onInput: onInput, onLevel: onLevel) }, done: done)
    }

    private func begin(
        targetFormat: AVAudioFormat,
        onInput: @escaping @Sendable (AnalyzerInput) -> Void,
        onLevel: @escaping @Sendable (Float) -> Void
    ) throws {
        let engine = AVAudioEngine()
        self.engine = engine
        let audio = AVAudioSession.sharedInstance()
        try audio.setCategory(.record, mode: .measurement, options: .duckOthers)
        try audio.setActive(true)
        let input = engine.inputNode
        let sourceFormat = input.outputFormat(forBus: 0)
        guard let c = Converter(from: sourceFormat, to: targetFormat) else {
            try? audio.setActive(false, options: .notifyOthersOnDeactivation)
            throw RexError(code: "dictation", message: String(localized: "This device has no microphone."))
        }
        let converter = OSAllocatedUnfairLock(uncheckedState: c)
        input.installTap(onBus: 0, bufferSize: 4096, format: sourceFormat) { @Sendable buffer, _ in
            if let ch = buffer.floatChannelData?[0], buffer.frameLength > 0 {
                let ptr = UnsafeBufferPointer(start: ch, count: Int(buffer.frameLength))
                onLevel(vDSP.rootMeanSquare(ptr))
            }
            let converted = converter.withLockUnchecked { $0.convertToInput(buffer) }
            if let converted { onInput(converted) }
        }
        do {
            try engine.start()
        } catch {
            halt()
            throw error
        }
    }

    /// Stops the microphone, or the start of it; others' sound comes back soon after.
    func stop() {
        turns.stop { self.halt() }
    }

    private func halt() {
        guard let engine else { return }
        engine.stop()
        engine.inputNode.removeTap(onBus: 0)
        self.engine = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    /// Converts microphone tap buffers into owned buffers in `target` format
    /// so each `AnalyzerInput` stays valid while `SpeechAnalyzer` consumes it.
    nonisolated struct Converter {
        private let converter: AVAudioConverter
        let target: AVAudioFormat

        /// Nil for a microphone that hears nothing, as a simulator's.
        init?(from source: AVAudioFormat, to target: AVAudioFormat = Recording.format) {
            guard source.sampleRate > 0, source.channelCount > 0,
                  let c = AVAudioConverter(from: source, to: target) else { return nil }
            if source.channelCount > 1, target.channelCount == 1 { c.channelMap = [0] }
            self.converter = c
            self.target = target
        }

        /// Converts `buffer` into a newly allocated PCM buffer in `target` format.
        func convertBuffer(_ buffer: AVAudioPCMBuffer) -> AVAudioPCMBuffer? {
            guard buffer.frameLength > 0 else { return nil }
            let frames = AVAudioFrameCount((Double(buffer.frameLength) * target.sampleRate / buffer.format.sampleRate).rounded(.up)) + 16
            guard let out = AVAudioPCMBuffer(pcmFormat: target, frameCapacity: frames) else { return nil }
            out.frameLength = 0
            var given = false
            let status = converter.convert(to: out, error: nil) { _, inputStatus in
                inputStatus.pointee = given ? .noDataNow : .haveData
                defer { given = true }
                return given ? nil : buffer
            }
            guard status != .error, out.frameLength > 0 else { return nil }
            return out
        }

        /// Converts `buffer` into a `Sendable` `AnalyzerInput`.
        func convertToInput(_ buffer: AVAudioPCMBuffer) -> AnalyzerInput? {
            guard let out = convertBuffer(buffer) else { return nil }
            return AnalyzerInput(buffer: out)
        }
    }
}

/// Work started and stopped in turn on a serial queue, as the microphone
/// is: a stop wins over a start asked for before it, which is passed over
/// if it has not begun, and stopped after it if it has.
nonisolated final class Turns: Sendable {
    private let queue: DispatchQueue
    private let stopped = Atomic<Bool>(false)

    init(on queue: DispatchQueue) {
        self.queue = queue
    }

    /// Queues start now, after what is queued; done is told, on the main
    /// actor, whether it ran (false: a stop came first) or what it threw.
    func start(_ start: @escaping @Sendable () throws -> Void, done: @escaping @MainActor @Sendable (Result<Bool, any Error>) -> Void) {
        queue.async { [self] in
            let r: Result<Bool, any Error> = stopped.load(ordering: .acquiring)
                ? .success(false)
                : Result { try start(); return true }
            Task { @MainActor in done(r) }
        }
    }

    /// Stops: a start not yet begun is passed over, and stop runs after one
    /// that has.
    func stop(_ stop: @escaping @Sendable () -> Void) {
        stopped.store(true, ordering: .releasing)
        queue.async(execute: stop)
    }
}

/// The microphone's level, from the audio thread to the main one, which
/// looks at it at most once a frame (Dictation.show): no task or allocation
/// for each buffer the microphone gives. A level is taken once, the newest,
/// so the bars fall as levels come, not as frames do.
nonisolated final class Meter: Sendable {
    /// The count of levels set, high, and the newest's bits, low: one word,
    /// so a level is never taken with another's count.
    private let latest = Atomic<UInt64>(0)

    /// Sets the newest RMS; one thread only, the audio thread.
    func set(_ rms: Float) {
        let count = (latest.load(ordering: .relaxed) >> 32) &+ 1
        latest.store(count << 32 | UInt64(rms.bitPattern), ordering: .releasing)
    }

    /// The newest level set since count, which it moves on; nil for none.
    func take(after count: inout UInt64) -> Float? {
        let v = latest.load(ordering: .acquiring)
        guard v >> 32 != count else { return nil }
        count = v >> 32
        return Float(bitPattern: UInt32(truncatingIfNeeded: v))
    }
}
