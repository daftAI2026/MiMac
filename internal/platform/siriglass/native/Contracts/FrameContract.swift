import AppKit
import Darwin
import Foundation
import SiriGlassBridge
import SiriGlassModel

@main struct FrameContract {
  static func expect(_ condition: Bool, line: Int = #line) {
    guard condition else {
      fputs("FAIL typed frame at line \(line)\n", stderr)
      exit(1)
    }
  }
  @MainActor static func main() throws {
    _ = NSApplication.shared
    let parameters = GlassParameters(
      revision: 1, kind: 3, personality: 1,
      variant: "listening", audioPowerDB: -60,
      diameter: 100, visible: false, layers: 3)
    let data = try JSONEncoder().encode(parameters)
    let handle = data.withUnsafeBytes { bytes in
      siriGlassCreate(bytes.bindMemory(to: UInt8.self).baseAddress, Int32(bytes.count))
    }
    expect(handle != 0)
    defer { _ = siriGlassDestroy(handle) }
    let amplitudes: [Float] = [1.25, 0.125]
    let frequencies: [Float] = [200, 8000]
    amplitudes.withUnsafeBufferPointer { a in
      frequencies.withUnsafeBufferPointer { f in
        // Rejected frames must not consume the revision.
        expect(siriGlassUpdateFrame(handle, 2, 2, -60, a.baseAddress, f.baseAddress, 2) == 0)
        expect(siriGlassUpdateFrame(handle, 2, 1, -60, a.baseAddress, f.baseAddress, 17) == 0)
        expect(siriGlassUpdateFrame(handle, 2, 1, -60, nil, f.baseAddress, 2) == 0)
        expect(siriGlassUpdateFrame(handle, 2, 1, .nan, a.baseAddress, f.baseAddress, 2) == 0)
        expect(siriGlassUpdateFrame(handle, 2, 1, -60, a.baseAddress, f.baseAddress, 2) == 1)
        expect(siriGlassUpdateFrame(handle, 2, 1, -60, a.baseAddress, f.baseAddress, 2) == 0)
      }
    }
    expect(siriGlassUpdateFrame(handle, 3, 0, -60, nil, nil, 0) == 1)
    var legacy = parameters
    legacy.revision = 4
    let legacyData = try JSONEncoder().encode(legacy)
    expect(
      legacyData.withUnsafeBytes { bytes in
        siriGlassUpdate(handle, bytes.bindMemory(to: UInt8.self).baseAddress, Int32(bytes.count))
      } == 1)
    expect(siriGlassUpdateFrame(handle, 4, 0, -60, nil, nil, 0) == 0)
    expect(siriGlassUpdateFrame(UInt64.max, 4, 0, -60, nil, nil, 0) == 0)
    print("PASS typed frame bounds, revision and legacy interoperability")
  }
}
