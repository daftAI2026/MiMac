package wetype

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"

	"mimac.local/mimac/internal/voicetools"
)

// Discover reads only the installed WeType reader's allowlisted MMKV fields.
// It reports shortcut data for display, but does not infer scope, gesture,
// key names, or a command contract from the stored values.
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

	result.Source = "wetype.settings#voicePTTShortcut_* and voiceToggleShortcut_*"
	if allFieldsMissing(candidate) {
		result.State = voicetools.ConfigMissing
		result.Source = "WeType MMKV store has no stored shortcut fields"
		return result
	}

	result.Actions = []voicetools.Action{
		wetypeAction(candidate, "voicePTTShortcut", "按住说话", "voicePTTShortcut_keyCodes", "voicePTTShortcut_modifiers", "voicePTTShortcut_tapCount"),
		wetypeAction(candidate, "voiceToggleShortcut", "切换听写", "voiceToggleShortcut_keyCodes", "voiceToggleShortcut_modifiers", "voiceToggleShortcut_tapCount"),
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

func allFieldsMissing(candidate Candidate) bool {
	if len(candidate.Fields) != len(allowlistedFields) {
		return false
	}
	for _, field := range candidate.Fields {
		if field.Status != "missing" {
			return false
		}
	}
	return true
}

func wetypeAction(candidate Candidate, actionID, name, keyCodes, modifiers, tapCount string) voicetools.Action {
	shortcut := map[string]FieldCandidate{
		keyCodes:  candidate.Fields[keyCodes],
		modifiers: candidate.Fields[modifiers],
		tapCount:  candidate.Fields[tapCount],
	}
	raw, _ := json.Marshal(shortcut)
	return voicetools.Action{
		ID:      actionID,
		Name:    name,
		Scope:   "unknown",
		Gesture: "unknown",
		Candidates: []voicetools.Candidate{{
			Raw: string(raw),
		}},
	}
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
