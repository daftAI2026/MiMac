import Foundation

/// Host-side contract; these bounds are not inferred system preconditions.
public struct AudioMetersInput: Codable, Equatable, Sendable {
    public var normalizedPowerAmplitudes: [Float]
    public var stopFrequenciesHz: [Float]

    public init(normalizedPowerAmplitudes: [Float], stopFrequenciesHz: [Float]) {
        self.normalizedPowerAmplitudes = normalizedPowerAmplitudes
        self.stopFrequenciesHz = stopFrequenciesHz
    }

    public func validationError() -> String? {
        guard (1...64).contains(normalizedPowerAmplitudes.count),
              normalizedPowerAmplitudes.count == stopFrequenciesHz.count else {
            return "audioMeters requires 1...64 equal-length amplitude and frequency values"
        }
        guard normalizedPowerAmplitudes.allSatisfy({ $0.isFinite && $0 >= 0 }) else {
            return "audioMeters amplitudes must be finite and nonnegative"
        }
        var previous: Float = 0
        for frequency in stopFrequenciesHz {
            guard frequency.isFinite, frequency > previous else {
                return "audioMeters frequencies must be finite, positive and strictly increasing"
            }
            previous = frequency
        }
        return nil
    }
}

/// Complete renderer input. It contains reference parameters only, not product states.
public struct GlassParameters: Codable, Equatable, Sendable {
    public var schemaVersion: Int
    public var revision: UInt64
    public var kind: Int32
    public var personality: Int32
    public var variant: String?
    public var excludesShadow: Bool?
    public var referenceContainerSpacing: Double?
    public var audioMeters: AudioMetersInput?
    public var referencePhaseAnimation: Bool?
    public var referenceChatInput: ReferenceChatInput?
    public var audioPowerDB: Float
    public var diameter: Double
    public var visible: Bool
    public var content: String
    public var layers: Int

    public init(
        schemaVersion: Int = 1,
        revision: UInt64,
        kind: Int32,
        personality: Int32,
        variant: String?,
        excludesShadow: Bool? = nil,
        referenceContainerSpacing: Double? = nil,
        audioMeters: AudioMetersInput? = nil,
        referencePhaseAnimation: Bool? = nil,
        referenceChatInput: ReferenceChatInput? = nil,
        audioPowerDB: Float,
        diameter: Double,
        visible: Bool,
        content: String = "clear",
        layers: Int = 1
    ) {
        self.schemaVersion = schemaVersion
        self.revision = revision
        self.kind = kind
        self.personality = personality
        self.variant = variant
        self.excludesShadow = excludesShadow
        self.referenceContainerSpacing = referenceContainerSpacing
        self.audioMeters = audioMeters
        self.referencePhaseAnimation = referencePhaseAnimation
        self.referenceChatInput = referenceChatInput
        self.audioPowerDB = audioPowerDB
        self.diameter = diameter
        self.visible = visible
        self.content = content
        self.layers = layers
    }

    public func validationError() -> String? {
        guard schemaVersion == 1 else { return "unsupported parameter schema version \(schemaVersion)" }
        guard revision > 0 else { return "revision must be greater than zero" }
        guard (0...5).contains(kind) else { return "kind must be between 0 and 5" }
        guard (0...3).contains(personality) else { return "personality must be between 0 and 3" }
        guard audioPowerDB.isFinite, (-160...0).contains(audioPowerDB) else { return "audioPowerDB must be finite and between -160 and 0 dB" }
        guard diameter.isFinite, diameter > 0, diameter <= 2048 else { return "diameter must be between 0 and 2048 points" }
        if let spacing = referenceContainerSpacing,
           !spacing.isFinite || !(0...100).contains(spacing) {
            return "referenceContainerSpacing must be finite and between 0 and 100 points"
        }
        guard content == "clear" || content == "white" else { return "content must be clear or white" }
        guard layers == 1 || layers == 3 else { return "layers must be 1 or 3" }
        if let audioMeters {
            guard kind == 2 || kind == 3 else { return "audioMeters requires a wave material (kind 2 or 3)" }
            if let error = audioMeters.validationError() { return error }
        }
        if referencePhaseAnimation == true, layers != 3 {
            return "referencePhaseAnimation requires three layers"
        }
        if let variant, !Self.variants.contains(variant) {
            return "variant is not one of the captured reference values"
        }
        return nil
    }

    /// Returns the parameters used to compose a reference render. Passing a
    /// personality resolves the captured chat-input state; `nil` preserves the
    /// caller's manual personality selection.
    public func resolvingReferencePersonality(_ personality: ReferenceWavePersonality?) -> Self {
        guard let personality else { return self }
        var resolved = self
        resolved.personality = personality.rawValue
        return resolved
    }

    public static let variants: Set<String> = ["siri", "hidden", "prompt", "listening", "thinking", "response", "transient"]

    public static let initial = GlassParameters(
        revision: 1,
        kind: 3,
        personality: 1,
        variant: "listening",
        audioPowerDB: -24,
        diameter: 280,
        visible: true
    )
}
