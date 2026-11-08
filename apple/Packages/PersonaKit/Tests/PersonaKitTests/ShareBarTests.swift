import CoreGraphics
import Testing
@testable import PersonaKit

struct ShareBarTests {
    /// The parts and the gaps between them, end to end.
    private func span(_ widths: [CGFloat], gap: CGFloat) -> CGFloat {
        widths.reduce(0, +) + gap * CGFloat(max(0, widths.count - 1))
    }

    @Test func smallSharesTakeTheirMinimumFromTheLargeOne() {
        // One computer with nearly all of the day's tokens and three with a little: the bar used to run 24 pt past
        // its end (three minimum-width dots and three gaps on top of the large part's full width).
        let widths = ShareBar.widths([295_000_000, 16_800, 16_800, 16_800], width: 300, gap: 2, minimum: 6)
        #expect(widths == [276, 6, 6, 6])
        #expect(span(widths, gap: 2) == 300)
    }

    @Test func sharesAboveTheMinimumAreInProportion() {
        let widths = ShareBar.widths([3, 1], width: 102, gap: 2, minimum: 6)
        #expect(widths == [75, 25])
        #expect(ShareBar.widths([42], width: 300, gap: 2, minimum: 6) == [300])
    }

    @Test func aPartPushedUnderTheMinimumByTheOthersIsHeldThereToo() {
        // 12 of 117 is just over the minimum at first; once the five tiny parts hold 10 each, it is not.
        let widths = ShareBar.widths([100, 12, 1, 1, 1, 1, 1], width: 100, gap: 0, minimum: 10)
        #expect(widths == [40, 10, 10, 10, 10, 10, 10])
    }

    @Test func alwaysFitsTheBar() {
        for values in [[5.0, 5, 5], [1_000_000, 1], [9, 3, 1, 1, 1], [1, 1, 1, 1, 1, 1, 1, 1]] {
            for width in [60.0, 120, 343] as [CGFloat] {
                let widths = ShareBar.widths(values, width: width, gap: 2, minimum: 6)
                #expect(abs(span(widths, gap: 2) - width) < 0.001, "\(values) in \(width)")
                if span(Array(repeating: 6, count: values.count), gap: 2) < width {
                    #expect(widths.allSatisfy { $0 >= 6 - 0.001 }, "\(values) in \(width)")
                }
            }
        }
    }

    @Test func tooNarrowForEveryMinimumGivesEqualParts() {
        let widths = ShareBar.widths([100, 1, 1], width: 20, gap: 2, minimum: 6)
        #expect(widths.allSatisfy { abs($0 - 16.0 / 3) < 0.001 })
        #expect(ShareBar.widths([], width: 300, gap: 2, minimum: 6).isEmpty)
    }
}
