package runningapp

import (
	"context"
	"errors"
	"testing"
)

func TestLaunchSnapshotKeepsIdentityAndReadinessSeparate(t *testing.T) {
	for _, finished := range []bool{false, true} {
		pid, got, err := launchSnapshot(context.Background(), func(context.Context) (int32, error) { return 42, nil }, func(_ context.Context, pid int32) (bool, error) {
			if pid != 42 {
				t.Fatalf("wrong PID %d", pid)
			}
			return finished, nil
		})
		if err != nil || pid != 42 || got != finished {
			t.Fatalf("snapshot = %d/%v/%v", pid, got, err)
		}
	}
}

func TestLaunchSnapshotRejectsChangedIdentityAndQueryFailure(t *testing.T) {
	calls := 0
	_, _, err := launchSnapshot(context.Background(), func(context.Context) (int32, error) { calls++; return int32(41 + calls), nil }, func(context.Context, int32) (bool, error) { return true, nil })
	if !errors.Is(err, ErrReadFailed) {
		t.Fatalf("changed PID accepted: %v", err)
	}
	want := errors.New("native query failed")
	_, _, err = launchSnapshot(context.Background(), func(context.Context) (int32, error) { return 42, nil }, func(context.Context, int32) (bool, error) { return false, want })
	if !errors.Is(err, want) {
		t.Fatalf("query failure hidden: %v", err)
	}
}

func TestLaunchSnapshotCancellationStopsBeforeNativeRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := launchSnapshot(ctx, func(context.Context) (int32, error) { t.Fatal("called after cancellation"); return 0, nil }, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}
