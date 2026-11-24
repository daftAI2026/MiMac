// Minimal public-Speech bridge for MiMac. Compile this dylib with a macOS 14.2
// deployment target; SpeechAnalyzer entry points are guarded at macOS 26.0.
import AVFoundation
import CoreMedia
import Darwin
import Foundation
import Speech

private let statePreparing: Int32 = 0
private let stateReady: Int32 = 1
private let stateDrained: Int32 = 2
private let stateCancelled: Int32 = 3
private let stateFailed: Int32 = 4

private let queueFullStatus: Int32 = 1
private let invalidBlockStatus: Int32 = 2
private let notReadyStatus: Int32 = 3
private let drainingStatus: Int32 = 4
private let closedStatus: Int32 = 5

private let maximumBlockSamples = 4_096
// Reserve one slot for the stream and one producer-held block so logical PCM
// in flight stays at or below 32,000 samples across the ring and async handoff.
// AVAudioPCMBuffer and temporary Swift arrays are copies outside this frame cap.
private let nativeQueueCapacity = 32_000 - (2 * maximumBlockSamples)
private let maximumFinalTextBytes = 64 * 1024
private let partialTimeoutStatus: Int32 = 0
private let partialUpdatedStatus: Int32 = 1
private let partialTerminalStatus: Int32 = 2
private let partialBufferTooSmallStatus: Int32 = 3
private let partialInvalidArgumentStatus: Int32 = 4

private enum QueueItem {
    case batch([Int16])
    case finished
    case wake
}

struct TranscriptSegment {
    let range: CMTimeRange
    let sequence: Int
    var text: String
    var isStable: Bool
}

struct TranscriptSnapshot {
    let stable: String
    let tentative: String
    let finalText: String
    let overLimit: Bool
}

struct TranscriptAccumulator {
    private var segments: [TranscriptSegment] = []
    private var finalizedThrough: CMTime?
    private var nextSequence = 0

    mutating func consume(range: CMTimeRange, text: String, isFinal: Bool, resultsFinalizationTime: CMTime) -> TranscriptSnapshot {
        if resultsFinalizationTime.isNumeric &&
            (finalizedThrough == nil || CMTimeCompare(resultsFinalizationTime, finalizedThrough!) > 0) {
            finalizedThrough = resultsFinalizationTime
        }
        let stableByTime = range.end.isNumeric && finalizedThrough.map {
            CMTimeCompare(range.end, $0) <= 0
        } == true
        let isStable = isFinal || stableByTime

        let overlapping = segments.indices.filter { transcriptRangesOverlap(segments[$0].range, range) }
        if text.isEmpty {
            // Empty volatile text revokes the prior tentative result for its
            // range, including any wider same-phrase revision it overlaps.
            segments.removeAll { !$0.isStable && transcriptRangesOverlap($0.range, range) }
        } else if let exact = segments.firstIndex(where: { $0.range == range }) {
            if !segments[exact].isStable || isStable {
                segments[exact].text = text
                segments[exact].isStable = segments[exact].isStable || isStable
            }
        } else if !overlapping.isEmpty {
            // Volatile result ranges can be revised or regrouped. Replace the
            // overlapping cluster with the newest interpretation so the same
            // audio interval never appears twice in captions or final output.
            // A non-final update cannot rewrite already finalized ranges.
            let overlapsStable = overlapping.contains { segments[$0].isStable }
            if !overlapsStable || isStable {
                segments.removeAll { transcriptRangesOverlap($0.range, range) }
                segments.append(TranscriptSegment(range: range, sequence: nextSequence, text: text, isStable: isStable))
                nextSequence += 1
            }
        } else {
            segments.append(TranscriptSegment(range: range, sequence: nextSequence, text: text, isStable: isStable))
            nextSequence += 1
        }

        if let finalizedThrough {
            for index in segments.indices where segments[index].range.end.isNumeric {
                if CMTimeCompare(segments[index].range.end, finalizedThrough) <= 0 {
                    segments[index].isStable = true
                }
            }
        }
        sortSegments()
        return makeSnapshot(final: false)
    }

    mutating func finish() -> TranscriptSnapshot {
        for index in segments.indices { segments[index].isStable = true }
        return makeSnapshot(final: true)
    }

    private mutating func sortSegments() {
        segments.sort { lhs, rhs in
            if lhs.range.start.isNumeric && rhs.range.start.isNumeric {
                let startOrder = CMTimeCompare(lhs.range.start, rhs.range.start)
                if startOrder != 0 { return startOrder < 0 }
                let endOrder = CMTimeCompare(lhs.range.end, rhs.range.end)
                if endOrder != 0 { return endOrder < 0 }
            }
            return lhs.sequence < rhs.sequence
        }
    }

    private func makeSnapshot(final: Bool) -> TranscriptSnapshot {
        let stable = segments.filter(\.isStable).map(\.text).joined()
        let tentative = final ? "" : segments.filter { !$0.isStable }.map(\.text).joined()
        let complete = segments.map(\.text).joined()
        let overLimit = segments.count > 8192 || stable.utf8.count + tentative.utf8.count > maximumFinalTextBytes || complete.utf8.count > maximumFinalTextBytes
        return TranscriptSnapshot(stable: stable, tentative: tentative, finalText: complete, overLimit: overLimit)
    }

}

func transcriptRangesOverlap(_ lhs: CMTimeRange, _ rhs: CMTimeRange) -> Bool {
    guard lhs.start.isNumeric, lhs.end.isNumeric, rhs.start.isNumeric, rhs.end.isNumeric else { return lhs == rhs }
    return CMTimeCompare(lhs.start, rhs.end) < 0 && CMTimeCompare(rhs.start, lhs.end) < 0
}

private struct BridgeFailure: Error, CustomStringConvertible {
    let description: String

    static func assets(_ locale: String, status: String) -> BridgeFailure {
        BridgeFailure(description: "assets unavailable: install the Apple Speech model for \(locale) (status: \(status))")
    }

    static func unsupported(_ locale: String, status: String) -> BridgeFailure {
        BridgeFailure(description: "assets unsupported: Apple Speech configuration \(locale) (status: \(status))")
    }

    static func installationRequest(_ locale: String, status: String, error: Error) -> BridgeFailure {
        BridgeFailure(description: "asset installation request failed: \(locale) (status: \(status)): \(error)")
    }
}

@available(macOS 26.0, *)
private func speechErrorCode(_ error: Error) -> SFSpeechError.Code? {
    let value = error as NSError
    guard value.domain == SFSpeechError.errorDomain else { return nil }
    return SFSpeechError.Code(rawValue: value.code)
}

// Use the framework's typed error domain for asset classification. Status can
// lag or reflect reservation state even when a shared model is usable.
@available(macOS 26.0, *)
func classifyPreparationFailure(_ error: Error, status: AssetInventory.Status, locale: String) -> Error {
    if status == .unsupported || speechErrorCode(error) == .noModel || speechErrorCode(error) == .cannotAllocateUnsupportedLocale {
        return BridgeFailure.unsupported(locale, status: "prepare failed: \(error)")
    }
    if speechErrorCode(error) == .assetLocaleNotAllocated {
        return BridgeFailure.assets(locale, status: "locale reservation required: \(error)")
    }
    return error
}

@available(macOS 26.0, *)
func classifyInstallationRequestFailure(_ error: Error, status: AssetInventory.Status, locale: String) -> Error {
    if status == .unsupported || speechErrorCode(error) == .noModel || speechErrorCode(error) == .cannotAllocateUnsupportedLocale {
        return BridgeFailure.unsupported(locale, status: "asset request failed: \(error)")
    }
    return BridgeFailure.installationRequest(locale, status: String(describing: status), error: error)
}

func findNewlyReservedLocale(requested: Locale, before: [Locale], after: [Locale]) -> Locale? {
    let requestedID = reservationLocaleKey(requested)
    let previouslyReserved = Set(before.map(reservationLocaleKey))
    return after.first { reservationLocaleKey($0) == requestedID && !previouslyReserved.contains(reservationLocaleKey($0)) }
}

private func reservationLocaleKey(_ locale: Locale) -> String {
	[
		locale.language.languageCode?.identifier ?? "",
		locale.language.script?.identifier ?? "",
		locale.region?.identifier ?? "",
	].joined(separator: "-").lowercased()
}

func releaseUncommittedReservation(
    _ locale: Locale?,
    preparationReachedReady: Bool,
    release: (Locale) async -> Bool
) async -> Bool {
    guard !preparationReachedReady, let locale else { return false }
    return await release(locale)
}

@available(macOS 26.0, *)
private final class AppleSpeechSession: @unchecked Sendable {
    private let condition = NSCondition()
    private var state = statePreparing
    private var cancelled = false
    private var drainRequested = false
    private var workerTerminated = false
    private var failureMessage = ""
    private var finalTextValue = ""
    private var stablePartialTextValue = ""
    private var tentativePartialTextValue = ""
    private var partialVersion: UInt64 = 0

    // The fixed ring plus one stream slot and one producer-held block is at
    // most 32,000 logical audio samples in flight. Transient copies are separate.
    private var ring = [Int16](repeating: 0, count: nativeQueueCapacity)
    private var ringHead = 0
    private var ringCount = 0
    private var queueWaiter: CheckedContinuation<QueueItem, Never>?

    private var analyzer: SpeechAnalyzer?
    private var inputContinuation: AsyncStream<AnalyzerInput>.Continuation?
    private var workerTask: Task<Void, Never>?
    private var analysisTask: Task<Void, Error>?
    private var resultsTask: Task<Void, Error>?
    private var cancellationTask: Task<Void, Never>?

    init(localeIdentifier: String, installAssets: Bool, enablePartials: Bool) {
        let task = Task.detached(priority: .userInitiated) { [self] in
            await run(localeIdentifier: localeIdentifier, installAssets: installAssets, enablePartials: enablePartials)
        }
        let cancelNow = withConditionLock {
            workerTask = task
            return cancelled
        }
        if cancelNow { task.cancel() }
    }

    private func withConditionLock<T>(_ body: () -> T) -> T {
        condition.lock()
        defer { condition.unlock() }
        return body()
    }

    func currentState() -> Int32 {
        withConditionLock { state }
    }

    func waitReady(timeoutMilliseconds: Int32) -> Int32 {
        condition.lock()
        defer { condition.unlock() }
        waitLocked(timeoutMilliseconds: timeoutMilliseconds) { self.state != statePreparing }
        return state
    }

    func waitDone(timeoutMilliseconds: Int32) -> Int32 {
        condition.lock()
        defer { condition.unlock() }
        waitLocked(timeoutMilliseconds: timeoutMilliseconds) {
            self.state == stateDrained || self.state == stateCancelled || self.state == stateFailed
        }
        return state
    }

    func waitPartial(
        afterVersion: UInt64,
        timeoutMilliseconds: Int32,
        stableOutput: UnsafeMutablePointer<UInt8>?,
        stableCapacity: Int32,
        stableLength: UnsafeMutablePointer<Int32>?,
        tentativeOutput: UnsafeMutablePointer<UInt8>?,
        tentativeCapacity: Int32,
        tentativeLength: UnsafeMutablePointer<Int32>?,
        versionOutput: UnsafeMutablePointer<UInt64>?
    ) -> Int32 {
        guard let stableOutput, let stableLength, let tentativeOutput, let tentativeLength,
              let versionOutput, stableCapacity >= 0, tentativeCapacity >= 0 else {
            return partialInvalidArgumentStatus
        }
        condition.lock()
        defer { condition.unlock() }
        waitLocked(timeoutMilliseconds: timeoutMilliseconds) {
            self.partialVersion != afterVersion || self.isTerminalLocked
        }
        versionOutput.pointee = partialVersion
        if partialVersion != afterVersion {
            let stableBytes = Array(stablePartialTextValue.utf8)
            let tentativeBytes = Array(tentativePartialTextValue.utf8)
            guard stableBytes.count <= Int(stableCapacity), tentativeBytes.count <= Int(tentativeCapacity) else {
                return partialBufferTooSmallStatus
            }
            for index in stableBytes.indices { stableOutput[index] = stableBytes[index] }
            for index in tentativeBytes.indices { tentativeOutput[index] = tentativeBytes[index] }
            stableLength.pointee = Int32(stableBytes.count)
            tentativeLength.pointee = Int32(tentativeBytes.count)
            return partialUpdatedStatus
        }
        return isTerminalLocked ? partialTerminalStatus : partialTimeoutStatus
    }

    private var isTerminalLocked: Bool {
        state == stateDrained || state == stateCancelled || state == stateFailed || workerTerminated
    }

    func waitTerminated(timeoutMilliseconds: Int32) -> Bool {
        condition.lock()
        defer { condition.unlock() }
        waitLocked(timeoutMilliseconds: timeoutMilliseconds) { self.workerTerminated }
        return workerTerminated
    }

    private func waitLocked(timeoutMilliseconds: Int32, until predicate: () -> Bool) {
        if predicate() { return }
        let deadline = Date(timeIntervalSinceNow: Double(max(0, timeoutMilliseconds)) / 1000.0)
        while !predicate() {
            if !condition.wait(until: deadline) { return }
        }
    }

    func tryWrite(_ samples: UnsafePointer<Int16>?, count: Int32) -> Int32 {
        guard let samples, count > 0, count <= maximumBlockSamples else { return invalidBlockStatus }
        var waiter: CheckedContinuation<QueueItem, Never>?
        let status = withConditionLock { () -> Int32 in
            guard state == stateReady, !drainRequested, !cancelled else {
                return cancelled ? closedStatus : (drainRequested ? drainingStatus : notReadyStatus)
            }
            let amount = Int(count)
            guard ringCount + amount <= nativeQueueCapacity else { return queueFullStatus }

            let tail = (ringHead + ringCount) % nativeQueueCapacity
            for index in 0..<amount {
                ring[(tail + index) % nativeQueueCapacity] = samples[index]
            }
            ringCount += amount
            waiter = queueWaiter
            queueWaiter = nil
            return 0
        }
        waiter?.resume(returning: .wake)
        return status
    }

    func beginDrain() -> Int32 {
        var waiter: CheckedContinuation<QueueItem, Never>?
        let status = withConditionLock { () -> Int32 in
            guard state == stateReady, !cancelled else {
                return cancelled ? closedStatus : notReadyStatus
            }
            guard !drainRequested else { return drainingStatus }
            drainRequested = true
            waiter = queueWaiter
            queueWaiter = nil
            condition.broadcast()
            return 0
        }
        waiter?.resume(returning: .wake)
        return status
    }

    func requestCancel() {
        let pending = withConditionLock { () -> (
            CheckedContinuation<QueueItem, Never>?,
            AsyncStream<AnalyzerInput>.Continuation?,
            Task<Void, Never>?,
            Task<Void, Error>?,
            Task<Void, Error>?,
            Task<Void, Never>?
        )? in
            guard state != stateDrained, state != stateFailed, !workerTerminated else { return nil }
            cancelled = true
            state = stateCancelled
            ringCount = 0
            ringHead = 0
            let waiter = queueWaiter
            queueWaiter = nil
            let continuation = inputContinuation
            let worker = workerTask
            let analysis = analysisTask
            let results = resultsTask
            if cancellationTask == nil, let analyzer {
                cancellationTask = Task { await analyzer.cancelAndFinishNow() }
            }
            condition.broadcast()
            return (waiter, continuation, worker, analysis, results, cancellationTask)
        }
        guard let (waiter, continuation, worker, analysis, results, _) = pending else { return }
        waiter?.resume(returning: .wake)
        continuation?.finish()
        worker?.cancel()
        analysis?.cancel()
        results?.cancel()
        // Keep cancellation running independently so analyzeSequence can be
        // interrupted while the Go owner waits for every task to join.
    }

    func errorMessage() -> String {
        withConditionLock { failureMessage }
    }

    func copyFinalText(into output: UnsafeMutablePointer<UInt8>?, capacity: Int32, length: UnsafeMutablePointer<Int32>?) -> Int32 {
        guard let output, let length, capacity >= 0 else { return 1 }
        let (state, text) = withConditionLock { (self.state, finalTextValue) }
        guard state == stateDrained else { return 2 }
        let bytes = Array(text.utf8)
        guard bytes.count <= maximumFinalTextBytes else { return 3 }
        guard Int(capacity) >= bytes.count else { return 4 }
        for index in bytes.indices { output[index] = bytes[index] }
        length.pointee = Int32(bytes.count)
        return 0
    }

    private func nextBatch() async -> [Int16]? {
        while true {
            let item = await withCheckedContinuation { (continuation: CheckedContinuation<QueueItem, Never>) in
                let immediate = withConditionLock { () -> QueueItem? in
                    if ringCount > 0 {
                        let amount = min(maximumBlockSamples, ringCount)
                        var batch = [Int16](repeating: 0, count: amount)
                        for index in 0..<amount {
                            batch[index] = ring[ringHead]
                            ringHead = (ringHead + 1) % nativeQueueCapacity
                        }
                        ringCount -= amount
                        return .batch(batch)
                    }
                    if cancelled || state == stateFailed || drainRequested { return .finished }
                    queueWaiter = continuation
                    return nil
                }
                if let immediate { continuation.resume(returning: immediate) }
            }
            switch item {
            case .batch(let batch): return batch
            case .finished: return nil
            case .wake: continue
            }
        }
    }

    private func run(localeIdentifier: String, installAssets: Bool, enablePartials: Bool) async {
        var localAnalyzer: SpeechAnalyzer?
        var localAnalysisTask: Task<Void, Error>?
        var localResultsTask: Task<Void, Error>?
        var newlyReservedLocale: Locale?
        var preparationReachedReady = false
        defer {
            withConditionLock {
                workerTerminated = true
                workerTask = nil
                condition.broadcast()
            }
        }

        do {
            try Task.checkCancellation()
            guard let locale = await SpeechTranscriber.supportedLocale(equivalentTo: Locale(identifier: localeIdentifier)) else {
                throw BridgeFailure(description: "unsupported Apple Speech locale: \(localeIdentifier)")
            }
            try Task.checkCancellation()

            let preset = SpeechTranscriber.Preset.progressiveTranscription
            var reportingOptions = preset.reportingOptions
            if enablePartials {
                reportingOptions.insert(.volatileResults)
            } else {
                reportingOptions.remove(.volatileResults)
            }
            let transcriber = SpeechTranscriber(
                locale: locale,
                transcriptionOptions: preset.transcriptionOptions,
                reportingOptions: reportingOptions,
                attributeOptions: preset.attributeOptions
            )
            var moduleStatus = await AssetInventory.status(forModules: [transcriber])
            if moduleStatus == .unsupported {
                throw BridgeFailure.unsupported(localeIdentifier, status: String(describing: moduleStatus))
            }
            if installAssets {
                let reservationsBeforeRequest = await AssetInventory.reservedLocales
                let request: AssetInstallationRequest?
                do {
                    request = try await AssetInventory.assetInstallationRequest(supporting: [transcriber])
                } catch {
                    let reservationsAfterRequest = await AssetInventory.reservedLocales
                    newlyReservedLocale = findNewlyReservedLocale(
                        requested: locale,
                        before: reservationsBeforeRequest,
                        after: reservationsAfterRequest
                    )
                    if Task.isCancelled || error is CancellationError { throw CancellationError() }
                    let requestStatus = await AssetInventory.status(forModules: [transcriber])
                    throw classifyInstallationRequestFailure(error, status: requestStatus, locale: localeIdentifier)
                }
                let reservationsAfterRequest = await AssetInventory.reservedLocales
                newlyReservedLocale = findNewlyReservedLocale(
                    requested: locale,
                    before: reservationsBeforeRequest,
                    after: reservationsAfterRequest
                )
                if let request {
                    try Task.checkCancellation()
                    do {
                        try await request.downloadAndInstall()
                    } catch {
                        if Task.isCancelled || error is CancellationError { throw CancellationError() }
                        let installStatus = await AssetInventory.status(forModules: [transcriber])
                        let category = classifyPreparationFailure(error, status: installStatus, locale: localeIdentifier)
                        if category is BridgeFailure { throw category }
                        throw BridgeFailure.assets(localeIdentifier, status: "installation failed: \(error)")
                    }
                    try Task.checkCancellation()
                }
                moduleStatus = await AssetInventory.status(forModules: [transcriber])
                if moduleStatus == .unsupported {
                    throw BridgeFailure.unsupported(localeIdentifier, status: String(describing: moduleStatus))
                }
            }

            let requestedFormat = AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: 16_000, channels: 1, interleaved: false)!
            guard let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber], considering: requestedFormat) else {
                if moduleStatus == .supported || moduleStatus == .downloading {
                    throw BridgeFailure.assets(localeIdentifier, status: String(describing: moduleStatus))
                }
                throw BridgeFailure(description: "Apple Speech has no compatible 16000 Hz mono Int16 input format (status: \(moduleStatus))")
            }
            guard format.sampleRate == 16_000, format.channelCount == 1, format.commonFormat == .pcmFormatInt16 else {
                throw BridgeFailure(description: "Apple Speech selected an incompatible audio format: \(format)")
            }

            let createdAnalyzer = SpeechAnalyzer(
                modules: [transcriber],
                options: .init(priority: .userInitiated, modelRetention: .whileInUse)
            )
            localAnalyzer = createdAnalyzer
            let cancelledBeforePrepare = withConditionLock {
                analyzer = createdAnalyzer
                return cancelled
            }
            if cancelledBeforePrepare { throw CancellationError() }

            do {
                try await createdAnalyzer.prepareToAnalyze(in: format)
            } catch {
                if Task.isCancelled || error is CancellationError { throw CancellationError() }
                let prepareStatus = await AssetInventory.status(forModules: [transcriber])
                throw classifyPreparationFailure(error, status: prepareStatus, locale: localeIdentifier)
            }
            try Task.checkCancellation()

            let (stream, continuation) = AsyncStream<AnalyzerInput>.makeStream(bufferingPolicy: .bufferingOldest(1))
            let resultTask = Task {
                if enablePartials {
                    try await collectStreamingResults(from: transcriber)
                } else {
                    try await collectFinalResults(from: transcriber)
                }
            }
            localResultsTask = resultTask
            let analysisTask = Task { () throws -> Void in
                let end = try await createdAnalyzer.analyzeSequence(stream)
                // Cancellation may return the last consumed sample normally.
                try Task.checkCancellation()
                // requestCancel publishes this flag before cancelling child tasks.
                if self.withConditionLock({ self.cancelled }) { throw CancellationError() }
                if let end {
                    try await createdAnalyzer.finalizeAndFinish(through: end)
                } else {
                    await createdAnalyzer.cancelAndFinishNow()
                    throw CancellationError()
                }
            }
            localAnalysisTask = analysisTask
            let cancelBeforeReady = withConditionLock {
                inputContinuation = continuation
                resultsTask = resultTask
                self.analysisTask = analysisTask
                let wasCancelled = cancelled
                if !wasCancelled {
                    state = stateReady
                    preparationReachedReady = true
                }
                condition.broadcast()
                return wasCancelled
            }
            if cancelBeforeReady { throw CancellationError() }

            while let samples = await nextBatch() {
                try Task.checkCancellation()
                guard let buffer = makeBuffer(samples, format: format) else {
                    throw BridgeFailure(description: "could not allocate Apple Speech PCM buffer")
                }
                let input = AnalyzerInput(buffer: buffer)
                var enqueued = false
                while !enqueued {
                    try Task.checkCancellation()
                    switch continuation.yield(input) {
                    case .enqueued:
                        enqueued = true
                    case .dropped:
                        // Keep this one batch and retry. The stream has capacity
                        // one; no input is discarded while the analyzer catches up.
                        try await Task.sleep(for: .milliseconds(2))
                    case .terminated:
                        throw BridgeFailure(description: "Apple Speech input stream terminated before drain")
                    @unknown default:
                        throw BridgeFailure(description: "unknown Apple Speech input stream state")
                    }
                }
            }
            continuation.finish()
            try await analysisTask.value
            try await resultTask.value

            let needsCancellationJoin = withConditionLock {
                if !cancelled && state != stateFailed {
                    state = stateDrained
                }
                condition.broadcast()
                return cancelled
            }
            // Cancellation can arrive after analysis returned successfully.
            // Do not report worker termination before its cancellation task joins.
            if needsCancellationJoin { throw CancellationError() }
        } catch {
            await cancelAndJoin(analyzer: localAnalyzer, analysisTask: localAnalysisTask, resultsTask: localResultsTask)
            _ = await releaseUncommittedReservation(
                newlyReservedLocale,
                preparationReachedReady: preparationReachedReady
            ) { reservedLocale in
                await AssetInventory.release(reservedLocale: reservedLocale)
            }
            withConditionLock {
                if state == stateFailed {
                    // Preserve a specific failure (such as final-text overflow)
                    // even if its cleanup also cancels the worker task.
                } else if cancelled || error is CancellationError {
                    state = stateCancelled
                } else {
                    state = stateFailed
                    if failureMessage.isEmpty { failureMessage = String(describing: error) }
                }
                condition.broadcast()
            }
        }
    }

    private func makeBuffer(_ samples: [Int16], format: AVAudioFormat) -> AVAudioPCMBuffer? {
        guard let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(samples.count)),
              let destination = buffer.int16ChannelData?[0] else { return nil }
        buffer.frameLength = AVAudioFrameCount(samples.count)
        samples.withUnsafeBufferPointer { source in
            if let baseAddress = source.baseAddress {
                memcpy(destination, baseAddress, samples.count * MemoryLayout<Int16>.stride)
            }
        }
        return buffer
    }

    private func collectFinalResults(from transcriber: SpeechTranscriber) async throws {
        var finalText = ""
        for try await result in transcriber.results {
            try Task.checkCancellation()
            guard result.isFinal else { continue }
            finalText += String(result.text.characters)
            guard finalText.utf8.count <= maximumFinalTextBytes else {
                throw failResultOverflow()
            }
        }
        withConditionLock {
            if state != stateCancelled && state != stateFailed {
                finalTextValue = finalText
            }
        }
    }

    private func collectStreamingResults(from transcriber: SpeechTranscriber) async throws {
        var accumulator = TranscriptAccumulator()

        for try await result in transcriber.results {
            try Task.checkCancellation()
            let snapshot = accumulator.consume(
                range: result.range,
                text: String(result.text.characters),
                isFinal: result.isFinal,
                resultsFinalizationTime: result.resultsFinalizationTime
            )
            guard !snapshot.overLimit else {
                throw failResultOverflow()
            }
            publishPartial(stable: snapshot.stable, tentative: snapshot.tentative)
        }

        let finalSnapshot = accumulator.finish()
        guard !finalSnapshot.overLimit else {
            throw failResultOverflow()
        }
        publishPartial(stable: finalSnapshot.finalText, tentative: "")
        withConditionLock {
            if state != stateCancelled && state != stateFailed {
                stablePartialTextValue = finalSnapshot.finalText
                tentativePartialTextValue = ""
                finalTextValue = finalSnapshot.finalText
                condition.broadcast()
            }
        }
    }

    private func publishPartial(stable: String, tentative: String) {
        withConditionLock {
            guard state != stateCancelled && state != stateFailed,
                  stablePartialTextValue != stable || tentativePartialTextValue != tentative else { return }
            stablePartialTextValue = stable
            tentativePartialTextValue = tentative
            partialVersion &+= 1
            condition.broadcast()
        }
    }

    private func failResultOverflow() -> BridgeFailure {
        let pending = withConditionLock { () -> (
            CheckedContinuation<QueueItem, Never>?,
            AsyncStream<AnalyzerInput>.Continuation?,
            Task<Void, Never>?,
            Task<Void, Error>?
        ) in
            failureMessage = "final text exceeds 65536 UTF-8 bytes"
            state = stateFailed
            ringCount = 0
            ringHead = 0
            let waiter = queueWaiter
            queueWaiter = nil
            let continuation = inputContinuation
            let worker = workerTask
            let analysis = analysisTask
            condition.broadcast()
            return (waiter, continuation, worker, analysis)
        }
        let (waiter, continuation, worker, analysis) = pending
        waiter?.resume(returning: .wake)
        continuation?.finish()
        analysis?.cancel()
        worker?.cancel()
        return BridgeFailure(description: "final text exceeds 65536 UTF-8 bytes")
    }

    private func cancelAndJoin(
        analyzer localAnalyzer: SpeechAnalyzer?,
        analysisTask localAnalysisTask: Task<Void, Error>?,
        resultsTask localResultsTask: Task<Void, Error>?
    ) async {
        if let localAnalysisTask { localAnalysisTask.cancel() }
        if let localResultsTask { localResultsTask.cancel() }

        var cancelTask: Task<Void, Never>?
        if let localAnalyzer {
            cancelTask = withConditionLock {
                if cancellationTask == nil {
                    cancellationTask = Task { await localAnalyzer.cancelAndFinishNow() }
                }
                return cancellationTask
            }
        }
        if let localAnalysisTask { _ = try? await localAnalysisTask.value }
        if let localResultsTask { _ = try? await localResultsTask.value }
        if let cancelTask { await cancelTask.value }
    }
}

@available(macOS 26.0, *)
private func object(for raw: UnsafeMutableRawPointer?) -> AppleSpeechSession? {
    guard let raw else { return nil }
    return Unmanaged<AppleSpeechSession>.fromOpaque(raw).takeUnretainedValue()
}

private func writeCString(_ text: String, buffer: UnsafeMutablePointer<CChar>?, capacity: Int32) {
    guard let buffer, capacity > 0 else { return }
    let bytes = Array(text.utf8)
    let copyCount = min(bytes.count, Int(capacity) - 1)
    for index in 0..<copyCount { buffer[index] = CChar(bitPattern: bytes[index]) }
    buffer[copyCount] = 0
}

@_cdecl("MiMacAppleSpeechIsAvailable")
public func MiMacAppleSpeechIsAvailable() -> Int32 {
    guard #available(macOS 26.0, *) else { return 0 }
    return SpeechTranscriber.isAvailable ? 1 : 0
}

@_cdecl("MiMacAppleSpeechCreate")
public func MiMacAppleSpeechCreate(
    _ locale: UnsafePointer<CChar>?,
    _ sampleRate: Int32,
    _ installAssets: Int32,
    _ statusOut: UnsafeMutablePointer<Int32>?,
    _ errorBuffer: UnsafeMutablePointer<CChar>?,
    _ errorCapacity: Int32
) -> UnsafeMutableRawPointer? {
    guard let statusOut else { return nil }
    guard #available(macOS 26.0, *) else {
        statusOut.pointee = 1
        writeCString("SpeechAnalyzer requires macOS 26 or later", buffer: errorBuffer, capacity: errorCapacity)
        return nil
    }
    guard sampleRate == 16_000, let locale else {
        statusOut.pointee = 2
        writeCString("expected 16000 Hz mono Int16 and a locale", buffer: errorBuffer, capacity: errorCapacity)
        return nil
    }
    let identifier = String(cString: locale)
    let session = AppleSpeechSession(localeIdentifier: identifier, installAssets: installAssets == 1, enablePartials: false)
    statusOut.pointee = 0
    return Unmanaged.passRetained(session).toOpaque()
}

@_cdecl("MiMacAppleSpeechCreateWithPartials")
public func MiMacAppleSpeechCreateWithPartials(
    _ locale: UnsafePointer<CChar>?,
    _ sampleRate: Int32,
    _ installAssets: Int32,
    _ enablePartials: Int32,
    _ statusOut: UnsafeMutablePointer<Int32>?,
    _ errorBuffer: UnsafeMutablePointer<CChar>?,
    _ errorCapacity: Int32
) -> UnsafeMutableRawPointer? {
    guard let statusOut else { return nil }
    guard #available(macOS 26.0, *) else {
        statusOut.pointee = 1
        writeCString("SpeechAnalyzer requires macOS 26 or later", buffer: errorBuffer, capacity: errorCapacity)
        return nil
    }
    guard sampleRate == 16_000, let locale, enablePartials == 1 else {
        statusOut.pointee = 2
        writeCString("expected 16000 Hz mono Int16, locale, and enabled partial results", buffer: errorBuffer, capacity: errorCapacity)
        return nil
    }
    let identifier = String(cString: locale)
    let session = AppleSpeechSession(localeIdentifier: identifier, installAssets: installAssets == 1, enablePartials: true)
    statusOut.pointee = 0
    return Unmanaged.passRetained(session).toOpaque()
}

@_cdecl("MiMacAppleSpeechWaitReady")
public func MiMacAppleSpeechWaitReady(_ raw: UnsafeMutableRawPointer?, _ timeoutMilliseconds: Int32) -> Int32 {
    guard #available(macOS 26.0, *), let session = object(for: raw) else { return stateFailed }
    return session.waitReady(timeoutMilliseconds: timeoutMilliseconds)
}

@_cdecl("MiMacAppleSpeechTryWrite")
public func MiMacAppleSpeechTryWrite(_ raw: UnsafeMutableRawPointer?, _ samples: UnsafePointer<Int16>?, _ count: Int32) -> Int32 {
    guard #available(macOS 26.0, *), let session = object(for: raw) else { return closedStatus }
    return session.tryWrite(samples, count: count)
}

@_cdecl("MiMacAppleSpeechBeginDrain")
public func MiMacAppleSpeechBeginDrain(_ raw: UnsafeMutableRawPointer?) -> Int32 {
    guard #available(macOS 26.0, *), let session = object(for: raw) else { return closedStatus }
    return session.beginDrain()
}

@_cdecl("MiMacAppleSpeechWaitDone")
public func MiMacAppleSpeechWaitDone(_ raw: UnsafeMutableRawPointer?, _ timeoutMilliseconds: Int32) -> Int32 {
    guard #available(macOS 26.0, *), let session = object(for: raw) else { return stateFailed }
    return session.waitDone(timeoutMilliseconds: timeoutMilliseconds)
}

@_cdecl("MiMacAppleSpeechWaitPartial")
public func MiMacAppleSpeechWaitPartial(
    _ raw: UnsafeMutableRawPointer?,
    _ afterVersion: UInt64,
    _ timeoutMilliseconds: Int32,
    _ stableOutput: UnsafeMutablePointer<UInt8>?,
    _ stableCapacity: Int32,
    _ stableLength: UnsafeMutablePointer<Int32>?,
    _ tentativeOutput: UnsafeMutablePointer<UInt8>?,
    _ tentativeCapacity: Int32,
    _ tentativeLength: UnsafeMutablePointer<Int32>?,
    _ versionOutput: UnsafeMutablePointer<UInt64>?
) -> Int32 {
    guard #available(macOS 26.0, *), let session = object(for: raw) else { return partialInvalidArgumentStatus }
    return session.waitPartial(
        afterVersion: afterVersion,
        timeoutMilliseconds: timeoutMilliseconds,
        stableOutput: stableOutput,
        stableCapacity: stableCapacity,
        stableLength: stableLength,
        tentativeOutput: tentativeOutput,
        tentativeCapacity: tentativeCapacity,
        tentativeLength: tentativeLength,
        versionOutput: versionOutput
    )
}

@_cdecl("MiMacAppleSpeechRequestCancel")
public func MiMacAppleSpeechRequestCancel(_ raw: UnsafeMutableRawPointer?) -> Int32 {
    guard #available(macOS 26.0, *), let session = object(for: raw) else { return 0 }
    session.requestCancel()
    return 0
}

@_cdecl("MiMacAppleSpeechWaitTerminated")
public func MiMacAppleSpeechWaitTerminated(_ raw: UnsafeMutableRawPointer?, _ timeoutMilliseconds: Int32) -> Int32 {
    guard #available(macOS 26.0, *), let session = object(for: raw) else { return -1 }
    return session.waitTerminated(timeoutMilliseconds: timeoutMilliseconds) ? 1 : 0
}

@_cdecl("MiMacAppleSpeechCopyFinalText")
public func MiMacAppleSpeechCopyFinalText(
    _ raw: UnsafeMutableRawPointer?,
    _ output: UnsafeMutablePointer<UInt8>?,
    _ capacity: Int32,
    _ length: UnsafeMutablePointer<Int32>?
) -> Int32 {
    guard #available(macOS 26.0, *), let session = object(for: raw) else { return 1 }
    return session.copyFinalText(into: output, capacity: capacity, length: length)
}

@_cdecl("MiMacAppleSpeechCopyErrorMessage")
public func MiMacAppleSpeechCopyErrorMessage(
    _ raw: UnsafeMutableRawPointer?,
    _ output: UnsafeMutablePointer<UInt8>?,
    _ capacity: Int32,
    _ length: UnsafeMutablePointer<Int32>?
) -> Int32 {
    guard #available(macOS 26.0, *), let session = object(for: raw), let output, let length, capacity > 0 else { return 1 }
    let bytes = Array(session.errorMessage().utf8)
    guard bytes.count < Int(capacity) else { return 2 }
    for index in bytes.indices { output[index] = bytes[index] }
    length.pointee = Int32(bytes.count)
    return 0
}

@_cdecl("MiMacAppleSpeechRelease")
public func MiMacAppleSpeechRelease(_ raw: UnsafeMutableRawPointer?) -> Int32 {
    guard #available(macOS 26.0, *), let raw, let session = object(for: raw) else { return 0 }
    guard session.waitTerminated(timeoutMilliseconds: 0) else { return 1 }
    Unmanaged<AppleSpeechSession>.fromOpaque(raw).release()
    return 0
}
