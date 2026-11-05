#!/usr/bin/env swift
import AppKit
import ApplicationServices
import CoreGraphics
import CryptoKit
import Darwin
import Foundation
import ImageIO

// Development-only capture of the six existing settings pages. No settings,
// plugins, permissions, input source or remote controls are toggled.
struct CaptureFailure: Error, CustomStringConvertible {
    let description: String
    init(_ description: String) { self.description = description }
}
let pages = [
    ("device", "设备", "cmd/mimac/settings.go"),
    ("mapping", "按键映射", "cmd/mimac/settings_mapping.go"),
    ("voice", "语音工具", "cmd/mimac/settings_voice.go"),
    ("plugins", "插件", "cmd/mimac/settings_mapping.go"),
    ("general", "通用", "cmd/mimac/native_preferences.go"),
    ("about", "关于", "cmd/mimac/settings_about.go")
]
func attribute(_ element: AXUIElement, _ name: String) -> CFTypeRef? {
    var value: CFTypeRef?
    return AXUIElementCopyAttributeValue(element, name as CFString, &value) == .success ? value : nil
}
func string(_ element: AXUIElement, _ name: String) -> String? { attribute(element, name) as? String }
func children(_ element: AXUIElement) -> [AXUIElement] { attribute(element, "AXChildren") as? [AXUIElement] ?? [] }
func matches(_ root: AXUIElement, role: String, text: String) throws -> [AXUIElement] {
    var queue = [(root, 0)], cursor = 0, result: [AXUIElement] = []
    while cursor < queue.count {
        guard cursor < 3000 else { throw CaptureFailure("MiMac accessibility tree exceeded the capture bound") }
        let (element, depth) = queue[cursor]; cursor += 1
        if string(element, "AXRole") == role && [string(element, "AXTitle"), string(element, "AXValue")].contains(text) { result.append(element) }
        if depth < 20 && string(element, "AXRole") != "AXMenuBar" {
            queue.append(contentsOf: children(element).map { ($0, depth + 1) })
        }
    }
    return result
}
func wait(_ timeout: TimeInterval, _ predicate: () throws -> Bool) throws {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if try predicate() { return }
        _ = RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.1))
    }
    throw CaptureFailure("Timed out waiting for the actual MiMac page")
}
func selectPage(_ label: String, in window: AXUIElement) throws {
    let buttons = try matches(window, role: "AXButton", text: label)
    guard buttons.count == 1, let button = buttons.first else { throw CaptureFailure("Expected one sidebar button: \(label)") }
    guard AXUIElementPerformAction(button, "AXPress" as CFString) == .success else { throw CaptureFailure("Sidebar action failed: \(label)") }
    try wait(8) { try matches(window, role: "AXHeading", text: label).count == 1 }
}
func sha(_ data: Data) -> String { SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined() }
func processStart(_ pid: pid_t) throws -> Date {
    // NSRunningApplication.launchDate is nil for some directly launched App
    // executables. The kernel's start time still binds this PID to the binary.
    var info = proc_bsdinfo()
    let size = Int32(MemoryLayout<proc_bsdinfo>.size)
    guard proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, size) == size,
          info.pbi_pid == UInt32(pid), info.pbi_start_tvsec > 0 else {
        throw CaptureFailure("Could not verify the running process start time")
    }
    return Date(timeIntervalSince1970: Double(info.pbi_start_tvsec) + Double(info.pbi_start_tvusec) / 1_000_000)
}
func pixels(_ url: URL) throws -> (String, Int, Int) {
    guard let source = CGImageSourceCreateWithURL(url as CFURL, nil), let image = CGImageSourceCreateImageAtIndex(source, 0, nil),
          let raw = image.dataProvider?.data else { throw CaptureFailure("Window capture is not a decodable image") }
    return (sha(raw as Data), image.width, image.height)
}
func screenshot(_ windowID: UInt32, to url: URL) throws {
    let process = Process(); process.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
    process.arguments = ["-x", "-o", "-l", String(windowID), url.path]
    process.standardOutput = FileHandle.nullDevice
    let errors = Pipe(); process.standardError = errors
    try process.run(); process.waitUntilExit()
    guard process.terminationStatus == 0 else { throw CaptureFailure("Window-only capture failed (exit \(process.terminationStatus)); check Screen Recording permission") }
}
func writeJSON(_ object: Any, to url: URL) throws {
    let data = try JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes])
    try (data + Data([10])).write(to: url, options: .atomic)
}

do {
    let args = Array(CommandLine.arguments.dropFirst())
    if args == ["--help"] {
        print("Usage: script/capture_workbench.swift --bundle /absolute/path/MiMac.app\nCaptures only the running App's six settings pages, then restores its original page.\nOutput: ~/Library/Caches/MiMac/workbench-captures/<run>/; latest.json is updated only after all pages succeed.")
        exit(0)
    }
    guard args.count == 2, args[0] == "--bundle", args[1].hasPrefix("/") else { throw CaptureFailure("Use --bundle with the canonical absolute MiMac.app path") }
    let bundleURL = URL(fileURLWithPath: args[1]).standardizedFileURL.resolvingSymlinksInPath()
    guard let bundle = Bundle(url: bundleURL), bundle.bundleIdentifier == "dev.mimac.app", let executable = bundle.executableURL else { throw CaptureFailure("Target is not the MiMac app bundle") }
    _ = NSApplication.shared
    let apps = NSWorkspace.shared.runningApplications.filter { $0.bundleIdentifier == "dev.mimac.app" }
    guard apps.count == 1, let app = apps.first, app.bundleURL?.resolvingSymlinksInPath().path == bundleURL.path else { throw CaptureFailure("Expected exactly one running MiMac at the supplied bundle path") }
    let launchDate = try processStart(app.processIdentifier)
    guard app.executableURL?.resolvingSymlinksInPath().path == executable.resolvingSymlinksInPath().path,
          let modified = try FileManager.default.attributesOfItem(atPath: executable.path)[.modificationDate] as? Date,
          modified <= launchDate else { throw CaptureFailure("The running App does not match the bundle executable, or that executable changed after launch") }
    let executableDigest = sha(try Data(contentsOf: executable))
    guard AXIsProcessTrusted() else { throw CaptureFailure("The current runner needs Accessibility permission; no prompt or setting was changed") }
    guard CGPreflightScreenCaptureAccess() else { throw CaptureFailure("The current runner needs Screen Recording permission; no prompt was opened") }
    let application = AXUIElementCreateApplication(app.processIdentifier)
    guard AXUIElementSetMessagingTimeout(application, 2) == .success else { throw CaptureFailure("Could not bound AX messaging time") }
    let windows = (attribute(application, "AXWindows") as? [AXUIElement] ?? []).filter { string($0, "AXTitle") == "MiMac" }
    guard windows.count == 1, let window = windows.first, (attribute(window, "AXSheets") as? [AXUIElement] ?? []).isEmpty else { throw CaptureFailure("Expected the one MiMac settings window without a sheet") }
    let original = try pages.filter { try matches(window, role: "AXHeading", text: $0.1).count == 1 }
    guard original.count == 1 else { throw CaptureFailure("Cannot identify the original settings page") }
    let cgWindows = (CGWindowListCopyWindowInfo(.optionOnScreenOnly, kCGNullWindowID) as? [[String: Any]] ?? []).filter {
        ($0[kCGWindowOwnerPID as String] as? Int32) == app.processIdentifier && ($0[kCGWindowLayer as String] as? Int) == 0 && ($0[kCGWindowName as String] as? String) == "MiMac"
    }
    guard cgWindows.count == 1, let info = cgWindows.first, let windowID = info[kCGWindowNumber as String] as? UInt32,
          let bounds = info[kCGWindowBounds as String] as? [String: Any] else { throw CaptureFailure("Expected one visible MiMac window for window-only capture") }
    let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appendingPathComponent("MiMac/workbench-captures", isDirectory: true)
    try FileManager.default.createDirectory(at: caches, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
    let run = caches.appendingPathComponent(UUID().uuidString, isDirectory: true)
    try FileManager.default.createDirectory(at: run, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
    var records: [[String: Any]] = []
    var restored = false
    let previousFront = NSWorkspace.shared.frontmostApplication
    defer {
        if !restored { try? selectPage(original[0].1, in: window) }
        if let previousFront, previousFront.processIdentifier != app.processIdentifier,
           NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier {
            _ = previousFront.activate(options: [])
        }
    }
    // Background MyGo windows can pause selection-motion frames. A matching
    // heading alone is not a settled active-page screenshot.
    _ = app.activate(options: [])
    try wait(5) { NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier }
    guard AXUIElementPerformAction(window, "AXRaise" as CFString) == .success else { throw CaptureFailure("Could not raise the MiMac window") }
    let mainResult = AXUIElementSetAttributeValue(window, "AXMain" as CFString, kCFBooleanTrue)
    guard mainResult == .success else { throw CaptureFailure("Could not make MiMac the main window") }
    try wait(5) { (attribute(window, "AXMain") as? NSNumber)?.boolValue == true }
    for (id, label, source) in pages {
        guard NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier else {
            throw CaptureFailure("Foreground changed during capture; stopping without taking it back")
        }
        try selectPage(label, in: window)
        let candidate = run.appendingPathComponent("candidate.png")
        var previous = "", stable = false, size = (0, 0)
        let deadline = Date().addingTimeInterval(8)
        // Wait for a stable native frame, including asynchronous icons and the
        // selection transition. Compare decoded pixels, not PNG metadata.
        while Date() < deadline {
            _ = RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.3))
            guard NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier,
                  (attribute(window, "AXMain") as? NSNumber)?.boolValue == true else {
                throw CaptureFailure("MiMac lost its foreground/main-window state; capture stopped")
            }
            try screenshot(windowID, to: candidate)
            guard NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier else {
                throw CaptureFailure("Foreground changed during the window capture")
            }
            let (digest, width, height) = try pixels(candidate)
            size = (width, height)
            if digest == previous { stable = true; break }
            previous = digest
        }
        guard stable, try matches(window, role: "AXHeading", text: label).count == 1 else { throw CaptureFailure("Page did not settle: \(label). Previous complete gallery remains unchanged") }
        let target = run.appendingPathComponent(id + ".png")
        try FileManager.default.moveItem(at: candidate, to: target)
        let data = try Data(contentsOf: target)
        records.append(["id": id, "title": label, "file": id + ".png", "width": size.0, "height": size.1,
                        "sha256": sha(data), "capturedAt": ISO8601DateFormatter().string(from: Date()),
                        "state": "当前配置", "appearance": "current", "sourcePaths": [source]])
        print("Captured \(label): \(size.0)×\(size.1)")
    }
    try selectPage(original[0].1, in: window); restored = true
    guard try processStart(app.processIdentifier) == launchDate,
          sha(try Data(contentsOf: executable)) == executableDigest else { throw CaptureFailure("Process or executable changed during capture; complete gallery was not published") }
    if let previousFront, previousFront.processIdentifier != app.processIdentifier {
        guard previousFront.activate(options: []) else { throw CaptureFailure("Could not restore the previous foreground application") }
        try wait(5) { NSWorkspace.shared.frontmostApplication?.processIdentifier == previousFront.processIdentifier }
    }
    let manifest: [String: Any] = ["schema": 1, "capturedAt": ISO8601DateFormatter().string(from: Date()),
        "app": ["bundleIdentifier": "dev.mimac.app", "executableSHA256": executableDigest, "binaryIdentity": "bundle-on-disk", "processStartedAt": ISO8601DateFormatter().string(from: launchDate),
                "version": bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "", "pid": app.processIdentifier],
        "window": ["width": bounds["Width"] ?? 0, "height": bounds["Height"] ?? 0],
        "originalPageRestored": restored, "foregroundApplicationRestored": true, "activeWindowCapture": true, "pages": records]
    try writeJSON(manifest, to: run.appendingPathComponent("manifest.json"))
    try writeJSON(["directory": run.lastPathComponent], to: caches.appendingPathComponent("latest.json"))
    print("Complete gallery: \(run.path)")
} catch {
    fputs("Capture stopped: \(error)\n", stderr)
    exit(1)
}
