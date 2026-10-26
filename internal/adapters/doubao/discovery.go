// Package doubao reads narrowly allowlisted fields from the verified Doubao
// MMKV configuration.
package doubao

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"

	"mimac.local/mimac/internal/voicetools"
)

const (
	bundleIdentifier     = "com.bytedance.inputmethod.doubaoime"
	verifiedShortVersion = "1.0.2"
	verifiedBuildVersion = "1"
	bundleName           = "DoubaoIme.app"
	mmkvDirectorySuffix  = "Library/Application Support/DoubaoIme/.com.apple.xpc.activityd"
	mmkvDataName         = "com.apple.xpc.activity"
)

type fieldSpec struct {
	name      string
	plistType string
}

var shortcutFields = []fieldSpec{
	{name: "asrShortcutKeyCode", plistType: "integer"},
	{name: "asrShortcutModifierFlags", plistType: "integer"},
	{name: "asrShortcutKeyDisplay", plistType: "string"},
	{name: "asrLongPressShortcutKeyCode", plistType: "integer"},
	{name: "asrLongPressShortcutModifierFlags", plistType: "integer"},
	{name: "asrLongPressShortcutKeyDisplay", plistType: "string"},
}

var microphoneSelectionField = fieldSpec{name: "selectedMicrophoneId", plistType: "string"}

var errShortcutFieldType = errors.New("doubao config: shortcut field type unsupported")
var errMicrophoneFieldType = errors.New("doubao config: selectedMicrophoneId must be a string or null")

// Discover requires the exact verified Doubao bundle and its one observed
// MMKV path. It never consults plist fallbacks or bundled defaults.
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
		result.Source = "macOS input method bundle metadata is required"
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
		result.Source = "Doubao bundle metadata could not be read"
		return result
	}
	if state != voicetools.Ready {
		result.State = state
		result.AppPath = bundle.Path
		result.Source = "DoubaoIme.app bundle identity"
		return result
	}
	result.AppPath = bundle.Path
	result.Version = formatBundleVersion(bundle.Version, bundle.Build)
	if bundle.Version != verifiedShortVersion || bundle.Build != verifiedBuildVersion {
		result.State = voicetools.Unsupported
		result.Source = "unverified Doubao version"
		return result
	}

	settingsPath := doubaoMMKVPath(locator.Home)
	result.SettingsPath = settingsPath
	candidate, err := readDoubaoMMKV(ctx, settingsPath)
	if err != nil {
		if errors.Is(err, voicetools.ErrFileMissing) {
			result.State = voicetools.ConfigMissing
			result.Source = "Doubao MMKV configuration was not found"
			return result
		}
		result.State = settingsErrorState(err)
		if errors.Is(err, errShortcutFieldType) {
			result.Source = "a whitelisted shortcut field has an unsupported type"
		} else if errors.Is(err, voicetools.ErrUnsupportedFile) || errors.Is(err, voicetools.ErrFileTooLarge) {
			result.Source = "Doubao MMKV format is unsupported"
		} else if ctx.Err() != nil {
			result.Source = "cancelled"
		} else {
			result.Source = "Doubao MMKV configuration could not be read safely"
		}
		return result
	}
	if err := ctx.Err(); err != nil {
		result.State = voicetools.ReadError
		result.Source = "cancelled"
		return result
	}
	if !candidate.valuePresent {
		result.State = voicetools.ConfigMissing
		result.Source = "Doubao MMKV key d is absent or cleared"
		return result
	}
	if !candidate.hasShortcutField {
		result.State = voicetools.Unsupported
		result.Source = "MMKV key d found; shortcut field layout is unverified"
		return result
	}

	result.State = voicetools.Ready
	result.Source = "Doubao MMKV key d; exact 1.0.2 (1) shortcut tuples; host TIS preflight required"
	result.Actions = []voicetools.Action{
		candidate.action("asrShortcut", "听写快捷键", "asrShortcut"),
		candidate.action("asrLongPressShortcut", "听写长按快捷键", "asrLongPressShortcut"),
	}
	return result
}

type settingsCandidate struct {
	values           map[string]json.RawMessage
	strings          map[string]string
	valuePresent     bool
	hasShortcutField bool
}

func doubaoMMKVPath(home string) string {
	return filepath.Join(home, mmkvDirectorySuffix, mmkvDataName)
}

func settingsErrorState(err error) voicetools.State {
	if errors.Is(err, errShortcutFieldType) || errors.Is(err, voicetools.ErrUnsupportedFile) || errors.Is(err, voicetools.ErrFileTooLarge) {
		return voicetools.Unsupported
	}
	return voicetools.ReadError
}

func formatBundleVersion(version, build string) string {
	if build == "" || build == version {
		return version
	}
	return version + " (" + build + ")"
}
