package shandian

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"

	"mimac.local/mimac/internal/voicetools"
)

// Discover reads only the verified Shandianshuo config fields exposed by the
// typed reader. The stored actions can be named, but their press timing and
// execution contract remain unknown until independently verified.
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
	if bundleState == voicetools.Ready && bundle.Identifier == bundleIdentifier &&
		bundle.Version == verifiedShortVersion && bundle.Build == verifiedBuildVersion {
		result.SettingsPath = configPath(locator)
	}
	if err != nil {
		if ctx.Err() != nil {
			result.State = voicetools.ReadError
			result.Source = "cancelled"
			return result
		}
		result.State = discoveryErrorState(err, bundleState)
		result.Source = "Shandianshuo configuration discovery or read failed"
		switch {
		case bundleState == voicetools.NotFound:
			result.Source = "闪电说.app bundle identity"
		case errors.Is(err, ErrUnverifiedBundle):
			result.Source = "unverified Shandianshuo version"
		case errors.Is(err, ErrConfigMissing):
			result.Source = "Shandianshuo config.json"
		case errors.Is(err, ErrUnsupportedSchema):
			result.Source = "unverified config schema"
		}
		return result
	}

	result.Source = "config.json#unified_hotkey,short_press_action,long_press_action"
	result.Actions = []voicetools.Action{
		shandianAction("short_press_action", candidate.ShortPressAction, candidate.UnifiedHotkey),
		shandianAction("long_press_action", candidate.LongPressAction, candidate.UnifiedHotkey),
	}
	result.State = voicetools.Ready
	return result
}

func discoveryErrorState(err error, bundleState voicetools.State) voicetools.State {
	switch {
	case bundleState == voicetools.Ambiguous, errors.Is(err, ErrAmbiguousBundle):
		return voicetools.Ambiguous
	case errors.Is(err, ErrUnsupportedPlatform), errors.Is(err, ErrUnverifiedBundle),
		errors.Is(err, ErrUnsupportedConfig), errors.Is(err, ErrUnsupportedSchema),
		errors.Is(err, ErrMalformedConfig), errors.Is(err, voicetools.ErrUnsupportedFile),
		errors.Is(err, voicetools.ErrFileTooLarge):
		return voicetools.Unsupported
	case bundleState == voicetools.NotFound:
		return voicetools.NotFound
	case bundleState == voicetools.Unsupported:
		return voicetools.Unsupported
	case errors.Is(err, ErrConfigMissing):
		return voicetools.ConfigMissing
	default:
		return voicetools.ReadError
	}
}

func shandianAction(id, rawAction string, hotkey UnifiedHotkey) voicetools.Action {
	action := voicetools.Action{
		ID:      id,
		Name:    actionName(rawAction),
		Scope:   "unknown",
		Gesture: "unknown",
	}
	if len(hotkey.Keys) == 0 {
		return action
	}
	rawCandidate, _ := json.Marshal(struct {
		RawAction     string        `json:"raw_action"`
		UnifiedHotkey UnifiedHotkey `json:"unified_hotkey"`
	}{RawAction: rawAction, UnifiedHotkey: hotkey})
	action.Candidates = []voicetools.Candidate{{Raw: string(rawCandidate)}}
	return action
}

func actionName(raw string) string {
	switch raw {
	case "VoiceInput":
		return "语音输入"
	case "VoiceAssistant":
		return "语音助手"
	default:
		return "未识别的语音动作"
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
