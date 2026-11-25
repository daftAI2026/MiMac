import AppKit
import AVFoundation
import CoreImage
import CryptoKit
import Foundation
import ScreenCaptureKit

private func emit(_ value: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]),
          let line = String(data: data, encoding: .utf8) else { return }
    print(line)
    fflush(stdout)
}

private final class RecordingDelegate: NSObject, SCRecordingOutputDelegate {
    private let lock = NSLock()
    private var finished = false
    private var failure: String?

    func recordingOutputDidStartRecording(_ recordingOutput: SCRecordingOutput) {
        emit(["event": "recording_started", "wall_epoch": Date().timeIntervalSince1970])
    }

    func recordingOutputDidFinishRecording(_ recordingOutput: SCRecordingOutput) {
        let duration = recordingOutput.recordedDuration.seconds
        emit(["event": "recording_finished", "wall_epoch": Date().timeIntervalSince1970,
              "bytes": recordingOutput.recordedFileSize,
              "duration_seconds": duration.isFinite ? duration as Any : NSNull()])
        lock.lock()
        finished = true
        lock.unlock()
    }

    func recordingOutput(_ recordingOutput: SCRecordingOutput, didFailWithError error: Error) {
        emit(["event": "recording_failed", "wall_epoch": Date().timeIntervalSince1970,
              "error": String(describing: error)])
        lock.lock()
        failure = String(describing: error)
        finished = true
        lock.unlock()
    }

    func status() -> (finished: Bool, failure: String?) {
        lock.lock()
        defer { lock.unlock() }
        return (finished, failure)
    }
}

private final class SampleProbe: NSObject, SCStreamOutput {
    private let output: URL
    private let roi: CGRect
    private let imageContext = CIContext(options: [.cacheIntermediates: false])
    private var frameCount = 0
    private var firstFrameWritten = false
    private var firstGreenWritten = false
    private var lastNonGreenSample: [String: Any]?

    init(output: URL, roi: CGRect) {
        self.output = output
        self.roi = roi
    }

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of type: SCStreamOutputType) {
        guard type == .screen, CMSampleBufferIsValid(sampleBuffer),
              let pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }
        // ScreenCaptureKit dispatches these samples on the serial handler queue.
        let wall = Date().timeIntervalSince1970
        let ptsSeconds = CMTimeGetSeconds(CMSampleBufferGetPresentationTimeStamp(sampleBuffer))
        guard ptsSeconds.isFinite else { return }
        frameCount += 1
        let index = frameCount
        let writeFirstFrame = !firstFrameWritten
        let greenCount = PermissionFramePixels.greenCount(in: pixelBuffer, region: roi, context: imageContext)
        let currentSample: [String: Any] = [
            "index": index, "presentation_seconds": ptsSeconds, "wall_received_epoch": wall,
            "pixel_format": CVPixelBufferGetPixelFormatType(pixelBuffer),
            "greenish_pixels_in_roi": greenCount as Any? ?? NSNull(),
        ]
        let previousNonGreen = lastNonGreenSample
        let writeFirstGreen = !firstGreenWritten && greenCount.map { $0 >= PermissionFramePixels.greenThreshold } == true
        if let greenCount, greenCount < PermissionFramePixels.greenThreshold { lastNonGreenSample = currentSample }
        var screenEvent = currentSample
        screenEvent["event"] = "screen_sample"
        screenEvent["target_roi_pixels"] = [roi.origin.x, roi.origin.y, roi.width, roi.height]
        emit(screenEvent)
        guard writeFirstFrame || writeFirstGreen else { return }
        guard let cgImage = imageContext.createCGImage(CIImage(cvPixelBuffer: pixelBuffer),
                                                       from: CGRect(x: 0, y: 0,
                                                                    width: CVPixelBufferGetWidth(pixelBuffer),
                                                                    height: CVPixelBufferGetHeight(pixelBuffer))) else { return }
        let bitmap = NSBitmapImageRep(cgImage: cgImage)
        guard let data = bitmap.representation(using: .png, properties: [:]) else { return }
        do {
            let digest = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
            if writeFirstFrame {
                let frameURL = output.deletingPathExtension().appendingPathExtension("first-frame.png")
                try data.write(to: frameURL, options: .atomic)
                emit(["event": "first_frame_png", "path": frameURL.path,
                      "sha256": digest, "width": cgImage.width, "height": cgImage.height,
                      "index": index, "presentation_seconds": ptsSeconds,
                      "wall_received_epoch": wall, "greenish_pixels_in_roi": greenCount as Any? ?? NSNull()])
                firstFrameWritten = true
            }
            if writeFirstGreen {
                let frameURL = output.deletingPathExtension().appendingPathExtension("first-green-row.png")
                try data.write(to: frameURL, options: .atomic)
                emit(["event": "first_green_live_sample", "path": frameURL.path,
                      "sha256": digest, "width": cgImage.width, "height": cgImage.height,
                      "threshold_pixels": PermissionFramePixels.greenThreshold, "target_roi_pixels": [roi.origin.x, roi.origin.y, roi.width, roi.height],
                      "previous_non_green_sample": previousNonGreen as Any? ?? NSNull(),
                      "current_green_sample": currentSample])
                firstGreenWritten = true
            }
        } catch {
            emit(["event": "first_frame_write_failed", "error": String(describing: error)])
        }
    }


}

@main struct WindowRecorder {
    @MainActor static func main() async throws {
        _ = NSApplication.shared
        let args = Array(CommandLine.arguments.dropFirst())
        guard (args.count == 10 || args.count == 11),
              let pid = Int32(args[2]), pid > 0,
              let duration = Double(args[5]), duration >= 0, duration <= 30,
              let roiX = Double(args[6]), let roiY = Double(args[7]),
              let roiWidth = Double(args[8]), let roiHeight = Double(args[9]),
              roiX >= 0, roiY >= 0, roiWidth > 0, roiHeight > 0,
              duration > 0 || args.count == 11 else {
            fatalError("usage: WindowRecorder bundleID bundlePath pid exactWindowTitle output.mp4 seconds(0..30) roi-x roi-y roi-width roi-height [stop-file; duration=0]")
        }
        guard CGPreflightScreenCaptureAccess() else {
            emit(["event": "preflight_failed", "screen_capture_access": false])
            exit(3)
        }
        let bundleID = args[0]
        let bundlePath = URL(fileURLWithPath: args[1]).resolvingSymlinksInPath().path
        let title = args[3]
        let output = URL(fileURLWithPath: args[4])
        let matchingApps = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
            .filter { $0.bundleURL?.resolvingSymlinksInPath().path == bundlePath }
        guard matchingApps.count == 1, matchingApps[0].processIdentifier == pid else {
            fatalError("the exact bundle ID, canonical app path and unique PID did not match")
        }
        let content = try await SCShareableContent.excludingDesktopWindows(true, onScreenWindowsOnly: true)
        let matches = content.windows.filter {
            $0.owningApplication?.processID == pid &&
            $0.owningApplication?.bundleIdentifier == bundleID && $0.title == title
        }
        guard matches.count == 1, let window = matches.first else {
            fatalError("the exact QA window was not uniquely visible")
        }
        let filter = SCContentFilter(desktopIndependentWindow: window)
        let config = SCStreamConfiguration()
        config.width = max(1, Int(ceil(window.frame.width * 2)))
        config.height = max(1, Int(ceil(window.frame.height * 2)))
        config.pixelFormat = kCVPixelFormatType_32BGRA
        config.minimumFrameInterval = CMTime(value: 1, timescale: 30)
        config.queueDepth = 5
        config.showsCursor = false
        config.capturesAudio = false
        config.captureMicrophone = false

        let recordingConfig = SCRecordingOutputConfiguration()
        recordingConfig.outputURL = output
        recordingConfig.videoCodecType = .h264
        recordingConfig.outputFileType = .mp4
        let recordingDelegate = RecordingDelegate()
        let recordingOutput = SCRecordingOutput(configuration: recordingConfig, delegate: recordingDelegate)
        let roi = CGRect(x: roiX, y: roiY, width: roiWidth, height: roiHeight)
        guard roi.maxX <= CGFloat(config.width), roi.maxY <= CGFloat(config.height) else {
            fatalError("the target permission-row ROI is outside the exact window capture")
        }
        let stopFile = args.count == 11 ? args[10] : nil
        let sampleProbe = SampleProbe(output: output, roi: roi)
        let stream = SCStream(filter: filter, configuration: config, delegate: nil)
        try stream.addRecordingOutput(recordingOutput)
        try stream.addStreamOutput(sampleProbe, type: .screen, sampleHandlerQueue: DispatchQueue(label: "mimac.qa.window-recorder"))

        let captureRequestedAt = Date().timeIntervalSince1970
        emit(["event": "capture_prepared", "wall_epoch": captureRequestedAt,
              "bundle_id": bundleID, "bundle_path": bundlePath, "pid": pid,
              "window_title": title, "window_id": window.windowID,
              "window_frame_points": [window.frame.origin.x, window.frame.origin.y,
                                      window.frame.width, window.frame.height],
              "capture_width": config.width, "capture_height": config.height,
              "roi_pixels": [roi.origin.x, roi.origin.y, roi.width, roi.height],
              "audio": false, "microphone": false, "cursor": false,
              "duration_limit_seconds": duration > 0 ? duration as Any : NSNull(),
              "stop_file": stopFile as Any? ?? NSNull()])
        try await stream.startCapture()
        emit(["event": "capture_running", "wall_epoch": Date().timeIntervalSince1970])
        if duration > 0 {
            try await Task.sleep(nanoseconds: UInt64(duration * 1_000_000_000))
        } else if let stopFile {
            while !FileManager.default.fileExists(atPath: stopFile) {
                try await Task.sleep(nanoseconds: 100_000_000)
            }
            emit(["event": "capture_stop_file_seen", "wall_epoch": Date().timeIntervalSince1970])
        }
        // Closing the panel on the last grant can finish the movie before the
        // runner releases its observer. The recording delegate owns completion.
        if !recordingDelegate.status().finished {
            do {
                try await stream.stopCapture()
            } catch let error as NSError {
                guard error.domain == SCStreamErrorDomain,
                      error.code == SCStreamError.Code.attemptToStopStreamState.rawValue else { throw error }
            }
        }
        for _ in 0..<60 {
            let (finished, failure) = recordingDelegate.status()
            if let failure { throw NSError(domain: "WindowRecorder", code: 1,
                                           userInfo: [NSLocalizedDescriptionKey: failure]) }
            if finished { return }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        fatalError("recording output did not finalize")
    }
}
