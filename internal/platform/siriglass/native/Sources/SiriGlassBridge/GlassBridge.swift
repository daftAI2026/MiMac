import AppKit
import Foundation
import SwiftUI
import SiriGlassModel
import SiriGlassNativeABI
import SiriGlassRenderer

private let maximumJSONBytes = 16_384
private enum ParameterDecodeResult {
    case success(GlassParameters)
    case failure(String)
}

private struct ReferenceRenderPlan {
    var compositionParameters: GlassParameters
    var referenceThinking: ReferenceThinkingScale?
    var personalityChanged: Bool
}

private let referenceThinkingWaveAnimation = Animation
    .interpolatingSpring(duration: 0.5, bounce: 0, initialVelocity: 0)
    .speed(0.4)

@MainActor private final class GlassEntryState: ObservableObject {
    @Published var parameters: GlassParameters
    @Published var glass: Glass?
    @Published var isExposed = false
    var compositionParameters: GlassParameters
    let thinkingScale: ChatThinkingScale

    init(parameters: GlassParameters, compositionParameters: GlassParameters, glass: Glass) {
        self.parameters = parameters
        self.compositionParameters = compositionParameters
        self.glass = glass
        thinkingScale = ChatThinkingScale(input: parameters.referenceChatInput)
    }
}

@MainActor private struct GlassScene: View {
    @ObservedObject var state: GlassEntryState
    @ObservedObject var thinkingScale: ChatThinkingScale

    var body: some View {
        ZStack {
            if state.parameters.visible, state.isExposed, let glass = state.glass {
                SiriGlassOrb(glass: glass, parameters: state.parameters,
                             thinkingScale: CGSize(width: thinkingScale.x, height: thinkingScale.y))
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color.clear)
    }
}

@MainActor private final class ClearGlassHostingView: NSHostingView<GlassScene> {
    override var isOpaque: Bool { false }
}

@MainActor private struct ContentRect {
    var x: Double
    var y: Double
    var width: Double
    var height: Double

    func appKitFrame(in contentView: NSView) -> NSRect? {
        let right = x + width
        let bottom = y + height
        let bounds = contentView.bounds
        guard x.isFinite, y.isFinite, width.isFinite, height.isFinite,
              right.isFinite, bottom.isFinite,
              bounds.minX.isFinite, bounds.minY.isFinite,
              bounds.width.isFinite, bounds.height.isFinite,
              x >= 0, y >= 0, width > 0, height > 0,
              right <= Double(bounds.width), bottom <= Double(bounds.height) else {
            return nil
        }

        let frameX = bounds.minX + CGFloat(x)
        let frameY = contentView.isFlipped
            ? bounds.minY + CGFloat(y)
            : bounds.maxY - CGFloat(bottom)
        return NSRect(x: frameX, y: frameY, width: CGFloat(width), height: CGFloat(height))
    }
}

@MainActor private final class GlassEntry {
    let state: GlassEntryState
    let view: NSView
    private var referenceThinking: ReferenceThinkingScale?
    private weak var window: NSWindow?
    private var visibilityObservers: [NSObjectProtocol] = []

    init(parameters: GlassParameters, compositionParameters: GlassParameters,
         referenceThinking: ReferenceThinkingScale?, glass: Glass) {
        self.referenceThinking = referenceThinking
        state = GlassEntryState(parameters: parameters,
                                compositionParameters: compositionParameters,
                                glass: glass)
        let host = ClearGlassHostingView(rootView: GlassScene(state: state, thinkingScale: state.thinkingScale))
        host.wantsLayer = true
        host.layer?.backgroundColor = NSColor.clear.cgColor
        host.layer?.isOpaque = false
        view = host
    }

    func renderPlan(for parameters: GlassParameters) -> ReferenceRenderPlan {
        guard let input = parameters.referenceChatInput else {
            return ReferenceRenderPlan(
                compositionParameters: parameters.resolvingReferencePersonality(nil),
                referenceThinking: nil,
                personalityChanged: false
            )
        }

        guard var next = referenceThinking else {
            let initial = ReferenceThinkingScale(input: input)
            return ReferenceRenderPlan(
                compositionParameters: parameters.resolvingReferencePersonality(initial.personality),
                referenceThinking: initial,
                personalityChanged: false
            )
        }

        let oldPersonality = next.personality
        _ = next.update(input)
        return ReferenceRenderPlan(
            compositionParameters: parameters.resolvingReferencePersonality(next.personality),
            referenceThinking: next,
            personalityChanged: oldPersonality != next.personality
        )
    }

    func attach(to window: NSWindow, contentRect: ContentRect? = nil) -> Bool {
        guard view.superview == nil, let content = window.contentView else { return false }
        if let contentRect {
            guard let frame = contentRect.appKitFrame(in: content) else { return false }
            view.frame = frame
            view.autoresizingMask = []
            view.layer?.masksToBounds = true
        } else {
            view.frame = content.bounds
            view.autoresizingMask = [.width, .height]
            view.layer?.masksToBounds = false
        }
        content.addSubview(view)
        self.window = window

        for name in [NSWindow.didChangeOcclusionStateNotification,
                     NSWindow.didMiniaturizeNotification,
                     NSWindow.didDeminiaturizeNotification] {
            let observer = NotificationCenter.default.addObserver(forName: name, object: window, queue: .main) { [weak self, weak window] _ in
                MainActor.assumeIsolated {
                    guard let self, let window else { return }
                    self.refreshVisibility(for: window)
                }
            }
            visibilityObservers.append(observer)
        }
        refreshVisibility(for: window)
        return true
    }

    func setRect(_ contentRect: ContentRect) -> Bool {
        guard let window, let content = window.contentView,
              view.superview === content,
              let frame = contentRect.appKitFrame(in: content) else { return false }
        view.autoresizingMask = []
        view.layer?.masksToBounds = true
        view.frame = frame
        return true
    }

    func update(_ parameters: GlassParameters, plan: ReferenceRenderPlan, glass: Glass) {
        state.parameters = parameters
        state.compositionParameters = plan.compositionParameters
        referenceThinking = plan.referenceThinking
        let reduceMotion = NSWorkspace.shared.accessibilityDisplayShouldReduceMotion
        state.thinkingScale.update(parameters.referenceChatInput,
                                   reduceMotion: reduceMotion)
        if plan.personalityChanged && !reduceMotion {
            withAnimation(referenceThinkingWaveAnimation) { state.glass = glass }
        } else {
            state.glass = glass
        }
        if let window { refreshVisibility(for: window) }
    }

    func close() {
        visibilityObservers.forEach(NotificationCenter.default.removeObserver)
        visibilityObservers.removeAll()
        state.isExposed = false
        state.glass = nil
        window = nil
        view.removeFromSuperview()
    }

    private func refreshVisibility(for window: NSWindow) {
        state.isExposed = state.parameters.visible && window.isVisible && window.occlusionState.contains(.visible)
    }
}

@MainActor private enum GlassRegistry {
    static var entries: [UInt64: GlassEntry] = [:]
    static var nextHandle: UInt64 = 0
    static var lastError = ""
}

private func onMain<T: Sendable>(_ fallback: T, _ body: @MainActor () -> T) -> T {
    guard Thread.isMainThread else { return fallback }
    return MainActor.assumeIsolated { body() }
}

@MainActor private func recordError(_ error: String) {
    GlassRegistry.lastError = error
    fputs("siri-glass: \(error)\n", stderr)
}

private func decodeParameters(_ data: Data) -> ParameterDecodeResult {
    guard let parameters = try? JSONDecoder().decode(GlassParameters.self, from: data) else {
        return .failure("parameter JSON could not be decoded")
    }
    if let validationError = parameters.validationError() { return .failure(validationError) }
    return .success(parameters)
}

@_cdecl("mimac_siri_glass_abi_version")
public func siriGlassABIVersion() -> UInt32 { 1 }

@_cdecl("mimac_siri_glass_create")
public func siriGlassCreate(_ bytes: UnsafePointer<UInt8>?, _ count: Int32) -> UInt64 {
    guard Thread.isMainThread else { return 0 }
    guard let bytes else { return onMain(0) { recordError("parameter JSON pointer is null"); return 0 } }
    guard count > 0, count <= maximumJSONBytes else {
        return onMain(0) { recordError("parameter JSON length must be between 1 and \(maximumJSONBytes) bytes"); return 0 }
    }
    let data = Data(bytes: bytes, count: Int(count))
    return onMain(UInt64(0)) {
        guard GlassRegistry.nextHandle < UInt64.max else {
            recordError("native handle sequence is exhausted")
            return 0
        }
        let parameters: GlassParameters
        switch decodeParameters(data) {
        case .success(let value): parameters = value
        case .failure(let message): recordError(message); return 0
        }
        do {
            let referenceThinking = parameters.referenceChatInput.map(ReferenceThinkingScale.init)
            let compositionParameters = parameters.resolvingReferencePersonality(referenceThinking?.personality)
            let glass = try PrivateGlassAdapter.shared.compose(compositionParameters)
            GlassRegistry.nextHandle += 1
            let handle = GlassRegistry.nextHandle
            GlassRegistry.entries[handle] = GlassEntry(
                parameters: parameters,
                compositionParameters: compositionParameters,
                referenceThinking: referenceThinking,
                glass: glass
            )
            GlassRegistry.lastError = ""
            return handle
        } catch {
            recordError(error.localizedDescription)
            return 0
        }
    }
}

@_cdecl("mimac_siri_glass_attach")
public func siriGlassAttach(_ handle: UInt64, _ nativeWindow: UInt) -> Int32 {
    return onMain(Int32(0)) {
        guard let entry = GlassRegistry.entries[handle], let rawWindow = UnsafeRawPointer(bitPattern: nativeWindow) else {
            recordError("attach received an unknown handle or null NSWindow")
            return 0
        }
        let window = Unmanaged<NSWindow>.fromOpaque(rawWindow).takeUnretainedValue()
        guard entry.attach(to: window) else {
            recordError("attach could not add the SwiftUI host view to the NSWindow")
            return 0
        }
        GlassRegistry.lastError = ""
        return 1
    }
}

@_cdecl("mimac_siri_glass_attach_rect")
public func siriGlassAttachRect(
    _ handle: UInt64,
    _ nativeWindow: UInt,
    _ x: Double,
    _ y: Double,
    _ width: Double,
    _ height: Double
) -> Int32 {
    return onMain(Int32(0)) {
        guard let entry = GlassRegistry.entries[handle] else {
            recordError("attach_rect received an unknown handle")
            return 0
        }
        guard let rawWindow = UnsafeRawPointer(bitPattern: nativeWindow) else {
            recordError("attach_rect received a null NSWindow")
            return 0
        }
        let window = Unmanaged<NSWindow>.fromOpaque(rawWindow).takeUnretainedValue()
        guard entry.attach(to: window, contentRect: ContentRect(x: x, y: y, width: width, height: height)) else {
            recordError("attach_rect requires an unattached handle and a finite positive rect inside content bounds")
            return 0
        }
        GlassRegistry.lastError = ""
        return 1
    }
}

@_cdecl("mimac_siri_glass_set_rect")
public func siriGlassSetRect(
    _ handle: UInt64,
    _ x: Double,
    _ y: Double,
    _ width: Double,
    _ height: Double
) -> Int32 {
    return onMain(Int32(0)) {
        guard let entry = GlassRegistry.entries[handle] else {
            recordError("set_rect received an unknown or destroyed handle")
            return 0
        }
        guard entry.setRect(ContentRect(x: x, y: y, width: width, height: height)) else {
            recordError("set_rect requires an attached handle and a finite positive rect inside content bounds")
            return 0
        }
        GlassRegistry.lastError = ""
        return 1
    }
}

@_cdecl("mimac_siri_glass_update")
public func siriGlassUpdate(_ handle: UInt64, _ bytes: UnsafePointer<UInt8>?, _ count: Int32) -> Int32 {
    guard Thread.isMainThread else { return 0 }
    guard let bytes else { return onMain(0) { recordError("parameter JSON pointer is null"); return 0 } }
    guard count > 0, count <= maximumJSONBytes else {
        return onMain(0) { recordError("parameter JSON length must be between 1 and \(maximumJSONBytes) bytes"); return 0 }
    }
    let data = Data(bytes: bytes, count: Int(count))
    return onMain(Int32(0)) {
        guard let entry = GlassRegistry.entries[handle] else {
            recordError("update received an unknown or destroyed handle")
            return 0
        }
        let parameters: GlassParameters
        switch decodeParameters(data) {
        case .success(let value): parameters = value
        case .failure(let message): recordError(message); return 0
        }
        guard parameters.revision > entry.state.parameters.revision else {
            recordError("update revision must be strictly greater than the current revision")
            return 0
        }
        do {
            let current = entry.state.compositionParameters
            let plan = entry.renderPlan(for: parameters)
            let next = plan.compositionParameters
            let compositionChanged = current.kind != next.kind
                || current.personality != next.personality
                || current.variant != next.variant
                || current.excludesShadow != next.excludesShadow
                || current.audioMeters != next.audioMeters
                || current.audioPowerDB != next.audioPowerDB
            let glass: Glass
            if compositionChanged {
                glass = try PrivateGlassAdapter.shared.compose(next)
            } else if let currentGlass = entry.state.glass {
                glass = currentGlass
            } else {
                recordError("current Glass value was unavailable during a render-only update")
                return 0
            }
            entry.update(parameters, plan: plan, glass: glass)
            GlassRegistry.lastError = ""
            return 1
        } catch {
            recordError(error.localizedDescription)
            return 0
        }
    }
}

@_cdecl("mimac_siri_glass_destroy")
public func siriGlassDestroy(_ handle: UInt64) -> Int32 {
    onMain(0) {
        guard let entry = GlassRegistry.entries.removeValue(forKey: handle) else {
            recordError("destroy received an unknown or already destroyed handle")
            return 0
        }
        entry.close()
        GlassRegistry.lastError = ""
        return 1
    }
}

/// Returns UTF-8 byte count, or negative required capacity including the NUL terminator.
@_cdecl("mimac_siri_glass_last_error")
public func siriGlassLastError(_ output: UnsafeMutablePointer<CChar>?, _ capacity: Int32) -> Int32 {
    guard Thread.isMainThread else { return 0 }
    let bytes = onMain([UInt8]()) { Array(GlassRegistry.lastError.utf8) }
    let required = bytes.count + 1
    guard capacity >= required, let output else { return -Int32(required) }
    for (index, byte) in bytes.enumerated() { output[index] = CChar(bitPattern: byte) }
    output[bytes.count] = 0
    return Int32(bytes.count)
}
