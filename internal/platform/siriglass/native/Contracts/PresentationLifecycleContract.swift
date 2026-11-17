import Foundation

@main struct PresentationLifecycleContract {
    static var failures = 0

    static func expect(_ value: Bool, _ message: String) {
        if !value {
            failures += 1
            fputs("FAIL \(message)\n", stderr)
        }
    }

    static func checkExit(recording: Bool, ownership: Bool) {
        var model = ReferenceThinkingScale(input: ReferenceChatInput(
            thinking: false, isRecording: true, hasVoiceInputOwnership: true))
        _ = model.update(ReferenceChatInput(
            thinking: true, isRecording: true, hasVoiceInputOwnership: true))
        expect(model.showThinkingWave && model.target == 0.73, "thinking edge must enter while input is owned")
        _ = model.update(ReferenceChatInput(
            thinking: true, isRecording: recording, hasVoiceInputOwnership: ownership))
        expect(!model.showThinkingWave && model.target == 1,
               "input exit must reset without a thinking edge: recording=\(recording), ownership=\(ownership)")
        expect(model.ownershipGateClosed, "ending input must identify its independent transition")
        expect(model.update(ReferenceChatInput(
            thinking: true, isRecording: recording, hasVoiceInputOwnership: ownership)) == nil,
               "an unchanged ended input must not repeat its transition")
        expect(!model.ownershipGateClosed, "an unchanged snapshot must clear the one-update cause")
        _ = model.update(ReferenceChatInput(
            thinking: true, isRecording: true, hasVoiceInputOwnership: true))
        expect(!model.showThinkingWave && model.target == 1,
               "reacquiring input must not replay an unchanged thinking value")
        _ = model.update(ReferenceChatInput(
            thinking: false, isRecording: true, hasVoiceInputOwnership: true))
        _ = model.update(ReferenceChatInput(
            thinking: true, isRecording: true, hasVoiceInputOwnership: true))
        expect(model.showThinkingWave && model.target == 0.73,
               "a fresh thinking edge must work after input recovery")
    }

    static func main() {
        let initial = ReferenceThinkingScale(input: ReferenceChatInput(
            thinking: true, isRecording: true, hasVoiceInputOwnership: true))
        expect(!initial.showThinkingWave && initial.target == 1,
               "initial values must not be replayed as change events")
        checkExit(recording: false, ownership: true)
        checkExit(recording: true, ownership: false)
        checkExit(recording: false, ownership: false)
        var concurrent = ReferenceThinkingScale(input: ReferenceChatInput(
            thinking: false, isRecording: true, hasVoiceInputOwnership: true))
        _ = concurrent.update(ReferenceChatInput(
            thinking: true, isRecording: false, hasVoiceInputOwnership: true))
        expect(concurrent.ownershipGateClosed && !concurrent.showThinkingWave && concurrent.target == 1,
               "input exit must dominate a simultaneous thinking edge")
        var opening = ReferenceThinkingScale(input: ReferenceChatInput(
            thinking: false, isRecording: false, hasVoiceInputOwnership: true))
        _ = opening.update(ReferenceChatInput(
            thinking: true, isRecording: true, hasVoiceInputOwnership: true))
        expect(opening.showThinkingWave && opening.target == 0.73,
               "a fresh thinking rise with the gate opening must enter")
        var initiallyUnowned = ReferenceThinkingScale(input: ReferenceChatInput(
            thinking: true, isRecording: true, hasVoiceInputOwnership: false))
        expect(initiallyUnowned.update(ReferenceChatInput(
            thinking: true, isRecording: true, hasVoiceInputOwnership: true)) == nil,
               "initial ownership acquisition must not replay thinking")
        if failures != 0 { exit(1) }
        print("PASS input exit, ownership loss, initial values and recovery edges")
    }
}
