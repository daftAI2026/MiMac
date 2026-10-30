package wetype

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unicode/utf8"

	"mimac.local/mimac/internal/platform/distributednotification"
	"mimac.local/mimac/internal/voicetools"
)

const microphoneSelectionNotificationName = "Settings2WeType_channel"

const (
	maxPreferredMicrophoneUIDBytes    = 4 << 10
	maxMicrophoneSelectionPayloadSize = 64 << 10
)

var (
	ErrMicrophoneSelectionUnsupported = errors.New("wetype microphone selection: unsupported platform or application identity")
	ErrInvalidMicrophoneSelection     = errors.New("wetype microphone selection: UID must be valid UTF-8 without NUL")
	ErrMicrophoneSelectionTooLarge    = errors.New("wetype microphone selection: UID exceeds the request size limit")
)

// MicrophoneSelectionRequest is a one-way request to update WeType's preferred
// microphone. Posting it does not confirm that WeType applied the selection or
// that an audio route is ready.
type MicrophoneSelectionRequest struct {
	object string
}

// NewMicrophoneSelectionRequest constructs the observed type 66 payload.
// A nil or empty UID requests WeType's automatic selection behavior; a
// non-empty UID is preserved exactly.
func NewMicrophoneSelectionRequest(preferredDeviceUID *string) (MicrophoneSelectionRequest, error) {
	var uid string
	if preferredDeviceUID != nil {
		uid = *preferredDeviceUID
		if !utf8.ValidString(uid) || strings.IndexByte(uid, 0) >= 0 {
			return MicrophoneSelectionRequest{}, ErrInvalidMicrophoneSelection
		}
		if len(uid) > maxPreferredMicrophoneUIDBytes {
			return MicrophoneSelectionRequest{}, ErrMicrophoneSelectionTooLarge
		}
	}
	payload, err := json.Marshal(struct {
		Type int    `json:"type"`
		Args string `json:"args"`
	}{Type: 66, Args: uid})
	if err != nil {
		return MicrophoneSelectionRequest{}, fmt.Errorf("wetype microphone selection: encode type 66 payload: %w", err)
	}
	if len(payload) > maxMicrophoneSelectionPayloadSize {
		return MicrophoneSelectionRequest{}, ErrMicrophoneSelectionTooLarge
	}
	return MicrophoneSelectionRequest{object: base64.StdEncoding.EncodeToString(payload)}, nil
}

// NotificationObject returns a fresh string containing the Base64-encoded JSON
// value posted as the distributed notification's object.
func (request MicrophoneSelectionRequest) NotificationObject() string {
	return request.object
}

type microphoneRequestVerifier func(context.Context, voicetools.Locator) error
type microphoneRequestPoster func(context.Context, string, string) error

// RequestMicrophoneSelection verifies the exact WeType bundle identity and
// posts type 66 with nil userInfo. A nil return reports only that the local
// distributed-notification post call completed; WeType sends no ACK for this
// request.
func RequestMicrophoneSelection(ctx context.Context, locator voicetools.Locator, preferredDeviceUID *string) error {
	return requestMicrophoneSelection(ctx, locator, preferredDeviceUID, verifyMicrophoneRequestTarget, postMicrophoneSelectionNotification)
}

func requestMicrophoneSelection(
	ctx context.Context,
	locator voicetools.Locator,
	preferredDeviceUID *string,
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
		return errors.New("wetype microphone selection: verifier and poster are required")
	}
	request, err := NewMicrophoneSelectionRequest(preferredDeviceUID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verify(ctx, locator); err != nil {
		return fmt.Errorf("wetype microphone selection: verify target: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := post(ctx, microphoneSelectionNotificationName, request.NotificationObject()); err != nil {
		return fmt.Errorf("wetype microphone selection: post type 66 notification: %w", err)
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
		return ErrBundleUnavailable
	}
	bundle, state, err := LocateApplication(ctx, locator)
	if err != nil {
		return fmt.Errorf("locate WeType bundle: %w", err)
	}
	if state != voicetools.Ready || !matchesBundleIdentity(bundle) {
		return ErrMicrophoneSelectionUnsupported
	}
	return ctx.Err()
}

func postMicrophoneSelectionNotification(ctx context.Context, name, object string) error {
	return distributednotification.PostObject(ctx, name, object)
}
