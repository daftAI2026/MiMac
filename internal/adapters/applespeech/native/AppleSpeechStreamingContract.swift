import CoreMedia
import Foundation

@main
struct AppleSpeechStreamingContract {
    static func main() {
        testWidenedAndResegmentedRangesReplaceTentativeText()
        testFinalizationMovesTextFromTentativeToStable()
        testEmptyVolatileResultRevokesTentativeRange()
    }

    private static func testWidenedAndResegmentedRangesReplaceTentativeText() {
        var accumulator = TranscriptAccumulator()
        let zero = CMTime(value: 0, timescale: 1)
        let first = accumulator.consume(range: timeRange(0, 10), text: "先说", isFinal: false, resultsFinalizationTime: zero)
        require(first.stable.isEmpty && first.tentative == "先说", "first result was not tentative")

        let widened = accumulator.consume(range: timeRange(0, 20), text: "先说后续", isFinal: false, resultsFinalizationTime: zero)
        require(widened.tentative == "先说后续", "widened same-start range was duplicated")

        let split = accumulator.consume(range: timeRange(20, 30), text: "收尾", isFinal: false, resultsFinalizationTime: zero)
        require(split.tentative == "先说后续收尾", "non-overlapping resegmented phrase ordering changed")

        let regrouped = accumulator.consume(range: timeRange(0, 30), text: "先说后续收尾", isFinal: true, resultsFinalizationTime: timeRange(0, 30).end)
        require(regrouped.stable == "先说后续收尾" && regrouped.tentative.isEmpty, "overlapping finalized range was not coalesced")

        let final = accumulator.finish()
        require(final.finalText == "先说后续收尾", "final text repeated an overlapping phrase")
    }

    private static func testFinalizationMovesTextFromTentativeToStable() {
        var accumulator = TranscriptAccumulator()
        let range = timeRange(0, 10)
        let zero = CMTime(value: 0, timescale: 1)
        let tentative = accumulator.consume(range: range, text: "稳定词", isFinal: false, resultsFinalizationTime: zero)
        require(tentative.stable.isEmpty && tentative.tentative == "稳定词", "volatile result was not tentative")
        let finalized = accumulator.consume(range: timeRange(10, 20), text: "新词", isFinal: false, resultsFinalizationTime: range.end)
        require(finalized.stable == "稳定词" && finalized.tentative == "新词", "finalization time did not promote the earlier range")
        require(accumulator.finish().finalText == "稳定词新词", "finalized phrase order was not preserved")
    }

    private static func testEmptyVolatileResultRevokesTentativeRange() {
        var accumulator = TranscriptAccumulator()
        let zero = CMTime(value: 0, timescale: 1)
        _ = accumulator.consume(range: timeRange(0, 10), text: "临时文字", isFinal: false, resultsFinalizationTime: zero)
        let revoked = accumulator.consume(range: timeRange(0, 10), text: "", isFinal: false, resultsFinalizationTime: zero)
        require(revoked.stable.isEmpty && revoked.tentative.isEmpty, "empty volatile result did not revoke the old draft")
        require(accumulator.finish().finalText.isEmpty, "revoked tentative text reached final output")
    }

    private static func timeRange(_ start: Int64, _ end: Int64) -> CMTimeRange {
        CMTimeRange(start: CMTime(value: start, timescale: 1), end: CMTime(value: end, timescale: 1))
    }

    private static func require(_ condition: @autoclosure () -> Bool, _ message: String) {
        if !condition() {
            fputs("Apple Speech streaming contract failed: \(message)\n", stderr)
            exit(1)
        }
    }
}
