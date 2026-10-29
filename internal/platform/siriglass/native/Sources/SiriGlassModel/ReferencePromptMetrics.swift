/// The four prompt insets recovered from ChatInputCenterContentView.
public struct ReferencePromptInsets: Equatable, Sendable {
    public let top: Double
    public let leading: Double
    public let bottom: Double
    public let trailing: Double

    public init(top: Double, leading: Double, bottom: Double, trailing: Double) {
        self.top = top
        self.leading = leading
        self.bottom = bottom
        self.trailing = trailing
    }
}

/// Recovered prompt metrics only; this does not model the complete foreground UI.
public struct ReferencePromptMetrics: Equatable, Sendable {
    /// ChatInputMetrics fields, separate from the prompt's insets.
    public static let waveformHeight = 55.0
    public static let waveformWidth = 74.0
    public static let itemSpacing = 12.0

    /// Reset icon's centered square frame; this does not establish its hit region.
    public static let resetIconFrameSize = 18.0

    public let isTransientCanvas: Bool
    public let isMac: Bool
    public let isIsland: Bool
    public let showsReset: Bool
    public let promptInsets: ReferencePromptInsets

    public init(
        isTransientCanvas: Bool,
        isMac: Bool,
        isIsland: Bool,
        showsReset: Bool
    ) {
        self.isTransientCanvas = isTransientCanvas
        self.isMac = isMac
        self.isIsland = isIsland
        self.showsReset = showsReset

        let vertical = isTransientCanvas || (isMac && isIsland) ? 7.5 : 7.0
        promptInsets = ReferencePromptInsets(
            top: vertical,
            leading: 14.0,
            bottom: vertical,
            trailing: showsReset ? vertical : 14.0
        )
    }
}
