import SwiftUI
import SiriGlassModel

/// View-lifetime state is owned by the bridge entry, so hiding the renderer
/// does not invent a fresh model. This is only the captured geometry callback.
@MainActor public final class ChatThinkingScale: ObservableObject {
    @Published public private(set) var x = 1.0
    @Published public private(set) var y = 1.0
    private var reference: ReferenceThinkingScale?

    public init(input: ReferenceChatInput?) {
        reference = input.map(ReferenceThinkingScale.init)
    }

    public static func exitAnimation(for input: ReferenceChatInput?, reduceMotion: Bool) -> Animation? {
        guard input?.sceneMode == "island", !reduceMotion else { return nil }
        return .spring(response: ReferenceThinkingScale.exitResponse,
                       dampingFraction: ReferenceThinkingScale.exitDampingFraction,
                       blendDuration: 0)
    }

    public func update(_ input: ReferenceChatInput?, reduceMotion: Bool) {
        guard let input else {
            guard reference != nil else { return }
            reference = nil
            x = 1
            y = 1
            return
        }
        guard reference != nil else {
            reference = ReferenceThinkingScale(input: input)
            x = 1
            y = 1
            return
        }
        guard let target = reference?.update(input) else { return }
        if reference?.ownershipGateClosed == true {
            // Exit restores both axes together; it never inherits the thinking
            // transition's delayed Y axis.
            if let animation = Self.exitAnimation(for: input, reduceMotion: reduceMotion) {
                withAnimation(animation) { x = 1; y = 1 }
            } else {
                var reset = Transaction(animation: nil)
                reset.disablesAnimations = true
                withTransaction(reset) { x = 1; y = 1 }
            }
            return
        }
        let spring = Animation.spring(
            response: ReferenceThinkingScale.response,
            dampingFraction: ReferenceThinkingScale.dampingFraction,
            blendDuration: 0
        )
        if x != target {
            withAnimation(reduceMotion ? nil : spring) { x = target }
        }
        if y != target {
            withAnimation(reduceMotion ? nil : spring.delay(ReferenceThinkingScale.yDelay)) { y = target }
        }
    }
}
