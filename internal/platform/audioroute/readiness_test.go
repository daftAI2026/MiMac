package audioroute

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitForInputRequiresExactActiveSingleRoute(t *testing.T) {
	cases := []struct {
		name  string
		value ProcessInput
	}{
		{"absent", ProcessInput{PID: 42}},
		{"active unknown route", ProcessInput{Found: true, PID: 42, RunningInput: true}},
		{"inactive", ProcessInput{Found: true, PID: 42, InputDeviceUIDs: []string{"test-input"}}},
		{"wrong pid", ProcessInput{Found: true, PID: 43, RunningInput: true, InputDeviceUIDs: []string{"test-input"}}},
		{"wrong uid", ProcessInput{Found: true, PID: 42, RunningInput: true, InputDeviceUIDs: []string{"physical-input"}}},
		{"ambiguous", ProcessInput{Found: true, PID: 42, RunningInput: true, InputDeviceUIDs: []string{"test-input", "physical-input"}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls := 0
			err := waitForInput(ctx, 42, "test-input", func(pid int32) (ProcessInput, error) {
				calls++
				if calls == 1 {
					return tt.value, nil
				}
				cancel()
				return ProcessInput{}, nil
			})
			if !errors.Is(err, context.Canceled) || calls != 2 {
				t.Fatalf("accepted unready snapshot: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestWaitForInputMatchesImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	err := waitForInput(ctx, 42, "test-input", func(pid int32) (ProcessInput, error) {
		calls++
		return ProcessInput{Found: true, PID: pid, RunningInput: true, InputDeviceUIDs: []string{"test-input"}}, nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("match: calls=%d err=%v", calls, err)
	}
}

func TestWaitForInputRejectsLateSnapshotAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := waitForInput(ctx, 42, "test-input", func(pid int32) (ProcessInput, error) {
		cancel()
		return ProcessInput{Found: true, PID: pid, RunningInput: true, InputDeviceUIDs: []string{"test-input"}}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("late match accepted: %v", err)
	}
}

func TestWaitForInputPropagatesQueryFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fault := errors.New("Core Audio permission denied")
	calls := 0
	err := waitForInput(ctx, 42, "test-input", func(int32) (ProcessInput, error) { calls++; return ProcessInput{}, fault })
	if !errors.Is(err, fault) || calls != 1 {
		t.Fatalf("query failure hidden: %v calls=%d", err, calls)
	}
}

func TestWaitForInputValidatesBeforeNativeQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cases := []struct {
		ctx  context.Context
		pid  int32
		uid  string
		want error
	}{{nil, 42, "test", ErrReadinessDeadlineRequired}, {context.Background(), 42, "test", ErrReadinessDeadlineRequired}, {ctx, 0, "test", ErrInvalidPID}, {ctx, 42, "", ErrMalformedData}, {ctx, 42, "   ", ErrMalformedData}, {ctx, 42, "a\x00b", ErrMalformedData}, {ctx, 42, "\xff", ErrMalformedData}}
	for _, tt := range cases {
		err := waitForInput(tt.ctx, tt.pid, tt.uid, func(int32) (ProcessInput, error) {
			t.Fatal("invalid request reached native query")
			return ProcessInput{}, nil
		})
		if !errors.Is(err, tt.want) {
			t.Fatalf("validation=%v want %v", err, tt.want)
		}
	}
}

func TestWaitForInputDeadlineEndsUnreadyWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := waitForInput(ctx, 42, "test-input", func(pid int32) (ProcessInput, error) { return ProcessInput{PID: pid}, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline ignored: %v", err)
	}
}
