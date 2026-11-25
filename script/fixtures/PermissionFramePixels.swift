import CoreImage
import CoreVideo
import Foundation

// The capture driver and the movie reader use the same top-left pixel region.
enum PermissionFramePixels {
    static let greenThreshold = 8

    static func greenCount(in buffer: CVPixelBuffer, region: CGRect, context: CIContext) -> Int? {
        let bounds = CGRect(x: 0, y: 0, width: CVPixelBufferGetWidth(buffer), height: CVPixelBufferGetHeight(buffer))
        guard !region.isEmpty, bounds.contains(region) else { return nil }
        let width = Int(region.width), height = Int(region.height)
        if CVPixelBufferGetPixelFormatType(buffer) == kCVPixelFormatType_32BGRA {
            guard CVPixelBufferLockBaseAddress(buffer, .readOnly) == kCVReturnSuccess else { return nil }
            defer { CVPixelBufferUnlockBaseAddress(buffer, .readOnly) }
            guard let base = CVPixelBufferGetBaseAddress(buffer) else { return nil }
            let stride = CVPixelBufferGetBytesPerRow(buffer)
            let start = base.advanced(by: Int(region.minY) * stride + Int(region.minX) * 4)
            return countBGRA(start.assumingMemoryBound(to: UInt8.self), width: width, height: height, stride: stride)
        }

        // ScreenCaptureKit can supply a video-range buffer when recording H.264.
        // Convert just the button's pixels, with Core Image's bottom-left origin.
        let image = CIImage(cvPixelBuffer: buffer)
        let crop = CGRect(x: region.minX, y: bounds.maxY - region.maxY, width: region.width, height: region.height)
        guard image.extent.contains(crop) else { return nil }
        let stride = width * 4
        var bytes = [UInt8](repeating: 0, count: stride * height)
        return bytes.withUnsafeMutableBufferPointer { storage in
            guard let base = storage.baseAddress else { return nil }
            context.render(image, toBitmap: base, rowBytes: stride, bounds: crop,
                           format: .BGRA8, colorSpace: CGColorSpaceCreateDeviceRGB())
            return countBGRA(base, width: width, height: height, stride: stride)
        }
    }

    private static func countBGRA(_ base: UnsafePointer<UInt8>, width: Int, height: Int, stride: Int) -> Int {
        var count = 0
        for y in 0..<height {
            let row = base.advanced(by: y * stride)
            for x in 0..<width {
                let pixel = row.advanced(by: x * 4)
                let green = Int(pixel[1])
                if green >= 80 && green > Int(pixel[2]) + 18 && green > Int(pixel[0]) + 12 {
                    count += 1
                }
            }
        }
        return count
    }
}
