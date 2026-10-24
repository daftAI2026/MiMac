package chatgpt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestParseKeybindingsUsesVerifiedDefaultWhenVoiceCommandIsAbsent(t *testing.T) {
	actions, state, usedDefault := parseKeybindings([]byte(`[{"command":"unrelatedCommand","key":"DROP_ME"}]`))
	if state != voicetools.Ready || !usedDefault {
		t.Fatalf("parse state/default = %q/%t, want ready/default", state, usedDefault)
	}
	if len(actions) != 2 {
		t.Fatalf("actions = %#v, want voice and end actions", actions)
	}
	voice := actionByID(t, actions, "composer.startVoiceMode")
	if voice.Scope != "app" || voice.Gesture != "toggle" || len(voice.Candidates) != 1 ||
		voice.Candidates[0] != (voicetools.Candidate{Raw: "Ctrl+Shift+V", Shortcut: "Control+Shift+V", Supported: false}) {
		t.Fatalf("voice default = %#v", voice)
	}
	end := actionByID(t, actions, "realtimeVoice.endCall")
	if end.Scope != "app" || end.Gesture != "end" || len(end.Candidates) != 0 {
		t.Fatalf("end action = %#v, want no default binding", end)
	}
}

func TestParseKeybindingsExplicitNullDisablesDefault(t *testing.T) {
	actions, state, usedDefault := parseKeybindings([]byte(`[{"command":"composer.startVoiceMode","key":null}]`))
	if state != voicetools.Ready || usedDefault {
		t.Fatalf("parse state/default = %q/%t, want ready/no default", state, usedDefault)
	}
	voice := actionByID(t, actions, "composer.startVoiceMode")
	if len(voice.Candidates) != 1 || voice.Candidates[0].Raw != "null" || voice.Candidates[0].Shortcut != "" || voice.Candidates[0].Supported {
		t.Fatalf("explicitly disabled voice action = %#v", voice)
	}
}

func TestParseKeybindingsPreservesMultipleOverrideRows(t *testing.T) {
	data := []byte(`[` +
		`{"command":"composer.startVoiceMode","key":"Ctrl+K","extra":"DROP_ME"},` +
		`{"command":"composer.startVoiceMode","key":"Control+Shift+V"},` +
		`{"command":"realtimeVoice.endCall","key":"Cmd+E"}]`)
	actions, state, usedDefault := parseKeybindings(data)
	if state != voicetools.Ready || usedDefault {
		t.Fatalf("parse state/default = %q/%t, want ready/no default", state, usedDefault)
	}
	voice := actionByID(t, actions, "composer.startVoiceMode")
	want := []voicetools.Candidate{
		{Raw: "Ctrl+K", Shortcut: "Control+K", Supported: false},
		{Raw: "Control+Shift+V", Shortcut: "Control+Shift+V", Supported: false},
	}
	if len(voice.Candidates) != len(want) {
		t.Fatalf("voice candidates = %#v, want %#v", voice.Candidates, want)
	}
	for i := range want {
		if voice.Candidates[i] != want[i] {
			t.Errorf("voice candidate[%d] = %#v, want %#v", i, voice.Candidates[i], want[i])
		}
	}
	end := actionByID(t, actions, "realtimeVoice.endCall")
	if len(end.Candidates) != 1 || end.Candidates[0] != (voicetools.Candidate{Raw: "Cmd+E", Shortcut: "Command+E", Supported: false}) {
		t.Fatalf("end action = %#v", end)
	}
}

func TestParseKeybindingsDoesNotExposeGlobalOrUnknownCommands(t *testing.T) {
	data := []byte(`[` +
		`{"command":"realtimeVoice","key":"Ctrl+K","account":"DROP_ME"},` +
		`{"command":"globalDictationHold","key":"Fn"},` +
		`{"command":"globalDictationSingleTap","key":"Fn"},` +
		`{"command":"unknown.secretAction","key":"DROP_ME"},` +
		`{"command":"realtimeVoice.toggleMicrophoneMute","key":"Ctrl+M"}]`)
	actions, state, usedDefault := parseKeybindings(data)
	if state != voicetools.Ready || !usedDefault || len(actions) != 2 {
		t.Fatalf("parse = %#v/%q/default=%t, want only two app actions and default", actions, state, usedDefault)
	}
	encoded, err := json.Marshal(actions)
	if err != nil {
		t.Fatal(err)
	}
	for _, ignored := range []string{"realtimeVoice\"", "globalDictation", "secretAction", "DROP_ME", "account"} {
		if strings.Contains(string(encoded), ignored) {
			t.Fatalf("allowlist output contains ignored value %q: %s", ignored, encoded)
		}
	}
}

func TestParseKeybindingsRejectsMalformedFileWithoutDefaultFallback(t *testing.T) {
	for _, input := range []string{
		`{"command":"composer.startVoiceMode","key":"Ctrl+K"}`,
		`[{"command":"composer.startVoiceMode","key":42}]`,
		`[{"command":"composer.startVoiceMode"}]`,
		`[{}]`,
		`[null]`,
		`[] trailing`,
	} {
		actions, state, usedDefault := parseKeybindings([]byte(input))
		if state != voicetools.Unsupported || len(actions) != 0 || usedDefault {
			t.Errorf("parse %q = %#v/%q/default=%t, want unsupported and no fallback", input, actions, state, usedDefault)
		}
	}
}

func TestParseKeybindingsIgnoresUnknownFieldsButValidatesRows(t *testing.T) {
	data := []byte(`[{"command":"composer.startVoiceMode","key":"Ctrl+Shift+V","unknown":{"private":"DROP_ME"}}]`)
	actions, state, usedDefault := parseKeybindings(data)
	if state != voicetools.Ready || usedDefault {
		t.Fatalf("parse state/default = %q/%t, want ready/no default", state, usedDefault)
	}
	encoded, err := json.Marshal(actions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "DROP_ME") || strings.Contains(string(encoded), "unknown") {
		t.Fatalf("unknown row field escaped allowlist: %s", encoded)
	}
	if _, state, usedDefault := parseKeybindings([]byte(`[{"command":"unknown","key":false}]`)); state != voicetools.Unsupported || usedDefault {
		t.Fatalf("invalid key on unknown command = %q/default=%t, want unsupported", state, usedDefault)
	}
}

func TestDiscoverReturnsCancelledStatusWithoutReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := Discover(ctx, voicetools.Locator{})
	if got.State != voicetools.ReadError || got.Source != "cancelled" || len(got.Actions) != 0 {
		t.Fatalf("cancelled discovery = %#v", got)
	}
}

func actionByID(t *testing.T, actions []voicetools.Action, id string) voicetools.Action {
	t.Helper()
	for _, action := range actions {
		if action.ID == id {
			return action
		}
	}
	t.Fatalf("action %q not found in %#v", id, actions)
	return voicetools.Action{}
}

func TestExplicitNullWinsOverOtherRows(t *testing.T) {
	for _, data := range []string{
		`[{"command":"composer.startVoiceMode","key":null},{"command":"composer.startVoiceMode","key":"Ctrl+K"}]`,
		`[{"command":"composer.startVoiceMode","key":"Ctrl+K"},{"command":"composer.startVoiceMode","key":null}]`,
	} {
		actions, state, usedDefault := parseKeybindings([]byte(data))
		action := actionByID(t, actions, "composer.startVoiceMode")
		if state != voicetools.Ready || usedDefault || !action.Disabled {
			t.Fatalf("disabled override not preserved: %+v", action)
		}
	}
}
