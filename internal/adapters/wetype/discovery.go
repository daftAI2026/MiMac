package wetype

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/voicetools"
)

const (
	pttActionID           = "voicePTTShortcut"
	toggleActionID        = "voiceToggleShortcut"
	pttKeyCodesField      = "voicePTTShortcut_keyCodes"
	pttModifiersField     = "voicePTTShortcut_modifiers"
	pttTapCountField      = "voicePTTShortcut_tapCount"
	toggleKeyCodesField   = "voiceToggleShortcut_keyCodes"
	toggleModifiersField  = "voiceToggleShortcut_modifiers"
	toggleTapCountField   = "voiceToggleShortcut_tapCount"
	keyCodeArrayValueType = "MMKV string containing JSON [Int]"
	int32ValueType        = "MMKV Int32"
	boolValueType         = "MMKV Bool"
	voiceToggleActionName = "启动语音输入（任意键结束）"
)

// Discover reads only the installed WeType reader's allowlisted MMKV fields.
// It exposes Fn PTT only for the confirmed [63]/0/1 tuple and uses the
// observed schema default only when every shortcut field is present.
func Discover(ctx context.Context, locator voicetools.Locator) voicetools.Snapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	result := voicetools.Snapshot{State: voicetools.ReadError, Source: "discovery"}
	if err := ctx.Err(); err != nil {
		result.Source = "cancelled"
		return result
	}
	if runtime.GOOS != "darwin" {
		result.State = voicetools.Unsupported
		result.Source = "macOS bundle metadata is required"
		return result
	}
	if locator.Home == "" {
		result.Source = "home directory unavailable"
		return result
	}

	candidate, bundle, bundleState, err := readInstalled(ctx, locator)
	result.AppPath = bundle.Path
	result.Version = formatBundleVersion(bundle.Version, bundle.Build)
	if bundle.Version == "" {
		result.Version = ""
	}
	if bundleState == voicetools.Ready && matchesBundleIdentity(bundle) {
		result.SettingsPath = settingsPath(locator)
	}
	if err != nil {
		if ctx.Err() != nil {
			result.State = voicetools.ReadError
			result.Source = "cancelled"
			return result
		}
		result.State = discoveryErrorState(err, bundleState)
		result.Source = "WeType MMKV discovery or read failed"
		if bundleState == voicetools.NotFound {
			result.Source = "WeType.app bundle identity"
		} else if errors.Is(err, voicetools.ErrFileMissing) {
			result.Source = "wetype.settings MMKV store"
		}
		return result
	}

	result.Source = "wetype.settings#voicePTTShortcut_*, voiceToggleShortcut_* and voiceShortcutGlobalEnabled"
	if allShortcutFieldsMissing(candidate) {
		result.State = voicetools.ConfigMissing
		result.Source = "WeType MMKV store has no stored shortcut fields"
		return result
	}

	scope := shortcutScope(candidate)
	result.Actions = []voicetools.Action{
		wetypeAction(candidate, scope, pttActionID, "按住说话", pttKeyCodesField, pttModifiersField, pttTapCountField),
		wetypeAction(candidate, scope, toggleActionID, voiceToggleActionName, toggleKeyCodesField, toggleModifiersField, toggleTapCountField),
	}
	result.State = voicetools.Ready
	return result
}

func discoveryErrorState(err error, bundleState voicetools.State) voicetools.State {
	switch {
	case bundleState == voicetools.Ambiguous, errors.Is(err, ErrAmbiguousBundle):
		return voicetools.Ambiguous
	case errors.Is(err, ErrUnsupportedPlatform),
		errors.Is(err, ErrUnsupportedFormat), errors.Is(err, ErrEncryptedStore),
		errors.Is(err, ErrMalformedValue), errors.Is(err, ErrMalformedRecord),
		errors.Is(err, voicetools.ErrUnsupportedFile), errors.Is(err, voicetools.ErrFileTooLarge):
		return voicetools.Unsupported
	case bundleState == voicetools.NotFound:
		return voicetools.NotFound
	case bundleState == voicetools.Unsupported:
		return voicetools.Unsupported
	case errors.Is(err, voicetools.ErrFileMissing):
		return voicetools.ConfigMissing
	default:
		return voicetools.ReadError
	}
}

func allShortcutFieldsMissing(candidate Candidate) bool {
	for _, name := range []string{
		pttKeyCodesField, pttModifiersField, pttTapCountField,
		toggleKeyCodesField, toggleModifiersField, toggleTapCountField,
	} {
		field, ok := candidate.Fields[name]
		if !ok || field.Status != "missing" {
			return false
		}
	}
	return true
}

func wetypeAction(candidate Candidate, scope, actionID, name, keyCodes, modifiers, tapCount string) voicetools.Action {
	shortcut := map[string]FieldCandidate{
		keyCodes:  candidate.Fields[keyCodes],
		modifiers: candidate.Fields[modifiers],
		tapCount:  candidate.Fields[tapCount],
	}
	raw, _ := json.Marshal(shortcut)
	action := voicetools.Action{
		ID:         actionID,
		Name:       name,
		Scope:      scope,
		Gesture:    "unknown",
		Candidates: []voicetools.Candidate{{Raw: string(raw)}},
	}
	if actionID == pttActionID && isFnPTT(candidate, keyCodes, modifiers, tapCount) {
		action.Gesture = string(bridge.ActionHold)
		action.Control = &voicetools.Control{
			Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish,
			AllowFnKey: true,
		}
		action.Candidates[0].Shortcut = "Fn"
		action.Candidates[0].Supported = true
	}
	return action
}

func shortcutScope(candidate Candidate) string {
	field, ok := candidate.Fields[voiceShortcutGlobalEnabledField]
	if ok && field.Status == "present" {
		if field.Type != boolValueType {
			return "unknown"
		}
		var enabled bool
		if len(field.Value) == 0 || json.Unmarshal(field.Value, &enabled) != nil || !enabled {
			return "unknown"
		}
		return "global"
	}
	if ok && field.Status == "missing" && allSixShortcutFieldsPresentAndWellFormed(candidate) {
		// WeType 3.0.0/670 reads this MMKV Bool with native default true.
		// The observed native default is evidence for the missing-key schema;
		// missing shortcut fields must never inherit the default scope.
		return "global"
	}
	return "unknown"
}

func allSixShortcutFieldsPresentAndWellFormed(candidate Candidate) bool {
	for _, spec := range allowlistedFields {
		if spec.name == voiceShortcutGlobalEnabledField {
			continue
		}
		field, ok := candidate.Fields[spec.name]
		if !ok || field.Status != "present" || field.Type != spec.valueType || len(field.Value) == 0 {
			return false
		}
		if spec.jsonInts {
			var codes []int
			if json.Unmarshal(field.Value, &codes) != nil || codes == nil {
				return false
			}
		} else {
			var value int32
			if json.Unmarshal(field.Value, &value) != nil {
				return false
			}
		}
	}
	return true
}

func isFnPTT(candidate Candidate, keyCodesKey, modifiersKey, tapCountKey string) bool {
	codesField, codesOK := candidate.Fields[keyCodesKey]
	modifiersField, modifiersOK := candidate.Fields[modifiersKey]
	tapCountField, tapCountOK := candidate.Fields[tapCountKey]
	if !codesOK || !modifiersOK || !tapCountOK ||
		codesField.Status != "present" || modifiersField.Status != "present" || tapCountField.Status != "present" ||
		codesField.Type != keyCodeArrayValueType || modifiersField.Type != int32ValueType || tapCountField.Type != int32ValueType {
		return false
	}
	var codes []int
	var modifiers, tapCount int32
	return json.Unmarshal(codesField.Value, &codes) == nil && len(codes) == 1 && codes[0] == 63 &&
		json.Unmarshal(modifiersField.Value, &modifiers) == nil && modifiers == 0 &&
		json.Unmarshal(tapCountField.Value, &tapCount) == nil && tapCount == 1
}

func formatBundleVersion(version, build string) string {
	if version == "" {
		return ""
	}
	if build == "" || build == version {
		return version
	}
	return version + " (" + build + ")"
}
