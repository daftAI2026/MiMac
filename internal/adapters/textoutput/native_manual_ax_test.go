//go:build darwin

package textoutput

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestManualAccessibilityRequiresOriginalForegroundAndSecureEditableState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		targetPID  int32
		frontPID   int32
		windowPID  int32
		secure     bool
		settable   uint8
		settableRC int32
		want       bool
	}{
		{name: "supported frontmost Electron root", targetPID: 55, frontPID: 55, windowPID: 55, settable: 1, settableRC: axErrorSuccess, want: true},
		{name: "no captured target process", frontPID: 55, windowPID: 55, settable: 1, settableRC: axErrorSuccess},
		{name: "foreground changed", targetPID: 55, frontPID: 56, windowPID: 55, settable: 1, settableRC: axErrorSuccess},
		{name: "focused window changed process", targetPID: 55, frontPID: 55, windowPID: 56, settable: 1, settableRC: axErrorSuccess},
		{name: "secure event input", targetPID: 55, frontPID: 55, windowPID: 55, secure: true, settable: 1, settableRC: axErrorSuccess},
		{name: "attribute unsupported", targetPID: 55, frontPID: 55, windowPID: 55, settable: 1, settableRC: axErrorAttrUnsupported},
		{name: "attribute not settable", targetPID: 55, frontPID: 55, windowPID: 55, settableRC: axErrorSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := manualAccessibilityEligible(tc.targetPID, tc.frontPID, tc.windowPID, tc.secure, tc.settableRC, tc.settable); got != tc.want {
				t.Fatalf("manual accessibility eligible = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestManualAccessibilityRequiresStableFocusedWindow(t *testing.T) {
	equal := func(left, right uintptr) uint8 { return 1 }
	if !manualAccessibilityWindowMatches(7, 7, equal) {
		t.Fatal("same focused window was rejected")
	}
	if manualAccessibilityWindowMatches(7, 8, func(left, right uintptr) uint8 { return 0 }) {
		t.Fatal("changed focused window was accepted")
	}
	if manualAccessibilityWindowMatches(0, 8, equal) {
		t.Fatal("missing initial focused window was accepted")
	}
}

func TestManualAccessibilityFocusWaitUsesOnlyReadCallbacksUntilTargetAppears(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		start := time.Now()
		focusCalls, validations := 0, 0
		element, pid, err := waitForManualAccessibilityElement(
			ctx,
			func() error { validations++; return nil },
			func() (uintptr, int32, error) {
				focusCalls++
				if focusCalls < 3 {
					return 0, 0, ErrNoFocusedElement
				}
				return 99, 55, nil
			},
		)
		if err != nil || element != 99 || pid != 55 {
			t.Fatalf("wait result = (%d, %d, %v), want original editable target", element, pid, err)
		}
		if focusCalls != 3 || validations != 3 {
			t.Fatalf("focus/validation calls = %d/%d, want 3/3", focusCalls, validations)
		}
		if elapsed := time.Since(start); elapsed != 2*manualAccessibilityRetryInterval {
			t.Fatalf("elapsed time = %s, want two read retries", elapsed)
		}
	})
}

func TestManualAccessibilityFocusWaitStopsAtTwoSecondDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		focusCalls, validations := 0, 0
		_, _, err := waitForManualAccessibilityElement(
			context.Background(),
			func() error { validations++; return nil },
			func() (uintptr, int32, error) { focusCalls++; return 0, 0, ErrNoFocusedElement },
		)
		if !errors.Is(err, ErrNoFocusedElement) {
			t.Fatalf("deadline result = %v, want ErrNoFocusedElement", err)
		}
		if elapsed := time.Since(start); elapsed != 2*time.Second || elapsed > manualAccessibilityFocusWaitLimit {
			t.Fatalf("focus wait elapsed %s, want maximum %s", elapsed, manualAccessibilityFocusWaitLimit)
		}
		if focusCalls == 0 || validations != focusCalls {
			t.Fatalf("focus/validation calls = %d/%d, want matching bounded read attempts", focusCalls, validations)
		}
	})
}

func TestManualAccessibilityFocusWaitCancellationAndSafetyGates(t *testing.T) {
	t.Run("cancel during wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { time.Sleep(75 * time.Millisecond); cancel() }()
			focusCalls := 0
			_, _, err := waitForManualAccessibilityElement(
				ctx,
				func() error { return nil },
				func() (uintptr, int32, error) { focusCalls++; return 0, 0, ErrNoFocusedElement },
			)
			if !errors.Is(err, context.Canceled) || focusCalls != 2 {
				t.Fatalf("canceled wait = err %v, focus calls %d; want context.Canceled after two reads", err, focusCalls)
			}
		})
	})
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "secure input", err: ErrSecureInput},
		{name: "frontmost process or focused window changed", err: ErrTargetChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				focusCalls := 0
				_, _, err := waitForManualAccessibilityElement(
					context.Background(),
					func() error { return test.err },
					func() (uintptr, int32, error) { focusCalls++; return 0, 0, ErrNoFocusedElement },
				)
				if !errors.Is(err, test.err) || focusCalls != 0 {
					t.Fatalf("safety gate = err %v, focus calls %d; want %v before target read", err, focusCalls, test.err)
				}
			})
		})
	}
}
