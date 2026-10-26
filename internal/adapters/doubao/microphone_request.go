package doubao

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unicode/utf8"

	"mimac.local/mimac/internal/voicetools"
)

const (
	selectedMicrophoneNotificationName = "DoubaoImeSettings.selectedMicrophoneId"
	selectedMicrophoneUserInfoKey      = "selectedMicrophoneId"
)

// ErrInvalidMicrophoneSelectionRequest reports a selected ID that cannot be
// represented safely as a CoreFoundation UTF-8 string.
var ErrInvalidMicrophoneSelectionRequest = errors.New("doubao microphone request: selectedMicrophoneId must be valid UTF-8 without NUL")

// MicrophoneSelectionRequest is a one-way request to update Doubao's selected
// microphone setting. It is not an acknowledgement that the target applied it.
type MicrophoneSelectionRequest struct {
	selectedMicrophoneID *string
}

// NewMicrophoneSelectionRequest copies the Optional ID into an immutable
// request value. A nil ID omits userInfo entirely; a pointer to "" sends an
// explicit empty string.
func NewMicrophoneSelectionRequest(selectedMicrophoneID *string) (MicrophoneSelectionRequest, error) {
	if selectedMicrophoneID == nil {
		return MicrophoneSelectionRequest{}, nil
	}
	value := *selectedMicrophoneID
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return MicrophoneSelectionRequest{}, ErrInvalidMicrophoneSelectionRequest
	}
	return MicrophoneSelectionRequest{selectedMicrophoneID: &value}, nil
}

// UserInfo returns a fresh payload map for inspection or testing. A nil map
// represents the nil Optional and contains no selectedMicrophoneId key.
func (request MicrophoneSelectionRequest) UserInfo() map[string]string {
	if request.selectedMicrophoneID == nil {
		return nil
	}
	return map[string]string{selectedMicrophoneUserInfoKey: *request.selectedMicrophoneID}
}

type microphoneRequestVerifier func(context.Context, voicetools.Locator) error
type microphoneRequestPoster func(context.Context, MicrophoneSelectionRequest) error

// RequestMicrophoneSelection validates the Doubao 1.0.2 (1) bundle in locator
// and posts its selectedMicrophoneId distributed notification. A nil return
// means the local post call completed; the notification has no target ACK and
// does not confirm that the in-memory value or effective route changed.
func RequestMicrophoneSelection(ctx context.Context, locator voicetools.Locator, selectedMicrophoneID *string) error {
	return requestMicrophoneSelection(ctx, locator, selectedMicrophoneID, verifyMicrophoneRequestTarget, postSelectedMicrophoneIDNotification)
}

func requestMicrophoneSelection(
	ctx context.Context,
	locator voicetools.Locator,
	selectedMicrophoneID *string,
	verify microphoneRequestVerifier,
	post microphoneRequestPoster,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if verify == nil || post == nil {
		return errors.New("doubao microphone request: verifier and poster are required")
	}
	request, err := NewMicrophoneSelectionRequest(selectedMicrophoneID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verify(ctx, locator); err != nil {
		return fmt.Errorf("doubao microphone request: verify target: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := post(ctx, request); err != nil {
		return fmt.Errorf("doubao microphone request: post selected-ID notification: %w", err)
	}
	return nil
}

func verifyMicrophoneRequestTarget(ctx context.Context, locator voicetools.Locator) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return ErrMicrophoneSelectionUnsupported
	}
	if locator.Home == "" {
		return errors.New("doubao microphone request: home directory unavailable")
	}
	candidates := voicetools.ApplicationCandidates(locator.InputMethodDirs, bundleName)
	if len(candidates) == 0 {
		return ErrMicrophoneSelectionUnsupported
	}
	bundle, state, err := voicetools.FindBundleInLocator(ctx, locator, candidates, bundleIdentifier)
	if err != nil {
		return fmt.Errorf("verify Doubao bundle: %w", err)
	}
	if state != voicetools.Ready || bundle.Identifier != bundleIdentifier || bundle.Version != verifiedShortVersion || bundle.Build != verifiedBuildVersion {
		return fmt.Errorf("%w: expected Doubao %s (%s)", ErrMicrophoneSelectionUnsupported, verifiedShortVersion, verifiedBuildVersion)
	}
	return ctx.Err()
}
