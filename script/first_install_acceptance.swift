import AppKit
import CryptoKit
import CoreGraphics
import Foundation

let fileManager = FileManager.default
let bundleID = "dev.mimac.app"
let bundleName = "MiMac.app"
let executable = "Contents/MacOS/MiMac"
let helperName = "ejected after 1 attempt(s)"
let home = fileManager.homeDirectoryForCurrentUser

struct Failure: Error, CustomStringConvertible {
    let description: String
    init(_ description: String) { self.description = description }
}

func command(_ path: String, _ arguments: [String]) throws -> Data {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: path)
    process.arguments = arguments
    let output = Pipe()
    process.standardOutput = output
    process.standardError = FileHandle.standardError
    try process.run()
    let bytes = output.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()
    guard process.terminationStatus == 0 else {
        throw Failure("\(path) exited \(process.terminationStatus)")
    }
    return bytes
}

func plist(_ bytes: Data) throws -> [String: Any] {
    guard let value = try PropertyListSerialization.propertyList(from: bytes, format: nil) as? [String: Any] else {
        throw Failure("invalid system property list")
    }
    return value
}

func sha256(_ url: URL) throws -> String {
    let data = try Data(contentsOf: url, options: .mappedIfSafe)
    return SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
}

func tick() {
    _ = RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.05))
}

func logNames(in directory: URL) -> Set<String> {
    let entries = try? fileManager.contentsOfDirectory(atPath: directory.path)
    return Set((entries ?? []).filter { $0.hasPrefix("detach-") && $0.hasSuffix(".log") })
}

func verifyBundle(_ path: URL, expectedSHA: String) throws {
    let raw = try Data(contentsOf: path.appendingPathComponent("Contents/Info.plist"))
    let info = try plist(raw)
    guard info["CFBundleIdentifier"] as? String == bundleID, info["CFBundleExecutable"] as? String == "MiMac" else {
        throw Failure("unexpected app identity at \(path.path)")
    }
    guard try sha256(path.appendingPathComponent(executable)) == expectedSHA else {
        throw Failure("the app differs from the frozen executable")
    }
    _ = try command("/usr/bin/codesign", ["--verify", "--deep", "--strict", path.path])
}

func attachedEntries(for image: URL) throws -> [[String: Any]] {
    let list = try plist(command("/usr/bin/hdiutil", ["info", "-plist"]))
    let images = list["images"] as? [[String: Any]] ?? []
    return images.filter { item in
        guard let path = item["image-path"] as? String else { return false }
        return URL(fileURLWithPath: path).resolvingSymlinksInPath().path == image.path
    }
}

func appearedBundles() -> [NSRunningApplication] {
    NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
}

func hasInitialWindow(_ pid: Int32) -> Bool {
    let visible = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID)
        as? [[String: Any]] ?? []
    return visible.contains { window in
        guard window[kCGWindowOwnerPID as String] as? Int32 == pid,
              window[kCGWindowLayer as String] as? Int == 0,
              let bounds = window[kCGWindowBounds as String] as? [String: CGFloat],
              let width = bounds["Width"], let height = bounds["Height"] else { return false }
        return width >= 400 && height >= 300
    }
}

func saveReceipt(_ receipt: [String: Any], at path: URL) throws {
    try JSONSerialization.data(withJSONObject: receipt, options: [.prettyPrinted, .sortedKeys])
        .write(to: path, options: .atomic)
}

func run() throws {
    let args = CommandLine.arguments
    let modes = ["--check", "--finder-open", "--wait-for-double-click"]
    guard args.count == 5 || args.count == 6, modes.contains(args[1]),
        (args[1] == "--check") == (args.count == 5),
        args[3].range(of: "^[a-fA-F0-9]{64}$", options: .regularExpression) != nil,
        args[4].range(of: "^[a-fA-F0-9]{64}$", options: .regularExpression) != nil else {
        throw Failure("usage: first-install-acceptance (--check|--finder-open|--wait-for-double-click) DMG IMAGE_SHA EXE_SHA [NEW_OUTPUT_DIR]")
    }
    let image = URL(fileURLWithPath: args[2]).resolvingSymlinksInPath()
    let imageSHA = args[3].lowercased()
    let exeSHA = args[4].lowercased()
    guard image.pathExtension.lowercased() == "dmg", try sha256(image) == imageSHA else {
        throw Failure("the disk image does not match the frozen SHA-256")
    }

    let destinations = [
        URL(fileURLWithPath: "/Applications").appendingPathComponent(bundleName),
        home.appendingPathComponent("Applications").appendingPathComponent(bundleName),
    ].map { $0.resolvingSymlinksInPath() }
    for path in destinations {
        do {
            _ = try fileManager.attributesOfItem(atPath: path.path)
            throw Failure("clean first install required; existing install at \(path.path)")
        } catch let error as CocoaError where error.code == .fileReadNoSuchFile || error.code == .fileNoSuchFile {
            continue
        }
    }
    let dailyConfig = home.appendingPathComponent("Library/Application Support/MiMac")
    guard !fileManager.fileExists(atPath: dailyConfig.path) else {
        throw Failure("the test account already has MiMac preferences")
    }
    guard appearedBundles().isEmpty, try attachedEntries(for: image).isEmpty else {
        throw Failure("the daily identity is running or the candidate is already mounted")
    }
    guard args[1] != "--check" else {
        print("READY fresh install; frozen image matches")
        return
    }

    let output = URL(fileURLWithPath: args[5], isDirectory: true).standardizedFileURL
    guard !fileManager.fileExists(atPath: output.path) else {
        throw Failure("use a new output directory")
    }
    try fileManager.createDirectory(at: output, withIntermediateDirectories: false)
    let resultPath = output.appendingPathComponent("first-install.json")
    let mount = output.appendingPathComponent("volume", isDirectory: true)
    try fileManager.createDirectory(at: mount, withIntermediateDirectories: false)
    let logDirectory = home.appendingPathComponent("Library/Caches/MiMac/appinstall", isDirectory: true)
    let oldLogs = logNames(in: logDirectory)
    var receipt: [String: Any] = ["image_sha256": imageSHA, "exe_sha256": exeSHA,
                                 "os": ProcessInfo.processInfo.operatingSystemVersionString,
                                 "entry": args[1], "status": "started"]
    try saveReceipt(receipt, at: resultPath)

    let mountedURL = mount.resolvingSymlinksInPath()
    let mountedApp = mountedURL.appendingPathComponent(bundleName)
    var device: String?
    defer {
        if let device, let entries = try? attachedEntries(for: image), !entries.isEmpty {
            _ = try? command("/usr/bin/hdiutil", ["detach", device])
        }
    }

    do {
        let attachment = try plist(command("/usr/bin/hdiutil", [
            "attach", "-readonly", "-nobrowse", "-noautoopen", "-mountpoint", mountedURL.path, "-plist", image.path,
        ]))
        let entities = attachment["system-entities"] as? [[String: Any]] ?? []
        device = entities.first { $0["mount-point"] as? String == mountedURL.path }?["dev-entry"] as? String
        guard device != nil else { throw Failure("could not bind the candidate mount") }
        try verifyBundle(mountedApp, expectedSHA: exeSHA)

        var launched: [Int32: String] = [:]
        let center = NSWorkspace.shared.notificationCenter
        let observer = center.addObserver(forName: NSWorkspace.didLaunchApplicationNotification, object: nil, queue: .main) { event in
            guard let app = event.userInfo?[NSWorkspace.applicationUserInfoKey] as? NSRunningApplication,
                  app.bundleIdentifier == bundleID,
                  let path = app.bundleURL?.resolvingSymlinksInPath().path else { return }
            launched[app.processIdentifier] = path
        }
        defer { center.removeObserver(observer) }

        if args[1] == "--finder-open" {
            let finder = Process()
            finder.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
            finder.arguments = ["-e", """
                on run argv
                    tell application "Finder" to open ((POSIX file (item 1 of argv)) as alias)
                end run
                """, mountedApp.path]
            try finder.run()
        } else {
            NSWorkspace.shared.activateFileViewerSelecting([mountedApp])
            print("READY double-click the selected MiMac app in Finder")
            fflush(stdout)
        }

        let destinationPaths = Set(destinations.map(\.path))
        let deadline = Date(timeIntervalSinceNow: 90)
        var installed: NSRunningApplication?
        var detached = false
        var nextMountCheck = Date.distantPast
        while Date() < deadline {
            for app in appearedBundles() {
                if let path = app.bundleURL?.resolvingSymlinksInPath().path {
                    launched[app.processIdentifier] = path
                    if destinationPaths.contains(path) { installed = app }
                }
            }
            if let installed, !installed.isTerminated, Date() >= nextMountCheck {
                detached = try attachedEntries(for: image).isEmpty
                nextMountCheck = Date(timeIntervalSinceNow: 0.25)
                if detached && hasInitialWindow(installed.processIdentifier) { break }
            }
            tick()
        }
        receipt["launches"] = launched.map { ["pid": $0.key, "path": $0.value] }.sorted { ($0["pid"] as? Int32 ?? 0) < ($1["pid"] as? Int32 ?? 0) }
        guard let installed, let target = installed.bundleURL?.resolvingSymlinksInPath(),
              launched.values.contains(mountedApp.path), detached, !installed.isTerminated,
              hasInitialWindow(installed.processIdentifier) else {
            throw Failure("missing the installer, initial app window, or automatic eject")
        }
        try verifyBundle(target, expectedSHA: exeSHA)
        let traces = logNames(in: logDirectory).subtracting(oldLogs)
        guard traces.count == 1, let trace = traces.first else {
            throw Failure("missing or ambiguous native eject receipt")
        }
        let detail = try String(contentsOf: logDirectory.appendingPathComponent(trace), encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        receipt["eject_log"] = detail
        guard detail == helperName else { throw Failure("first eject attempt was not confirmed") }
        receipt["installed_path"] = target.path
        receipt["installed_pid"] = installed.processIdentifier
        receipt["initial_window_visible"] = true
        receipt["status"] = "verified"
        try saveReceipt(receipt, at: resultPath)

        guard installed.terminate() else { throw Failure("the exact newly installed instance did not accept quit") }
        let exitDeadline = Date(timeIntervalSinceNow: 8)
        while !installed.isTerminated && Date() < exitDeadline { tick() }
        guard installed.isTerminated else { throw Failure("the verified instance did not exit") }
        receipt["status"] = "passed"
        receipt["retired"] = true
        try saveReceipt(receipt, at: resultPath)
        print("PASSED \(resultPath.path)")
    } catch {
        receipt["status"] = "blocked"
        receipt["error"] = String(describing: error)
        try saveReceipt(receipt, at: resultPath)
        throw error
    }
}

do {
    try run()
} catch {
    FileHandle.standardError.write(Data("BLOCKED \(error)\n".utf8))
    exit(1)
}
