package main

import (
	"context"
	"errors"
	"testing"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/voicetools"
)

func TestPreparedInputSelectionPrecedesDeviceStartupAndPressesNeverRewrite(t *testing.T) {
	saved := "original-input"
	prepared, verified := false, 0
	state := &preparedSelectionState{current: &saved}
	route, _, _, _, requests, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), &saved, nil, nil, nil,
		state.configure,
		func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
			request, open := deps.requestSelection, deps.openDeviceOwner
			deps.prepareSelection = func(ctx context.Context, locator voicetools.Locator, uid *string) error {
				prepared = true
				return request(ctx, locator, uid)
			}
			deps.verifySelection = func(_ context.Context, _ voicetools.Locator, uid *string) error {
				if uid == nil || *uid != compatAudioDeviceUID {
					t.Fatal("wrong prepared UID")
				}
				verified++
				return nil
			}
			deps.openDeviceOwner = func(generation uint64, path string) (compatATVVOwner, error) {
				if !prepared {
					t.Fatal("device startup preceded microphone preparation")
				}
				return open(generation, path)
			}
		})
	for range 2 {
		if err := route.BeforeStart(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if values := requests.snapshot(); len(values) != 1 || values[0] == nil || *values[0] != compatAudioDeviceUID {
		t.Fatalf("press rewrote selection: %v", values)
	}
	if verified != 2 {
		t.Fatalf("verified %d presses, want 2", verified)
	}
	closeCompatRouteForTest(t, route)
	values := requests.snapshot()
	if len(values) != 2 || values[1] == nil || *values[1] != saved {
		t.Fatalf("saved selection not restored: %v", values)
	}
}

func TestPreparedInputFailureKeepsRestorationLeaseReachable(t *testing.T) {
	for _, stage := range []string{"confirmation", "cancelled", "device-open"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("prepare failed")
			inspector := newCompatRouteTestInspector()
			identity := &compatRouteTestIdentity{}
			requests := &compatRouteTestRequest{}
			ownerOpens := 0
			var current *string
			request := func(ctx context.Context, locator voicetools.Locator, uid *string) error {
				current = cloneMicrophoneOptional(uid)
				return requests.request(ctx, locator, uid)
			}
			deps := compatAudioRouteDependencies{
				newInspector: func() (compatAudioInspector, error) { return inspector, nil },
				readSelection: func(context.Context, voicetools.Locator) (compatAudioSelection, error) {
					return compatAudioSelection{preferredUID: cloneMicrophoneOptional(current)}, nil
				},
				captureIdentity:  func(int32, string) (compatProcessIdentity, error) { return identity, nil },
				requestSelection: request,
				prepareSelection: func(ctx context.Context, locator voicetools.Locator, uid *string) error {
					if err := request(ctx, locator, uid); err != nil {
						return err
					}
					if stage == "confirmation" {
						return failure
					}
					if stage == "cancelled" {
						cancel()
					}
					return nil
				},
				verifySelection: func(context.Context, voicetools.Locator, *string) error { return nil },
				openDeviceOwner: func(uint64, string) (compatATVVOwner, error) { ownerOpens++; return nil, failure },
				openSink:        func(audiooutput.Config) (xiaomiPreparedSink, error) { return &compatRouteTestSink{}, nil },
			}
			locator := voicetools.Locator{Home: "/fixture", InputMethodDirs: []string{"/fixture"}}
			route, err := openLiveXiaomiRouteWithDependencies(ctx, 7, "/fixture/libMiMacATVV.dylib", compatDoubaoBundleID,
				"/fixture/DoubaoIme.app/Contents/MacOS/DoubaoIme", locator, deps)
			wantErr := failure
			if stage == "cancelled" {
				wantErr = context.Canceled
			}
			if route == nil || !errors.Is(err, wantErr) {
				t.Fatalf("partial preparation = %T, %v", route, err)
			}
			if stage != "device-open" && ownerOpens != 0 {
				t.Fatal("opened device after failed preference preparation")
			}
			if err := route.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			values := requests.snapshot()
			if len(values) != 2 || values[0] == nil || *values[0] != compatAudioDeviceUID || values[1] != nil {
				t.Fatalf("partial preparation did not restore original auto preference: %v", values)
			}
		})
	}
}

func TestPreparedInputWarmCaptureCannotPassThePressIdleGuard(t *testing.T) {
	checks := 0
	state := &preparedSelectionState{}
	route, inspector, _, _, requests, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil,
		state.configure,
		func(deps *compatAudioRouteDependencies, inspector *compatRouteTestInspector) {
			request := deps.requestSelection
			deps.prepareSelection = func(ctx context.Context, locator voicetools.Locator, uid *string) error {
				inspector.setRunning(true)
				return request(ctx, locator, uid)
			}
			deps.verifySelection = func(context.Context, voicetools.Locator, *string) error { checks++; return nil }
		})
	if err := route.BeforeStart(context.Background()); !errors.Is(err, errCompatRouteTargetBusy) {
		t.Fatalf("warm capture accepted: %v", err)
	}
	if checks != 0 {
		t.Fatal("verified preference before idle guard")
	}
	inspector.setRunning(false)
	if err := route.BeforeStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requests.snapshot()) != 1 {
		t.Fatal("press requested capture reconfiguration")
	}
	closeCompatRouteForTest(t, route)
}
