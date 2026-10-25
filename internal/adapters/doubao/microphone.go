package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"

	"mimac.local/mimac/internal/voicetools"
)

var ErrMicrophoneSelectionUnsupported = errors.New("doubao microphone selection: unverified platform or bundle")

// MicrophoneSelection describes only the persisted selectedMicrophoneId field.
// Automatic means no explicit UID was saved; it does not identify the device
// Doubao or Core Audio currently routes to at runtime.
type MicrophoneSelection struct {
	Automatic bool
	DeviceUID string
}

// ReadMicrophoneSelection reads only the exact selectedMicrophoneId field from
// the verified Doubao 1.0.2 (1) MMKV key d. It uses the same fixed locator as
// shortcut discovery and never writes configuration or returns other fields.
// Absent, null, and empty values mean that no explicit device UID is saved.
func ReadMicrophoneSelection(ctx context.Context, locator voicetools.Locator) (MicrophoneSelection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return MicrophoneSelection{}, err
	}
	if runtime.GOOS != "darwin" {
		return MicrophoneSelection{}, ErrMicrophoneSelectionUnsupported
	}
	if locator.Home == "" {
		return MicrophoneSelection{}, errors.New("doubao microphone selection: home directory unavailable")
	}

	bundlePaths := voicetools.ApplicationCandidates(locator.InputMethodDirs, bundleName)
	bundle, state, err := voicetools.FindBundleInLocator(ctx, locator, bundlePaths, bundleIdentifier)
	if err != nil {
		return MicrophoneSelection{}, fmt.Errorf("doubao microphone selection: verify bundle: %w", err)
	}
	if state != voicetools.Ready || bundle.Version != verifiedShortVersion || bundle.Build != verifiedBuildVersion {
		return MicrophoneSelection{}, fmt.Errorf("%w: expected Doubao %s (%s)", ErrMicrophoneSelectionUnsupported, verifiedShortVersion, verifiedBuildVersion)
	}
	if err := ctx.Err(); err != nil {
		return MicrophoneSelection{}, err
	}

	candidate, err := readDoubaoMMKV(ctx, doubaoMMKVPath(locator.Home), microphoneSelectionField)
	if err != nil {
		return MicrophoneSelection{}, fmt.Errorf("doubao microphone selection: read verified MMKV: %w", err)
	}
	return candidate.microphoneSelection()
}

func (candidate settingsCandidate) microphoneSelection() (MicrophoneSelection, error) {
	selection := MicrophoneSelection{Automatic: true}
	if !candidate.valuePresent {
		return selection, nil
	}
	raw, present := candidate.values[microphoneSelectionField.name]
	if !present || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return selection, nil
	}
	var deviceUID string
	if err := json.Unmarshal(raw, &deviceUID); err != nil {
		return MicrophoneSelection{}, errMicrophoneFieldType
	}
	if deviceUID == "" {
		return selection, nil
	}
	return MicrophoneSelection{DeviceUID: deviceUID}, nil
}
