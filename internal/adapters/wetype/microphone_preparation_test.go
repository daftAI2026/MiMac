package wetype

import (
	"context"
	"errors"
	"testing"
	"time"

	"mimac.local/mimac/internal/voicetools"
)

func TestRequestMicrophoneSelectionConfirmedSkipsMatchingPreference(t *testing.T) {
	uid := "device-uid"
	reads, requests, waits := 0, 0, 0
	err := requestMicrophoneSelectionConfirmed(context.Background(), voicetools.Locator{}, &uid, microphonePreparationDeps{
		read: func(context.Context, voicetools.Locator) (*string, error) {
			reads++
			return &uid, nil
		},
		request: func(context.Context, voicetools.Locator, *string) error {
			requests++
			return nil
		},
		wait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	})
	if err != nil || reads != 1 || requests != 0 || waits != 0 {
		t.Fatalf("result/read/request/wait = %v/%d/%d/%d, want nil/1/0/0", err, reads, requests, waits)
	}
}

func TestRequestMicrophoneSelectionConfirmedWaitsForAsynchronousPreference(t *testing.T) {
	oldUID, desiredUID := "old-device", "device-uid"
	reads, requests, waits := 0, 0, 0
	err := requestMicrophoneSelectionConfirmed(context.Background(), voicetools.Locator{}, &desiredUID, microphonePreparationDeps{
		read: func(context.Context, voicetools.Locator) (*string, error) {
			reads++
			if requests == 0 || reads == 2 {
				return &oldUID, nil
			}
			return &desiredUID, nil
		},
		request: func(_ context.Context, _ voicetools.Locator, got *string) error {
			requests++
			if got == nil || *got != desiredUID {
				t.Fatalf("requested UID = %#v, want %q", got, desiredUID)
			}
			return nil
		},
		wait: func(_ context.Context, interval time.Duration) error {
			waits++
			if interval != 20*time.Millisecond {
				t.Fatalf("poll interval = %s, want 20ms", interval)
			}
			return nil
		},
	})
	if err != nil || reads != 3 || requests != 1 || waits != 1 {
		t.Fatalf("result/read/request/wait = %v/%d/%d/%d, want nil/3/1/1", err, reads, requests, waits)
	}
}

func TestRequestMicrophoneSelectionConfirmedCancellationReturns(t *testing.T) {
	oldUID, desiredUID := "old-device", "device-uid"
	ctx, cancel := context.WithCancel(context.Background())
	reads, requests, waits := 0, 0, 0
	err := requestMicrophoneSelectionConfirmed(ctx, voicetools.Locator{}, &desiredUID, microphonePreparationDeps{
		read: func(context.Context, voicetools.Locator) (*string, error) {
			reads++
			return &oldUID, nil
		},
		request: func(context.Context, voicetools.Locator, *string) error {
			requests++
			return nil
		},
		wait: func(context.Context, time.Duration) error {
			waits++
			cancel()
			return context.Canceled
		},
	})
	if !errors.Is(err, context.Canceled) || reads != 2 || requests != 1 || waits != 1 {
		t.Fatalf("result/read/request/wait = %v/%d/%d/%d, want canceled/2/1/1", err, reads, requests, waits)
	}
}

func TestRequestMicrophoneSelectionConfirmedReturnsReadError(t *testing.T) {
	oldUID, desiredUID := "old-device", "device-uid"
	wantErr := errors.New("MMKV read failed")
	reads, requests := 0, 0
	err := requestMicrophoneSelectionConfirmed(context.Background(), voicetools.Locator{}, &desiredUID, microphonePreparationDeps{
		read: func(context.Context, voicetools.Locator) (*string, error) {
			reads++
			if reads == 1 {
				return &oldUID, nil
			}
			return nil, wantErr
		},
		request: func(context.Context, voicetools.Locator, *string) error {
			requests++
			return nil
		},
		wait: func(context.Context, time.Duration) error {
			t.Fatal("wait called after a preference read error")
			return nil
		},
	})
	if !errors.Is(err, wantErr) || reads != 2 || requests != 1 {
		t.Fatalf("result/read/request = %v/%d/%d, want wrapped read error/2/1", err, reads, requests)
	}
}

func TestRequestMicrophoneSelectionConfirmedAutomaticUIDsAreEquivalent(t *testing.T) {
	empty := ""
	for _, test := range []struct {
		name string
		got  *string
		want *string
	}{
		{name: "nil stored, empty requested", want: &empty},
		{name: "empty stored, nil requested", got: &empty},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads, requests := 0, 0
			err := requestMicrophoneSelectionConfirmed(context.Background(), voicetools.Locator{}, test.want, microphonePreparationDeps{
				read: func(context.Context, voicetools.Locator) (*string, error) {
					reads++
					return test.got, nil
				},
				request: func(context.Context, voicetools.Locator, *string) error {
					requests++
					return nil
				},
				wait: waitForMicrophoneSelectionPoll,
			})
			if err != nil || reads != 1 || requests != 0 {
				t.Fatalf("result/read/request = %v/%d/%d, want nil/1/0", err, reads, requests)
			}
		})
	}
}

func TestRequestMicrophoneSelectionConfirmedReturnsDeadlineWhenUnmatched(t *testing.T) {
	oldUID, desiredUID := "old-device", "device-uid"
	waitErr := context.DeadlineExceeded
	reads, requests, waits := 0, 0, 0
	err := requestMicrophoneSelectionConfirmed(context.Background(), voicetools.Locator{}, &desiredUID, microphonePreparationDeps{
		read: func(context.Context, voicetools.Locator) (*string, error) {
			reads++
			return &oldUID, nil
		},
		request: func(context.Context, voicetools.Locator, *string) error {
			requests++
			return nil
		},
		wait: func(context.Context, time.Duration) error {
			waits++
			return waitErr
		},
	})
	if !errors.Is(err, waitErr) || !errors.Is(err, ErrMicrophoneSelectionConfirmationTimeout) || reads != 2 || requests != 1 || waits != 1 {
		t.Fatalf("result/read/request/wait = %v/%d/%d/%d, want deadline/2/1/1", err, reads, requests, waits)
	}
}

func TestRequestMicrophoneSelectionConfirmedAddsDeadlineWhenMissing(t *testing.T) {
	oldUID, desiredUID := "old-device", "device-uid"
	started := time.Now()
	var observedDeadline time.Time
	err := requestMicrophoneSelectionConfirmed(context.Background(), voicetools.Locator{}, &desiredUID, microphonePreparationDeps{
		read:    func(context.Context, voicetools.Locator) (*string, error) { return &oldUID, nil },
		request: func(context.Context, voicetools.Locator, *string) error { return nil },
		wait: func(ctx context.Context, _ time.Duration) error {
			var ok bool
			observedDeadline, ok = ctx.Deadline()
			if !ok {
				t.Fatal("confirmation context has no deadline")
			}
			return context.DeadlineExceeded
		},
	})
	if !errors.Is(err, ErrMicrophoneSelectionConfirmationTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want distinguishable confirmation timeout", err)
	}
	if observedDeadline.Before(started.Add(microphoneSelectionConfirmationTimeout)) || observedDeadline.After(time.Now().Add(microphoneSelectionConfirmationTimeout)) {
		t.Fatalf("confirmation deadline = %s, want within %s of call start", observedDeadline, microphoneSelectionConfirmationTimeout)
	}
}

func TestRequestMicrophoneSelectionConfirmedRespectsCallerDeadline(t *testing.T) {
	oldUID, desiredUID := "old-device", "device-uid"
	wantDeadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), wantDeadline)
	defer cancel()
	var observedDeadline time.Time
	err := requestMicrophoneSelectionConfirmed(ctx, voicetools.Locator{}, &desiredUID, microphonePreparationDeps{
		read:    func(context.Context, voicetools.Locator) (*string, error) { return &oldUID, nil },
		request: func(context.Context, voicetools.Locator, *string) error { return nil },
		wait: func(ctx context.Context, _ time.Duration) error {
			observedDeadline, _ = ctx.Deadline()
			return context.DeadlineExceeded
		},
	})
	if !errors.Is(err, ErrMicrophoneSelectionConfirmationTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want distinguishable confirmation timeout", err)
	}
	if !observedDeadline.Equal(wantDeadline) {
		t.Fatalf("confirmation deadline = %s, want caller deadline %s", observedDeadline, wantDeadline)
	}
}

func TestVerifyMicrophoneSelectionReadsOnceAndNeverRequests(t *testing.T) {
	uid, other := "device-uid", "other-device"
	for _, test := range []struct {
		name    string
		current *string
		want    *string
		wantErr error
	}{
		{name: "matching exact UID", current: &uid, want: &uid},
		{name: "nil and empty automatic match", current: nil, want: func() *string { v := ""; return &v }()},
		{name: "changed", current: &other, want: &uid, wantErr: ErrMicrophoneSelectionChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			err := verifyMicrophoneSelection(context.Background(), voicetools.Locator{}, test.want,
				func(context.Context, voicetools.Locator) (*string, error) {
					reads++
					return test.current, nil
				})
			if reads != 1 || !errors.Is(err, test.wantErr) {
				t.Fatalf("read count/error = %d/%v, want 1/%v", reads, err, test.wantErr)
			}
		})
	}
}

func TestVerifyMicrophoneSelectionPropagatesReadError(t *testing.T) {
	wantErr := errors.New("MMKV unavailable")
	reads := 0
	err := verifyMicrophoneSelection(context.Background(), voicetools.Locator{}, nil,
		func(context.Context, voicetools.Locator) (*string, error) {
			reads++
			return nil, wantErr
		})
	if reads != 1 || !errors.Is(err, wantErr) {
		t.Fatalf("read count/error = %d/%v, want 1/wrapped read error", reads, err)
	}
}
