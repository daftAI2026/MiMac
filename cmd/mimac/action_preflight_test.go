package main

import (
	"errors"
	"testing"

	"mimac.local/mimac/internal/adapters/doubao"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/voicetools"
)

func preflightTestPlan(t *testing.T, action string) bridge.ActionPlan {
	t.Helper()
	p, err := bridge.NewActionPlan(bridge.ActionSpec{ToolID: "voice-doubao", ActionID: action, CandidateRaw: "verified", Revision: 1, Scope: bridge.ActionGlobal, Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish}, "RightCommand")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestActionInputSourcePreflight(t *testing.T) {
	tools := []*voiceTool{{id: "voice-doubao", requiredInputSource: doubao.RequiredInputSourceID}}
	for _, tc := range []struct {
		name, action, current string
		readErr               error
		allowed               bool
	}{
		{name: "verified", action: "asrLongPressShortcut", current: "com.bytedance.inputmethod.doubaoime.pinyin", allowed: true},
		{name: "other source", action: "asrLongPressShortcut", current: "com.apple.keylayout.ABC"},
		{name: "failed query", action: "asrLongPressShortcut", readErr: errors.New("unavailable")},
		{name: "unknown action", action: "future", current: "com.bytedance.inputmethod.doubaoime.pinyin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkActionInputSource(tools, preflightTestPlan(t, tc.action), func() (string, error) { return tc.current, tc.readErr })
			if (err == nil) != tc.allowed {
				t.Fatalf("error=%v allowed=%v", err, tc.allowed)
			}
		})
	}
}

func TestPreflightBlocksStartButNeverFrozenRelease(t *testing.T) {
	for _, voice := range []bool{false, true} {
		t.Run(map[bool]string{false: "mapped action", true: "reserved voice"}[voice], func(t *testing.T) {
			d := newDeviceCoordinator()
			sink := &toolShortcutsTestSink{}
			if err := d.attachOutput(sink); err != nil {
				t.Fatal(err)
			}
			plan := preflightTestPlan(t, "asrLongPressShortcut")
			d.voiceButton = "voice"
			button := "voice"
			if voice {
				d.resolveVoiceAction = func() (bridge.ActionPlan, error) { return plan, nil }
			} else {
				button = "up"
				d.actions = map[string]voicetools.ActionRef{"up": {ToolID: "voice-doubao"}}
				d.resolveAction = func(voicetools.ActionRef) (bridge.ActionPlan, error) { return plan, nil }
			}
			allowed := false
			calls := 0
			d.preflightAction = func(bridge.ActionPlan) error {
				calls++
				if !allowed {
					return errActionInputSource
				}
				return nil
			}
			device, _ := bridge.NewDevice("test", 1)
			edge := func(id uint64, kind devices.EdgeKind) {
				press, _ := bridge.NewPressToken(device, button, id)
				_, err := d.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: kind, Press: press}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			edge(1, devices.EdgeDown)
			edge(1, devices.EdgeUp)
			if len(sink.effects) != 0 || calls != 1 || d.report().Pressed != 0 {
				t.Fatal("blocked start emitted output or retained press")
			}
			allowed = true
			edge(2, devices.EdgeDown)
			if len(sink.effects) != 1 {
				t.Fatal("positive start did not emit key-down")
			}
			allowed = false
			edge(2, devices.EdgeUp)
			if len(sink.effects) != 2 || calls != 2 || d.report().Pressed != 0 {
				t.Fatal("source change blocked frozen release or rechecked preflight")
			}
			if _, ok := sink.effects[1].(bridge.KeyUpEffect); !ok {
				t.Fatalf("release=%T", sink.effects[1])
			}
		})
	}
}

// WeType's global PTT path still selects its IME internally. If that source
// is unavailable, the observed vendor version traps before audio delivery.
func TestWeTypeRegistrationRequiresSelectedInputSource(t *testing.T) {
	plan, err := bridge.NewActionPlan(bridge.ActionSpec{ToolID: "voice-wetype", ActionID: "voicePTTShortcut", CandidateRaw: "verified", Revision: 1, Scope: bridge.ActionGlobal, Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish}, "Fn")
	if err != nil {
		t.Fatal(err)
	}
	tools := builtInVoiceTools()
	for _, tc := range []struct {
		id      string
		allowed bool
	}{
		{"com.apple.keylayout.ABC", false},
		{"com.tencent.inputmethod.wetype.pinyin", true},
	} {
		err := checkActionInputSource(tools, plan, func() (string, error) { return tc.id, nil })
		if (err == nil) != tc.allowed {
			t.Fatalf("source=%q error=%v allowed=%t", tc.id, err, tc.allowed)
		}
	}
}
