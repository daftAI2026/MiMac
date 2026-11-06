import AppKit
import Carbon
import Darwin
import Foundation

private let fixtureTitle = "MiMac 语音验收"
private let maximumRuntime: TimeInterval = 30 * 60
private let pollInterval: TimeInterval = 0.05

private struct FixtureError: Error, CustomStringConvertible {
    let description: String
}

private func jsonData(_ object: Any) throws -> Data {
    try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys, .withoutEscapingSlashes])
}

private func atomicWrite(_ object: Any, to url: URL) throws {
    var data = try jsonData(object)
    data.append(0x0a)
    try data.write(to: url, options: .atomic)
}

private func appendJSONLine(_ object: Any, to url: URL) throws {
    var data = try jsonData(object)
    data.append(0x0a)
    if !FileManager.default.fileExists(atPath: url.path) {
        guard FileManager.default.createFile(atPath: url.path, contents: Data()) else {
            throw FixtureError(description: "could not create events.jsonl")
        }
    }
    let handle = try FileHandle(forWritingTo: url)
    defer { try? handle.close() }
    try handle.seekToEnd()
    try handle.write(contentsOf: data)
}

private func monotonicNanoseconds() -> UInt64 {
    var timebase = mach_timebase_info_data_t()
    mach_timebase_info(&timebase)
    let product = mach_continuous_time().multipliedFullWidth(by: UInt64(timebase.numer))
    return UInt64(timebase.denom).dividingFullWidth(product).quotient
}

private func currentInputSourceID() -> String? {
    guard let source = TISCopyCurrentKeyboardInputSource()?.takeRetainedValue(),
          let property = TISGetInputSourceProperty(source, kTISPropertyInputSourceID),
          let sourceID = Unmanaged<CFString>.fromOpaque(property).takeUnretainedValue() as String? else {
        return nil
    }
    return sourceID
}

private func rangeJSON(_ range: NSRange) -> [String: Int] {
    ["location": range.location, "length": range.length]
}

private final class AcceptanceTextView: NSTextView {
}

private final class HostController: NSObject, NSApplicationDelegate, NSTextViewDelegate {
    private let resultDirectory: URL
    private let pidURL: URL
    private let commandURL: URL
    private let resultURL: URL
    private let eventsURL: URL
    private let launchUptime = ProcessInfo.processInfo.systemUptime

    private var window: NSWindow!
    private var textView: AcceptanceTextView!
    private var pollTimer: Timer?
    private var lastCommandData: Data?
    private var lastSequence: Int64 = -1
    private var isTerminating = false

    init(resultDirectory: URL) {
        self.resultDirectory = resultDirectory
        self.pidURL = resultDirectory.appendingPathComponent("pid")
        self.commandURL = resultDirectory.appendingPathComponent("command.json")
        self.resultURL = resultDirectory.appendingPathComponent("result.json")
        self.eventsURL = resultDirectory.appendingPathComponent("events.jsonl")
        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildWindow()
        do {
            try writePID()
            let timer = Timer(timeInterval: pollInterval, repeats: true) { [weak self] _ in
                self?.poll()
            }
            pollTimer = timer
            RunLoop.main.add(timer, forMode: .common)
        } catch {
            fputs("LiveSpeechHost: \(error)\n", stderr)
            NSApp.terminate(nil)
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func textDidChange(_ notification: Notification) {
        appendTextEvent()
    }

    private func buildWindow() {
        let scrollView = NSScrollView(frame: NSRect(x: 0, y: 0, width: 720, height: 260))
        scrollView.hasVerticalScroller = true
        scrollView.hasHorizontalScroller = false
        scrollView.autohidesScrollers = true
        scrollView.borderType = .bezelBorder

        textView = AcceptanceTextView(frame: scrollView.bounds)
        textView.isEditable = true
        textView.isSelectable = true
        textView.isRichText = false
        textView.importsGraphics = false
        textView.allowsUndo = true
        textView.usesFontPanel = false
        textView.usesRuler = false
        textView.font = .systemFont(ofSize: 15)
        textView.string = ""
        textView.delegate = self
        textView.setAccessibilityIdentifier("live-speech-text")
        scrollView.documentView = textView

        window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 760, height: 320),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = fixtureTitle
        window.minSize = NSSize(width: 520, height: 220)
        window.center()
        window.contentView = scrollView
        window.isReleasedWhenClosed = false
        window.makeKeyAndOrderFront(nil)
    }

    private func writePID() throws {
        let staleOutputs = [pidURL, resultURL, eventsURL].filter { FileManager.default.fileExists(atPath: $0.path) }
        guard staleOutputs.isEmpty else {
            throw FixtureError(description: "result directory is not unique; existing output: \(staleOutputs.map(\.lastPathComponent).joined(separator: ","))")
        }
        try Data("\(getpid())\n".utf8).write(to: pidURL, options: .atomic)
    }

    private func poll() {
        guard !isTerminating else { return }
        if ProcessInfo.processInfo.systemUptime - launchUptime >= maximumRuntime {
            terminateNormally(reason: "maximum_runtime")
            return
        }

        guard let data = try? Data(contentsOf: commandURL), data != lastCommandData else { return }
        lastCommandData = data

        do {
            guard let command = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
                throw FixtureError(description: "command.json must contain an object")
            }
            guard let sequenceNumber = command["seq"] as? NSNumber,
                  CFGetTypeID(sequenceNumber) != CFBooleanGetTypeID(),
                  sequenceNumber.doubleValue.isFinite,
                  sequenceNumber.doubleValue.rounded(.towardZero) == sequenceNumber.doubleValue,
                  sequenceNumber.doubleValue >= 0,
                  sequenceNumber.doubleValue <= Double(Int64.max) else {
                throw FixtureError(description: "command seq must be a nonnegative integer")
            }
            let sequence = sequenceNumber.int64Value
            guard sequence > lastSequence else {
                try writeError(sequence: sequence, action: command["action"] as? String, message: "command seq must increase")
                return
            }
            guard let action = command["action"] as? String else {
                throw FixtureError(description: "command action must be a string")
            }
            lastSequence = sequence
            handle(sequence: sequence, action: action)
        } catch {
            try? writeError(sequence: nil, action: nil, message: String(describing: error))
        }
    }

    private func handle(sequence: Int64, action: String) {
        switch action {
        case "prepare":
            NSApp.activate(ignoringOtherApps: true)
            window.makeKeyAndOrderFront(nil)
            window.makeKey()
            let firstResponderSet = window.makeFirstResponder(textView)
            textView.inputContext?.activate()
            DispatchQueue.main.async { [weak self] in
                guard let self, !self.isTerminating else { return }
                let observation = self.captureState()
                let ready = firstResponderSet && (observation["focused"] as? Bool == true)
                self.writeResult(sequence: sequence, action: action, actionReady: ready, state: observation)
            }
        case "snapshot":
            writeResult(sequence: sequence, action: action, actionReady: nil, state: captureState())
        case "quit":
            writeResult(sequence: sequence, action: action, actionReady: nil, state: captureState())
            terminateNormally(reason: "command")
        default:
            try? writeError(sequence: sequence, action: action, message: "unsupported action")
        }
    }

    private func captureState() -> [String: Any] {
        let selection = textView.selectedRange()
        let marked = textView.markedRange()
        let frontmostPID = NSWorkspace.shared.frontmostApplication.map { Int($0.processIdentifier) }
        let appActive = NSApp.isActive
        let windowIsKey = window.isKeyWindow
        let firstResponderIsTextView = window.firstResponder === textView
        let focused = appActive && frontmostPID == Int(getpid()) && windowIsKey
            && firstResponderIsTextView && textView.window === window
        return [
            "pid": Int(getpid()),
            "window_title": window.title,
            "focused": focused,
            "app_active": appActive,
            "frontmost_pid": frontmostPID as Any? ?? NSNull(),
            "window_is_key": windowIsKey,
            "first_responder_is_text_view": firstResponderIsTextView,
            "input_source_id": currentInputSourceID() as Any? ?? NSNull(),
            "text": textView.string,
            "has_marked_text": textView.hasMarkedText(),
            "marked_text_location": marked.location,
            "marked_text_length": marked.length,
            "selection_location": selection.location,
            "selection_length": selection.length,
            "time": Date().timeIntervalSince1970,
            "monotonic_ns": monotonicNanoseconds()
        ]
    }

    private func writeResult(sequence: Int64, action: String, actionReady: Bool?, state: [String: Any]) {
        var result = state
        result["seq"] = sequence
        result["action"] = action
        result["status"] = "ok"
        if let actionReady { result["action_ready"] = actionReady }
        do {
            try atomicWrite(result, to: resultURL)
        } catch {
            fputs("LiveSpeechHost: could not write result.json: \(error)\n", stderr)
        }
    }

    private func writeError(sequence: Int64?, action: String?, message: String) throws {
        var result: [String: Any] = [
            "status": "error",
            "pid": Int(getpid()),
            "error": message,
            "time": Date().timeIntervalSince1970,
            "monotonic_ns": monotonicNanoseconds()
        ]
        if let sequence { result["seq"] = sequence }
        if let action { result["action"] = action }
        try atomicWrite(result, to: resultURL)
    }

    private func appendTextEvent() {
        guard textView != nil else { return }
        let selection = textView.selectedRange()
        let marked = textView.markedRange()
        var event = captureState()
        event["event"] = "text_did_change"
        event["selection"] = rangeJSON(selection)
        event["marked_text"] = rangeJSON(marked)
        do {
            try appendJSONLine(event, to: eventsURL)
        } catch {
            fputs("LiveSpeechHost: could not append events.jsonl: \(error)\n", stderr)
        }
    }

    private func terminateNormally(reason: String) {
        guard !isTerminating else { return }
        isTerminating = true
        pollTimer?.invalidate()
        pollTimer = nil
        _ = reason
        NSApp.terminate(nil)
    }
}

private func makeResultDirectory(from path: String) throws -> URL {
    let directory = URL(fileURLWithPath: path, isDirectory: true).standardizedFileURL
    var isDirectory: ObjCBool = false
    guard directory.path.hasPrefix("/"),
          FileManager.default.fileExists(atPath: directory.path, isDirectory: &isDirectory),
          isDirectory.boolValue else {
        throw FixtureError(description: "argument must be an existing absolute result directory")
    }
    return directory
}

do {
    let arguments = Array(CommandLine.arguments.dropFirst())
    guard arguments.count == 1 else {
        fputs("Usage: LiveSpeechHost <existing-unique-result-directory>\n", stderr)
        exit(2)
    }
    let directory = try makeResultDirectory(from: arguments[0])
    let app = NSApplication.shared
    app.setActivationPolicy(.regular)
    let controller = HostController(resultDirectory: directory)
    app.delegate = controller
    app.run()
} catch {
    fputs("LiveSpeechHost: \(error)\n", stderr)
    exit(1)
}
