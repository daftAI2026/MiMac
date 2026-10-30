package main

import (
	"context"
	"errors"
	"testing"

	"mimac.local/mimac/internal/platform/audioroute"
)

func TestApplicationAudioInspectorColdClientIsIdleButNotReceiverReady(t *testing.T) {
	base := newCompatRouteTestInspector()
	base.processes, base.input = nil, audioroute.ProcessInput{PID: 42}
	waitError := errors.New("actual receiver is not ready")
	inspector := &compatApplicationInspector{
		compatAudioInspector: applicationInspectorWaitFailure{base, waitError},
		bundleID:             "test.inputmethod",
		resolve:              func(context.Context) (int32, error) { return 42, nil },
	}
	processes, err := inspector.InputsForBundle("test.inputmethod")
	if err != nil || len(processes) != 1 || !processes[0].Found || processes[0].PID != 42 {
		t.Fatalf("cold process discovery = %+v, %v", processes, err)
	}
	input, err := inspector.ProcessInput(42)
	if err != nil || !input.Found || input.RunningInput || len(input.InputDeviceUIDs) != 0 {
		t.Fatalf("cold input = %+v, %v", input, err)
	}
	if err := inspector.WaitForInput(context.Background(), 42, compatAudioDeviceUID); !errors.Is(err, waitError) {
		t.Fatalf("cold client bypassed real receiver gate: %v", err)
	}
	_, pid, uid, calls, _ := base.snapshot()
	if calls != 1 || pid != 42 || uid != compatAudioDeviceUID {
		t.Fatalf("receiver gate = %d/%q/%d", pid, uid, calls)
	}
}

type applicationInspectorWaitFailure struct {
	*compatRouteTestInspector
	err error
}

func (i applicationInspectorWaitFailure) WaitForInput(ctx context.Context, pid int32, uid string) error {
	_ = i.compatRouteTestInspector.WaitForInput(ctx, pid, uid)
	return i.err
}

func TestApplicationAudioInspectorRejectsCompetingOrUnknownClients(t *testing.T) {
	for _, tc := range []struct {
		name         string
		clients      []audioroute.ProcessInput
		resolveError error
	}{
		{"other PID", []audioroute.ProcessInput{{Found: true, PID: 43}}, nil},
		{"multiple clients", []audioroute.ProcessInput{{Found: true, PID: 42}, {Found: true, PID: 43}}, nil},
		{"unknown identity", nil, errors.New("cannot read application identity")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newCompatRouteTestInspector()
			base.processes = tc.clients
			i := &compatApplicationInspector{base, "test.inputmethod", func(context.Context) (int32, error) { return 42, tc.resolveError }}
			if _, err := i.ProcessInput(42); err == nil {
				t.Fatal("unknown/competing target was accepted as idle")
			}
			if err := i.WaitForInput(context.Background(), 42, compatAudioDeviceUID); err == nil {
				t.Fatal("unknown/competing receiver was accepted")
			}
		})
	}
}

func TestApplicationAudioInspectorPreservesActiveInputAndProcessRetirement(t *testing.T) {
	base := newCompatRouteTestInspector()
	base.input.RunningInput, base.input.InputDeviceUIDs = true, []string{"other-device"}
	pid := int32(42)
	i := &compatApplicationInspector{base, "test.inputmethod", func(context.Context) (int32, error) { return pid, nil }}
	input, err := i.ProcessInput(42)
	if err != nil || !input.RunningInput || len(input.InputDeviceUIDs) != 1 || input.InputDeviceUIDs[0] != "other-device" {
		t.Fatalf("active input lost: %+v %v", input, err)
	}
	base.processes = nil
	pid = 43
	input, err = i.ProcessInput(42)
	if err != nil || input.Found {
		t.Fatalf("replacement reused captured PID: %+v %v", input, err)
	}
	pid = 0
	processes, err := i.InputsForBundle("test.inputmethod")
	if err != nil || len(processes) != 0 {
		t.Fatalf("retired target remained: %+v %v", processes, err)
	}
}

func TestCompatApplicationRouteCanPrepareAndRestoreBeforeFirstAudioClient(t *testing.T) {
	target := compatAudioRouteTarget{bundleID: "com.tencent.inputmethod.wetype", bundleName: "WeType.app", executableName: "WeType", name: "WeType"}
	saved := "old-device"
	route, _, _, _, requests, _, _ := compatRouteTestFixtureForTarget(t, target, &saved, nil, nil, nil, func(deps *compatAudioRouteDependencies, base *compatRouteTestInspector) {
		base.processes, base.input = nil, audioroute.ProcessInput{PID: 42}
		deps.newInspector = func() (compatAudioInspector, error) {
			return &compatApplicationInspector{base, target.bundleID, func(context.Context) (int32, error) { return 42, nil }}, nil
		}
	})
	if err := route.BeforeStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := route.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	values := requests.snapshot()
	if len(values) != 2 || values[0] == nil || *values[0] != compatAudioDeviceUID || values[1] == nil || *values[1] != saved {
		t.Fatalf("cold lifecycle selection/restore = %v", values)
	}
}
