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

func elements(_ roots: [AXUIElement]) -> [AXUIElement] {
    var queue = roots
    var cursor = 0
    while cursor < queue.count && cursor < 2000 {
        queue.append(contentsOf: attribute(queue[cursor], "AXChildren") as? [AXUIElement] ?? [])
        cursor += 1
    }
    return Array(queue.prefix(cursor))
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

let args = CommandLine.arguments
guard args.count == 3, AXIsProcessTrusted() else { stop("AX is unavailable or the bundle path is missing") }
let operation = args[1]
let bundleURL = URL(fileURLWithPath: args[2]).resolvingSymlinksInPath()
guard let bundle = Bundle(url: bundleURL), let id = bundle.bundleIdentifier,
      id.hasPrefix("dev.mimac.verification."),
      let name = bundle.object(forInfoDictionaryKey: "CFBundleDisplayName") as? String,
      name.hasPrefix("MiMac ") else { stop("a dedicated MiMac verification bundle is required") }
let apps = NSRunningApplication.runningApplications(withBundleIdentifier: id)
guard apps.count <= 1, apps.allSatisfy({ $0.bundleURL?.resolvingSymlinksInPath().path == bundleURL.path }) else {
    stop("ambiguous verification process")
}
let appPID = apps.first?.processIdentifier ?? 0
if operation == "quit-app" {
    guard let app = apps.first, app.terminate() else { stop("the exact verification app did not accept quit") }
    emit(["performed": operation, "app": appPID])
    exit(0)
}
let settingsPID = NSRunningApplication.runningApplications(withBundleIdentifier: settingsID).first?.processIdentifier ?? 0
let front = NSWorkspace.shared.frontmostApplication
let frontPID = front?.processIdentifier ?? 0
let settingsWindows = windows(settingsPID)
let pages = settingsWindows.filter { (inputPage + controlPage).contains(property($0, "AXTitle")) }
let page = pages.count == 1 ? pages.first : nil
let title = page.map { property($0, "AXTitle") } ?? ""
let pageElements = page.map { elements([$0]) } ?? []
let restart = pageElements.filter {
    property($0, "AXIdentifier") == "action-button-1" &&
    property($0, "AXRole") == "AXButton" && restartTitles.contains(property($0, "AXTitle")) &&
    attribute($0, "AXEnabled") as? Bool == true
}
let suppliedApp = pageElements.contains { property($0, "AXIdentifier") == "\(name)_Title" }
let credentialWindow = front?.bundleIdentifier == "com.apple.SecurityAgent" ||
    front?.bundleIdentifier == "com.apple.LocalAuthentication.UIAgent"
let password = credentialWindow || elements(settingsWindows).contains {
    property($0, "AXRole") == "AXSecureTextField" || property($0, "AXSubrole") == "AXSecureTextField"
}
let scene = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
let ordinaryOrder: [Int32] = scene.compactMap { row in
    guard let pid = row[kCGWindowOwnerPID as String] as? Int32, pid == appPID || pid == settingsPID,
          row[kCGWindowLayer as String] as? Int == 0, let box = frame(row),
          box.width >= 400 && box.height >= 250 else { return nil }
    return pid
}

if operation == "probe" {
    emit(["app": appPID, "settings": settingsPID, "front": frontPID, "page": title,
          "restart": restart.count == 1 && suppliedApp, "password": password,
          "settings_above_setup": settingsPID > 0 && ordinaryOrder.starts(with: [settingsPID, appPID])])
    exit(0)
}
guard let page, appPID > 0, settingsPID > 0, frontPID == settingsPID, !password else {
    stop("the verification app and expected Settings front window are required")
}
if operation == "restart" {
    guard inputPage.contains(title), suppliedApp, restart.count == 1 else { stop("restart confirmation is unavailable") }
    guard AXUIElementPerformAction(restart[0], kAXPressAction as CFString) == .success else { stop("AX rejected restart") }
    emit(["performed": operation, "app": appPID, "settings": settingsPID])
    exit(0)
}
guard (operation == "drag-input" && inputPage.contains(title)) ||
      (operation == "drag-control" && controlPage.contains(title)) else { stop("incorrect permission pane") }
guard (attribute(page, "AXSheets") as? [AXUIElement] ?? []).isEmpty, restart.isEmpty,
      CGPreflightPostEventAccess() else { stop("the permission list cannot accept a native drag") }

func visibleWindow(_ pid: Int32, _ title: String) -> CGRect? {
    let matching = scene.filter {
        $0[kCGWindowOwnerPID as String] as? Int32 == pid && $0[kCGWindowName as String] as? String == title
    }
    return matching.count == 1 ? frame(matching[0]) : nil
}
let addButtons = pageElements.filter {
    property($0, "AXRole") == "AXButton" && ["Add", "添加"].contains(property($0, "AXDescription"))
}.compactMap(position)
guard addButtons.count == 1, let panel = visibleWindow(appPID, "\(name) 系统权限"),
      let settings = visibleWindow(settingsPID, title) else { stop("the card or list geometry is unavailable") }
let source = CGPoint(x: panel.midX, y: panel.minY + 100)
let button = addButtons[0]
let destination = CGPoint(x: button.x + 94, y: button.y - 23)
let gap = min(abs(panel.maxX - settings.minX), abs(settings.maxX - panel.minX))
guard abs(gap - 16) <= 1, abs(panel.minY - settings.minY) <= 1,
      !settings.contains(source), settings.contains(destination), !panel.contains(destination),
      let events = CGEventSource(stateID: .combinedSessionState) else { stop("the drag geometry does not match the current windows") }

func post(_ type: CGEventType, _ point: CGPoint) {
    let event = CGEvent(mouseEventSource: events, mouseType: type, mouseCursorPosition: point, mouseButton: .left)!
    event.setIntegerValueField(.mouseEventClickState, value: 1)
    event.post(tap: .cgSessionEventTap)
}

post(.mouseMoved, source)
post(.leftMouseDown, source)
usleep(130_000)
for step in 1...24 {
    let t = Double(step) / 24
    post(.leftMouseDragged, CGPoint(x: source.x + (destination.x - source.x) * t,
                                   y: source.y + (destination.y - source.y) * t))
    usleep(25_000)
}
usleep(280_000)
post(.leftMouseUp, destination)
emit(["performed": operation, "app": appPID, "settings": settingsPID])
