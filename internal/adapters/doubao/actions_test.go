package doubao

import (
	"encoding/json"
	"strings"
	"testing"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/voicetools"
)

func TestNormalizeVerifiedShortcutContracts(t *testing.T) {
	candidate := shortcutCandidate(t, map[string]any{
		"asrShortcutKeyCode":                uint64(61),
		"asrShortcutModifierFlags":          uint64(0),
		"asrShortcutKeyDisplay":             "rightoption",
		"asrLongPressShortcutKeyCode":       uint64(54),
		"asrLongPressShortcutModifierFlags": uint64(1048592),
		"asrLongPressShortcutKeyDisplay":    "RightCommand",
	})

	tests := []struct {
		id, name, prefix, shortcut, gesture string
		control                             voicetools.Control
	}{
		{
			id: "asrShortcut", name: "听写快捷键", prefix: "asrShortcut",
			shortcut: "RightOption", gesture: "toggle",
			control: voicetools.Control{Trigger: bridge.ActionTap, Request: bridge.RequestToggle, ReleaseRequest: bridge.RequestNone},
		},
		{
			id: "asrLongPressShortcut", name: "听写长按快捷键", prefix: "asrLongPressShortcut",
			shortcut: "RightCommand", gesture: "hold",
			control: voicetools.Control{Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish},
		},
	}

	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			got := candidate.action(test.id, test.name, test.prefix)
			if got.Scope != "global" || got.Gesture != test.gesture || got.Control == nil || *got.Control != test.control {
				t.Fatalf("normalized action = %#v", got)
			}
			if len(got.Candidates) != 1 || !got.Candidates[0].Supported || got.Candidates[0].Shortcut != test.shortcut {
				t.Fatalf("normalized candidate = %#v", got.Candidates)
			}
			if !strings.Contains(got.Candidates[0].Raw, test.prefix+"KeyCode") || !strings.Contains(got.Candidates[0].Raw, test.prefix+"ModifierFlags") || !strings.Contains(got.Candidates[0].Raw, test.prefix+"KeyDisplay") {
				t.Fatalf("candidate raw did not preserve its three allowlisted fields: %s", got.Candidates[0].Raw)
			}
			if sourceID, ok := RequiredInputSourceID(test.id); !ok || sourceID != requiredInputSourceID {
				t.Fatalf("input-source requirement = %q, %t", sourceID, ok)
			}
		})
	}
	if _, ok := RequiredInputSourceID("unknownAction"); ok {
		t.Fatal("unknown action returned an input-source requirement")
	}
}

func TestNormalizeRejectsDisplayThatDoesNotMatchVerifiedTuple(t *testing.T) {
	candidate := shortcutCandidate(t, map[string]any{
		"asrShortcutKeyCode":       uint64(61),
		"asrShortcutModifierFlags": uint64(0),
		"asrShortcutKeyDisplay":    "Escape",
	})

	got := candidate.action("asrShortcut", "听写快捷键", "asrShortcut")
	if got.Scope != "unknown" || got.Gesture != "unverified" || got.Control != nil || len(got.Candidates) != 1 || got.Candidates[0].Supported {
		t.Fatalf("normalizer accepted a display value outside the verified tuple: %#v", got)
	}
	if !strings.Contains(got.Candidates[0].Raw, `"asrShortcutKeyDisplay":"Escape"`) {
		t.Fatalf("candidate raw did not preserve the display field: %s", got.Candidates[0].Raw)
	}
}

func TestNormalizeRejectsUnknownOrIncompleteTuples(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]any
		id     string
		label  string
		prefix string
	}{
		{
			name: "right option flags changed", id: "asrShortcut", label: "听写快捷键", prefix: "asrShortcut",
			values: map[string]any{
				"asrShortcutKeyCode": uint64(61), "asrShortcutModifierFlags": uint64(1), "asrShortcutKeyDisplay": "rightoption",
			},
		},
		{
			name: "right command side flags removed", id: "asrLongPressShortcut", label: "听写长按快捷键", prefix: "asrLongPressShortcut",
			values: map[string]any{
				"asrLongPressShortcutKeyCode": uint64(54), "asrLongPressShortcutModifierFlags": uint64(0), "asrLongPressShortcutKeyDisplay": "RightCommand",
			},
		},
		{
			name: "right command flags changed", id: "asrLongPressShortcut", label: "听写长按快捷键", prefix: "asrLongPressShortcut",
			values: map[string]any{
				"asrLongPressShortcutKeyCode": uint64(54), "asrLongPressShortcutModifierFlags": uint64(1048593), "asrLongPressShortcutKeyDisplay": "RightCommand",
			},
		},
		{
			name: "unknown key code", id: "asrShortcut", label: "听写快捷键", prefix: "asrShortcut",
			values: map[string]any{
				"asrShortcutKeyCode": uint64(49), "asrShortcutModifierFlags": uint64(0), "asrShortcutKeyDisplay": "rightoption",
			},
		},
		{
			name: "missing display", id: "asrShortcut", label: "听写快捷键", prefix: "asrShortcut",
			values: map[string]any{"asrShortcutKeyCode": uint64(61), "asrShortcutModifierFlags": uint64(0)},
		},
		{
			name: "null key code", id: "asrShortcut", label: "听写快捷键", prefix: "asrShortcut",
			values: map[string]any{"asrShortcutKeyCode": nil, "asrShortcutModifierFlags": uint64(0), "asrShortcutKeyDisplay": "rightoption"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := shortcutCandidate(t, test.values).action(test.id, test.label, test.prefix)
			if got.Scope != "unknown" || got.Gesture != "unverified" || got.Control != nil || len(got.Candidates) != 1 || got.Candidates[0].Supported {
				t.Fatalf("unsupported tuple was normalized: %#v", got)
			}
		})
	}
}

func shortcutCandidate(t *testing.T, values map[string]any) settingsCandidate {
	t.Helper()
	encodedValues := make(map[string]json.RawMessage, len(values))
	stringsByField := make(map[string]string)
	for field, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		encodedValues[field] = encoded
		if strings.HasSuffix(field, "KeyDisplay") && value != nil {
			text, ok := value.(string)
			if !ok {
				t.Fatalf("test display %s has type %T", field, value)
			}
			stringsByField[field] = text
		}
	}
	return settingsCandidate{values: encodedValues, strings: stringsByField, valuePresent: true, hasShortcutField: len(values) != 0}
}
