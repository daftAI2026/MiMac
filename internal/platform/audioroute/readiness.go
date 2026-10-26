package audioroute

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrReadinessDeadlineRequired = errors.New("audioroute: input readiness requires a context deadline")
var ErrInputRouteMismatch = errors.New("audioroute: active input route does not match requested UID")

// WaitForInput observes a previously identified consumer PID until Core Audio
// reports running input with exactly the requested device UID. It never opens
// a stream, selects a device, launches an app, or changes system defaults.
//
// The caller must pin the consumer's identity and supply a bounded context.
// Success is a routing snapshot, not a lease or proof of PCM delivery/ASR.
// Multi-device input is ambiguous and does not satisfy this predicate.
// Backends that do not expose process input UIDs (including the tested native
// process-tap endpoint) remain unconfirmed; activity alone is never accepted.
// Polling exists only during this bounded wait; an immediate match allocates
// no timer. Native property calls remain synchronous and may outlive ctx;
// cancellation is rechecked before accepting their result.
func (i *Inspector) WaitForInput(ctx context.Context, pid int32, uid string) error {
	return waitForInput(ctx, pid, uid, i.ProcessInput)
}

func waitForInput(ctx context.Context, pid int32, uid string, inspect func(int32) (ProcessInput, error)) error {
	if ctx == nil {
		return ErrReadinessDeadlineRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrReadinessDeadlineRequired
	}
	if pid <= 0 {
		return ErrInvalidPID
	}
	if strings.TrimSpace(uid) == "" || strings.ContainsRune(uid, '\x00') || !utf8.ValidString(uid) {
		return fmt.Errorf("%w: invalid input UID", ErrMalformedData)
	}
	var ticker *time.Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, err := inspect(pid)
		if cancelled := ctx.Err(); cancelled != nil {
			return cancelled
		}
		if err != nil {
			return fmt.Errorf("audioroute: observe input readiness: %w", err)
		}
		if snapshot.Found && snapshot.PID == pid && snapshot.RunningInput {
			if len(snapshot.InputDeviceUIDs) == 1 && snapshot.InputDeviceUIDs[0] == uid {
				return nil
			}
			if len(snapshot.InputDeviceUIDs) > 0 {
				return fmt.Errorf("%w: expected UID %q, got %q", ErrInputRouteMismatch, uid, snapshot.InputDeviceUIDs)
			}
		}
		if ticker == nil {
			ticker = time.NewTicker(20 * time.Millisecond)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
