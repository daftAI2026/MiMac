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
