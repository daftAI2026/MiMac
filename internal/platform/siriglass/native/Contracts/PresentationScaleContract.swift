import Foundation
import SiriGlassModel

@main struct PresentationScaleContract {
    @MainActor static func main() {
        for scene in ["island", "canvasOnly", "app", "standby", "assistantMenu", "assistantField", "lightweightToolsMenu", "future-scene"] {
            let input = ReferenceChatInput(thinking: true, isRecording: false,
                                           hasVoiceInputOwnership: true, sceneMode: scene)
            precondition((ChatThinkingScale.exitAnimation(for: input, reduceMotion: false) != nil) == (scene == "island"),
                         "incorrect scene exit animation")
            precondition(ChatThinkingScale.exitAnimation(for: input, reduceMotion: true) == nil,
                         "Reduce Motion must suppress the exit animation")
        }
        precondition(ChatThinkingScale.exitAnimation(for: nil, reduceMotion: false) == nil,
                     "missing input must not assume a scene")
        let owned = ReferenceChatInput(thinking: false, isRecording: true, hasVoiceInputOwnership: true)
        let thinking = ReferenceChatInput(thinking: true, isRecording: true, hasVoiceInputOwnership: true)
        let scale = ChatThinkingScale(input: owned)
        scale.update(thinking, reduceMotion: false)
        precondition(scale.x == 0.73 && scale.y == 0.73, "thinking targets not set")
        scale.update(ReferenceChatInput(thinking: true, isRecording: false, hasVoiceInputOwnership: true), reduceMotion: false)
        precondition(scale.x == 1 && scale.y == 1, "recording exit did not reset both targets")
        scale.update(thinking, reduceMotion: false)
        precondition(scale.x == 1 && scale.y == 1, "reopening replayed stale thinking")
        scale.update(owned, reduceMotion: true)
        scale.update(thinking, reduceMotion: true)
        precondition(scale.x == 0.73 && scale.y == 0.73, "fresh edge failed with Reduce Motion")
        scale.update(ReferenceChatInput(thinking: true, isRecording: true, hasVoiceInputOwnership: false), reduceMotion: true)
        precondition(scale.x == 1 && scale.y == 1, "ownership exit did not reset both targets")
        print("PASS actual scale target transitions; animation pixels require native window validation")
    }
}
