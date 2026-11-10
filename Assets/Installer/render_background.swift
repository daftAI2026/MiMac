import AppKit
import CoreGraphics
import Foundation

func fail(_ message: String) -> Never {
    fputs("MiMac installer background: \(message)\n", stderr)
    exit(1)
}

guard CommandLine.arguments.count == 2 else {
    fail("usage: render_background.swift OUTPUT.png")
}

let output = URL(fileURLWithPath: CommandLine.arguments[1])
let width = 600
let height = 268
let scale = 2
let bitmap = NSBitmapImageRep(
    bitmapDataPlanes: nil,
    pixelsWide: width * scale,
    pixelsHigh: height * scale,
    bitsPerSample: 8,
    samplesPerPixel: 4,
    hasAlpha: true,
    isPlanar: false,
    colorSpaceName: .deviceRGB,
    bytesPerRow: 0,
    bitsPerPixel: 0
)!
bitmap.size = NSSize(width: width, height: height)
guard let graphics = NSGraphicsContext(bitmapImageRep: bitmap) else {
    fail("cannot create bitmap context")
}

NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = graphics
graphics.imageInterpolation = .high
graphics.shouldAntialias = true
let context = graphics.cgContext
// NSGraphicsContext derives its backing scale from bitmap.size. Applying a
// second scale here would move the text outside the Retina bitmap.
let colors = [
    NSColor(srgbRed: 0.906, green: 0.906, blue: 0.906, alpha: 1).cgColor,
    NSColor(srgbRed: 1, green: 1, blue: 1, alpha: 1).cgColor,
] as CFArray
guard let gradient = CGGradient(
    colorsSpace: CGColorSpace(name: CGColorSpace.sRGB),
    colors: colors,
    locations: [0, 1]
) else {
    fail("cannot create background gradient")
}
context.drawLinearGradient(
    gradient,
    start: CGPoint(x: 0, y: height),
    end: CGPoint(x: 0, y: 0),
    options: []
)

let brand = NSAttributedString(
    string: "双击安装 MiMac",
    attributes: [
        .font: NSFont.systemFont(ofSize: 19, weight: .semibold),
        .foregroundColor: NSColor(srgbRed: 0.204, green: 0.204, blue: 0.204, alpha: 1),
    ]
)
brand.draw(at: NSPoint(x: (CGFloat(width) - brand.size().width) / 2, y: 211))

NSGraphicsContext.restoreGraphicsState()
guard let png = bitmap.representation(using: .png, properties: [:]) else {
    fail("cannot encode PNG")
}
do {
    try png.write(to: output, options: .atomic)
} catch {
    fail("cannot write \(output.path): \(error)")
}
