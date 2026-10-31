package wetype

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mimac.local/mimac/internal/voicetools"
)

const microphoneSelectionPollInterval = 20 * time.Millisecond
const microphoneSelectionConfirmationTimeout = 1500 * time.Millisecond

var (
	// ErrMicrophoneSelectionChanged means the stored preference no longer
	// matches the value expected by the caller.
	ErrMicrophoneSelectionChanged = errors.New("wetype microphone selection changed")
	// ErrMicrophoneSelectionConfirmationTimeout marks a deadline while waiting
	// for the stored preference to match the requested value.
	ErrMicrophoneSelectionConfirmationTimeout = errors.New("wetype microphone selection confirmation timed out")
)

type microphoneSelectionReadFunc func(context.Context, voicetools.Locator) (*string, error)
type microphoneSelectionRequestFunc func(context.Context, voicetools.Locator, *string) error
type microphoneSelectionWaitFunc func(context.Context, time.Duration) error

type microphonePreparationDeps struct {
	read    microphoneSelectionReadFunc
	request microphoneSelectionRequestFunc
	wait    microphoneSelectionWaitFunc
}

func defaultMicrophonePreparationDeps() microphonePreparationDeps {
	return microphonePreparationDeps{
		read:    ReadPreferredMicrophoneUID,
		request: RequestMicrophoneSelection,
		wait:    waitForMicrophoneSelectionPoll,
	}
}

// RequestMicrophoneSelectionConfirmed requests WeType's preferred microphone
// only when the stored preference differs, then waits for the allowlisted
// MMKV value to match. This confirms configuration only; it does not confirm
// that WeType's live Core Audio route is ready or delivering PCM.
func RequestMicrophoneSelectionConfirmed(ctx context.Context, locator voicetools.Locator, preferredDeviceUID *string) error {
	return requestMicrophoneSelectionConfirmed(ctx, locator, preferredDeviceUID, defaultMicrophonePreparationDeps())
}

func requestMicrophoneSelectionConfirmed(
	ctx context.Context,
	locator voicetools.Locator,
	preferredDeviceUID *string,
	deps microphonePreparationDeps,
) error {
	ctx, cancel := microphoneSelectionConfirmationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return microphoneSelectionContextError(err)
	}
	if _, err := NewMicrophoneSelectionRequest(preferredDeviceUID); err != nil {
		return err
	}
	if deps.read == nil || deps.request == nil || deps.wait == nil {
		return errors.New("wetype microphone preparation: reader, requester, and waiter are required")
	}

	current, err := deps.read(ctx, locator)
	if err != nil {
		return microphoneSelectionOperationError("wetype microphone preparation: read current preference", err)
	}
	if err := ctx.Err(); err != nil {
		return microphoneSelectionContextError(err)
	}
	if sameAutomaticOrUID(current, preferredDeviceUID) {
		return nil
	}
	if err := deps.request(ctx, locator, cloneMicrophoneUID(preferredDeviceUID)); err != nil {
		return microphoneSelectionOperationError("wetype microphone preparation: request preferred microphone", err)
	}
	if err := ctx.Err(); err != nil {
		return microphoneSelectionContextError(err)
	}

	for {
		current, err = deps.read(ctx, locator)
		if err != nil {
			return microphoneSelectionOperationError("wetype microphone preparation: read preference confirmation", err)
		}
		if err := ctx.Err(); err != nil {
			return microphoneSelectionContextError(err)
		}
		if sameAutomaticOrUID(current, preferredDeviceUID) {
			return nil
		}
		if err := deps.wait(ctx, microphoneSelectionPollInterval); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return microphoneSelectionContextError(ctxErr)
			}
			return microphoneSelectionOperationError("wetype microphone preparation: wait for preference confirmation", err)
		}
	}
}

// VerifyMicrophoneSelection reads the stored preference once and never posts
// a notification. A nil and empty UID both mean WeType's automatic choice.
func VerifyMicrophoneSelection(ctx context.Context, locator voicetools.Locator, preferredDeviceUID *string) error {
	return verifyMicrophoneSelection(ctx, locator, preferredDeviceUID, ReadPreferredMicrophoneUID)
}

func verifyMicrophoneSelection(
	ctx context.Context,
	locator voicetools.Locator,
	preferredDeviceUID *string,
	read microphoneSelectionReadFunc,
) error {
	ctx = microphonePreparationContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := NewMicrophoneSelectionRequest(preferredDeviceUID); err != nil {
		return err
	}
	if read == nil {
		return errors.New("wetype microphone preparation: reader is required")
	}
	current, err := read(ctx, locator)
	if err != nil {
		return fmt.Errorf("wetype microphone preparation: read preference: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !sameAutomaticOrUID(current, preferredDeviceUID) {
		return ErrMicrophoneSelectionChanged
	}
	return nil
}

func microphonePreparationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func microphoneSelectionConfirmationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = microphonePreparationContext(ctx)
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, microphoneSelectionConfirmationTimeout)
}

func microphoneSelectionContextError(err error) error {
	return microphoneSelectionOperationError("wetype microphone preparation", err)
}

func microphoneSelectionOperationError(operation string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w: %w", operation, ErrMicrophoneSelectionConfirmationTimeout, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func sameAutomaticOrUID(left, right *string) bool {
	leftAutomatic := left == nil || *left == ""
	rightAutomatic := right == nil || *right == ""
	if leftAutomatic || rightAutomatic {
		return leftAutomatic && rightAutomatic
	}
	return *left == *right
}

func cloneMicrophoneUID(uid *string) *string {
	if uid == nil {
		return nil
	}
	copy := *uid
	return &copy
}

func waitForMicrophoneSelectionPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
