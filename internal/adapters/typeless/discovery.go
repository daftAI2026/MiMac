package typeless

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/voicetools"
)

const (
	bundleIdentifier         = "now.typeless.desktop"
	bundleName               = "Typeless.app"
	verifiedShortVersion     = "2.8.3"
	verifiedBuildVersion     = "2.8.3.132"
	typelessSettingsRelative = "Library/Application Support/Typeless/app-settings.json"
)

// Discover reads only Typeless shortcut bindings from the known app-settings
// file. It never loads Typeless code, uses compiled defaults, or writes files.
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

	bundle, state, err := LocateApplication(ctx, locator)
	if err != nil {
		result.State = state
		result.AppPath = bundle.Path
		result.Source = "bundle metadata could not be read"
		return result
	}
	if state != voicetools.Ready {
		result.State = state
		result.AppPath = bundle.Path
		result.Source = "Typeless.app bundle identity"
		return result
	}
	result.AppPath = bundle.Path
	result.Version = formatBundleVersion(bundle.Version, bundle.Build)
	if bundle.Version != verifiedShortVersion || bundle.Build != verifiedBuildVersion {
		result.State = voicetools.Unsupported
		result.Source = "unverified Typeless version"
		return result
	}

	settingsPath := filepath.Join(locator.Home, typelessSettingsRelative)
	result.SettingsPath = settingsPath
	result.Source = "app-settings.json#featureShortcutBindings (default userData candidate)"
	info, err := os.Lstat(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		result.State = voicetools.ConfigMissing
		return result
	}
	if err != nil {
		result.State = voicetools.ReadError
		result.Source = "settings file metadata"
		return result
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		result.State = voicetools.Unsupported
		result.Source = "settings path is not a regular file"
		return result
	}
	if err := ctx.Err(); err != nil {
		result.State = voicetools.ReadError
		result.Source = "cancelled"
		return result
	}

	config, err := ReadFile(settingsPath)
	if err != nil {
		result.State = typelessReadState(err)
		result.Source = "app-settings.json read"
		return result
	}
	if err := ctx.Err(); err != nil {
		result.State = voicetools.ReadError
		result.Source = "cancelled"
		return result
	}
	if config.FeatureShortcutBindings.Status != StatusPresent {
		result.State = voicetools.Unsupported
		result.Source = "featureShortcutBindings is missing or unsupported"
		return result
	}

	normalized := Normalize(config)
	actions := make([]voicetools.Action, 0, 3)
	for _, action := range normalized[:3] {
		if action.Status == StatusMalformed ||
			action.Status == StatusUnknown || action.Status == StatusUnsupported {
			result.State = voicetools.Unsupported
			result.Source = "known shortcut field is malformed or unsupported"
			return result
		}

		toolAction := voicetools.Action{
			ID:      string(action.ID),
			Name:    action.Label,
			Scope:   "global",
			Gesture: typelessGesture(action.ID),
			Control: typelessControl(action.ID),
		}
		for _, candidate := range action.Candidates {
			toolAction.Candidates = append(toolAction.Candidates, voicetools.Candidate{
				Raw:       candidate.Raw,
				Shortcut:  candidate.Shortcut,
				Supported: candidate.Status == StatusPresent,
			})
		}
		actions = append(actions, toolAction)
	}

	result.State = voicetools.Ready
	result.Actions = actions
	return result
}

func formatBundleVersion(version, build string) string {
	if build == "" || build == version {
		return version
	}
	return version + " (" + build + ")"
}

func typelessGesture(id ActionID) string {
	switch id {
	case ActionDictation:
		return "tap-to-start-stop"
	case ActionAskAnything, ActionTranslation:
		return "tap-to-start; end via main Typeless shortcut"
	default:
		return "unknown"
	}
}

func typelessReadState(err error) voicetools.State {
	var readErr *ReadError
	if !errors.As(err, &readErr) {
		return voicetools.ReadError
	}
	switch readErr.Code() {
	case ErrorTooLarge, ErrorMalformedJSON, ErrorUnsupportedRoot, ErrorUnsupportedFile:
		return voicetools.Unsupported
	default:
		return voicetools.ReadError
	}
}

// These are requests from the reviewed shortcut contract, not confirmation
// that a running target has started, stopped, or submitted recognized text.
func typelessControl(id ActionID) *voicetools.Control {
	request := bridge.RequestStart
	switch id {
	case ActionDictation:
		request = bridge.RequestToggle
	case ActionAskAnything, ActionTranslation:
	default:
		return nil
	}
	return &voicetools.Control{Trigger: bridge.ActionTap, Request: request, ReleaseRequest: bridge.RequestNone}
}
