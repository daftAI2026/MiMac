package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/adapters/wetype"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/platform/processidentity"
	"mimac.local/mimac/internal/voicetools"
)

// This fixture models the target's preference, including writes made outside
// MiMac. A request log alone cannot detect an unwanted restoration on Close.
type preparedSelectionState struct {
	current *string
	readErr error
}

func (s *preparedSelectionState) configure(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
	request := deps.requestSelection
	deps.readSelection = func(context.Context, voicetools.Locator) (compatAudioSelection, error) {
		return compatAudioSelection{preferredUID: cloneMicrophoneOptional(s.current)}, s.readErr
	}
	deps.requestSelection = func(ctx context.Context, locator voicetools.Locator, uid *string) error {
		if err := request(ctx, locator, uid); err != nil {
			return err
		}
		s.current = cloneMicrophoneOptional(uid)
		return nil
	}
	deps.prepareSelection = deps.requestSelection
	deps.verifySelection = func(context.Context, voicetools.Locator, *string) error {
		if s.readErr != nil {
			return s.readErr
		}
		if s.current == nil || *s.current != compatAudioDeviceUID {
			return wetype.ErrMicrophoneSelectionChanged
		}
		return nil
	}
}

func TestConfirmedSelectionClosePreservesExternalChoice(t *testing.T) {
	external, automatic := "user-headset", ""
	for name, choice := range map[string]*string{"headset": &external, "unset": nil, "automatic": &automatic} {
		t.Run(name, func(t *testing.T) {
			saved := "original-input"
			state := &preparedSelectionState{current: &saved}
			route, _, _, owner, requests, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), &saved, nil, nil, nil, state.configure)
			state.current = cloneMicrophoneOptional(choice)
			if err := route.BeforeStart(context.Background()); !errors.Is(err, wetype.ErrMicrophoneSelectionChanged) {
				t.Fatalf("external change accepted: %v", err)
			}
			for range 2 {
				closeCompatRouteForTest(t, route)
			}
			if !reflect.DeepEqual(state.current, choice) || len(requests.snapshot()) != 1 {
				t.Fatalf("Close overwrote external choice: current=%v requests=%v", state.current, requests.snapshot())
			}
			if _, closes := owner.counts(); closes != 1 {
				t.Fatalf("owner closed %d times", closes)
			}
		})
	}
}

func TestConfirmedSelectionCloseReadFailureRetainsLease(t *testing.T) {
	saved := "original-input"
	state := &preparedSelectionState{current: &saved}
	route, _, _, owner, requests, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), &saved, nil, nil, nil, state.configure)
	failure := errors.New("preference temporarily unreadable")
	state.readErr = failure
	if err := route.Close(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("unreadable preference discarded: %v", err)
	}
	if len(requests.snapshot()) != 1 || route.isLeaseDone() {
		t.Fatal("read failure restored or retired an unknown preference")
	}
	state.readErr = nil
	closeCompatRouteForTest(t, route)
	if !reflect.DeepEqual(state.current, &saved) || len(requests.snapshot()) != 2 {
		t.Fatal("retry did not restore the still-owned preference")
	}
	if _, closes := owner.counts(); closes != 1 {
		t.Fatalf("owner closed %d times", closes)
	}
}

func TestConfirmedSelectionCloseRetryPreservesLaterExternalChoice(t *testing.T) {
	saved, external := "original-input", "user-headset"
	state := &preparedSelectionState{current: &saved}
	route, inspector, _, owner, requests, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), &saved, nil, nil, nil, state.configure)
	inspector.setRunning(true)
	if err := route.Close(context.Background()); !errors.Is(err, audiosession.ErrCleanupPending) {
		t.Fatalf("busy close = %v", err)
	}
	state.current = &external
	inspector.setRunning(false)
	closeCompatRouteForTest(t, route)
	if !reflect.DeepEqual(state.current, &external) || len(requests.snapshot()) != 1 {
		t.Fatal("retry overwrote the later external choice")
	}
	if _, closes := owner.counts(); closes != 1 {
		t.Fatalf("owner closed %d times", closes)
	}
}

func TestConfirmedSelectionCloseRechecksTargetAfterPreferenceRead(t *testing.T) {
	for _, change := range []string{"busy", "identity"} {
		t.Run(change, func(t *testing.T) {
			saved := "original-input"
			state := &preparedSelectionState{current: &saved}
			route, inspector, identity, _, requests, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), &saved, nil, nil, nil, state.configure)
			read := route.readSelection
			route.readSelection = func(ctx context.Context, locator voicetools.Locator) (compatAudioSelection, error) {
				if change == "busy" {
					inspector.setRunning(true)
				} else {
					identity.setError(processidentity.ErrIdentityChanged)
				}
				return read(ctx, locator)
			}
			err := route.Close(context.Background())
			if change == "busy" && !errors.Is(err, errCompatRouteTargetBusy) {
				t.Fatalf("new capture not guarded: %v", err)
			}
			if change == "identity" && !errors.Is(err, processidentity.ErrIdentityChanged) {
				t.Fatalf("changed identity not guarded: %v", err)
			}
			if len(requests.snapshot()) != 1 {
				t.Fatal("restored after target changed during preference read")
			}
			// A busy original target retains recovery. A changed identity is
			// retired on the cleanup retry and cannot receive a later write.
			route.readSelection = read
			inspector.setRunning(false)
			closeCompatRouteForTest(t, route)
			want := 1
			if change == "busy" {
				want = 2
			}
			if len(requests.snapshot()) != want {
				t.Fatal("wrong restoration behavior on cleanup retry")
			}
		})
	}
}
