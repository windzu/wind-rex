import CoreGraphics
import SwiftUI
import XCTest

nonisolated final class BrandLoadingTests: XCTestCase {
    func testIndeterminateTrimStaysWithinUnitRangeAcrossCycle() {
        let samples = 240
        let duration = BrandLoadingPhase.cycleDuration
        for step in -20...(samples * 2) {
            let elapsed = duration * Double(step) / Double(samples)
            let phase = BrandLoadingPhase.state(mode: .indeterminate, elapsed: elapsed, reduceMotion: false)
            XCTAssertGreaterThanOrEqual(phase.trimFrom, 0, "elapsed=\(elapsed)")
            XCTAssertLessThanOrEqual(phase.trimFrom, 1, "elapsed=\(elapsed)")
            XCTAssertGreaterThanOrEqual(phase.trimTo, 0, "elapsed=\(elapsed)")
            XCTAssertLessThanOrEqual(phase.trimTo, 1, "elapsed=\(elapsed)")
            XCTAssertLessThanOrEqual(phase.trimFrom, phase.trimTo, "elapsed=\(elapsed)")
            XCTAssertGreaterThanOrEqual(phase.litOpacity, 0, "elapsed=\(elapsed)")
            XCTAssertLessThanOrEqual(phase.litOpacity, 1, "elapsed=\(elapsed)")
            XCTAssertTrue(phase.isAnimating)
            XCTAssertFalse(phase.isFailed)
        }
    }

    func testIndeterminateNonFiniteElapsedStaysValid() {
        for elapsed in [Double.nan, .infinity, -.infinity] {
            let phase = BrandLoadingPhase.state(mode: .indeterminate, elapsed: elapsed, reduceMotion: false)
            XCTAssertEqual(phase.trimFrom, 0)
            XCTAssertGreaterThanOrEqual(phase.trimTo, 0)
            XCTAssertLessThanOrEqual(phase.trimTo, 1)
        }
    }

    func testIndeterminateDrawHoldSweepRestProgression() {
        let d = BrandLoadingPhase.cycleDuration
        let earlyDraw = BrandLoadingPhase.state(mode: .indeterminate, elapsed: d * 0.15, reduceMotion: false)
        let lateDraw = BrandLoadingPhase.state(mode: .indeterminate, elapsed: d * 0.45, reduceMotion: false)
        let hold = BrandLoadingPhase.state(mode: .indeterminate, elapsed: d * 0.64, reduceMotion: false)
        let sweep = BrandLoadingPhase.state(mode: .indeterminate, elapsed: d * 0.82, reduceMotion: false)
        let rest = BrandLoadingPhase.state(mode: .indeterminate, elapsed: d * 0.97, reduceMotion: false)

        XCTAssertEqual(earlyDraw.trimFrom, 0)
        XCTAssertEqual(lateDraw.trimFrom, 0)
        XCTAssertGreaterThan(lateDraw.trimTo, earlyDraw.trimTo)

        XCTAssertEqual(hold.trimFrom, 0)
        XCTAssertEqual(hold.trimTo, 1)
        XCTAssertEqual(hold.litOpacity, 1)

        XCTAssertGreaterThan(sweep.trimFrom, 0)
        XCTAssertLessThan(sweep.trimFrom, 1)
        XCTAssertEqual(sweep.trimTo, 1)

        XCTAssertEqual(rest.trimFrom, 0)
        XCTAssertEqual(rest.trimTo, 0)
        XCTAssertEqual(rest.litOpacity, 0)
    }

    func testReduceMotionReturnsStaticNonSweepingState() {
        let d = BrandLoadingPhase.cycleDuration
        for fraction in [0.0, 0.25, 0.5, 0.85, 1.5] {
            let phase = BrandLoadingPhase.state(mode: .indeterminate, elapsed: d * fraction, reduceMotion: true)
            XCTAssertEqual(phase.trimFrom, 0)
            XCTAssertEqual(phase.trimTo, 1)
            XCTAssertEqual(phase.litOpacity, 1)
            XCTAssertFalse(phase.isAnimating)
            XCTAssertFalse(phase.isFailed)
        }
    }

    func testDeterminateProgressClampsAndStopsSweeping() {
        let mid = BrandLoadingPhase.state(mode: .determinate(0.42), elapsed: 1.0, reduceMotion: false)
        XCTAssertEqual(mid.trimFrom, 0)
        XCTAssertEqual(mid.trimTo, 0.42, accuracy: 0.0001)
        XCTAssertEqual(mid.litOpacity, 1)
        XCTAssertFalse(mid.isAnimating)
        XCTAssertFalse(mid.isFailed)

        let zero = BrandLoadingPhase.state(mode: .determinate(0), elapsed: 0, reduceMotion: false)
        XCTAssertEqual(zero.trimTo, 0)
        XCTAssertEqual(zero.litOpacity, 0)

        let low = BrandLoadingPhase.state(mode: .determinate(-0.5), elapsed: 0, reduceMotion: false)
        XCTAssertEqual(low.trimTo, 0)

        let high = BrandLoadingPhase.state(mode: .determinate(1.7), elapsed: 0, reduceMotion: false)
        XCTAssertEqual(high.trimTo, 1)

        let nan = BrandLoadingPhase.state(mode: .determinate(.nan), elapsed: 0, reduceMotion: false)
        XCTAssertEqual(nan.trimTo, 0)
    }

    func testFailedModeStopsAnimationAndHighlightsError() {
        let unknown = BrandLoadingPhase.state(mode: .failed(nil), elapsed: 0.7, reduceMotion: false)
        XCTAssertEqual(unknown.trimFrom, 0)
        XCTAssertEqual(unknown.trimTo, 1)
        XCTAssertFalse(unknown.isAnimating)
        XCTAssertTrue(unknown.isFailed)

        let partial = BrandLoadingPhase.state(mode: .failed(0.65), elapsed: 0.7, reduceMotion: false)
        XCTAssertEqual(partial.trimFrom, 0)
        XCTAssertEqual(partial.trimTo, 0.65, accuracy: 0.0001)
        XCTAssertFalse(partial.isAnimating)
        XCTAssertTrue(partial.isFailed)

        let zeroFailed = BrandLoadingPhase.state(mode: .failed(0), elapsed: 0, reduceMotion: false)
        XCTAssertEqual(zeroFailed.trimTo, 1)
        XCTAssertTrue(zeroFailed.isFailed)
    }

    func testModeInitFromProgressFields() {
        XCTAssertEqual(BrandLoadingMode(fraction: nil, failed: false), .indeterminate)
        XCTAssertEqual(BrandLoadingMode(fraction: 0.5, failed: false), .determinate(0.5))
        XCTAssertEqual(BrandLoadingMode(fraction: nil, failed: true), .failed(nil))
        XCTAssertEqual(BrandLoadingMode(fraction: 0.3, failed: true), .failed(0.3))
    }

    func testWindRexMarkShapeGeometryProducesValidBoundingBox() {
        let canvasRect = CGRect(x: 0, y: 0, width: 80, height: 80)
        let canvasPath = WindRexMarkShape(fit: .canvas).path(in: canvasRect)
        let canvasBounds = canvasPath.boundingRect
        XCTAssertFalse(canvasPath.isEmpty)
        XCTAssertGreaterThan(canvasBounds.width, 40)
        XCTAssertGreaterThan(canvasBounds.height, 15)
        XCTAssertTrue(canvasRect.contains(canvasBounds))
        XCTAssertGreaterThan(WindRexMarkShape.strokeWidth(in: canvasRect, fit: .canvas), 4.0)

        let tightRect = CGRect(x: 0, y: 0, width: 36, height: 18)
        let tightPath = WindRexMarkShape(fit: .tight).path(in: tightRect)
        let tightBounds = tightPath.boundingRect
        XCTAssertFalse(tightPath.isEmpty)
        XCTAssertGreaterThan(tightBounds.width, 28)
        XCTAssertGreaterThan(tightBounds.height, 12)
        XCTAssertTrue(tightRect.contains(tightBounds))
        XCTAssertGreaterThan(WindRexMarkShape.strokeWidth(in: tightRect, fit: .tight), 2.0)

        let emptyPath = WindRexMarkShape(fit: .canvas).path(in: .zero)
        XCTAssertTrue(emptyPath.isEmpty)
        XCTAssertEqual(WindRexMarkShape.strokeWidth(in: .zero), 0)
    }

    @MainActor func testAccessibilityLabelsAndPercentages() {
        XCTAssertEqual(BrandLoading.defaultAccessibilityLabel(for: .indeterminate), "Loading")
        XCTAssertEqual(BrandLoading.defaultAccessibilityLabel(for: .determinate(0.4)), "Loading")
        XCTAssertEqual(BrandLoading.defaultAccessibilityLabel(for: .failed(nil)), "Loading failed")

        XCTAssertNil(BrandLoading.accessibilityPercent(for: .indeterminate))
        XCTAssertNotNil(BrandLoading.accessibilityPercent(for: .determinate(0.65)))
        XCTAssertNotNil(BrandLoading.accessibilityPercent(for: .failed(0.25)))
        XCTAssertNil(BrandLoading.accessibilityPercent(for: .failed(nil)))
    }
}
