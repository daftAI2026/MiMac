import SwiftUI
import SiriGlassModel

private let layerScale = Double(bitPattern: 0x3FF0A3D70A3D70A4)
private let anchorHigh = Double(bitPattern: 0x3FEAAAAAAAAAAAAA)
private let anchorLow = Double(bitPattern: 0x3FC5555555555556)
private let staticAnchors = [
    UnitPoint(x: 0.5, y: anchorHigh),
    UnitPoint(x: anchorHigh, y: anchorLow),
    UnitPoint(x: anchorLow, y: anchorLow),
]

/// Public SwiftUI glass rendering with the frozen reference's captured shape and layer geometry.
public struct SiriGlassOrb: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    public let glass: Glass
    public let parameters: GlassParameters
    public let thinkingScale: CGSize

    public init(glass: Glass, parameters: GlassParameters, thinkingScale: CGSize = CGSize(width: 1, height: 1)) {
        self.glass = glass
        self.parameters = parameters
        self.thinkingScale = thinkingScale
    }

    public var body: some View {
        // All three original undulation surfaces share their parent's glass
        // container. Without it SwiftUI renders separate spheres and shadows.
        if parameters.layers == 3 || parameters.referenceContainerSpacing != nil || parameters.referenceChatInput != nil || thinkingScale != CGSize(width: 1, height: 1) {
            let spacing = parameters.referenceContainerSpacing ?? 10
            GlassEffectContainer(spacing: CGFloat(spacing)) {
                // Isolate the inferred parent spacing with a same-sized clear slot;
                // this does not reproduce the rest of ChatInputCenterContentView.
                Color.clear
                    .frame(width: extent, height: extent)
                    .background(alignment: .center) {
                        orbLayers
                    }
                    .scaleEffect(x: thinkingScale.width, y: thinkingScale.height, anchor: .bottom)
            }
        } else {
            orbLayers
        }
    }

    private var extent: Double { parameters.diameter * 1.5 }

    private var orbLayers: some View {
        ZStack {
            if parameters.layers == 3 {
                ForEach(0..<3, id: \.self) { index in
                    if parameters.referencePhaseAnimation == true, !reduceMotion {
                        disk.glassEffectTransition(.identity).phaseAnimator([true, false]) { content, phase in
                            content.scaleEffect(phase ? 1 : layerScale, anchor: staticAnchors[index])
                        } animation: { _ in
                            .spring(response: 1, dampingFraction: 1, blendDuration: 0)
                                .delay(Double(index) * 0.25)
                        }
                    } else {
                        disk.scaleEffect(x: layerScale, y: layerScale, anchor: staticAnchors[index])
                    }
                }
            } else {
                disk
            }
        }
        .frame(width: extent, height: extent)
    }

    private var disk: some View {
        (parameters.content == "white" ? Color.white : Color.clear)
            .frame(width: parameters.diameter, height: parameters.diameter)
            .glassEffect(glass, in: Circle())
    }
}
