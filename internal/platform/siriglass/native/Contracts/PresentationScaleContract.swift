import Foundation
import SiriGlassModel

@main struct PresentationScaleContract {
    @MainActor static func main() {
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
