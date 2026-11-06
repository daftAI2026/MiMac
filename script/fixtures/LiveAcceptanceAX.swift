import ApplicationServices
import CoreAudio
import Darwin
import Foundation

private let maximumNodes = 2_000

private struct ToolFailure: Error {
    let stage: String
    let message: String
    let details: [String: Any]

    init(_ stage: String, _ message: String, details: [String: Any] = [:]) {
        self.stage = stage
        self.message = message
        self.details = details
    }
}

private struct Traversal {
    var elements: [AXUIElement] = []
    var nodes: [[String: Any]] = []
    var rootWindowCount = 0
    var visitedCount = 0
    var truncated = false
    var errors: [[String: Any]] = []
}

private func emit(_ object: Any) {
    if let data = try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys, .withoutEscapingSlashes]),
       let line = String(data: data, encoding: .utf8) {
        print(line)
    } else {
        print("{\"status\":\"error\",\"stage\":\"json_encode\",\"error\":\"could not encode output\"}")
    }
}

private func axAttribute(_ element: AXUIElement, _ name: String) -> (AXError, CFTypeRef?) {
    var value: CFTypeRef?
    let error = AXUIElementCopyAttributeValue(element, name as CFString, &value)
    return (error, value)
}

private func axString(_ element: AXUIElement, _ name: String) -> String? {
    let (error, value) = axAttribute(element, name)
    guard error == .success else { return nil }
    return value as? String
}

private func axJSONValue(_ element: AXUIElement, _ name: String) -> Any {
    let (error, value) = axAttribute(element, name)
    guard error == .success, let value else { return NSNull() }
    if let string = value as? String { return string }
    if let number = value as? NSNumber {
        return CFGetTypeID(value) == CFBooleanGetTypeID() ? number.boolValue as Any : number
    }
    return String(describing: value)
}

private func axChildren(_ element: AXUIElement) -> (AXError, [AXUIElement]) {
    let (error, value) = axAttribute(element, "AXChildren")
    guard error == .success else { return (error, []) }
    return (.success, value as? [AXUIElement] ?? [])
}

private func isOutputRole(_ role: String) -> Bool {
    role == "AXWindow" || role == "AXButton" || role == "AXStaticText"
        || role == "AXTextField" || role == "AXTextArea" || role == "AXComboBox"
}

private func record(_ element: AXUIElement, role: String) -> [String: Any] {
    [
        "role": role,
        "title": axJSONValue(element, "AXTitle"),
        "description": axJSONValue(element, "AXDescription"),
        "value": axJSONValue(element, "AXValue"),
        "identifier": axJSONValue(element, "AXIdentifier"),
        "enabled": axJSONValue(element, "AXEnabled")
    ]
}

private func collectWindowTree(_ windows: [AXUIElement]) -> Traversal {
    var result = Traversal(rootWindowCount: windows.count)
    var queue = windows.map { ($0, 0) }
    var cursor = 0
    while cursor < queue.count && result.visitedCount < maximumNodes {
        let (element, depth) = queue[cursor]
        cursor += 1
        result.visitedCount += 1
        result.elements.append(element)

        if let role = axString(element, "AXRole"), isOutputRole(role) {
            result.nodes.append(record(element, role: role))
        }

        if depth < 64 {
            let (error, children) = axChildren(element)
            if error == .success {
                queue.append(contentsOf: children.map { ($0, depth + 1) })
            } else if error != .noValue && error != .attributeUnsupported {
                result.errors.append(["stage": "AXChildren", "error_code": error.rawValue, "depth": depth])
            }
        }
    }
    result.truncated = cursor < queue.count
    return result
}

private func applicationElement(pid: pid_t) throws -> AXUIElement {
    let application = AXUIElementCreateApplication(pid)
    let error = AXUIElementSetMessagingTimeout(application, 2)
    guard error == .success else {
        throw ToolFailure("ax_timeout", "could not set bounded AX messaging timeout", details: ["ax_error_code": error.rawValue])
    }
    return application
}

private func readWindows(from application: AXUIElement) throws -> [AXUIElement] {
    let (error, value) = axAttribute(application, "AXWindows")
    guard error == .success, let windows = value as? [AXUIElement] else {
        throw ToolFailure("ax_windows", "could not read root AXWindows", details: ["ax_error_code": error.rawValue])
    }
    return windows
}

private func snapshot(pid: pid_t) throws -> [String: Any] {
    let trusted = AXIsProcessTrusted()
    guard trusted else {
        throw ToolFailure("ax_trust", "Accessibility is not trusted for this process", details: ["pid": Int(pid), "ax_trusted": false])
    }
    let application = try applicationElement(pid: pid)
    let windows = try readWindows(from: application)
    let traversal = collectWindowTree(windows)
    let partial = traversal.truncated || !traversal.errors.isEmpty
    return [
        "status": partial ? "partial" : "ok",
        "pid": Int(pid),
        "ax_trusted": trusted,
        "root_window_count": traversal.rootWindowCount,
        "visited_node_count": traversal.visitedCount,
        "truncated": traversal.truncated,
        "nodes": traversal.nodes,
        "traversal_errors": traversal.errors
    ]
}

private func press(pid: pid_t, role requestedRole: String, label: String) throws -> [String: Any] {
    let trusted = AXIsProcessTrusted()
    guard trusted else {
        throw ToolFailure("ax_trust", "Accessibility is not trusted for this process", details: ["pid": Int(pid), "ax_trusted": false])
    }
    let application = try applicationElement(pid: pid)
    let windows = try readWindows(from: application)
    let traversal = collectWindowTree(windows)
    guard !traversal.truncated, traversal.errors.isEmpty else {
        throw ToolFailure("ax_tree_incomplete", "cannot prove a unique exact AX match from an incomplete window tree", details: [
            "pid": Int(pid), "ax_trusted": trusted, "visited_node_count": traversal.visitedCount,
            "truncated": traversal.truncated, "traversal_errors": traversal.errors
        ])
    }

    let matches = traversal.elements.filter { element in
        guard axString(element, "AXRole") == requestedRole else { return false }
        return axString(element, "AXTitle") == label || axString(element, "AXDescription") == label
    }
    guard matches.count == 1, let target = matches.first else {
        throw ToolFailure("ax_match", "expected exactly one exact role and title/description match", details: [
            "pid": Int(pid), "ax_trusted": trusted, "role": requestedRole, "label": label,
            "match_count": matches.count
        ])
    }
    let (enabledError, enabledValue) = axAttribute(target, "AXEnabled")
    guard enabledError == .success, let enabled = enabledValue as? NSNumber, enabled.boolValue else {
        throw ToolFailure("ax_disabled", "the unique exact AX match is not confirmed enabled", details: [
            "pid": Int(pid), "ax_trusted": trusted, "role": requestedRole, "label": label,
            "enabled_error_code": enabledError.rawValue, "enabled": enabledValue as? NSNumber as Any? ?? NSNull()
        ])
    }
    let actionError = AXUIElementPerformAction(target, "AXPress" as CFString)
    guard actionError == .success else {
        throw ToolFailure("ax_press", "AXPress failed", details: ["pid": Int(pid), "ax_error_code": actionError.rawValue])
    }
    return [
        "status": "ok", "action": "press", "pid": Int(pid), "ax_trusted": trusted,
        "role": requestedRole, "label": label, "matched_count": 1, "enabled": true
    ]
}

private func audioAddress(_ selector: AudioObjectPropertySelector,
                          scope: AudioObjectPropertyScope = kAudioObjectPropertyScopeGlobal) -> AudioObjectPropertyAddress {
    AudioObjectPropertyAddress(mSelector: selector, mScope: scope, mElement: kAudioObjectPropertyElementMain)
}

private func audioStatusError(_ stage: String, _ status: OSStatus, details: [String: Any] = [:]) -> ToolFailure {
    ToolFailure(stage, "CoreAudio property query failed", details: details.merging(["os_status": Int(status)]) { _, new in new })
}

private func audioValue<T: FixedWidthInteger>(_ objectID: AudioObjectID,
                           address: AudioObjectPropertyAddress,
                           qualifier: UnsafeRawPointer? = nil,
                           qualifierSize: UInt32 = 0,
                           as type: T.Type = T.self,
                           stage: String) throws -> T {
    var value: T = 0
    var size = UInt32(MemoryLayout<T>.size)
    var mutableAddress = address
    let status = withUnsafeMutablePointer(to: &value) { output in
        AudioObjectGetPropertyData(objectID, &mutableAddress, qualifierSize, qualifier, &size, output)
    }
    guard status == noErr else { throw audioStatusError(stage, status) }
    guard size == UInt32(MemoryLayout<T>.size) else {
        throw ToolFailure(stage, "CoreAudio returned an unexpected property size", details: ["expected_size": MemoryLayout<T>.size, "actual_size": size])
    }
    return value
}

private func audioOwnedString(_ objectID: AudioObjectID, address: AudioObjectPropertyAddress, stage: String) throws -> String {
    var raw: UnsafeRawPointer?
    var size = UInt32(MemoryLayout<UnsafeRawPointer?>.size)
    var mutableAddress = address
    let status = withUnsafeMutablePointer(to: &raw) { output in
        AudioObjectGetPropertyData(objectID, &mutableAddress, 0, nil, &size, UnsafeMutableRawPointer(output))
    }
    guard status == noErr else { throw audioStatusError(stage, status) }
    guard size == UInt32(MemoryLayout<UnsafeRawPointer?>.size), let raw else {
        throw ToolFailure(stage, "CoreAudio returned no CFString", details: ["actual_size": size])
    }
    // The current CoreAudio SDK documents these CFString properties as caller-owned.
    return Unmanaged<CFString>.fromOpaque(raw).takeRetainedValue() as String
}

private func inputDeviceIDs(processObject: AudioObjectID) throws -> [AudioObjectID] {
    var address = audioAddress(kAudioProcessPropertyDevices, scope: kAudioObjectPropertyScopeInput)
    var byteCount: UInt32 = 0
    let sizeStatus = AudioObjectGetPropertyDataSize(processObject, &address, 0, nil, &byteCount)
    guard sizeStatus == noErr else { throw audioStatusError("input_devices_size", sizeStatus) }
    let stride = UInt32(MemoryLayout<AudioObjectID>.stride)
    guard byteCount % stride == 0, byteCount / stride <= 4_096 else {
        throw ToolFailure("input_devices_size", "CoreAudio returned an invalid or excessive input device list size", details: ["byte_count": byteCount])
    }
    let count = Int(byteCount / stride)
    if count == 0 { return [] }
    var values = [AudioObjectID](repeating: kAudioObjectUnknown, count: count)
    var actualByteCount = byteCount
    var mutableAddress = address
    let status = values.withUnsafeMutableBytes { output in
        AudioObjectGetPropertyData(processObject, &mutableAddress, 0, nil, &actualByteCount, output.baseAddress!)
    }
    guard status == noErr else { throw audioStatusError("input_devices", status) }
    guard actualByteCount % stride == 0, actualByteCount <= byteCount else {
        throw ToolFailure("input_devices", "CoreAudio returned an inconsistent input device list size", details: ["allocated_bytes": byteCount, "actual_bytes": actualByteCount])
    }
    return Array(values.prefix(Int(actualByteCount / stride)))
}

private func audioSnapshot(pid: pid_t) throws -> [String: Any] {
    let translationAddress = audioAddress(kAudioHardwarePropertyTranslatePIDToProcessObject)
    var targetPID = pid
    let processObject: AudioObjectID = try withUnsafePointer(to: &targetPID) { pidPointer in
        try audioValue(AudioObjectID(kAudioObjectSystemObject),
                       address: translationAddress,
                       qualifier: UnsafeRawPointer(pidPointer),
                       qualifierSize: UInt32(MemoryLayout<pid_t>.size),
                       as: AudioObjectID.self,
                       stage: "translate_pid")
    }
    guard processObject != kAudioObjectUnknown else {
        throw ToolFailure("translate_pid", "CoreAudio has no Process object for the target PID", details: ["target_pid": Int(pid)])
    }

    let reportedPID: pid_t = try audioValue(processObject, address: audioAddress(kAudioProcessPropertyPID), as: pid_t.self, stage: "reported_pid")
    guard reportedPID == pid else {
        throw ToolFailure("reported_pid", "CoreAudio Process PID did not match target PID", details: ["target_pid": Int(pid), "reported_pid": Int(reportedPID)])
    }
    let bundleID = try audioOwnedString(processObject, address: audioAddress(kAudioProcessPropertyBundleID), stage: "bundle_id")
    let runningInputRaw: UInt32 = try audioValue(processObject, address: audioAddress(kAudioProcessPropertyIsRunningInput), as: UInt32.self, stage: "running_input")
    guard runningInputRaw == 0 || runningInputRaw == 1 else {
        throw ToolFailure("running_input", "CoreAudio returned an invalid running-input value", details: ["running_input": runningInputRaw])
    }
    let deviceIDs = try inputDeviceIDs(processObject: processObject)
    let devices = try deviceIDs.map { deviceID -> [String: Any] in
        let uid = try audioOwnedString(deviceID, address: audioAddress(kAudioDevicePropertyDeviceUID), stage: "device_uid")
        return ["object_id": deviceID, "uid": uid]
    }

    return [
        "status": "ok",
        "target_pid": Int(pid),
        "reported_pid": Int(reportedPID),
        "process_object_id": processObject,
        "bundle_id": bundleID,
        "running_input_status": 0,
        "running_input": Int(runningInputRaw),
        "input_devices": devices
    ]
}

private func parsePID(_ value: String) throws -> pid_t {
    guard let parsed = Int32(value), parsed > 0 else {
        throw ToolFailure("arguments", "PID must be a positive decimal integer")
    }
    return pid_t(parsed)
}

private func usage() -> Never {
    fputs("Usage: LiveAcceptanceAX PID snapshot | PID press ROLE LABEL | PID audio\n", stderr)
    exit(2)
}

let arguments = Array(CommandLine.arguments.dropFirst())
var targetPID: pid_t?
do {
    if arguments == ["--self-test"] {
        // Exercise the exact typed getter without opening an input stream or
        // requiring any application, AX permission, or microphone permission.
        let device: AudioObjectID = try audioValue(AudioObjectID(kAudioObjectSystemObject), address: audioAddress(kAudioHardwarePropertyDefaultOutputDevice), as: AudioObjectID.self, stage: "numeric_property_contract")
        emit(["status": "ok", "scope": "read_only_numeric_property_contract", "default_output_object": device])
        exit(0)
    }
    guard arguments.count >= 2 else { usage() }
    let pid = try parsePID(arguments[0])
    targetPID = pid
    let result: [String: Any]
    switch arguments[1] {
    case "snapshot":
        guard arguments.count == 2 else { usage() }
        result = try snapshot(pid: pid)
    case "press":
        guard arguments.count == 4 else { usage() }
        result = try press(pid: pid, role: arguments[2], label: arguments[3])
    case "audio":
        guard arguments.count == 2 else { usage() }
        result = try audioSnapshot(pid: pid)
    default:
        usage()
    }
    emit(result)
} catch let failure as ToolFailure {
    var result: [String: Any] = ["status": "error", "stage": failure.stage, "error": failure.message]
    if let targetPID { result["target_pid"] = Int(targetPID) }
    for (key, value) in failure.details { result[key] = value }
    emit(result)
    exit(1)
} catch {
    var result: [String: Any] = ["status": "error", "stage": "unexpected", "error": String(describing: error)]
    if let targetPID { result["target_pid"] = Int(targetPID) }
    emit(result)
    exit(1)
}
