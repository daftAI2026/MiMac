/// Captured inputs to ChatInputView's thinking-change callback, not MiMac states.
public struct ReferenceChatInput: Codable, Equatable, Sendable {
    public var thinking: Bool
    public var isRecording: Bool
    public var hasVoiceInputOwnership: Bool
    public var sceneMode: String?

    public init(thinking: Bool, isRecording: Bool, hasVoiceInputOwnership: Bool,
                sceneMode: String? = nil) {
        self.thinking = thinking
        self.isRecording = isRecording
        self.hasVoiceInputOwnership = hasVoiceInputOwnership
        self.sceneMode = sceneMode
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

/// The two parent observers have initial=false. A thinking edge samples input
/// ownership; its independent ending edge resets the parent scale/personality.
/// The initial showThinkingWave value is false even when input starts thinking,
/// matching onChange(initial: false); a later thinking edge samples both gates.
public struct ReferenceThinkingScale: Sendable {
    public static let response = 0.42
    public static let dampingFraction = 0.67
    public static let yDelay = 0.045
    public static let exitResponse = 0.3
    public static let exitDampingFraction = 0.8
    public private(set) var target = 1.0
    public private(set) var showThinkingWave = false
    // One display-observer edge, not a Core completion or a PCM end signal.
    public private(set) var ownershipGateClosed = false
    private var observedThinking: Bool
    private var observedOwnedRecording: Bool

    public var personality: ReferenceWavePersonality {
        showThinkingWave ? .thinking : .attending
    }

    public init(input: ReferenceChatInput) {
        observedThinking = input.thinking
        observedOwnedRecording = input.isRecording && input.hasVoiceInputOwnership
    }

    public mutating func update(_ input: ReferenceChatInput) -> Double? {
        let thinkingChanged = input.thinking != observedThinking
        let ownedRecording = input.isRecording && input.hasVoiceInputOwnership
        ownershipGateClosed = observedOwnedRecording && !ownedRecording
        observedThinking = input.thinking
        observedOwnedRecording = ownedRecording
        if ownershipGateClosed {
            showThinkingWave = false
            target = 1
            return target
        }
        guard thinkingChanged else { return nil }
        let effectiveThinking = input.thinking && input.isRecording && input.hasVoiceInputOwnership
        showThinkingWave = effectiveThinking
        target = effectiveThinking ? 0.73 : 1
        return target
    }
}
