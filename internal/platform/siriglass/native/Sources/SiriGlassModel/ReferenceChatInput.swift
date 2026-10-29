/// Captured inputs to ChatInputView's thinking-change callback, not MiMac states.
public struct ReferenceChatInput: Codable, Equatable, Sendable {
    public var thinking: Bool
    public var isRecording: Bool
    public var hasVoiceInputOwnership: Bool

    public init(thinking: Bool, isRecording: Bool, hasVoiceInputOwnership: Bool) {
        self.thinking = thinking
        self.isRecording = isRecording
        self.hasVoiceInputOwnership = hasVoiceInputOwnership
    }
}

/// Host selector indices used by `GlassParameters.personality` in this bridge.
/// The native adapter maps these indices through SwiftUI case getters; the
/// actual SiriWaveOptions enum payload bytes are thinking=1, attending=0,
/// responding=2, unresolved=3.
public enum ReferenceWavePersonality: Int32, Sendable {
    case thinking = 0
    case attending = 1
    case responding = 2
    case unresolved = 3
}

/// Mirrors initial=false and the callback's gate. Recording/ownership changes
/// alone do not rerun it. Scale belongs to the parent content, not each disk.
/// The initial showThinkingWave value is false even when input starts thinking,
/// matching onChange(initial: false); a later thinking edge samples both gates.
public struct ReferenceThinkingScale: Sendable {
    public static let response = 0.42
    public static let dampingFraction = 0.67
    public static let yDelay = 0.045
    public private(set) var target = 1.0
    public private(set) var showThinkingWave = false
    private var observedThinking: Bool

    public var personality: ReferenceWavePersonality {
        showThinkingWave ? .thinking : .attending
    }

    public init(input: ReferenceChatInput) {
        observedThinking = input.thinking
    }

    public mutating func update(_ input: ReferenceChatInput) -> Double? {
        guard input.thinking != observedThinking else { return nil }
        observedThinking = input.thinking
        let effectiveThinking = input.thinking && input.isRecording && input.hasVoiceInputOwnership
        showThinkingWave = effectiveThinking
        target = effectiveThinking ? 0.73 : 1
        return target
    }
}
