import AVFoundation
import AppKit
import CoreImage
import CoreVideo
import CryptoKit
import Foundation
import Vision

private func emit(_ value: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]),
          let line = String(data: data, encoding: .utf8) else { return }
    print(line)
    fflush(stdout)
}

private func savePNG(_ image: CGImage, to path: String) throws -> String {
    let rep = NSBitmapImageRep(cgImage: image)
    guard let data = rep.representation(using: .png, properties: [:]) else {
        throw NSError(domain: "FrameScan", code: 1,
                      userInfo: [NSLocalizedDescriptionKey: "could not encode PNG"])
    }
    try data.write(to: URL(fileURLWithPath: path), options: .atomic)
    return SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
}

@main struct FrameScan {
    static func main() async throws {
        let args = Array(CommandLine.arguments.dropFirst())
        guard args.count == 6,
              let x = Int(args[1]), let y = Int(args[2]),
              let width = Int(args[3]), let height = Int(args[4]), width > 0, height > 0 else {
            fatalError("usage: FrameScan video.mp4 roi-x roi-y roi-width roi-height output-prefix")
        }
        let videoURL = URL(fileURLWithPath: args[0])
        let prefix = args[5]
        let asset = AVURLAsset(url: videoURL)
        guard let track = try await asset.loadTracks(withMediaType: .video).first else {
            fatalError("video track is missing")
        }
        let reader = try AVAssetReader(asset: asset)
        let settings: [String: Any] = [
            kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA,
            kCVPixelBufferIOSurfacePropertiesKey as String: [:],
        ]
        let output = AVAssetReaderTrackOutput(track: track, outputSettings: settings)
        output.alwaysCopiesSampleData = false
        guard reader.canAdd(output) else { fatalError("AVAssetReader rejected the video track") }
        reader.add(output)
        guard reader.startReading() else { throw reader.error ?? NSError(domain: "FrameScan", code: 2) }

        let context = CIContext(options: [.cacheIntermediates: false])
        var frameIndex = 0
        var firstPTS: Double?
        var firstGreen: [String: Any]?
        var firstTerminalStatusText: [String: Any]?
        var previousNonGreen: (info: [String: Any], buffer: CVPixelBuffer)?
        var previousFrame: [String: Any]?
        var lastPTS = 0.0
        let region = CGRect(x: x, y: y, width: width, height: height)
        while let sample = output.copyNextSampleBuffer() {
            let pts = CMTimeGetSeconds(CMSampleBufferGetPresentationTimeStamp(sample))
            guard pts.isFinite else {
                throw NSError(domain: "FrameScan", code: 7,
                              userInfo: [NSLocalizedDescriptionKey: "Invalid presentation time at frame \(frameIndex + 1)"])
            }
            frameIndex += 1
            if firstPTS == nil { firstPTS = pts }
            lastPTS = pts
            // Keep the complete timeline for matching ScreenCaptureKit samples.
            // Once the granted badge is legible, no more pixels or OCR are needed.
            if firstTerminalStatusText != nil { continue }
            guard let buffer = CMSampleBufferGetImageBuffer(sample),
                  let green = PermissionFramePixels.greenCount(in: buffer, region: region, context: context) else {
                throw NSError(domain: "FrameScan", code: 4,
                              userInfo: [NSLocalizedDescriptionKey: "Unreadable target pixels at frame \(frameIndex)"])
            }
            var info: [String: Any] = [
                "frame_index": frameIndex, "presentation_seconds": pts,
                "seconds_from_first_sample": pts - (firstPTS ?? pts), "greenish_pixels_in_roi": green,
            ]
            if green < PermissionFramePixels.greenThreshold {
                if firstGreen == nil { previousNonGreen = (info, buffer) }
                continue
            }
            let image = CIImage(cvPixelBuffer: buffer)
            guard let cgImage = context.createCGImage(image, from: image.extent) else {
                throw NSError(domain: "FrameScan", code: 5)
            }
            let lines = try recognizeBadge(cgImage, inside: region)
            info["target_row_status_lines"] = lines
            if firstGreen == nil {
                if let prev = previousNonGreen {
                    let previousImage = CIImage(cvPixelBuffer: prev.buffer)
                    if let cg = context.createCGImage(previousImage, from: previousImage.extent) {
                        let path = prefix + ".previous.png"
                        previousFrame = prev.info.merging(["png": path, "sha256": try savePNG(cg, to: path)]) { _, new in new }
                    }
                    previousNonGreen = nil
                }
                let path = prefix + ".first-green-roi.png"
                firstGreen = info.merging(["png": path, "sha256": try savePNG(cgImage, to: path)]) { _, new in new }
            }
            if lines.contains(where: { $0.contains("已授权") || $0.contains("已授權") }) {
                if firstGreen?["frame_index"] as? Int == frameIndex {
                    firstTerminalStatusText = firstGreen
                } else {
                    let path = prefix + ".first-target-row-status.png"
                    firstTerminalStatusText = info.merging(["png": path, "sha256": try savePNG(cgImage, to: path)]) { _, new in new }
                }
            }
        }
        guard reader.status == .completed, frameIndex > 0 else {
            throw reader.error ?? NSError(domain: "FrameScan", code: 3,
                                          userInfo: [NSLocalizedDescriptionKey: "video scan did not complete"])
        }
        emit(["event": "scan_complete", "video": videoURL.path, "frame_count": frameIndex,
              "first_presentation_seconds": firstPTS as Any? ?? NSNull(),
              "last_presentation_seconds": lastPTS,
              "roi_pixels": [x, y, width, height],
              "first_target_row_status_text": firstTerminalStatusText as Any? ?? NSNull(),
              "previous_non_green_frame": previousFrame as Any? ?? NSNull(),
              "first_green_pixel_threshold": firstGreen as Any? ?? NSNull()])
    }

    private static func recognizeBadge(_ image: CGImage, inside roi: CGRect) throws -> [String] {
        let request = VNRecognizeTextRequest()
        request.recognitionLevel = .accurate
        request.usesLanguageCorrection = false
        request.recognitionLanguages = ["zh-Hans", "en-US"]
        // Cropping keeps a granted badge on another row out of the evidence.
        guard let badge = image.cropping(to: roi) else { throw NSError(domain: "FrameScan", code: 6) }
        try VNImageRequestHandler(cgImage: badge).perform([request])
        return (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }
    }
}
