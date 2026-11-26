import SwiftUI

/// The WindRex handwritten single-stroke "W" mark from `assets/brand/windrex.svg`.
/// Preserves the SVG's exact cubic Bézier curves and stroke width proportion so
/// `trim(from:to:)` traces the mark strictly in stroke order from start to tip.
nonisolated struct WindRexMarkShape: Shape, Sendable {
    enum Fit: Equatable, Sendable {
        /// Fits the 200×200 SVG icon canvas into the rect (centered marks).
        case canvas
        /// Fits tightly around the stroke's padded bounds (compact indicators).
        case tight
    }

    static let svgSize = CGSize(width: 200, height: 200)
    static let svgStrokeWidth: CGFloat = 11.7
    /// Stroke path bounds [36...165] × [72...129] padded by half stroke width (5.85).
    static let tightBounds = CGRect(x: 30, y: 66, width: 141, height: 69)

    var fit: Fit = .canvas

    static func sourceBounds(for fit: Fit) -> CGRect {
        switch fit {
        case .canvas: CGRect(origin: .zero, size: svgSize)
        case .tight: tightBounds
        }
    }

    static func scale(in rect: CGRect, fit: Fit = .canvas) -> CGFloat {
        guard rect.width > 0, rect.height > 0 else { return 0 }
        let box = sourceBounds(for: fit)
        return min(rect.width / box.width, rect.height / box.height)
    }

    static func strokeWidth(in rect: CGRect, fit: Fit = .canvas) -> CGFloat {
        let s = scale(in: rect, fit: fit)
        guard s > 0 else { return 0 }
        return max(1.5, svgStrokeWidth * s)
    }

    func path(in rect: CGRect) -> Path {
        let s = Self.scale(in: rect, fit: fit)
        guard s > 0 else { return Path() }
        let box = Self.sourceBounds(for: fit)
        func pt(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
            CGPoint(
                x: rect.midX + (x - box.midX) * s,
                y: rect.midY + (y - box.midY) * s
            )
        }
        // Single-stroke path from assets/brand/windrex.svg:
        // M 36 72 C 47 75 47 129 58 128 C 67 127 73 78 82 79
        // C 91 80 94 129 105 129 C 114 129 117 100 131 100
        // C 142 100 152 100 165 100
        var path = Path()
        path.move(to: pt(36, 72))
        path.addCurve(to: pt(58, 128), control1: pt(47, 75), control2: pt(47, 129))
        path.addCurve(to: pt(82, 79), control1: pt(67, 127), control2: pt(73, 78))
        path.addCurve(to: pt(105, 129), control1: pt(91, 80), control2: pt(94, 129))
        path.addCurve(to: pt(131, 100), control1: pt(114, 129), control2: pt(117, 100))
        path.addCurve(to: pt(165, 100), control1: pt(142, 100), control2: pt(152, 100))
        return path
    }
}

/// Loading state rendered by `BrandLoading`.
nonisolated enum BrandLoadingMode: Equatable, Sendable {
    /// Unknown progress: loops a bright stroke smoothly along the WindRex path.
    case indeterminate
    /// Known progress (0...1): lights the path from start up to the progress fraction.
    case determinate(Double)
    /// Error state: stops sweeping animation and highlights the mark in error red.
    case failed(Double?)

    init(fraction: Double?, failed: Bool) {
        if failed {
            self = .failed(fraction)
        } else if let fraction {
            self = .determinate(fraction)
        } else {
            self = .indeterminate
        }
    }
}

/// Pure, deterministic animation state for one frame of `BrandLoading`.
nonisolated struct BrandLoadingPhase: Equatable, Sendable {
    static let cycleDuration: TimeInterval = 1.8
    static let drawEnd: Double = 0.58
    static let holdEnd: Double = 0.70
    static let sweepEnd: Double = 0.94
    static let defaultTrackOpacity: Double = 0.24

    let trimFrom: CGFloat
    let trimTo: CGFloat
    let trackOpacity: Double
    let litOpacity: Double
    let isAnimating: Bool
    let isFailed: Bool

    static func clampProgress(_ value: Double) -> CGFloat {
        guard value.isFinite else { return 0 }
        return CGFloat(min(max(value, 0), 1))
    }

    static func easeInOut(_ t: Double) -> Double {
        let x = min(max(t, 0), 1)
        if x < 0.5 {
            return 4 * x * x * x
        }
        let f = -2 * x + 2
        return 1 - (f * f * f) / 2
    }

    static func normalizedPhase(elapsed: TimeInterval) -> Double {
        guard elapsed.isFinite, cycleDuration > 0 else { return 0 }
        let mod = elapsed.truncatingRemainder(dividingBy: cycleDuration)
        let wrapped = mod < 0 ? mod + cycleDuration : mod
        return min(max(wrapped / cycleDuration, 0), 1)
    }

    static func state(mode: BrandLoadingMode, elapsed: TimeInterval, reduceMotion: Bool) -> BrandLoadingPhase {
        switch mode {
        case .failed(let fraction):
            let end: CGFloat = if let fraction {
                clampProgress(fraction) > 0 ? clampProgress(fraction) : 1
            } else {
                1
            }
            return BrandLoadingPhase(
                trimFrom: 0,
                trimTo: end,
                trackOpacity: defaultTrackOpacity,
                litOpacity: 1,
                isAnimating: false,
                isFailed: true
            )

        case .determinate(let fraction):
            let end = clampProgress(fraction)
            return BrandLoadingPhase(
                trimFrom: 0,
                trimTo: end,
                trackOpacity: defaultTrackOpacity,
                litOpacity: end > 0 ? 1 : 0,
                isAnimating: false,
                isFailed: false
            )

        case .indeterminate:
            if reduceMotion {
                return BrandLoadingPhase(
                    trimFrom: 0,
                    trimTo: 1,
                    trackOpacity: defaultTrackOpacity,
                    litOpacity: 1,
                    isAnimating: false,
                    isFailed: false
                )
            }
            let u = normalizedPhase(elapsed: elapsed)
            if u < drawEnd {
                let p = easeInOut(u / drawEnd)
                let fadeIn = min(1, p / 0.06)
                return BrandLoadingPhase(
                    trimFrom: 0,
                    trimTo: CGFloat(p),
                    trackOpacity: defaultTrackOpacity,
                    litOpacity: fadeIn,
                    isAnimating: true,
                    isFailed: false
                )
            } else if u < holdEnd {
                return BrandLoadingPhase(
                    trimFrom: 0,
                    trimTo: 1,
                    trackOpacity: defaultTrackOpacity,
                    litOpacity: 1,
                    isAnimating: true,
                    isFailed: false
                )
            } else if u < sweepEnd {
                let p = easeInOut((u - holdEnd) / (sweepEnd - holdEnd))
                let fadeOut = min(1, (1 - p) / 0.08)
                return BrandLoadingPhase(
                    trimFrom: CGFloat(p),
                    trimTo: 1,
                    trackOpacity: defaultTrackOpacity,
                    litOpacity: fadeOut,
                    isAnimating: true,
                    isFailed: false
                )
            } else {
                return BrandLoadingPhase(
                    trimFrom: 0,
                    trimTo: 0,
                    trackOpacity: defaultTrackOpacity,
                    litOpacity: 0,
                    isAnimating: true,
                    isFailed: false
                )
            }
        }
    }
}

/// The WindRex single-stroke mark that lights progressively along its curve
/// while connecting or reporting loading progress.
struct BrandLoading: View {
    var mode: BrandLoadingMode = .indeterminate
    var fit: WindRexMarkShape.Fit = .canvas
    var size: CGSize = CGSize(width: 80, height: 80)
    var accessibilityLabel: LocalizedStringKey? = nil

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.colorScheme) private var colorScheme
    @State private var startDate = Date.now

    static func defaultAccessibilityLabel(for mode: BrandLoadingMode) -> LocalizedStringKey {
        switch mode {
        case .indeterminate, .determinate:
            "Loading"
        case .failed:
            "Loading failed"
        }
    }

    static func accessibilityPercent(for mode: BrandLoadingMode) -> String? {
        let fraction: Double? = switch mode {
        case .indeterminate: nil
        case .determinate(let f): f
        case .failed(let f): f
        }
        guard let fraction else { return nil }
        let clamped = Double(BrandLoadingPhase.clampProgress(fraction))
        return clamped.formatted(.percent.precision(.fractionLength(0)))
    }

    private var animates: Bool {
        mode == .indeterminate && !reduceMotion
    }

    var body: some View {
        Group {
            if animates {
                TimelineView(.animation) { context in
                    strokeLayer(
                        phase: BrandLoadingPhase.state(
                            mode: mode,
                            elapsed: context.date.timeIntervalSince(startDate),
                            reduceMotion: false
                        )
                    )
                }
            } else {
                strokeLayer(
                    phase: BrandLoadingPhase.state(
                        mode: mode,
                        elapsed: 0,
                        reduceMotion: reduceMotion
                    )
                )
                .animation(reduceMotion ? nil : .easeOut(duration: 0.2), value: mode)
            }
        }
        .frame(width: size.width, height: size.height)
        .onAppear { startDate = .now }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityLabel ?? Self.defaultAccessibilityLabel(for: mode))
        .optionalAccessibilityValue(Self.accessibilityPercent(for: mode))
    }

    @ViewBuilder
    private func strokeLayer(phase: BrandLoadingPhase) -> some View {
        let rect = CGRect(origin: .zero, size: size)
        let lineWidth = WindRexMarkShape.strokeWidth(in: rect, fit: fit)
        let style = StrokeStyle(lineWidth: lineWidth, lineCap: .round, lineJoin: .round)
        let baseColor: Color = phase.isFailed ? .red : .primary
        let trackAlpha = colorScheme == .dark ? phase.trackOpacity : max(0.18, phase.trackOpacity - 0.04)
        let glowAlpha = colorScheme == .dark ? 0.38 : 0.14
        let glowRadius = colorScheme == .dark ? lineWidth * 0.8 : lineWidth * 0.35

        ZStack {
            WindRexMarkShape(fit: fit)
                .stroke(baseColor.opacity(trackAlpha), style: style)
            WindRexMarkShape(fit: fit)
                .trim(from: phase.trimFrom, to: phase.trimTo)
                .stroke(baseColor, style: style)
                .opacity(phase.litOpacity)
                .shadow(color: baseColor.opacity(glowAlpha * phase.litOpacity), radius: glowRadius)
        }
    }
}

/// Compact top loading pill showing the WindRex single-stroke mark lighting
/// along its curve while a terminal program reports progress (OSC 9;4).
struct TopBrandLoading: View {
    let mode: BrandLoadingMode

    var body: some View {
        HStack(spacing: 6) {
            BrandLoading(mode: mode, fit: .tight, size: CGSize(width: 36, height: 18))
            if case .determinate(let fraction) = mode {
                Text(Double(BrandLoadingPhase.clampProgress(fraction)), format: .percent.precision(.fractionLength(0)))
                    .font(.caption2.weight(.semibold).monospacedDigit())
                    .foregroundStyle(.secondary)
            } else if case .failed = mode {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.red)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .glassEffect(.regular, in: .capsule)
        .allowsHitTesting(false)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(BrandLoading.defaultAccessibilityLabel(for: mode))
        .optionalAccessibilityValue(BrandLoading.accessibilityPercent(for: mode))
    }
}

private extension View {
    @ViewBuilder
    func optionalAccessibilityValue(_ value: String?) -> some View {
        if let value {
            self.accessibilityValue(value)
        } else {
            self
        }
    }
}
