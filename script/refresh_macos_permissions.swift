#!/usr/bin/env swift
import AppKit
import ApplicationServices
import CoreGraphics
import CryptoKit
import Foundation
import Darwin

private let expectedBundleID = "dev.mimac.app"
private let settingsBundleID = "com.apple.systempreferences"
private let titleIdentifier = "MiMac_Title"
private let toggleIdentifier = "MiMac_Toggle"
private let goToIdentifier = "GoToWindow"
private let pathFieldIdentifier = "PathTextField"
private let openWindowTitles: Set<String> = ["Open", "打开"]
private let pollInterval: TimeInterval = 0.10
private let pageTimeout: TimeInterval = 18
private let uiTimeout: TimeInterval = 12
private let toggleTimeout: TimeInterval = 18

private enum Permission: String, Codable, CaseIterable {
    case inputMonitoring = "input-monitoring"
    case accessibility = "accessibility"

    var pageTitles: Set<String> {
        switch self {
        case .inputMonitoring:
            return ["Input Monitoring", "输入监控"]
        case .accessibility:
            return ["Accessibility", "辅助功能", "Device Control and Data Access", "设备控制与数据访问", "设备控制和数据访问"]
        }
    }

    var systemSettingsRoute: String {
        switch self {
        case .inputMonitoring:
            return "x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent"
        case .accessibility:
            return "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
        }
    }
}

private enum Mode: String, Equatable {
    case apply
    case inspect
}

private struct Options {
    var bundlePath: String?
    var permission: String?
    var mode: Mode?
    var resumeJournal: String?
}

private struct Journal: Codable {
    var schemaVersion: Int = 1
    var runID: String
    var bundlePath: String
    var executableSHA256: String
    var permissionArgument: String
    var mode: String
    var status: String = "in_progress"
    var stage: String = "created"
    var activePermission: String?
    var completedPermissions: [String] = []
    var attemptedActions: [String] = []
    var observations: [String: String] = [:]
    var note: String = ""
    var startedAt: String
    var updatedAt: String
}

private struct LogLine: Codable {
    var time: String
    var level: String
    var stage: String
    var event: String
    var detail: String
}

private struct ToolFailure: Error {
    var status: String
    var stage: String
    var message: String
}

private var journal: Journal!
private var journalURL: URL!
private var reportURL: URL!
private var logURL: URL!
private var targetBundlePath = ""
private var targetExecutableSHA256 = ""

private func timestamp() -> String {
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return formatter.string(from: Date())
}

private func usage() -> String {
    return """
    Usage:
      script/refresh_macos_permissions.swift --bundle /absolute/path/MiMac.app \\
        --permission input-monitoring|accessibility|both --apply|--inspect [--resume /path/to/journal.json]

    --inspect reads the currently open, recognized System Settings page without changing it.
    --apply may open a known privacy page, then proceeds only after its exact page title is verified.
    Pending runs write a recoverable journal and stage log under ~/Library/Caches/MiMac/permissions-refresh.
    """
}

private func parseArguments() throws -> Options {
    var result = Options()
    var index = 1
    let args = CommandLine.arguments
    while index < args.count {
        let arg = args[index]
        switch arg {
        case "--bundle":
            index += 1
            guard index < args.count, result.bundlePath == nil else { throw ToolFailure(status: "refused", stage: "arguments", message: "--bundle requires one absolute path") }
            result.bundlePath = args[index]
        case "--permission":
            index += 1
            guard index < args.count, result.permission == nil else { throw ToolFailure(status: "refused", stage: "arguments", message: "--permission requires one value") }
            result.permission = args[index]
        case "--apply", "--inspect":
            guard result.mode == nil else { throw ToolFailure(status: "refused", stage: "arguments", message: "Choose exactly one of --apply or --inspect") }
            result.mode = arg == "--apply" ? .apply : .inspect
        case "--resume":
            index += 1
            guard index < args.count, result.resumeJournal == nil else { throw ToolFailure(status: "refused", stage: "arguments", message: "--resume requires one journal path") }
            result.resumeJournal = args[index]
        case "--help", "-h":
            print(usage())
            exit(0)
        default:
            throw ToolFailure(status: "refused", stage: "arguments", message: "Unknown argument: \(arg)")
        }
        index += 1
    }
    guard result.bundlePath != nil else { throw ToolFailure(status: "refused", stage: "arguments", message: "--bundle is required") }
    guard let permission = result.permission,
          [Permission.inputMonitoring.rawValue, Permission.accessibility.rawValue, "both"].contains(permission) else {
        throw ToolFailure(status: "refused", stage: "arguments", message: "--permission must be input-monitoring, accessibility, or both")
    }
    guard result.mode != nil else { throw ToolFailure(status: "refused", stage: "arguments", message: "Choose --apply or --inspect") }
    if result.resumeJournal != nil && result.mode != .apply {
        throw ToolFailure(status: "refused", stage: "arguments", message: "--resume is valid only with --apply")
    }
    return result
}

private func cachesRoot() throws -> URL {
    guard let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first else {
        throw ToolFailure(status: "error", stage: "journal", message: "Could not locate the user Caches directory")
    }
    let root = caches.appendingPathComponent("MiMac/permissions-refresh", isDirectory: true)
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
    return root.standardizedFileURL
}

private func writeJSON<T: Encodable>(_ value: T, to url: URL) throws {
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
    let data = try encoder.encode(value)
    try data.write(to: url, options: .atomic)
    try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
}

private func writeReport() {
    guard journal != nil, reportURL != nil else { return }
    try? writeJSON(journal!, to: reportURL)
}

private func appendLog(_ level: String, event: String, detail: String) {
    guard journal != nil, logURL != nil else { return }
    let item = LogLine(time: timestamp(), level: level, stage: journal.stage, event: event, detail: detail)
    guard let data = try? JSONEncoder().encode(item),
          let handle = try? FileHandle(forWritingTo: logURL) else { return }
    defer { try? handle.close() }
    do {
        try handle.seekToEnd()
        try handle.write(contentsOf: data + Data([0x0a]))
    } catch { }
}

private func checkpoint(_ stage: String, status: String = "in_progress", note: String? = nil, permission: Permission? = nil) {
    journal.stage = stage
    journal.status = status
    journal.updatedAt = timestamp()
    if let note { journal.note = note }
    if let permission { journal.activePermission = permission.rawValue }
    try? writeJSON(journal!, to: journalURL)
    writeReport()
    appendLog(status == "pending" || status == "refused" || status == "error" ? status : "info", event: "checkpoint", detail: note ?? stage)
}

private func pending(_ stage: String, _ message: String, permission: Permission? = nil) -> ToolFailure {
    checkpoint(stage, status: "pending", note: message, permission: permission)
    return ToolFailure(status: "pending", stage: stage, message: message)
}

private func refused(_ stage: String, _ message: String, permission: Permission? = nil) -> ToolFailure {
    checkpoint(stage, status: "refused", note: message, permission: permission)
    return ToolFailure(status: "refused", stage: stage, message: message)
}

private func validateBundle(_ path: String) throws -> String {
    guard path.hasPrefix("/") else { throw ToolFailure(status: "refused", stage: "bundle_validation", message: "--bundle must be an absolute path") }
    let inputURL = URL(fileURLWithPath: path).standardizedFileURL
    let canonicalURL = inputURL.resolvingSymlinksInPath().standardizedFileURL
    guard inputURL.path == canonicalURL.path else {
        throw ToolFailure(status: "refused", stage: "bundle_validation", message: "Pass the canonical bundle path directly; symbolic-link paths are not accepted")
    }
    guard inputURL.pathExtension == "app" else { throw ToolFailure(status: "refused", stage: "bundle_validation", message: "The target must be a .app bundle") }
    var isDirectory: ObjCBool = false
    guard FileManager.default.fileExists(atPath: inputURL.path, isDirectory: &isDirectory), isDirectory.boolValue else {
        throw ToolFailure(status: "refused", stage: "bundle_validation", message: "The target app bundle does not exist as a directory")
    }
    let infoURL = inputURL.appendingPathComponent("Contents/Info.plist")
    guard let infoData = try? Data(contentsOf: infoURL),
          let rawInfo = try? PropertyListSerialization.propertyList(from: infoData, options: [], format: nil),
          let info = rawInfo as? [String: Any] else {
        throw ToolFailure(status: "refused", stage: "bundle_validation", message: "Could not read the target app Info.plist")
    }
    guard (info["CFBundleIdentifier"] as? String) == expectedBundleID else {
        throw ToolFailure(status: "refused", stage: "bundle_validation", message: "Only CFBundleIdentifier dev.mimac.app can be managed")
    }
    guard let executableName = info["CFBundleExecutable"] as? String, !executableName.isEmpty else {
        throw ToolFailure(status: "refused", stage: "bundle_validation", message: "CFBundleExecutable is missing")
    }
    let executableURL = inputURL.appendingPathComponent("Contents/MacOS", isDirectory: true).appendingPathComponent(executableName)
    guard FileManager.default.isExecutableFile(atPath: executableURL.path) else {
        throw ToolFailure(status: "refused", stage: "bundle_validation", message: "The declared app executable is missing or not executable")
    }
    guard executableURL.resolvingSymlinksInPath().standardizedFileURL.path == executableURL.standardizedFileURL.path,
          !executableName.contains("/"), executableName != ".", executableName != ".." else {
        throw ToolFailure(status: "refused", stage: "bundle_validation", message: "The declared executable must be a direct file inside Contents/MacOS")
    }
    targetExecutableSHA256 = SHA256.hash(data: try Data(contentsOf: executableURL)).map { String(format: "%02x", $0) }.joined()
    return inputURL.path
}

private func runningMiMacDescriptions() -> [String] {
    return NSWorkspace.shared.runningApplications
        .filter { $0.bundleIdentifier == expectedBundleID }
        .map { app in
            let path = app.bundleURL?.standardizedFileURL.path ?? "(bundle path unavailable)"
            return "pid=\(app.processIdentifier), path=\(path)"
        }
}

private func axAttribute(_ element: AXUIElement, _ name: String) -> CFTypeRef? {
    var value: CFTypeRef?
    guard AXUIElementCopyAttributeValue(element, name as CFString, &value) == .success else { return nil }
    return value
}

private func axString(_ element: AXUIElement, _ name: String) -> String? {
    return axAttribute(element, name) as? String
}

private func axElement(_ element: AXUIElement, _ name: String) -> AXUIElement? {
    guard let value = axAttribute(element, name), CFGetTypeID(value) == AXUIElementGetTypeID() else { return nil }
    return (value as! AXUIElement)
}

private func axNumber(_ element: AXUIElement, _ name: String) -> NSNumber? {
    return axAttribute(element, name) as? NSNumber
}

private func axChildren(_ element: AXUIElement) -> [AXUIElement] {
    var result: [AXUIElement] = []
    for name in ["AXChildren", "AXSheets"] {
        if let children = axAttribute(element, name) as? [AXUIElement] {
            for child in children where !result.contains(where: { CFEqual($0, child) }) {
                result.append(child)
            }
        }
    }
    return result
}

private func findElements(roots: [AXUIElement], identifier: String, stopAfter limit: Int = 2, maxNodes: Int = 4000) -> [AXUIElement] {
    var queue: [(AXUIElement, Int)] = roots.map { ($0, 0) }
    var cursor = 0
    var visited = 0
    var matches: [AXUIElement] = []
    while cursor < queue.count && visited < maxNodes && matches.count < limit {
        let (element, depth) = queue[cursor]
        cursor += 1
        visited += 1
        if axString(element, "AXIdentifier") == identifier { matches.append(element) }
        guard depth < 24, axString(element, "AXRole") != "AXMenuBar" else { continue }
        for child in axChildren(element) { queue.append((child, depth + 1)) }
    }
    return matches
}

private func findButtons(roots: [AXUIElement], labels: Set<String>, identifiers: Set<String> = [], stopAfter limit: Int = 2) -> [AXUIElement] {
    var queue: [(AXUIElement, Int)] = roots.map { ($0, 0) }
    var cursor = 0
    var visited = 0
    var matches: [AXUIElement] = []
    while cursor < queue.count && visited < 4000 && matches.count < limit {
        let (element, depth) = queue[cursor]
        cursor += 1
        visited += 1
        if axString(element, "AXRole") == "AXButton" {
            let values = ["AXTitle", "AXDescription", "AXHelp", "AXIdentifier", "AXValue"].compactMap { axString(element, $0) }
            if values.contains(where: { labels.contains($0) || identifiers.contains($0) }) { matches.append(element) }
        }
        guard depth < 24, axString(element, "AXRole") != "AXMenuBar" else { continue }
        for child in axChildren(element) { queue.append((child, depth + 1)) }
    }
    return matches
}

private func appWindows(_ application: AXUIElement) -> [AXUIElement] {
    return axAttribute(application, "AXWindows") as? [AXUIElement] ?? []
}

private func exactPageWindows(_ windows: [AXUIElement]) -> [(AXUIElement, Permission, String)] {
    var found: [(AXUIElement, Permission, String)] = []
    for window in windows {
        guard let title = axString(window, "AXTitle") else { continue }
        for permission in Permission.allCases where permission.pageTitles.contains(title) {
            found.append((window, permission, title))
        }
    }
    return found
}

private func pageWindow(_ permission: Permission, windows: [AXUIElement]) throws -> (AXUIElement, String) {
    let matches = exactPageWindows(windows).filter { $0.1 == permission }
    guard matches.count == 1, let match = matches.first else {
        if matches.count > 1 { throw refused("page_check", "More than one \(permission.rawValue) page window is open", permission: permission) }
        throw pending("page_check", "The exact \(permission.rawValue) page title is not visible yet", permission: permission)
    }
    return (match.0, match.2)
}

private func ensureSystemSettings(mode: Mode) throws -> (NSRunningApplication, AXUIElement) {
    let candidates = NSWorkspace.shared.runningApplications.filter { $0.bundleIdentifier == settingsBundleID }
    guard candidates.count == 1, let app = candidates.first else {
        if candidates.isEmpty { throw pending("settings_discovery", "System Settings is not running; open the intended privacy page and rerun") }
        throw refused("settings_discovery", "More than one System Settings process is visible; refusing ambiguous UI")
    }
    let element = AXUIElementCreateApplication(app.processIdentifier)
    let timeout = AXUIElementSetMessagingTimeout(element, 2.0)
    guard timeout == .success else {
        throw pending("ax_timeout", "Could not set a bounded Accessibility messaging timeout for System Settings")
    }
    if !AXIsProcessTrusted() {
        throw pending("ax_trust", "The helper's current runner lacks Accessibility trust; no trust prompt was opened")
    }
    if mode == .apply {
        let running = runningMiMacDescriptions()
        if !running.isEmpty {
            throw refused("mimac_running", "MiMac must be stopped before a permission refresh; still running: \(running.joined(separator: "; "))")
        }
    }
    return (app, element)
}

private func frontmostIsSettings(_ app: NSRunningApplication) -> Bool {
    return NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier
}

private func activateSettings(_ app: NSRunningApplication, permission: Permission) throws {
    _ = app.activate(options: [.activateAllWindows])
    let ready = waitUntil(uiTimeout) { frontmostIsSettings(app) }
    guard ready else { throw pending("settings_foreground", "System Settings did not become the foreground app", permission: permission) }
    checkpoint("\(permission.rawValue).settings_foreground", permission: permission)
}

private func waitUntil(_ timeout: TimeInterval, _ condition: () -> Bool) -> Bool {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if condition() { return true }
        let remaining = deadline.timeIntervalSinceNow
        if remaining <= 0 { break }
        _ = RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: min(pollInterval, remaining)))
    }
    return condition()
}

private func hasAuthenticationPrompt(_ appElement: AXUIElement) -> Bool {
    let authTerms: Set<String> = ["password", "authenticate", "authentication", "touch id", "unlock", "administrator", "密码", "验证", "认证", "解锁", "管理员"]
    let windows = appWindows(appElement)
    for window in windows {
        let title = (axString(window, "AXTitle") ?? "").lowercased()
        if authTerms.contains(where: { title.contains($0) }) { return true }
        let sheets = axAttribute(window, "AXSheets") as? [AXUIElement] ?? []
        var queue = sheets
        var cursor = 0
        var visited = 0
        while cursor < queue.count && visited < 500 {
            let element = queue[cursor]
            cursor += 1
            visited += 1
            let elementRole = axString(element, "AXRole") ?? ""
            let labels = ["AXTitle", "AXDescription", "AXHelp"].compactMap { axString(element, $0) }
            if authTerms.contains(where: { term in labels.contains(where: { $0.lowercased().contains(term) }) }) { return true }
            if elementRole == "AXStaticText", let value = axString(element, "AXValue") {
                // Static text only. Never read values from text fields or secure controls.
                if authTerms.contains(where: { value.lowercased().contains($0) }) { return true }
            }
            if elementRole == "AXButton" {
                let buttonLabels = labels
                if buttonLabels.contains(where: { ["Allow", "允许"].contains($0) }) { return true }
            }
            queue.append(contentsOf: axChildren(element))
        }
    }
    let securityAgents = NSWorkspace.shared.runningApplications.filter { $0.bundleIdentifier == "com.apple.SecurityAgent" }
    return securityAgents.contains { !appWindows(AXUIElementCreateApplication($0.processIdentifier)).isEmpty }
}

private func ensureNoAuthenticationPrompt(_ appElement: AXUIElement, permission: Permission) throws {
    if hasAuthenticationPrompt(appElement) {
        throw pending("\(permission.rawValue).authentication", "A system authentication or Allow prompt is present; complete it yourself, then rerun. The helper did not inspect credential fields or press Allow", permission: permission)
    }
}

private func rowAndToggle(in page: AXUIElement) throws -> (AXUIElement?, AXUIElement?, NSNumber?) {
    let titles = findElements(roots: [page], identifier: titleIdentifier, stopAfter: 3)
    if titles.count > 1 { throw refused("entry_check", "Multiple MiMac_Title entries are present; refusing to choose one") }
    guard let title = titles.first else { return (nil, nil, nil) }
    guard axString(title, "AXValue") == "MiMac" else { throw refused("entry_check", "MiMac_Title is not the expected MiMac row label") }
    var row: AXUIElement? = title
    for _ in 0..<12 {
        if let current = row, axString(current, "AXRole") == "AXRow" { break }
        guard let current = row, let parent = axElement(current, "AXParent") else { row = nil; break }
        row = parent
    }
    guard let matchedRow = row, axString(matchedRow, "AXRole") == "AXRow" else {
        throw refused("entry_check", "MiMac_Title has no AXRow parent")
    }
    let toggles = findElements(roots: [page], identifier: toggleIdentifier, stopAfter: 3)
    guard toggles.count == 1, let toggle = toggles.first,
          axString(toggle, "AXRole") == "AXCheckBox",
          axString(toggle, "AXSubrole") == "AXSwitch" else {
        throw refused("entry_check", "Expected one MiMac_Toggle AXCheckBox/AXSwitch")
    }
    var toggleRow: AXUIElement? = toggle
    for _ in 0..<12 {
        if let current = toggleRow, axString(current, "AXRole") == "AXRow" { break }
        guard let current = toggleRow, let parent = axElement(current, "AXParent") else { toggleRow = nil; break }
        toggleRow = parent
    }
    guard let toggleRow, CFEqual(toggleRow, matchedRow) else {
        throw refused("entry_check", "MiMac_Title and MiMac_Toggle do not belong to the same AXRow")
    }
    return (matchedRow, toggle, axNumber(toggle, "AXValue"))
}

private func selectMiMacRow(_ row: AXUIElement, permission: Permission) throws {
    if axNumber(row, "AXSelected")?.boolValue != true {
        let result = AXUIElementSetAttributeValue(row, "AXSelected" as CFString, kCFBooleanTrue)
        if result != .success && actionNames(row).contains("AXSelect") {
            _ = AXUIElementPerformAction(row, "AXSelect" as CFString)
        }
    }
    guard axNumber(row, "AXSelected")?.boolValue == true else {
        throw pending(stageKey(permission, "entry_selection"), "MiMac row could not be selected and read back", permission: permission)
    }
    checkpoint(stageKey(permission, "entry_selected"), permission: permission)
}

private func pressUniqueButton(_ roots: [AXUIElement], labels: Set<String>, identifiers: Set<String> = [], stage: String, permission: Permission, journalAction: String? = nil) throws {
    let buttons = findButtons(roots: roots, labels: labels, identifiers: identifiers)
    guard buttons.count == 1, let button = buttons.first else {
        if buttons.count > 1 { throw refused(stage, "Multiple matching buttons are visible; refusing to choose", permission: permission) }
        throw pending(stage, "The expected button is not visible yet", permission: permission)
    }
    guard axNumber(button, "AXEnabled")?.boolValue == true else {
        throw pending(stage, "The expected button is currently disabled", permission: permission)
    }
    if let journalAction, !markAction(permission, journalAction) {
        throw pending(stage, "This button action was already attempted in the current journal", permission: permission)
    }
    let result = AXUIElementPerformAction(button, "AXPress" as CFString)
    guard result == .success else { throw pending(stage, "The expected button action was not accepted", permission: permission) }
    checkpoint(stage + ".requested", permission: permission)
}

private func findOpenWindows(_ appElement: AXUIElement) -> [AXUIElement] {
    return appWindows(appElement).filter {
        axString($0, "AXRole") == "AXWindow" && openWindowTitles.contains(axString($0, "AXTitle") ?? "")
    }
}

// A page transition must never silently discard an unclassified modal.
// Only the exact privacy page, one known Open panel, and its Go To sheet
// may participate in a refresh. An ordinary Settings window is allowed
// only before navigating to a known privacy URL.
private func ensureKnownSurfaces(_ appElement: AXUIElement, allowOrdinaryPage: Bool) throws {
    let windows = appWindows(appElement)
    let opens = findOpenWindows(appElement)
    guard opens.count <= 1 else { throw refused("surface_check", "More than one Open panel is visible") }
    let pages = exactPageWindows(windows)
    guard pages.count <= 1 else { throw refused("surface_check", "More than one privacy page is visible") }
    var ordinary: [AXUIElement] = []
    var sheets: [AXUIElement] = []
    // AXSheets is the window's modal relationship. Do not walk the entire
    // Settings content tree (which may contain thousands of unrelated rows).
    // Some releases expose the focused sheet as an app window or a direct
    // child instead, so include those shallow relationships as well.
    for window in windows {
        var candidates = axAttribute(window, "AXSheets") as? [AXUIElement] ?? []
        if axString(window, "AXRole") == "AXSheet" { candidates.append(window) }
        candidates.append(contentsOf: (axAttribute(window, "AXChildren") as? [AXUIElement] ?? []).filter {
            axString($0, "AXRole") == "AXSheet"
        })
        for sheet in candidates where !sheets.contains(where: { CFEqual($0, sheet) }) { sheets.append(sheet) }
    }
    for sheet in sheets {
        guard axString(sheet, "AXIdentifier") == goToIdentifier, opens.count == 1,
              goToWindows(appElement, openWindow: opens[0]).contains(where: { CFEqual($0, sheet) }) else {
            throw pending("surface_check", "An unclassified Settings sheet is open; no refresh action was sent")
        }
    }
    for window in windows {
        if pages.contains(where: { CFEqual($0.0, window) }) || opens.contains(where: { CFEqual($0, window) }) { continue }
        if sheets.contains(where: { CFEqual($0, window) }) { continue }
        guard allowOrdinaryPage, pages.isEmpty, opens.isEmpty, sheets.isEmpty,
              axString(window, "AXRole") == "AXWindow",
              axString(window, "AXSubrole") == "AXStandardWindow",
              axNumber(window, "AXModal")?.boolValue != true else {
            throw pending("surface_check", "An unclassified Settings window is open; no refresh action was sent")
        }
        ordinary.append(window)
    }
    if pages.isEmpty {
        guard allowOrdinaryPage, ordinary.count == 1 else {
            throw pending("surface_check", "Expected one ordinary Settings window before privacy navigation")
        }
    }
}

private func goToWindows(_ appElement: AXUIElement, openWindow: AXUIElement) -> [AXUIElement] {
    var matches = findElements(roots: [openWindow], identifier: goToIdentifier, stopAfter: 3)
    for window in appWindows(appElement) where !CFEqual(window, openWindow) {
        for match in findElements(roots: [window], identifier: goToIdentifier, stopAfter: 3) {
            if !matches.contains(where: { CFEqual($0, match) }) { matches.append(match) }
        }
    }
    return matches
}

private func openPanelIsKeyWindow(_ appElement: AXUIElement, _ openWindow: AXUIElement) -> Bool {
    if axNumber(openWindow, "AXMain")?.boolValue == true { return true }
    if let focused = axElement(appElement, "AXFocusedWindow"), CFEqual(focused, openWindow) { return true }
    // macOS exposes the Go To Folder sheet itself as AXFocusedWindow while
    // its owning Open panel is neither AXMain nor focused. Accept only this
    // exact sheet belonging to our unique picker, not another settings window.
    if let focused = axElement(appElement, "AXFocusedWindow"),
       axString(focused, "AXIdentifier") == goToIdentifier,
       goToWindows(appElement, openWindow: openWindow).contains(where: { CFEqual($0, focused) }) {
        return true
    }
    return false
}

private func raiseOpenPanel(_ permission: Permission, app: NSRunningApplication, appElement: AXUIElement, openWindow: AXUIElement) throws {
    if !frontmostIsSettings(app) { try activateSettings(app, permission: permission) }
    guard actionNames(openWindow).contains("AXRaise") else {
        throw pending(stageKey(permission, "open_panel_raise"), "The Open panel does not expose AXRaise; refusing to confirm in an uncertain window", permission: permission)
    }
    let result = AXUIElementPerformAction(openWindow, "AXRaise" as CFString)
    guard result == .success else { throw pending(stageKey(permission, "open_panel_raise"), "AXRaise was not accepted for the Open panel", permission: permission) }
    let ready = waitUntil(uiTimeout) { frontmostIsSettings(app) && openPanelIsKeyWindow(appElement, openWindow) }
    guard ready else { throw pending(stageKey(permission, "open_panel_key"), "System Settings is not foreground with the Open panel as its key window", permission: permission) }
    checkpoint(stageKey(permission, "open_panel_raised"), permission: permission)
}

private func postCommandShiftG() throws {
    let source = CGEventSource(stateID: .hidSystemState)
    let sequence: [(CGKeyCode, CGEventFlags)] = [
        (0x37, .maskCommand),
        (0x38, [.maskCommand, .maskShift]),
        (0x05, [.maskCommand, .maskShift]),
        (0x05, [.maskCommand, .maskShift]),
        (0x38, .maskCommand),
        (0x37, [])
    ]
    for (index, (key, flags)) in sequence.enumerated() {
        let isDown = index < 3
        guard let event = CGEvent(keyboardEventSource: source, virtualKey: key, keyDown: isDown) else {
            throw pending("file_picker.shortcut", "Could not create the one Cmd-Shift-G keyboard sequence")
        }
        event.flags = flags
        event.post(tap: .cghidEventTap)
    }
}

private func postOneReturn() throws {
    guard CGPreflightPostEventAccess() else {
        throw pending("file_picker.path_confirm", "The current runner cannot post the required keyboard event")
    }
    let source = CGEventSource(stateID: .privateState)
    guard let down = CGEvent(keyboardEventSource: source, virtualKey: 0x24, keyDown: true),
          let up = CGEvent(keyboardEventSource: source, virtualKey: 0x24, keyDown: false) else {
        throw pending("file_picker.path_confirm", "Could not create one Return key event")
    }
    down.flags = []
    up.flags = []
    down.post(tap: .cghidEventTap)
    up.post(tap: .cghidEventTap)
}

private func actionNames(_ element: AXUIElement) -> [String] {
    var names: CFArray?
    guard AXUIElementCopyActionNames(element, &names) == .success else { return [] }
    return names as? [String] ?? []
}

private func prepareExistingPicker(_ permission: Permission, app: NSRunningApplication, appElement: AXUIElement, openWindow: AXUIElement) throws {
    try raiseOpenPanel(permission, app: app, appElement: appElement, openWindow: openWindow)
    try ensureNoAuthenticationPrompt(appElement, permission: permission)
    var goToMatches = goToWindows(appElement, openWindow: openWindow)
    if goToMatches.count > 1 {
        throw refused(stageKey(permission, "goto_lookup"), "More than one GoToWindow is visible", permission: permission)
    }
    if goToMatches.isEmpty {
        let knownConfirmed = journal.activePermission == permission.rawValue &&
            ["goto_dismissed", "awaiting_toggle", "open_requested", "open_dismissed"].contains { journal.stage == stageKey(permission, $0) }
        let hasVerifiedAttempt = journal.activePermission == permission.rawValue &&
            (journal.attemptedActions.contains(stageKey(permission, "path_confirm")) ||
             journal.attemptedActions.contains(stageKey(permission, "goto_submit"))) &&
            journal.observations[permission.rawValue + ".path_readback"] == "canonical_path_match"
        if !knownConfirmed && !hasVerifiedAttempt {
            if journal.attemptedActions.contains(stageKey(permission, "path_confirm")) {
                throw pending(stageKey(permission, "goto_state_uncertain"), "GoToWindow disappeared before its dismissal was journaled; refusing to reopen or confirm an uncertain picker", permission: permission)
            }
            checkpoint(stageKey(permission, "file_picker_open"), permission: permission)
            try postCommandShiftG()
            let appeared = waitUntil(uiTimeout) { goToWindows(appElement, openWindow: openWindow).count == 1 }
            guard appeared else { throw pending(stageKey(permission, "goto_wait"), "GoToWindow did not appear after one Cmd-Shift-G sequence", permission: permission) }
            goToMatches = goToWindows(appElement, openWindow: openWindow)
        }
    }
    if let goTo = goToMatches.first {
        let fields = findElements(roots: [goTo], identifier: pathFieldIdentifier, stopAfter: 2)
        guard fields.count == 1, let field = fields.first,
              axString(field, "AXRole") == "AXTextField" else {
            throw refused(stageKey(permission, "path_field"), "Expected one PathTextField AXTextField inside GoToWindow", permission: permission)
        }
        if axString(field, "AXValue") != targetBundlePath {
            let setResult = AXUIElementSetAttributeValue(field, "AXValue" as CFString, targetBundlePath as CFString)
            guard setResult == .success, axString(field, "AXValue") == targetBundlePath else {
                throw pending(stageKey(permission, "path_readback"), "PathTextField did not accept and read back the canonical bundle path", permission: permission)
            }
            journal.observations[permission.rawValue + ".path_readback"] = "canonical_path_match"
            checkpoint(stageKey(permission, "path_set_readback"), permission: permission)
        } else {
            journal.observations[permission.rawValue + ".path_readback"] = "canonical_path_match"
            checkpoint(stageKey(permission, "path_already_matches"), permission: permission)
        }
        if axNumber(field, "AXFocused")?.boolValue != true {
            let focusResult = AXUIElementSetAttributeValue(field, "AXFocused" as CFString, kCFBooleanTrue)
            guard focusResult == .success, axNumber(field, "AXFocused")?.boolValue == true else {
                throw pending(stageKey(permission, "path_focus"), "PathTextField did not become focused and read back", permission: permission)
            }
        }
        guard frontmostIsSettings(app) else {
            throw pending(stageKey(permission, "confirm_foreground"), "System Settings lost foreground before confirmation", permission: permission)
        }
        let suggestionReady = waitUntil(uiTimeout) {
            findElements(roots: [goToMatches[0]], identifier: targetBundlePath, stopAfter: 2).count == 1
        }
        guard suggestionReady else {
            throw pending(stageKey(permission, "path_resolution"), "The canonical path suggestion did not resolve uniquely; no key was sent", permission: permission)
        }
        guard frontmostIsSettings(app), openPanelIsKeyWindow(appElement, openWindow),
              axString(field, "AXValue") == targetBundlePath else {
            throw pending(stageKey(permission, "confirm_foreground"), "The verified picker or canonical path changed before submission", permission: permission)
        }
        let actionKey = stageKey(permission, "goto_submit")
        if journal.attemptedActions.contains(actionKey) {
            throw pending(stageKey(permission, "path_confirm"), "A prior GoTo submission is journaled and the sheet is still open; no duplicate Return was sent", permission: permission)
        }
        journal.attemptedActions.append(actionKey)
        checkpoint(stageKey(permission, "path_confirm_requested"), permission: permission)
        // PathTextField AXConfirm can commit completion without navigating.
        // Submit the modal's resolved canonical suggestion with one plain
        // Return, rather than treating that field-action receipt as success.
        try postOneReturn()
        appendLog("info", event: "single_return", detail: "Submitted the exact resolved path in the focused GoToWindow")
        let dismissed = waitUntil(uiTimeout) { goToWindows(appElement, openWindow: openWindow).isEmpty }
        guard dismissed else { throw pending(stageKey(permission, "goto_dismiss"), "GoToWindow remains after the single AXConfirm/Return attempt; no second key or click was sent", permission: permission) }
        checkpoint(stageKey(permission, "goto_dismissed"), permission: permission)
    }
    let enabled = waitUntil(uiTimeout) {
        let buttons = findButtons(roots: [openWindow], labels: ["Open", "打开"], identifiers: ["OKButton"])
        return buttons.count == 1 && axNumber(buttons[0], "AXEnabled")?.boolValue == true
    }
    guard enabled else { throw pending("\(permission.rawValue).open_enabled", "The file panel's exact Open button did not become enabled", permission: permission) }
    let openButtons = findButtons(roots: [openWindow], labels: ["Open", "打开"], identifiers: ["OKButton"])
    guard openButtons.count == 1, let openButton = openButtons.first else {
        throw refused("\(permission.rawValue).open_button", "The file panel Open button is ambiguous", permission: permission)
    }
    guard axNumber(openButton, "AXEnabled")?.boolValue == true else {
        throw pending("\(permission.rawValue).open_enabled", "The file panel Open button is not enabled", permission: permission)
    }
    guard markAction(permission, "open") else {
        throw pending(stageKey(permission, "open_pending"), "Open was already attempted in this journal and the file panel remains; inspect it before retrying", permission: permission)
    }
    checkpoint(stageKey(permission, "awaiting_toggle"), permission: permission)
    let openResult = AXUIElementPerformAction(openButton, "AXPress" as CFString)
    guard openResult == .success else { throw pending("\(permission.rawValue).open_press", "The file panel Open action was not accepted", permission: permission) }
    let panelClosed = waitUntil(uiTimeout) { findOpenWindows(appElement).isEmpty }
    guard panelClosed else { throw pending("\(permission.rawValue).open_dismiss", "The file panel remains open after selecting the canonical bundle", permission: permission) }
    checkpoint("\(permission.rawValue).open_dismissed", permission: permission)
}

private func stageKey(_ permission: Permission, _ action: String) -> String { permission.rawValue + "." + action }

private func markAction(_ permission: Permission, _ action: String) -> Bool {
    let key = stageKey(permission, action)
    guard !journal.attemptedActions.contains(key) else { return false }
    journal.attemptedActions.append(key)
    checkpoint(stageKey(permission, action + "_requested"), permission: permission)
    return true
}

private func currentPagePermission(_ appElement: AXUIElement) throws -> (AXUIElement, Permission, String) {
    let matches = exactPageWindows(appWindows(appElement))
    guard matches.count == 1, let match = matches.first else {
        if matches.count > 1 { throw refused("page_check", "More than one recognized privacy page window is open") }
        throw pending("page_check", "No exact recognized Input Monitoring or Accessibility page title is visible")
    }
    return match
}

private func navigateToPage(_ permission: Permission, app: NSRunningApplication, appElement: AXUIElement) throws -> AXUIElement {
    try ensureNoAuthenticationPrompt(appElement, permission: permission)
    checkpoint("\(permission.rawValue).navigation_requested", permission: permission)
    guard let route = URL(string: permission.systemSettingsRoute), NSWorkspace.shared.open(route) else {
        throw pending("\(permission.rawValue).navigation", "System Settings did not accept the known privacy page URL", permission: permission)
    }
    let visible = waitUntil(pageTimeout) {
        let windows = exactPageWindows(appWindows(appElement)).filter { $0.1 == permission }
        return windows.count == 1
    }
    guard visible else { throw pending("\(permission.rawValue).page_wait", "The exact privacy page title did not appear after navigation", permission: permission) }
    try activateSettings(app, permission: permission)
    let (page, title) = try pageWindow(permission, windows: appWindows(appElement))
    journal.observations[permission.rawValue + ".page_title"] = title
    checkpoint("\(permission.rawValue).page_verified", permission: permission)
    return page
}

private func processPermission(_ permission: Permission, app: NSRunningApplication, appElement: AXUIElement, resumeExistingPicker: Bool) throws {
    journal.activePermission = permission.rawValue
    let (page, pageTitle) = try pageWindow(permission, windows: appWindows(appElement))
    journal.observations[permission.rawValue + ".page_title"] = pageTitle
    checkpoint("\(permission.rawValue).page_verified", permission: permission)
    try activateSettings(app, permission: permission)
    try ensureNoAuthenticationPrompt(appElement, permission: permission)
    try ensureKnownSurfaces(appElement, allowOrdinaryPage: false)

    let openWindows = findOpenWindows(appElement)
    if resumeExistingPicker || !openWindows.isEmpty {
        guard openWindows.count == 1 else { throw refused("\(permission.rawValue).file_picker", "More than one Open panel is visible", permission: permission) }
        if journal.attemptedActions.contains(stageKey(permission, "open")) {
            let panelClosed = waitUntil(uiTimeout) { findOpenWindows(appElement).isEmpty }
            guard panelClosed else {
                throw pending(stageKey(permission, "open_dismiss"), "Open was already attempted; the panel is still visible, so no second press was sent", permission: permission)
            }
            checkpoint(stageKey(permission, "open_dismissed"), permission: permission)
        } else {
            try prepareExistingPicker(permission, app: app, appElement: appElement, openWindow: openWindows[0])
        }
    } else {
        let isAwaitingToggle = journal.activePermission == permission.rawValue && journal.attemptedActions.contains(stageKey(permission, "open"))
        if isAwaitingToggle {
            let ready = waitUntil(toggleTimeout) {
                guard let (_, _, value) = try? rowAndToggle(in: page) else { return false }
                return value?.boolValue == true
            }
            guard ready else { throw pending(stageKey(permission, "toggle_wait"), "The expected MiMac_Toggle=1 did not appear after the journaled Open action", permission: permission) }
            let (_, _, value) = try rowAndToggle(in: page)
            guard value?.boolValue == true else { throw pending(stageKey(permission, "toggle_readback"), "MiMac_Toggle did not read back as 1", permission: permission) }
            journal.observations[permission.rawValue + ".toggle"] = "1"
            if !journal.completedPermissions.contains(permission.rawValue) { journal.completedPermissions.append(permission.rawValue) }
            checkpoint(stageKey(permission, "ui_grant_requested"), status: "ui_grant_requested", note: "System Settings row reads MiMac_Toggle=1; this is a UI request result, not an independent TCC capability check", permission: permission)
            appendLog("info", event: "permission_ui_state", detail: "MiMac_Toggle=1; labeled ui_grant_requested")
            return
        }
        let (row, _, _) = try rowAndToggle(in: page)
        if let row {
            try selectMiMacRow(row, permission: permission)
            let removeButtons = findButtons(roots: [page], labels: ["Remove", "移除"])
            guard removeButtons.count == 1, let removeButton = removeButtons.first else {
                throw refused(stageKey(permission, "remove_button"), "Expected one exact Remove button after selecting the MiMac_Title row", permission: permission)
            }
            guard axNumber(removeButton, "AXEnabled")?.boolValue == true else {
                throw pending(stageKey(permission, "remove_button"), "Remove is disabled for the selected MiMac row", permission: permission)
            }
            guard markAction(permission, "remove") else {
                throw pending(stageKey(permission, "remove_pending"), "Remove was already attempted in this journal and the MiMac row remains; inspect the page before continuing", permission: permission)
            }
            guard AXUIElementPerformAction(removeButton, "AXPress" as CFString) == .success else {
                throw pending(stageKey(permission, "remove_press"), "Remove action was not accepted", permission: permission)
            }
            let removed = waitUntil(uiTimeout) { findElements(roots: [page], identifier: titleIdentifier, stopAfter: 1).isEmpty }
            guard removed else { throw pending(stageKey(permission, "remove_wait"), "MiMac_Title did not disappear after one Remove action", permission: permission) }
            checkpoint(stageKey(permission, "row_removed"), permission: permission)
        }
        try pressUniqueButton([page], labels: ["Add", "添加"], identifiers: ["Add"], stage: stageKey(permission, "add"), permission: permission, journalAction: "add")
        let panelAppeared = waitUntil(uiTimeout) { findOpenWindows(appElement).count == 1 }
        guard panelAppeared else { throw pending(stageKey(permission, "open_panel_wait"), "The open-panel window did not appear after Add", permission: permission) }
        guard findOpenWindows(appElement).count == 1, let openWindow = findOpenWindows(appElement).first else {
            throw refused(stageKey(permission, "open_panel"), "The open-panel window is ambiguous", permission: permission)
        }
        try activateSettings(app, permission: permission)
        try prepareExistingPicker(permission, app: app, appElement: appElement, openWindow: openWindow)
    }

    let (verifiedPage, verifiedTitle) = try pageWindow(permission, windows: appWindows(appElement))
    journal.observations[permission.rawValue + ".page_title"] = verifiedTitle
    let enabled = waitUntil(toggleTimeout) {
        guard let (_, _, value) = try? rowAndToggle(in: verifiedPage) else { return false }
        return value?.boolValue == true
    }
    guard enabled else {
        throw pending(stageKey(permission, "toggle_wait"), "The exact MiMac row did not settle to MiMac_Toggle=1", permission: permission)
    }
    let (_, _, finalValue) = try rowAndToggle(in: verifiedPage)
    guard finalValue?.boolValue == true else { throw pending(stageKey(permission, "toggle_readback"), "MiMac_Toggle did not read back as 1", permission: permission) }
    journal.observations[permission.rawValue + ".toggle"] = "1"
    if !journal.completedPermissions.contains(permission.rawValue) { journal.completedPermissions.append(permission.rawValue) }
    checkpoint(stageKey(permission, "ui_grant_requested"), status: "ui_grant_requested", note: "System Settings row reads MiMac_Toggle=1; this is a UI request result, not an independent TCC capability check", permission: permission)
    appendLog("info", event: "permission_ui_state", detail: "MiMac_Toggle=1; labeled ui_grant_requested")
}

private func inspectCurrentPage(_ appElement: AXUIElement) throws {
    try ensureKnownSurfaces(appElement, allowOrdinaryPage: false)
    let (_, permission, title) = try currentPagePermission(appElement)
    let (page, _) = try pageWindow(permission, windows: appWindows(appElement))
    let titles = findElements(roots: [page], identifier: titleIdentifier, stopAfter: 3)
    guard titles.count <= 1 else { throw refused("inspect", "Multiple MiMac_Title entries are present") }
    var state = "missing"
    if titles.count == 1 {
        let (_, _, value) = try rowAndToggle(in: page)
        guard let value else { throw pending("inspect", "MiMac_Toggle has no readable AXValue", permission: permission) }
        state = value.boolValue ? "1" : "0"
    }
    journal.observations["current_page"] = title
    journal.observations[permission.rawValue + ".toggle"] = state
    checkpoint("inspect_complete", status: "inspected", note: "Read current page and MiMac row only; no UI action was performed", permission: permission)
}

private func runApply(_ permissionArgument: String, app: NSRunningApplication, appElement: AXUIElement) throws {
    let desired: [Permission]
    if permissionArgument == "both" { desired = Permission.allCases }
    else if let single = Permission(rawValue: permissionArgument) { desired = [single] }
    else { throw refused("arguments", "Unsupported permission value") }

    try ensureNoAuthenticationPrompt(appElement, permission: desired.first ?? .inputMonitoring)
    try ensureKnownSurfaces(appElement, allowOrdinaryPage: true)
    if exactPageWindows(appWindows(appElement)).isEmpty {
        _ = try navigateToPage(desired[0], app: app, appElement: appElement)
        try ensureKnownSurfaces(appElement, allowOrdinaryPage: false)
    }
    let openWindows = findOpenWindows(appElement)
    let gotoVisible = appWindows(appElement).contains { !findElements(roots: [$0], identifier: goToIdentifier, stopAfter: 1).isEmpty }
    let pickerVisible = !openWindows.isEmpty || gotoVisible
    let initial = try currentPagePermission(appElement)
    if pickerVisible && !desired.contains(initial.1) {
        throw refused("resume_page", "A file picker is already open on \(initial.2), outside the requested permission set")
    }
    if pickerVisible && openWindows.count != 1 {
        throw refused("resume_picker", "A picker is open but the unique Open panel could not be identified")
    }

    var order = desired
    if permissionArgument == "both" {
        order = [initial.1] + desired.filter { $0 != initial.1 }
    }
    var firstPass = true
    for permission in order {
        try ensureNoAuthenticationPrompt(appElement, permission: permission)
        try ensureKnownSurfaces(appElement, allowOrdinaryPage: false)
        let current = try currentPagePermission(appElement)
        let page: AXUIElement
        if current.1 == permission {
            page = current.0
        } else {
            if pickerVisible && firstPass {
                throw refused("resume_page", "An open picker cannot be redirected to a different permission page")
            }
            page = try navigateToPage(permission, app: app, appElement: appElement)
        }
        let hasCurrentPicker = firstPass && pickerVisible
        try ensureKnownSurfaces(appElement, allowOrdinaryPage: false)
        if journal.completedPermissions.contains(permission.rawValue) {
            guard !hasCurrentPicker else {
                throw pending(stageKey(permission, "completed_readback"), "A completed journal page now has a picker open; inspect it before continuing", permission: permission)
            }
            let (_, _, value) = try rowAndToggle(in: page)
            guard value?.boolValue == true else {
                throw pending(stageKey(permission, "completed_readback"), "The previously completed MiMac row no longer reads MiMac_Toggle=1; no new grant was attempted", permission: permission)
            }
            checkpoint(stageKey(permission, "completed_revalidated"), note: "Read the current unique MiMac row and MiMac_Toggle=1 again", permission: permission)
            firstPass = false
            continue
        }
        try processPermission(permission, app: app, appElement: appElement, resumeExistingPicker: hasCurrentPicker)
        firstPass = false
        _ = page
    }
    let completed = desired.allSatisfy { journal.completedPermissions.contains($0.rawValue) }
    guard completed else { throw pending("apply_incomplete", "One or more requested permission pages remain incomplete") }
    checkpoint("complete", status: "ui_grant_requested", note: "Requested UI state confirmed for all selected permission rows; actual runtime TCC capability remains a separate verification", permission: desired.last)
}

private func initializeJournal(options: Options, validatedBundle: String) throws {
    let root = try cachesRoot()
    if let resume = options.resumeJournal {
        let candidate = URL(fileURLWithPath: resume).standardizedFileURL.resolvingSymlinksInPath().standardizedFileURL
        let rootPrefix = root.path.hasSuffix("/") ? root.path : root.path + "/"
        guard candidate.path.hasPrefix(rootPrefix), candidate.lastPathComponent == "journal.json" else {
            throw ToolFailure(status: "refused", stage: "journal_resume", message: "--resume must point to a journal.json under the helper Caches directory")
        }
        guard let data = try? Data(contentsOf: candidate), let restored = try? JSONDecoder().decode(Journal.self, from: data) else {
            throw ToolFailure(status: "refused", stage: "journal_resume", message: "The selected journal is missing or unreadable")
        }
        guard restored.bundlePath == validatedBundle, restored.executableSHA256 == targetExecutableSHA256,
              restored.permissionArgument == (options.permission ?? ""), restored.mode == "apply" else {
            throw ToolFailure(status: "refused", stage: "journal_resume", message: "Journal target or permission does not match this invocation")
        }
        journal = restored
        journalURL = candidate
        reportURL = candidate.deletingLastPathComponent().appendingPathComponent("report.json")
        logURL = candidate.deletingLastPathComponent().appendingPathComponent("stages.jsonl")
        checkpoint("resume_loaded", note: "Loaded recoverable journal and rechecked live UI state")
        return
    }
    let runID = ISO8601DateFormatter().string(from: Date()).replacingOccurrences(of: ":", with: "").replacingOccurrences(of: "-", with: "") + "-" + String(UUID().uuidString.prefix(8))
    let runDir = root.appendingPathComponent(runID, isDirectory: true)
    try FileManager.default.createDirectory(at: runDir, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
    journalURL = runDir.appendingPathComponent("journal.json")
    reportURL = runDir.appendingPathComponent("report.json")
    logURL = runDir.appendingPathComponent("stages.jsonl")
    _ = FileManager.default.createFile(atPath: logURL.path, contents: Data(), attributes: [.posixPermissions: 0o600])
    let now = timestamp()
    journal = Journal(runID: runID, bundlePath: validatedBundle, executableSHA256: targetExecutableSHA256, permissionArgument: options.permission ?? "", mode: options.mode?.rawValue ?? "", startedAt: now, updatedAt: now)
    try writeJSON(journal!, to: journalURL)
    writeReport()
}

private func printFinal(_ status: String, _ message: String) {
    let object: [String: Any] = [
        "status": status,
        "message": message,
        "journal": journalURL?.path ?? "",
        "report": reportURL?.path ?? "",
        "stage_log": logURL?.path ?? ""
    ]
    if let data = try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]),
       let output = String(data: data, encoding: .utf8) { print(output) }
    else { print("\(status): \(message)") }
}

do {
    let options = try parseArguments()
    guard let requestedPath = options.bundlePath, let permission = options.permission, let mode = options.mode else {
        throw ToolFailure(status: "refused", stage: "arguments", message: "Required arguments are missing")
    }
    targetBundlePath = try validateBundle(requestedPath)
    try initializeJournal(options: options, validatedBundle: targetBundlePath)
    journal.permissionArgument = permission
    journal.mode = mode.rawValue
    checkpoint("bundle_validated", note: "Validated CFBundleIdentifier dev.mimac.app and executable path")

    let (app, appElement) = try ensureSystemSettings(mode: mode)
    _ = app
    _ = appElement
    if mode == .inspect {
        try inspectCurrentPage(appElement)
        printFinal("inspected", "Read the current recognized System Settings page without changing it")
        exit(0)
    }
    try runApply(permission, app: app, appElement: appElement)
    printFinal("ui_grant_requested", "System Settings shows the requested MiMac toggle state; runtime TCC access was not independently verified")
    exit(0)
} catch let failure as ToolFailure {
    if journal != nil {
        checkpoint(failure.stage, status: failure.status, note: failure.message)
        printFinal(failure.status, failure.message)
    } else {
        fputs("\(failure.status): \(failure.message)\n", stderr)
        fputs(usage() + "\n", stderr)
    }
    exit(failure.status == "pending" ? 2 : 3)
} catch {
    let message = "Unexpected helper error: \(error.localizedDescription)"
    if journal != nil {
        checkpoint("unexpected_error", status: "error", note: message)
        printFinal("error", message)
    } else {
        fputs("error: \(message)\n", stderr)
    }
    exit(4)
}
