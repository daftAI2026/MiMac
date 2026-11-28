import AppKit
import SwiftUI

private struct PermissionRow: Decodable {
    let ID: Int32
    let Title: String
    let Status: String
    let Granted: Bool
}

// Value snapshots come from the Go host. SwiftUI owns only the rendered form;
// preference persistence, login items and appearance stay with MyGo.
private struct PreferencesState: Decodable {
    var Login = false
    var Dock = false
    var VoicePresentation = 0
    var ShowLiveText = false
    var LiveTextSupported: Bool? = nil
    var LiveTextStatus: String? = nil
    var OnlyShowMiMacVoicePrompt = false
    var VoicePromptTools: String? = nil
    var DotAtBottom = false
    var Appearance = "system"
    var Dark = false
    var TextSize = 0
    var BodySize = 13.0
    var CaptionSize = 12.0
    var Permissions: [PermissionRow] = []
    var Disabled = true
    var Error = ""
}

private enum VoicePresentationChoice: Int, CaseIterable {
    case miMacSphere = 0
    case dotBar = 1
    case followVoiceTool = 2

    var title: String {
        switch self {
        case .miMacSphere: return "MiMac 语音球"
        case .dotBar: return "点阵语音条"
        case .followVoiceTool: return "跟随语音工具"
        }
    }
}

@discardableResult
private func emitVoicePresentationSelection(_ index: Int, changed: (Int32, Int32) -> Void) -> Bool {
    guard VoicePresentationChoice(rawValue: index) != nil else { return false }
    changed(5, Int32(index))
    return true
}

private typealias PreferenceCallback = @convention(c) (UInt64, Int32, Int32) -> Void

private struct SettingsForm: View {
    let state: PreferencesState
    let changed: (Int32, Int32) -> Void

    private func toggle(_ current: Bool, kind: Int32) -> Binding<Bool> {
        Binding(get: { current }, set: { changed(kind, $0 ? 1 : 0) })
    }

    private var appearance: Binding<Int> {
        let selected = ["system", "light", "dark"].firstIndex(of: state.Appearance) ?? 0
        return Binding(get: { selected }, set: { changed(3, Int32($0)) })
    }

    private var textSize: Binding<Int> {
        Binding(get: { state.TextSize }, set: { changed(4, Int32($0)) })
    }

    private var voicePresentation: Binding<Int> {
        Binding(get: { state.VoicePresentation }, set: { emitVoicePresentationSelection($0, changed: changed) })
    }

    private var liveTextToggle: Binding<Bool> {
        Binding(
            get: { state.LiveTextSupported == false ? false : state.ShowLiveText },
            set: { value in
                guard state.LiveTextSupported != false else { return }
                changed(7, value ? 1 : 0)
            }
        )
    }

    private var liveTextToggleIdentity: String {
        "\(state.LiveTextSupported ?? true)-\(state.ShowLiveText)"
    }

    var body: some View {
        Form {
            Section {
                Toggle("登录时启动", isOn: toggle(state.Login, kind: 1))
                Toggle("在 Dock 中显示", isOn: toggle(state.Dock, kind: 2))
            }

            Section {
                Picker("样式", selection: voicePresentation) {
                    ForEach(VoicePresentationChoice.allCases, id: \.rawValue) { choice in
                        Text(choice.title).tag(choice.rawValue)
                    }
                }
                .pickerStyle(.menu)
                .accessibilityIdentifier("voice-overlay-row")
                .accessibilityHint("跟随语音工具时，由工具决定是否显示提示")

                if state.VoicePresentation == VoicePresentationChoice.dotBar.rawValue {
                    Picker("点阵位置", selection: toggle(state.DotAtBottom, kind: 8)) {
                        Text("屏幕顶部").tag(false)
                        Text("屏幕底部").tag(true)
                    }
                    .pickerStyle(.menu)
                    .accessibilityIdentifier("voice-dot-position")
                }
            } header: {
                Text("语音提示").font(.system(size: state.BodySize, weight: .semibold))
            }

            Section {
                HStack(alignment: .center) {
                    Text("外观")
                    Spacer()
                    Picker("外观", selection: appearance) {
                        Text("跟随系统").tag(0)
                        Text("浅色").tag(1)
                        Text("深色").tag(2)
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .fixedSize()
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("appearance-row")

                HStack(alignment: .center) {
                    Text("文字大小")
                    Spacer()
                    Picker("文字大小", selection: textSize) {
                        Text("小").tag(-1)
                        Text("标准").tag(0)
                        Text("大").tag(1)
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .fixedSize()
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("text-size-row")
            } header: {
                Text("显示").font(.system(size: state.BodySize, weight: .semibold))
            }

            if !state.Permissions.isEmpty {
                Section {
                    ForEach(state.Permissions, id: \.ID) { permission in
                        LabeledContent(permission.Title) {
                            HStack(alignment: .center, spacing: 12) {
                                if permission.Granted {
                                    Label(permission.Status, systemImage: "checkmark.circle")
                                        .foregroundStyle(.green)
                                } else {
                                    Text(permission.Status).foregroundStyle(.secondary)
                                }
                                Button("管理…") { changed(6, permission.ID) }
                                    .accessibilityLabel("管理" + permission.Title)
                            }
                        }
                    }
                } header: {
                    Text("系统权限").font(.system(size: state.BodySize, weight: .semibold))
                }
            }

            if state.VoicePresentation != VoicePresentationChoice.followVoiceTool.rawValue {
                Section {
                    if state.VoicePresentation == VoicePresentationChoice.dotBar.rawValue {
                        Toggle("点阵实时字幕", isOn: liveTextToggle)
                            .id(liveTextToggleIdentity)
                            .accessibilityIdentifier("voice-live-text-row")
                            .disabled(!(state.LiveTextSupported ?? true))
                    }
                    Toggle("仅显示 MiMac 语音提示", isOn: toggle(state.OnlyShowMiMacVoicePrompt, kind: 9))
                        .id(state.OnlyShowMiMacVoicePrompt)
                        .accessibilityIdentifier("voice-prompt-suppression-row")
                } header: {
                    Text("实验室").font(.system(size: state.BodySize, weight: .semibold))
                } footer: {
                    VStack(alignment: .leading, spacing: 4) {
                        if let tools = state.VoicePromptTools, !tools.isEmpty {
                            Text("可隐藏\(tools)的录音条")
                                .font(.system(size: state.CaptionSize))
                                .accessibilityIdentifier("voice-prompt-suppression-status")
                        }
                        if state.VoicePresentation == VoicePresentationChoice.dotBar.rawValue,
                           let status = state.LiveTextStatus, !status.isEmpty {
                            Text(status)
                                .font(.system(size: state.CaptionSize))
                                .accessibilityIdentifier("voice-live-text-status")
                        }
                    }
                }
            }

            if !state.Error.isEmpty {
                Text(state.Error)
                    .font(.system(size: state.CaptionSize))
                    .foregroundStyle(.primary)
                    .accessibilityAddTraits(.updatesFrequently)
            }
        }
        .formStyle(.grouped)
        .toggleStyle(.switch)
        .tint(.primary)
        .font(.system(size: state.BodySize))
        .disabled(state.Disabled)
        .scrollContentBackground(.hidden)
        // Insets, row metrics and control geometry belong to the native style.
        .environment(\.colorScheme, state.Dark ? .dark : .light)
    }
}

@MainActor
private final class PreferencesHost {
    private let window: NSWindow
    private let view: NSHostingView<SettingsForm>
    private var constraints: [NSLayoutConstraint] = []
    private let token: UInt64
    private let callback: PreferenceCallback
    private var closed = false

    init(window: NSWindow, left: Double, top: Double, right: Double, bottom: Double,
         token: UInt64, callback: @escaping PreferenceCallback) {
        self.window = window
        self.token = token
        self.callback = callback
        view = NSHostingView(rootView: SettingsForm(state: PreferencesState(), changed: { _, _ in }))
        view.translatesAutoresizingMaskIntoConstraints = false
        let content = window.contentView!
        content.addSubview(view)
        constraints = [
            view.leadingAnchor.constraint(equalTo: content.leadingAnchor, constant: left),
            view.trailingAnchor.constraint(equalTo: content.trailingAnchor, constant: -right),
            view.topAnchor.constraint(equalTo: content.topAnchor, constant: top),
            view.bottomAnchor.constraint(equalTo: content.bottomAnchor, constant: -bottom)
        ]
        NSLayoutConstraint.activate(constraints)
    }

    func update(_ json: UnsafePointer<CChar>) -> Int32 {
        guard !closed else { return 2 }
        let data = Data(String(cString: json).utf8)
        guard let state = try? JSONDecoder().decode(PreferencesState.self, from: data),
              state.BodySize.isFinite, state.BodySize >= 12,
              state.CaptionSize.isFinite, state.CaptionSize >= 12,
              (-1...1).contains(state.TextSize),
              VoicePresentationChoice(rawValue: state.VoicePresentation) != nil,
              ["system", "light", "dark"].contains(state.Appearance) else { return 1 }
        view.rootView = SettingsForm(state: state, changed: { [weak self] kind, value in
            guard let self, !self.closed else { return }
            self.callback(self.token, kind, value)
        })
        return 0
    }

    func close() {
        guard !closed else { return }
        closed = true
        view.rootView = SettingsForm(state: PreferencesState(), changed: { _, _ in })
        NSLayoutConstraint.deactivate(constraints)
        constraints.removeAll()
        view.removeFromSuperview()
    }
}

@MainActor
@_cdecl("MiMacPreferencesCreate")
public func createPreferences(_ rawWindow: UnsafeRawPointer?, _ left: Double, _ top: Double,
                       _ right: Double, _ bottom: Double, _ token: UInt64,
                       _ callback: @escaping @convention(c) (UInt64, Int32, Int32) -> Void) -> UnsafeMutableRawPointer? {
    guard Thread.isMainThread, let rawWindow,
          [left, top, right, bottom].allSatisfy({ $0.isFinite && $0 >= 0 }) else { return nil }
    let window = Unmanaged<NSWindow>.fromOpaque(rawWindow).takeUnretainedValue()
    guard window.contentView != nil else { return nil }
    let host = PreferencesHost(window: window, left: left, top: top, right: right, bottom: bottom,
                               token: token, callback: callback)
    return Unmanaged.passRetained(host).toOpaque()
}

@MainActor
@_cdecl("MiMacPreferencesUpdate")
public func updatePreferences(_ rawHost: UnsafeRawPointer?, _ json: UnsafePointer<CChar>?) -> Int32 {
    guard Thread.isMainThread, let rawHost, let json else { return 1 }
    return Unmanaged<PreferencesHost>.fromOpaque(rawHost).takeUnretainedValue().update(json)
}

@MainActor
@_cdecl("MiMacPreferencesClose")
public func closePreferences(_ rawHost: UnsafeRawPointer?) {
    guard Thread.isMainThread, let rawHost else { return }
    let host = Unmanaged<PreferencesHost>.fromOpaque(rawHost).takeRetainedValue()
    host.close()
}
