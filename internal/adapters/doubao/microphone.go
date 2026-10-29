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

var ErrMicrophoneSelectionUnsupported = errors.New("doubao microphone selection: unsupported platform or application identity")

// MicrophoneSelection describes only the persisted selectedMicrophoneId field.
// Automatic preserves the route/display classification for nil or empty values;
// it does not identify the device Doubao or Core Audio routes to at runtime.
type MicrophoneSelection struct {
	Automatic bool
	DeviceUID string
	// SelectedMicrophoneID preserves the stored Optional<String> value. A nil
	// pointer means the field was absent or null; a non-nil pointer to "" is
	// distinct and must be restored as an explicitly present empty string.
	SelectedMicrophoneID *string `json:"selectedMicrophoneId,omitempty"`
}

// ReadMicrophoneSelection reads only the exact selectedMicrophoneId field from
// the CRC-verified Doubao MMKV key d. It uses the same fixed locator as
// shortcut discovery and never writes configuration or returns other fields.
// Automatic retains the route display meaning for nil and empty values;
// SelectedMicrophoneID preserves the original optional string for restoration.
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
	if state != voicetools.Ready || bundle.Identifier != bundleIdentifier {
		return MicrophoneSelection{}, ErrMicrophoneSelectionUnsupported
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
	selection.SelectedMicrophoneID = &deviceUID
	if deviceUID == "" {
		return selection, nil
	}
	selection.Automatic = false
	selection.DeviceUID = deviceUID
	return selection, nil
}
