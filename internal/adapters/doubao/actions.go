package doubao

import (
	"bytes"
	"encoding/json"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/voicetools"
)

const requiredInputSourceID = "com.bytedance.inputmethod.doubaoime.pinyin"

type shortcutContract struct {
	id, name, prefix string
	keyCode          uint64
	modifierFlags    uint64
	keyDisplay       string
	shortcut         string
	gesture          string
	control          voicetools.Control
}

var verifiedShortcutContracts = []shortcutContract{
	{
		id: "asrShortcut", name: "听写快捷键", prefix: "asrShortcut",
		keyCode: 61, modifierFlags: 0, keyDisplay: "rightoption", shortcut: "RightOption", gesture: "toggle",
		control: voicetools.Control{
			Trigger: bridge.ActionTap, Request: bridge.RequestToggle, ReleaseRequest: bridge.RequestNone,
		},
	},
	{
		id: "asrLongPressShortcut", name: "听写长按快捷键", prefix: "asrLongPressShortcut",
		keyCode: 54, modifierFlags: 1048592, keyDisplay: "RightCommand", shortcut: "RightCommand", gesture: "hold",
		control: voicetools.Control{
			Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish,
		},
	},
}

// RequiredInputSourceID is a runtime precondition for the exact shortcut
// contracts verified in this adapter. Global describes the key's scope, not
// availability under every selected input source. The host must check this
// ID before beginning an action; hold release still uses its frozen plan.
func RequiredInputSourceID(actionID string) (string, bool) {
	for _, contract := range verifiedShortcutContracts {
		if contract.id == actionID {
			return requiredInputSourceID, true
		}
	}
	return "", false
}

func (candidate settingsCandidate) action(id, name, prefix string) voicetools.Action {
	contract, ok := shortcutContractFor(id, prefix)
	if !ok {
		return voicetools.Action{ID: id, Name: name, Scope: "unknown", Gesture: "unverified"}
	}
	return normalizeAction(candidate, contract)
}

func shortcutContractFor(id, prefix string) (shortcutContract, bool) {
	for _, contract := range verifiedShortcutContracts {
		if contract.id == id && contract.prefix == prefix {
			return contract, true
		}
	}
	return shortcutContract{}, false
}

func normalizeAction(candidate settingsCandidate, contract shortcutContract) voicetools.Action {
	fields := make(map[string]json.RawMessage, 3)
	for _, suffix := range []string{"KeyCode", "ModifierFlags", "KeyDisplay"} {
		field := contract.prefix + suffix
		if raw, exists := candidate.values[field]; exists {
			fields[field] = raw
		}
	}

	action := voicetools.Action{
		ID: contract.id, Name: contract.name, Scope: "unknown", Gesture: "unverified",
	}
	if len(fields) == 0 {
		return action
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		raw = []byte("{}")
	}
	shortcut := candidate.strings[contract.prefix+"KeyDisplay"]
	candidateValue := voicetools.Candidate{Raw: string(raw), Shortcut: shortcut}
	action.Candidates = []voicetools.Candidate{candidateValue}

	keyCode, keyOK := storedUnsignedInteger(fields[contract.prefix+"KeyCode"])
	modifierFlags, flagsOK := storedUnsignedInteger(fields[contract.prefix+"ModifierFlags"])
	display, displayPresent := candidate.strings[contract.prefix+"KeyDisplay"]
	if !keyOK || !flagsOK || !displayPresent ||
		keyCode != contract.keyCode || modifierFlags != contract.modifierFlags || display != contract.keyDisplay {
		return action
	}

	// Scope is the target shortcut's global keyboard scope. RequiredInputSourceID
	// remains a separate runtime gate and is deliberately not inferred from this
	// static configuration normalization.
	action.Scope = "global"
	action.Gesture = contract.gesture
	control := contract.control
	action.Control = &control
	action.Candidates[0].Shortcut = contract.shortcut
	action.Candidates[0].Supported = true
	return action
}

func storedUnsignedInteger(raw json.RawMessage) (uint64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, false
	}
	var value uint64
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return 0, false
	}
	return value, true
}
