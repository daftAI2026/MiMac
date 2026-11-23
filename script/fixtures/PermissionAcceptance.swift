import AppKit
import ApplicationServices

let settingsID = "com.apple.systempreferences"
let inputPage = ["Input Monitoring", "输入监控"]
let controlPage = ["Device Control and Data Access", "Accessibility", "设备控制和数据访问", "辅助功能"]
let restartTitles = ["Quit & Reopen", "退出并重新打开"]

func emit(_ fields: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: fields, options: .sortedKeys) else { exit(2) }
    print(String(decoding: data, as: UTF8.self))
}

func stop(_ reason: String) -> Never {
    emit(["error": reason])
    exit(2)
}

func attribute(_ element: AXUIElement, _ name: String) -> CFTypeRef? {
    var value: CFTypeRef?
    return AXUIElementCopyAttributeValue(element, name as CFString, &value) == .success ? value : nil
}

func property(_ element: AXUIElement, _ name: String) -> String {
    attribute(element, name) as? String ?? ""
}

func elementsByRoot(_ roots: [AXUIElement]) -> [[AXUIElement]] {
    var queue: [(Int, AXUIElement)] = roots.enumerated().map { ($0.offset, $0.element) }
    var result = Array(repeating: [AXUIElement](), count: roots.count)
    var cursor = 0
    while cursor < queue.count && cursor < 2000 {
        let (root, element) = queue[cursor]
        result[root].append(element)
        let children = attribute(element, "AXChildren") as? [AXUIElement] ?? []
        queue.append(contentsOf: children.map { (root, $0) })
        cursor += 1
    }
    return result
}

func elements(_ roots: [AXUIElement]) -> [AXUIElement] {
    elementsByRoot(roots).flatMap { $0 }
}

func uniqueAXElements(_ values: [AXUIElement]) -> [AXUIElement] {
    var unique: [AXUIElement] = []
    for value in values where !unique.contains(where: { CFEqual($0, value) }) {
        unique.append(value)
    }
    return unique
}

func sheets(in roots: [AXUIElement]) -> [AXUIElement] {
    var queue = roots
    var visited: [AXUIElement] = []
    var found: [AXUIElement] = []
    var cursor = 0
    while cursor < queue.count && visited.count < 2000 {
        let element = queue[cursor]
        cursor += 1
        if visited.contains(where: { CFEqual($0, element) }) { continue }
        visited.append(element)
        if property(element, "AXRole") == "AXSheet" { found.append(element) }
        let attachedSheets = attribute(element, "AXSheets") as? [AXUIElement] ?? []
        found.append(contentsOf: attachedSheets)
        queue.append(contentsOf: attachedSheets)
        queue.append(contentsOf: attribute(element, "AXChildren") as? [AXUIElement] ?? [])
    }
    return uniqueAXElements(found)
}

func windows(_ pid: Int32) -> [AXUIElement] {
    guard pid > 0 else { return [] }
    return attribute(AXUIElementCreateApplication(pid), "AXWindows") as? [AXUIElement] ?? []
}

func frame(_ row: [String: Any]) -> CGRect? {
    guard let rect = row[kCGWindowBounds as String] as? [String: CGFloat],
          let x = rect["X"], let y = rect["Y"], let width = rect["Width"], let height = rect["Height"] else { return nil }
    return CGRect(x: x, y: y, width: width, height: height)
}

func position(_ element: AXUIElement) -> CGPoint? {
    guard let value = attribute(element, "AXPosition"), CFGetTypeID(value) == AXValueGetTypeID() else { return nil }
    var result = CGPoint.zero
    return AXValueGetValue(value as! AXValue, .cgPoint, &result) ? result : nil
}

func rect(_ element: AXUIElement) -> CGRect? {
    guard let positionValue = attribute(element, "AXPosition"),
          let sizeValue = attribute(element, "AXSize"),
          CFGetTypeID(positionValue) == AXValueGetTypeID(), CFGetTypeID(sizeValue) == AXValueGetTypeID() else { return nil }
    var origin = CGPoint.zero
    var size = CGSize.zero
    guard AXValueGetValue(positionValue as! AXValue, .cgPoint, &origin),
          AXValueGetValue(sizeValue as! AXValue, .cgSize, &size) else { return nil }
    return CGRect(origin: origin, size: size)
}

let args = CommandLine.arguments
guard args.count >= 3, AXIsProcessTrusted() else { stop("AX is unavailable or the bundle path is missing") }
let operation = args[1]
let focusOperation = operation == "recover-settings-focus"
guard (operation == "cancel-auth" && args.count == 5) ||
      (focusOperation && args.count == 6) ||
      (operation != "cancel-auth" && !focusOperation && args.count == 3) else {
    stop("unexpected arguments for permission operation")
}
let bundleURL = URL(fileURLWithPath: args[2]).resolvingSymlinksInPath()
guard let bundle = Bundle(url: bundleURL), let id = bundle.bundleIdentifier,
      let name = bundle.object(forInfoDictionaryKey: "CFBundleDisplayName") as? String,
      name.hasPrefix("MiMac ") else { stop("a dedicated MiMac verification bundle is required") }
let isIsolated = id.hasPrefix("dev.mimac.verification.") && name != "MiMac Dev" &&
    bundleURL.path.hasPrefix(NSHomeDirectory() + "/Library/Caches/MiMac/")
let isDev = id == "dev.mimac.app.dev" && name == "MiMac Dev" &&
    ["/.mygo/dev/darwin-arm64/MiMac Dev.app", "/.mygo/dev/darwin-amd64/MiMac Dev.app"].contains(where: bundleURL.path.hasSuffix)
guard isDev || isIsolated else { stop("a dedicated verification identity or frozen Dev is required") }
// Observe only Dev and Settings through CGWindowList. This deliberately avoids
// AX traversal so window first-frame and attachment timings stay lightweight.
if operation == "observe-windows" {
    var previous: Data?
    while true {
        autoreleasepool {
            let scanStartedAt = Date().timeIntervalSince1970
            let ownApps = NSRunningApplication.runningApplications(withBundleIdentifier: id)
                .filter { $0.bundleURL?.resolvingSymlinksInPath().path == bundleURL.path }
            let devPIDs = Set(ownApps.map { $0.processIdentifier })
            let settingsApps = NSRunningApplication.runningApplications(withBundleIdentifier: settingsID)
            let settingsPID = settingsApps.first?.processIdentifier ?? 0
            let frontmostPID = NSWorkspace.shared.frontmostApplication?.processIdentifier ?? 0
            let scene = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
            var visibleWindows: [[String: Any]] = []
            var permissionCard: CGRect?
            var matchingSettingsPage: [[String: Any]] = []
            var settingsGeometry: [(String, CGRect)] = []
            for (order, row) in scene.enumerated() {
                guard let pid = row[kCGWindowOwnerPID as String] as? Int32 else { continue }
                let owner = devPIDs.contains(pid) ? "dev" : (pid == settingsPID && pid > 0 ? "settings" : nil)
                guard let owner,
                      let bounds = frame(row) else { continue }
                let title = row[kCGWindowName as String] as? String ?? ""
                let permissionTitle = title == name + " 系统权限"
                let onboardingTitle = title == name + " 首次设置"
                let settingsPage = (inputPage + controlPage).contains(title)
                let windowID = row[kCGWindowNumber as String] as? UInt32 ?? 0
                let layer = row[kCGWindowLayer as String] as? Int ?? 0
                let alpha = row[kCGWindowAlpha as String] as? Double ?? 1
                visibleWindows.append([
                    "owner": owner, "pid": Int(pid), "window_id": Int(windowID),
                    "title": title, "title_matches_permission_card": permissionTitle,
                    "title_matches_onboarding": onboardingTitle,
                    "title_matches_settings_page": settingsPage,
                    "bounds": [Double(bounds.minX), Double(bounds.minY), Double(bounds.width), Double(bounds.height)],
                    "layer": layer, "alpha": alpha, "frontmost": pid == frontmostPID,
                    "front_to_back_order": order
                ])
                if devPIDs.contains(pid), permissionTitle { permissionCard = bounds }
                if pid == settingsPID {
                    if settingsPage {
                        matchingSettingsPage.append(["title": title, "bounds": [Double(bounds.minX), Double(bounds.minY), Double(bounds.width), Double(bounds.height)]])
                    }
                    if layer == 0 && alpha > 0.05 && bounds.width >= 400 && bounds.height >= 250 {
                        settingsGeometry.append((title, bounds))
                    }
                }
            }
            visibleWindows.sort {
                let leftOrder = $0["front_to_back_order"] as? Int ?? 0
                let rightOrder = $1["front_to_back_order"] as? Int ?? 0
                return leftOrder < rightOrder
            }
            let attachmentSettings = matchingSettingsPage.count == 1
                ? matchingSettingsPage.first.flatMap { value -> CGRect? in
                    guard let title = value["title"] as? String else { return nil }
                    return settingsGeometry.first(where: { $0.0 == title })?.1
                }
                : (settingsGeometry.count == 1 ? settingsGeometry[0].1 : nil)
            var attachment: [String: Any] = ["known": false, "attached": false]
            if let permissionCard, let settings = attachmentSettings {
                let gap = min(abs(permissionCard.maxX - settings.minX), abs(settings.maxX - permissionCard.minX))
                let aligned = abs(permissionCard.minY - settings.minY) <= 1
                attachment = [
                    "known": true, "attached": abs(gap - 16) <= 1 && aligned,
                    "horizontal_gap": Double(gap), "top_delta": Double(permissionCard.minY - settings.minY),
                    "permission_card_bounds": [Double(permissionCard.minX), Double(permissionCard.minY), Double(permissionCard.width), Double(permissionCard.height)],
                    "settings_bounds": [Double(settings.minX), Double(settings.minY), Double(settings.width), Double(settings.height)]
                ]
            }
            let state: [String: Any] = [
                "dev_pids": devPIDs.map { Int($0) }.sorted(),
                "settings_pid": Int(settingsPID), "frontmost_pid": Int(frontmostPID),
                "visible_windows": visibleWindows,
                "permission_card_visible": permissionCard != nil,
                "permission_card_title_match": permissionCard != nil,
                "settings_page_title_matches": matchingSettingsPage,
                "attachment": attachment
            ]
            if let data = try? JSONSerialization.data(withJSONObject: state, options: [.sortedKeys]), data != previous {
                emit(["sample_started_at": scanStartedAt, "sample_completed_at": Date().timeIntervalSince1970, "state": state])
                fflush(stdout)
                previous = data
            }
        }
        RunLoop.main.run(until: Date(timeIntervalSinceNow: 0.1))
    }
}
// Keep semantic permission labels in a separate, lower-rate AX log. Timing
// conclusions come only from the CGWindowList observer above.
if operation == "observe-ax-feedback" {
    var previous: Data?
    while true {
        autoreleasepool {
            let scanStartedAt = Date().timeIntervalSince1970
            let ownApps = NSRunningApplication.runningApplications(withBundleIdentifier: id)
                .filter { $0.bundleURL?.resolvingSymlinksInPath().path == bundleURL.path }
            var panels: [[String: Any]] = []
            for app in ownApps {
                for window in windows(app.processIdentifier) {
                    let title = property(window, "AXTitle")
                    guard [name + " 系统权限", name + " 首次设置"].contains(title) else { continue }
                    var labels: [String] = []
                    for node in elements([window]) {
                        for key in ["AXTitle", "AXValue", "AXDescription"] {
                            let value = property(node, key)
                            if ["已授权", "全部已授权", "未授权", "授权…"].contains(value) ||
                                value.hasPrefix("已授权 ") {
                                labels.append(value)
                                break
                            }
                        }
                    }
                    panels.append(["pid": Int(app.processIdentifier), "title": title, "labels": labels])
                }
            }
            panels.sort {
                let leftPID = $0["pid"] as? Int ?? 0
                let rightPID = $1["pid"] as? Int ?? 0
                if leftPID != rightPID { return leftPID < rightPID }
                return ($0["title"] as? String ?? "") < ($1["title"] as? String ?? "")
            }
            let state: [String: Any] = ["dev_pids": ownApps.map { Int($0.processIdentifier) }.sorted(), "panels": panels]
            if let data = try? JSONSerialization.data(withJSONObject: state, options: [.sortedKeys]), data != previous {
                emit(["sample_started_at": scanStartedAt, "sample_completed_at": Date().timeIntervalSince1970, "state": state])
                fflush(stdout)
                previous = data
            }
        }
        RunLoop.main.run(until: Date(timeIntervalSinceNow: 0.25))
    }
}
let apps = NSRunningApplication.runningApplications(withBundleIdentifier: id)
guard apps.allSatisfy({ $0.bundleURL?.resolvingSymlinksInPath().path == bundleURL.path }) else {
    stop("ambiguous verification process")
}
// A permission probe may share the exact signed bundle with the GUI during
// a system reopen. Only the window-owning app is eligible for an action.
let candidates = apps.count > 1 ? apps.filter { !windows($0.processIdentifier).isEmpty } : apps
guard candidates.count <= 1 else { stop("ambiguous verification process") }
let appPID = candidates.first?.processIdentifier ?? 0
if operation == "quit-app" {
    guard let app = candidates.first, app.terminate() else { stop("the exact verification app did not accept quit") }
    emit(["performed": operation, "app": appPID])
    exit(0)
}
let settingsApps = NSRunningApplication.runningApplications(withBundleIdentifier: settingsID)
let settingsApp = settingsApps.count == 1 ? settingsApps.first : nil
let settingsPID = settingsApp?.processIdentifier ?? 0
let front = NSWorkspace.shared.frontmostApplication
let frontPID = front?.processIdentifier ?? 0
let settingsWindows = windows(settingsPID)
let pageIndices = settingsWindows.indices.filter { (inputPage + controlPage).contains(property(settingsWindows[$0], "AXTitle")) }
let pageIndex = pageIndices.count == 1 ? pageIndices[0] : nil
let page = pageIndex.map { settingsWindows[$0] }
let title = page.map { property($0, "AXTitle") } ?? ""
let systemSheets = settingsWindows.flatMap { attribute($0, "AXSheets") as? [AXUIElement] ?? [] }
let credentialWindow = front?.bundleIdentifier == "com.apple.SecurityAgent" ||
    front?.bundleIdentifier == "com.apple.LocalAuthentication.UIAgent"
// The page tree is also part of the Settings window trees. Expand those
// trees. Keep its full page walk for action detection, and omit that root from
// the password scan so the same Settings page is not queried twice.
let pageElements = page.map { elements([$0]) } ?? []
let otherSettingsWindows: [AXUIElement]
if let pageIndex {
    otherSettingsWindows = settingsWindows.enumerated().filter { $0.offset != pageIndex }.map { $0.element }
} else {
    otherSettingsWindows = settingsWindows
}
let otherSettingsTrees = credentialWindow ? [] : elementsByRoot(otherSettingsWindows)
let restart = pageElements.filter {
    property($0, "AXIdentifier") == "action-button-1" &&
    property($0, "AXRole") == "AXButton" && restartTitles.contains(property($0, "AXTitle")) &&
    attribute($0, "AXEnabled") as? Bool == true
}
let suppliedApp = pageElements.contains { property($0, "AXIdentifier") == "\(name)_Title" }
let password = credentialWindow || pageElements.contains {
    property($0, "AXRole") == "AXSecureTextField" || property($0, "AXSubrole") == "AXSecureTextField"
} || otherSettingsTrees.flatMap { $0 }.contains {
    property($0, "AXRole") == "AXSecureTextField" || property($0, "AXSubrole") == "AXSecureTextField"
}
let scene = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
let permissionCardVisible = scene.contains {
    $0[kCGWindowOwnerPID as String] as? Int32 == appPID &&
    $0[kCGWindowName as String] as? String == "\(name) 系统权限" &&
    ($0[kCGWindowAlpha as String] as? Double ?? 1) > 0.05
}
let permissionCardAX = windows(appPID).filter {
    property($0, "AXRole") == "AXWindow" && property($0, "AXTitle") == "\(name) 系统权限"
}
let permissionSummary = permissionCardAX.flatMap { elements([$0]) }.flatMap { element in
    ["AXTitle", "AXValue", "AXDescription"].map { property(element, $0) }
}.filter { $0.contains("已授权") || $0.contains("未授权") || $0.contains("全部已授权") }.sorted()
let ordinaryOrder: [Int32] = scene.compactMap { row in
    guard let pid = row[kCGWindowOwnerPID as String] as? Int32, pid == appPID || pid == settingsPID,
          row[kCGWindowLayer as String] as? Int == 0, let box = frame(row),
          box.width >= 400 && box.height >= 250 else { return nil }
    return pid
}

let extraAccessWarning: Bool
if operation == "probe" || focusOperation {
    let warningOwner = NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.accessibility.universalAccessAuthWarn").first
    let quotedNames = ["“" + name + "”", "\"" + name + "\"", "「" + name + "」"]
    extraAccessWarning = warningOwner.map { owner in
        elements(windows(owner.processIdentifier)).contains { node in
            guard property(node, "AXRole") == "AXStaticText" else { return false }
            let text = property(node, "AXValue")
            return quotedNames.contains(where: { text.contains($0) })
        }
    } ?? false
} else {
    extraAccessWarning = false
}

if operation == "cancel-auth" {
    guard isDev else { stop("auth cancellation is restricted to the verified canonical Dev") }
    guard let expectedAppPID = Int32(args[3]), let expectedSettingsPID = Int32(args[4]),
          expectedAppPID > 0, expectedSettingsPID > 0,
          apps.count == 1, candidates.count == 1, appPID == expectedAppPID,
          settingsPID == expectedSettingsPID else {
        stop("the expected frozen Dev and System Settings PIDs are not unique")
    }
    guard let page, inputPage.contains(title) else {
        stop("the exact Input Monitoring page is not present in the expected Settings process")
    }
    guard restart.isEmpty else {
        stop("a restart action is present; the password-sheet Cancel flow is not isolated")
    }
    let pageSheets = sheets(in: [page])
    let otherWindowSheets = sheets(in: otherSettingsWindows)
    let targetSheetRole = pageSheets.first.map { property($0, "AXRole") }
    guard pageSheets.count == 1, let targetSheetRole, targetSheetRole == "AXSheet",
          otherWindowSheets.isEmpty else {
        stop("exactly one AXSheet must belong to the verified Input Monitoring page, with no sheet in another Settings window")
    }
    guard frontPID == settingsPID || credentialWindow else {
        stop("frontmost app is not System Settings or its credential UI")
    }
    let sheetElements = elements(pageSheets)
    let secureFields = sheetElements.filter {
        property($0, "AXRole") == "AXSecureTextField" || property($0, "AXSubrole") == "AXSecureTextField"
    }
    let cancelButtons = sheetElements.filter { element in
        property(element, "AXRole") == "AXButton" &&
        attribute(element, "AXEnabled") as? Bool == true &&
        [property(element, "AXTitle"), property(element, "AXDescription")]
            .contains(where: { ["Cancel", "取消"].contains($0) })
    }
    guard secureFields.count == 1, cancelButtons.count == 1, let cancelButton = cancelButtons.first else {
        stop("the exact Settings password sheet must contain one secure field and one enabled Cancel button")
    }
    let cancelLabel = [property(cancelButton, "AXTitle"), property(cancelButton, "AXDescription")]
        .first(where: { ["Cancel", "取消"].contains($0) }) ?? ""
    let startedAt = Date().timeIntervalSince1970
    guard AXUIElementPerformAction(cancelButton, kAXPressAction as CFString) == .success else {
        stop("AXPress on the exact Cancel button failed; no text was entered")
    }
    let completedAt = Date().timeIntervalSince1970
    emit([
        "status": "CANCELLED", "action": "AXPress Cancel",
        "app": Int(expectedAppPID), "settings": Int(expectedSettingsPID),
        "bundle_id": id, "bundle_path": bundleURL.path,
        "authentication_bundle": front?.bundleIdentifier ?? "",
        "frontmost_bundle_id": front?.bundleIdentifier ?? "",
        "settings_page": title, "authentication_sheet_count": pageSheets.count,
        "target_page_sheet_count": pageSheets.count, "target_sheet_role": targetSheetRole,
        "target_sheet_source": "verified-page-subtree",
        "other_window_sheet_count": otherWindowSheets.count,
        "secure_field_count": secureFields.count, "cancel_button_count": cancelButtons.count,
        "cancel_label": cancelLabel, "text_entered": false,
        "press_started_at": startedAt, "press_completed_at": completedAt
    ])
    exit(0)
}

if operation == "probe" {
    emit(["app": appPID, "settings": settingsPID, "front": frontPID, "page": title,
          "bundle_id": id, "bundle_path": bundleURL.path,
          "settings_bundle_id": settingsApp?.bundleIdentifier ?? "",
          "settings_bundle_path": settingsApp?.bundleURL?.resolvingSymlinksInPath().path ?? "",
          "settings_process_count": settingsApps.count, "settings_page_count": pageIndices.count,
          "frontmost_bundle_id": front?.bundleIdentifier ?? "",
          "restart": restart.count == 1 && suppliedApp, "password": password, "extra_access_warning": extraAccessWarning,
          "permission_card_visible": permissionCardVisible, "permission_summary": permissionSummary,
          "permission_card_window_count": permissionCardAX.count,
          "system_sheet": !systemSheets.isEmpty, "system_sheet_titles": systemSheets.map { property($0, "AXTitle") },
          "settings_above_setup": settingsPID > 0 && ordinaryOrder.starts(with: [settingsPID, appPID])])
    exit(0)
}
if focusOperation {
    guard let expectedAppPID = Int32(args[3]), let expectedSettingsPID = Int32(args[4]),
          expectedAppPID > 0, expectedSettingsPID > 0,
          let expectedPage = (inputPage + controlPage).first(where: { $0 == args[5] }) else {
        stop("focus recovery needs exact positive app and Settings PIDs and a recognized page title")
    }
    let before: [String: Any] = [
        "app": Int(appPID), "settings": Int(settingsPID), "front": Int(frontPID),
        "page": title, "frontmost_bundle_id": front?.bundleIdentifier ?? "",
        "app_bundle_id": id, "app_bundle_path": bundleURL.path,
        "settings_bundle_id": settingsApp?.bundleIdentifier ?? "",
        "settings_bundle_path": settingsApp?.bundleURL?.resolvingSymlinksInPath().path ?? "",
        "settings_process_count": settingsApps.count, "settings_page_count": pageIndices.count,
        "permission_card_visible": permissionCardVisible,
        "permission_card_window_count": permissionCardAX.count,
        "password": password, "system_sheet_count": systemSheets.count,
        "restart_action_visible": restart.count == 1 && suppliedApp,
        "extra_access_warning": extraAccessWarning,
    ]
    guard apps.count == 1, candidates.count == 1, appPID == expectedAppPID else {
        emit(["error": "focus recovery is restricted to the unique verified app process", "activation_performed": false,
              "expected_app": Int(expectedAppPID), "before": before])
        exit(2)
    }
    guard settingsApps.count == 1, settingsPID == expectedSettingsPID,
          settingsApp?.bundleIdentifier == settingsID else {
        emit(["error": "the exact unique System Settings process changed before focus recovery", "activation_performed": false,
              "expected_settings": Int(expectedSettingsPID), "before": before])
        exit(2)
    }
    guard pageIndices.count == 1, page != nil, title == expectedPage,
          !password, systemSheets.isEmpty, restart.isEmpty,
          !extraAccessWarning, permissionCardVisible, permissionCardAX.count == 1 else {
        emit(["error": "the exact open permission page is blocked by changed page, password, sheet, restart, or permission-card state",
              "activation_performed": false, "expected_page": expectedPage, "before": before])
        exit(2)
    }
    let settingsScene = scene.filter {
        $0[kCGWindowOwnerPID as String] as? Int32 == expectedSettingsPID &&
        $0[kCGWindowName as String] as? String == expectedPage &&
        ($0[kCGWindowAlpha as String] as? Double ?? 1) > 0.05
    }
    guard settingsScene.count == 1 else {
        emit(["error": "the exact Settings page is not uniquely visible on screen", "activation_performed": false,
              "expected_page": expectedPage, "visible_page_window_count": settingsScene.count, "before": before])
        exit(2)
    }
    if frontPID == expectedSettingsPID {
        emit(["performed": operation, "app": Int(expectedAppPID), "settings": Int(expectedSettingsPID),
              "expected_page": expectedPage, "already_frontmost": true, "activation_performed": false,
              "settings_frontmost_after": true,
              "page_after": expectedPage, "before": before,
              "permission_change_requested": false, "native_drag_performed": false])
        exit(0)
    }
    guard let runningSettings = settingsApp else {
        emit(["error": "the unique System Settings process is unavailable", "activation_performed": false, "before": before])
        exit(2)
    }
    let startedAt = Date().timeIntervalSince1970
    let activationAccepted = runningSettings.activate(options: [])
    guard activationAccepted else {
        emit(["error": "System Settings rejected its single foreground activation", "activation_performed": true,
              "activation_accepted": false, "activation_started_at": startedAt, "before": before])
        exit(2)
    }
    RunLoop.main.run(until: Date(timeIntervalSinceNow: 0.15))
    let frontmostAfterPID = NSWorkspace.shared.frontmostApplication?.processIdentifier ?? 0
    let completedAt = Date().timeIntervalSince1970
    let action: [String: Any] = [
        "performed": operation, "app": Int(expectedAppPID), "settings": Int(expectedSettingsPID),
        "expected_page": expectedPage, "already_frontmost": false, "activation_performed": true,
        "activation_accepted": activationAccepted, "activation_started_at": startedAt,
        "activation_completed_at": completedAt,
        "settings_frontmost_after": frontmostAfterPID == expectedSettingsPID,
        "frontmost_after_pid": Int(frontmostAfterPID),
        "before": before,
        "permission_change_requested": false, "native_drag_performed": false,
    ]
    emit(action)
    exit(0)
}
guard let page, appPID > 0, settingsPID > 0, frontPID == settingsPID, !password else {
    stop("the verification app and expected Settings front window are required")
}
if operation == "confirm-control" {
    guard isDev, controlPage.contains(title),
          (attribute(page, "AXSheets") as? [AXUIElement] ?? []).isEmpty, restart.isEmpty else {
        stop("an unobstructed Dev device-control row is required")
    }
    guard suppliedApp else { emit(["pending": true]); exit(0) }
    let toggles = pageElements.filter { property($0, "AXIdentifier") == "\(name)_Toggle" }
    guard toggles.count <= 1 else { stop("ambiguous permission switch") }
    guard let toggle = toggles.first,
          let currentValue = (attribute(toggle, "AXValue") as? NSNumber)?.intValue else {
        emit(["pending": true])
        exit(0)
    }
    if currentValue == 1 {
        emit(["confirmed": true, "app": appPID, "settings": settingsPID, "permission_value": currentValue])
        exit(0)
    }
    guard attribute(toggle, "AXEnabled") as? Bool == true else {
        emit(["pending": true, "permission_value": currentValue])
        exit(0)
    }
    guard currentValue == 0 else { emit(["pending": true, "permission_value": currentValue]); exit(0) }
    let startedAt = Date().timeIntervalSince1970
    guard AXUIElementPerformAction(toggle, kAXPressAction as CFString) == .success else { stop("confirmation failed") }
    let completedAt = Date().timeIntervalSince1970
    emit(["performed": operation, "app": appPID, "settings": settingsPID,
          "ax_press_started_at": startedAt, "ax_press_completed_at": completedAt,
          "ax_press_duration_ms": (completedAt - startedAt) * 1_000])
    exit(0)
}
if operation == "restart" {
    guard suppliedApp, restart.count == 1 else { stop("restart confirmation is unavailable") }
    let settingsScene: [[String: Any]] = scene.compactMap { row in
        guard row[kCGWindowOwnerPID as String] as? Int32 == settingsPID,
              let box = frame(row) else { return nil }
        return ["id": row[kCGWindowNumber as String] ?? 0,
                "layer": row[kCGWindowLayer as String] ?? 0,
                "frame": [box.minX, box.minY, box.width, box.height]]
    }
    let startedAt = Date().timeIntervalSince1970
    guard AXUIElementPerformAction(restart[0], kAXPressAction as CFString) == .success else { stop("AX rejected restart") }
    let completedAt = Date().timeIntervalSince1970
    emit(["performed": operation, "app": appPID, "settings": settingsPID,
          "settings_windows_before_confirm": settingsScene,
          "ax_press_started_at": startedAt, "ax_press_completed_at": completedAt,
          "ax_press_duration_ms": (completedAt - startedAt) * 1_000])
    exit(0)
}
let inputDrop = operation == "drag-input"
let controlDrop = operation == "drag-control"
let escapeRegression = operation == "drag-cancel-escape"
let wrongTargetRegression = operation == "drag-wrong-target"
guard (inputDrop && inputPage.contains(title)) ||
      (controlDrop && controlPage.contains(title)) ||
      ((escapeRegression || wrongTargetRegression) && (inputPage + controlPage).contains(title)) else {
    stop("incorrect permission pane")
}
guard (attribute(page, "AXSheets") as? [AXUIElement] ?? []).isEmpty, restart.isEmpty,
      CGPreflightPostEventAccess() else { stop("the permission list cannot accept a native drag") }

func visibleWindow(_ pid: Int32, _ title: String) -> CGRect? {
    let matching = scene.filter {
        $0[kCGWindowOwnerPID as String] as? Int32 == pid && $0[kCGWindowName as String] as? String == title
    }
    return matching.count == 1 ? frame(matching[0]) : nil
}
let addButtonElements = pageElements.filter {
    property($0, "AXRole") == "AXButton" && ["Add", "添加"].contains(property($0, "AXDescription"))
}
let addButtons = addButtonElements.compactMap(position)
guard addButtonElements.count == 1, addButtons.count == 1,
      let panel = visibleWindow(appPID, "\(name) 系统权限"),
      let settings = visibleWindow(settingsPID, title) else { stop("the card or list geometry is unavailable") }
let addButtonBounds = addButtonElements.first.flatMap(rect)
let button = addButtons[0]
let destination = CGPoint(x: button.x + 94, y: button.y - 23)
let gap = min(abs(panel.maxX - settings.minX), abs(settings.maxX - panel.minX))
guard abs(gap - 16) <= 1, abs(panel.minY - settings.minY) <= 1,
      settings.contains(destination), !panel.contains(destination),
      let events = CGEventSource(stateID: .combinedSessionState) else { stop("the drag geometry does not match the current windows") }

var source = CGPoint(x: panel.midX, y: panel.minY + 100)
var sourceMethod = "panel-center"
var iconHitTest = false
var iconSourceBounds: [Double] = []
var regressionIconBounds: CGRect?
if escapeRegression || wrongTargetRegression {
    let appWindows = windows(appPID).filter {
        property($0, "AXRole") == "AXWindow" && property($0, "AXTitle") == "\(name) 系统权限"
    }
    guard appWindows.count == 1, let panelElement = appWindows.first,
          let panelAXBounds = rect(panelElement) else { stop("the Dev permission panel AX bounds are unavailable") }
    let iconLabel = "\(name) 应用图标"
    let appTree = elements([panelElement])
    let iconLabelNodes = appTree.filter { element in
        property(element, "AXRole") != "AXWindow" &&
            ["AXTitle", "AXValue", "AXDescription"].contains { property(element, $0) == iconLabel }
    }
    var iconElement: AXUIElement?
    var iconBounds: CGRect?
    if iconLabelNodes.count > 1 {
        stop("the labelled Dev application icon is ambiguous")
    }
    if let labelledIcon = iconLabelNodes.first, let bounds = rect(labelledIcon),
       (75...100).contains(Int(bounds.width.rounded())),
       (75...100).contains(Int(bounds.height.rounded())),
       abs(bounds.midX - panelAXBounds.midX) <= 3,
       panelAXBounds.contains(CGPoint(x: bounds.midX, y: bounds.midY)) {
        iconElement = labelledIcon
        iconBounds = bounds
        sourceMethod = "ax-labelled-icon"
    } else if iconLabelNodes.isEmpty || iconLabelNodes.first.flatMap(rect) == nil {
        let centeredImages = appTree.filter { element in
            guard property(element, "AXRole") == "AXImage", let bounds = rect(element) else { return false }
            return (72...88).contains(Int(bounds.width.rounded())) &&
                (72...88).contains(Int(bounds.height.rounded())) &&
                abs(bounds.midX - panelAXBounds.midX) <= 3 &&
                panelAXBounds.contains(CGPoint(x: bounds.midX, y: bounds.midY))
        }
        guard centeredImages.count == 1, let fallback = centeredImages.first, let bounds = rect(fallback) else {
            stop("the AX icon is unavailable and a unique centered 80pt icon rect could not be validated")
        }
        iconElement = fallback
        iconBounds = bounds
        sourceMethod = "validated-centered-image-fallback"
    } else {
        stop("the labelled Dev application icon bounds do not match the centered icon slot")
    }
    guard let iconBounds else { stop("the Dev application icon bounds are unavailable") }
    guard iconElement != nil else { stop("the Dev application icon AX element is unavailable") }
    regressionIconBounds = iconBounds
    let settingsIsRightOfPanel = settings.midX > panel.midX
    source = CGPoint(x: settingsIsRightOfPanel ? iconBounds.minX + 3 : iconBounds.maxX - 3,
                     y: iconBounds.midY)
    guard iconBounds.contains(source), panel.contains(source), !settings.contains(source) else {
        stop("the drag start point is outside the icon bounds or the permission panel")
    }
    var hitElement: AXUIElement?
    let hitError = AXUIElementCopyElementAtPosition(AXUIElementCreateApplication(appPID),
                                                    Float(source.x), Float(source.y), &hitElement)
    guard hitError == .success, let hitElement, let hitBounds = rect(hitElement), hitBounds.contains(source) else {
        stop("the icon-edge source failed its AX hit-test")
    }
    var hitPID: pid_t = 0
    guard AXUIElementGetPid(hitElement, &hitPID) == .success, hitPID == appPID else {
        stop("the icon-edge hit-test resolved outside the Dev process")
    }
    let hitMatchesIcon = ["AXTitle", "AXValue", "AXDescription"].contains { property(hitElement, $0) == iconLabel }
    let hitRectWithinIcon = iconBounds.contains(CGPoint(x: hitBounds.midX, y: hitBounds.midY)) &&
        hitBounds.width <= iconBounds.width + 1 && hitBounds.height <= iconBounds.height + 1
    guard hitMatchesIcon || hitRectWithinIcon else { stop("the AX hit-test did not resolve inside the application icon") }
    iconHitTest = true
    iconSourceBounds = [Double(iconBounds.minX), Double(iconBounds.minY), Double(iconBounds.width), Double(iconBounds.height)]
}
guard panel.contains(source), !settings.contains(source) else { stop("the drag start point is outside the permission panel") }

func post(_ type: CGEventType, _ point: CGPoint) {
    let event = CGEvent(mouseEventSource: events, mouseType: type, mouseCursorPosition: point, mouseButton: .left)!
    event.setIntegerValueField(.mouseEventClickState, value: 1)
    event.post(tap: .cgSessionEventTap)
}

func overlayVisible() -> Bool {
    let rows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
    return rows.contains {
        $0[kCGWindowOwnerPID as String] as? Int32 == appPID &&
        $0[kCGWindowName as String] as? String == "\(name) 权限拖放区域"
    }
}

func waitForOverlay(_ expectedVisible: Bool, timeout: TimeInterval) -> TimeInterval? {
    let deadline = Date().timeIntervalSince1970 + timeout
    while Date().timeIntervalSince1970 < deadline {
        if overlayVisible() == expectedVisible { return Date().timeIntervalSince1970 }
        usleep(20_000)
    }
    return nil
}

func beginIconEdgeDrag() -> (point: CGPoint, overlayStartedAt: TimeInterval)? {
    guard let iconBounds = regressionIconBounds, !overlayVisible() else { return nil }
    let inward = CGPoint(x: source.x < iconBounds.midX ? source.x + 9 : source.x - 9, y: source.y)
    guard iconBounds.contains(inward) else { return nil }
    post(.mouseMoved, source)
    post(.leftMouseDown, source)
    usleep(20_000)
    post(.leftMouseDragged, inward)
    guard let startedAt = waitForOverlay(true, timeout: 1.5) else {
        let escapeDown = CGEvent(keyboardEventSource: events, virtualKey: 53, keyDown: true)!
        escapeDown.post(tap: .cgSessionEventTap)
        let escapeUp = CGEvent(keyboardEventSource: events, virtualKey: 53, keyDown: false)!
        escapeUp.post(tap: .cgSessionEventTap)
        post(.leftMouseUp, inward)
        return nil
    }
    return (inward, startedAt)
}

func moveHeldPointer(from start: CGPoint, to point: CGPoint) {
    for step in 1...24 {
        let t = Double(step) / 24
        post(.leftMouseDragged, CGPoint(x: start.x + (point.x - start.x) * t,
                                       y: start.y + (point.y - start.y) * t))
        usleep(25_000)
    }
}

func valueString(_ element: AXUIElement, _ key: String) -> String {
    guard let value = attribute(element, key) else { return "" }
    if let string = value as? String { return string }
    return String(describing: value)
}

func permissionSnapshot() -> [String: Any]? {
    let cards = windows(appPID).filter {
        property($0, "AXRole") == "AXWindow" && property($0, "AXTitle") == "\(name) 系统权限"
    }
    guard cards.count == 1, let card = cards.first else { return nil }
    let summaries = elements([card]).flatMap { element in
        ["AXTitle", "AXValue", "AXDescription"].map { valueString(element, $0) }
    }.filter { $0.contains("已授权") || $0.contains("未授权") || $0.contains("全部已授权") }.sorted()
    guard !summaries.isEmpty else { return nil }
    let targetControls = pageElements.filter { element in
        ["AXIdentifier", "AXTitle", "AXDescription"].map { valueString(element, $0) }
            .contains { $0.contains(name) }
    }.map { element in
        ["role": valueString(element, "AXRole"),
         "identifier": valueString(element, "AXIdentifier"),
         "title": valueString(element, "AXTitle"),
         "description": valueString(element, "AXDescription"),
         "value": valueString(element, "AXValue"),
         "enabled": valueString(element, "AXEnabled")]
    }
    return ["summary": summaries, "dev_controls": targetControls]
}

func currentPageTitle() -> String? {
    let matching = windows(settingsPID).filter { (inputPage + controlPage).contains(property($0, "AXTitle")) }
    return matching.count == 1 ? matching.first.map { property($0, "AXTitle") } : nil
}

if escapeRegression || wrongTargetRegression {
    guard let beforeState = permissionSnapshot() else { stop("the Dev permission state is unavailable before the drag regression") }
    let target: CGPoint
    var targetDescription: [String: Any] = [:]
    if escapeRegression {
        target = destination
        targetDescription = ["target_kind": "permission-list-cancelled-after-entry"]
    } else {
        let titleTargets = pageElements.filter { element in
            property(element, "AXRole") == "AXStaticText" &&
                ["AXTitle", "AXValue", "AXDescription"].contains { valueString(element, $0) == title }
        }
        guard titleTargets.count == 1, let titleBounds = rect(titleTargets[0]) else {
            stop("the exact Settings page-title AX target is unavailable")
        }
        target = CGPoint(x: titleBounds.midX, y: titleBounds.midY)
        guard let addButtonBounds, settings.contains(target), !panel.contains(target), !addButtonBounds.contains(target) else {
            stop("the Settings title is not a safe unpermissioned drop target")
        }
        targetDescription = ["target_kind": "unpermissioned-settings-title",
                            "target_role": "AXStaticText", "target_title": title,
                            "target_bounds": [titleBounds.minX, titleBounds.minY, titleBounds.width, titleBounds.height]]
    }

    guard let activeDrag = beginIconEdgeDrag() else {
        stop("the Dev icon-edge drag did not expose the live permission-drop overlay")
    }
    if escapeRegression {
        moveHeldPointer(from: activeDrag.point, to: target)
        guard overlayVisible() else {
            let escapeDown = CGEvent(keyboardEventSource: events, virtualKey: 53, keyDown: true)!
            escapeDown.post(tap: .cgSessionEventTap)
            let escapeUp = CGEvent(keyboardEventSource: events, virtualKey: 53, keyDown: false)!
            escapeUp.post(tap: .cgSessionEventTap)
            post(.leftMouseUp, activeDrag.point)
            stop("the live overlay disappeared before Escape reached the permission target")
        }
        let reachedTargetAt = Date().timeIntervalSince1970
        targetDescription["permission_target_point"] = [Double(target.x), Double(target.y)]
        targetDescription["escape_pressed_at"] = reachedTargetAt
        let escapeDown = CGEvent(keyboardEventSource: events, virtualKey: 53, keyDown: true)!
        escapeDown.post(tap: .cgSessionEventTap)
        let escapeUp = CGEvent(keyboardEventSource: events, virtualKey: 53, keyDown: false)!
        escapeUp.post(tap: .cgSessionEventTap)
        post(.leftMouseUp, target)
        targetDescription["permission_target_reached_while_overlay_visible"] = true
        targetDescription["target_reached_at"] = reachedTargetAt
    } else {
        moveHeldPointer(from: activeDrag.point, to: target)
        usleep(100_000)
        post(.leftMouseUp, target)
    }
    guard let overlayRemovedAt = waitForOverlay(false, timeout: 2) else {
        stop("the drag overlay did not disappear after cancellation or wrong-target release")
    }
    guard let afterState = permissionSnapshot() else { stop("the Dev permission state is unavailable after the drag regression") }
    let afterFrontmost = NSWorkspace.shared.frontmostApplication?.processIdentifier ?? 0
    let afterPageTitle = currentPageTitle()
    let unchanged = beforeState as NSDictionary == afterState as NSDictionary &&
        afterFrontmost == settingsPID && afterPageTitle == title
    guard unchanged else {
        emit(["error": "cancel or wrong-target drag changed permission state or Settings page",
              "performed": operation, "before": beforeState, "after": afterState,
              "frontmost_pid_after": Int(afterFrontmost), "settings_page_after": afterPageTitle ?? ""])
        exit(2)
    }
    emit(["performed": operation, "app": appPID, "settings": settingsPID,
          "source_kind": sourceMethod, "source_inset_points": 3, "source_bounds": iconSourceBounds,
          "icon_hit_test": iconHitTest,
          "drag_started_overlay_observed": true,
          "drag_start_overlay_observed_at": activeDrag.overlayStartedAt,
          "drag_ended_overlay_removed": true, "overlay_removed_observed_at": overlayRemovedAt,
          "permission_state_unchanged": true, "permission_state_before": beforeState,
          "permission_state_after": afterState, "frontmost_pid_after": Int(afterFrontmost),
          "settings_page_after": afterPageTitle ?? ""].merging(targetDescription) { _, new in new })
    exit(0)
}

post(.mouseMoved, source)
let mouseDownAt = Date().timeIntervalSince1970
post(.leftMouseDown, source)
usleep(130_000)
let firstMoveAt = Date().timeIntervalSince1970
for step in 1...24 {
    let t = Double(step) / 24
    post(.leftMouseDragged, CGPoint(x: source.x + (destination.x - source.x) * t,
                                   y: source.y + (destination.y - source.y) * t))
    usleep(25_000)
}
usleep(280_000)
let releasedAt = Date().timeIntervalSince1970
post(.leftMouseUp, destination)
emit(["performed": operation, "app": appPID, "settings": settingsPID,
      "source_kind": "panel-center", "mouse_down_at": mouseDownAt,
      "first_move_at": firstMoveAt, "mouse_up_at": releasedAt,
      "destination": [destination.x, destination.y]])
