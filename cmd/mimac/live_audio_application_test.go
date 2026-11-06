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
	compatAudioInspector
	err error
}

func (i applicationInspectorWaitFailure) WaitForInput(ctx context.Context, pid int32, uid string) error {
	_ = i.compatAudioInspector.WaitForInput(ctx, pid, uid)
	return i.err
}

type applicationInspectorEnumerationFailure struct {
	compatAudioInspector
	err   error
	calls int
}

func (i *applicationInspectorEnumerationFailure) InputsForBundle(string) ([]audioroute.ProcessInput, error) {
	i.calls++
	return nil, i.err
}

type applicationInspectorProcessFailure struct {
	compatAudioInspector
	err   error
	calls int
	pid   int32
}

func (i *applicationInspectorProcessFailure) ProcessInput(pid int32) (audioroute.ProcessInput, error) {
	i.calls++
	i.pid = pid
	return audioroute.ProcessInput{}, i.err
}

type applicationInspectorDirectWaitFailure struct {
	compatAudioInspector
	err   error
	calls int
	pid   int32
	uid   string
}

func (i *applicationInspectorDirectWaitFailure) WaitForInput(ctx context.Context, pid int32, uid string) error {
	i.calls++
	i.pid, i.uid = pid, uid
	return i.err
}

func TestApplicationAudioInspectorDiscoveryUsesVerifiedApplicationAndExactPID(t *testing.T) {
	base := newCompatRouteTestInspector()
	enumerationError := errors.New("unrelated Core Audio object has no bundle metadata")
	wrapped := &applicationInspectorEnumerationFailure{compatAudioInspector: base, err: enumerationError}
	inspector := &compatApplicationInspector{wrapped, "test.inputmethod", func(context.Context) (int32, error) { return 42, nil }}
	inputs, err := inspector.InputsForBundle("test.inputmethod")
	if err != nil || len(inputs) != 1 || inputs[0].PID != 42 || wrapped.calls != 0 {
		t.Fatalf("target discovery used unrelated metadata: %+v %v global=%d", inputs, err, wrapped.calls)
	}
	queryError := errors.New("actual target query failed")
	target := &applicationInspectorProcessFailure{compatAudioInspector: wrapped, err: queryError}
	inspector.compatAudioInspector = target
	if _, err := inspector.InputsForBundle("test.inputmethod"); !errors.Is(err, queryError) {
		t.Fatalf("target query error became idle: %v", err)
	}
	inspector.compatAudioInspector = wrapped
	base.input = audioroute.ProcessInput{Found: true, PID: 43}
	if _, err := inspector.InputsForBundle("test.inputmethod"); !errors.Is(err, errCompatRouteTargetMissing) {
		t.Fatalf("wrong direct PID accepted: %v", err)
	}
	base.input = audioroute.ProcessInput{PID: 42}
	reads := 0
	inspector.resolve = func(context.Context) (int32, error) {
		reads++
		if reads > 1 {
			return 43, nil
		}
		return 42, nil
	}
	if _, err := inspector.InputsForBundle("test.inputmethod"); !errors.Is(err, errCompatRouteTargetMissing) {
		t.Fatalf("application changed during discovery: %v", err)
	}
}

func TestApplicationAudioInspectorActiveReceiverUsesExactPIDWithoutBundleEnumeration(t *testing.T) {
	base := newCompatRouteTestInspector()
	base.input = audioroute.ProcessInput{
		Found: true, PID: 42, RunningInput: true,
		InputDeviceUIDs: []string{compatAudioDeviceUID},
	}
	enumerationError := errors.New("unrelated Core Audio object has no bundle ID")
	wrapped := &applicationInspectorEnumerationFailure{compatAudioInspector: base, err: enumerationError}
	i := &compatApplicationInspector{
		compatAudioInspector: wrapped,
		bundleID:             "test.inputmethod",
		resolve:              func(context.Context) (int32, error) { return 42, nil },
	}

	input, err := i.ProcessInput(42)
	if err != nil || !input.Found || input.PID != 42 || !input.RunningInput || len(input.InputDeviceUIDs) != 1 || input.InputDeviceUIDs[0] != compatAudioDeviceUID {
		t.Fatalf("exact active target input = %+v, %v", input, err)
	}
	if err := i.WaitForInput(context.Background(), 42, compatAudioDeviceUID); err != nil {
		t.Fatalf("exact active target readiness = %v", err)
	}
	_, waitPID, waitUID, waitCalls, processCalls := base.snapshot()
	if processCalls != 1 || waitCalls != 1 || waitPID != 42 || waitUID != compatAudioDeviceUID {
		t.Fatalf("PID-specific receiver calls = process:%d wait:%d pid:%d uid:%q", processCalls, waitCalls, waitPID, waitUID)
	}
	if wrapped.calls != 0 {
		t.Fatalf("active receiver used global bundle enumeration %d times", wrapped.calls)
	}
}

func TestApplicationAudioInspectorPIDQueriesRejectUnknownOrChangedTarget(t *testing.T) {
	resolveError := errors.New("cannot read application identity")
	for _, tc := range []struct {
		name        string
		resolvedPID int32
		resolveErr  error
	}{
		{"resolver error", 0, resolveError},
		{"target PID changed", 43, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newCompatRouteTestInspector()
			enumerationError := errors.New("global enumeration must not be used")
			wrapped := &applicationInspectorEnumerationFailure{compatAudioInspector: base, err: enumerationError}
			i := &compatApplicationInspector{
				compatAudioInspector: wrapped,
				bundleID:             "test.inputmethod",
				resolve:              func(context.Context) (int32, error) { return tc.resolvedPID, tc.resolveErr },
			}

			input, err := i.ProcessInput(42)
			if tc.resolveErr != nil {
				if !errors.Is(err, tc.resolveErr) {
					t.Fatalf("ProcessInput resolver error = %v", err)
				}
			} else if err != nil || input.Found {
				t.Fatalf("ProcessInput accepted changed target: %+v, %v", input, err)
			}
			if err := i.WaitForInput(context.Background(), 42, compatAudioDeviceUID); err == nil {
				t.Fatal("WaitForInput accepted unknown or changed target")
			}
			_, _, _, waitCalls, processCalls := base.snapshot()
			if waitCalls != 0 || processCalls != 0 {
				t.Fatalf("queried target after resolver failure/mismatch: process:%d wait:%d", processCalls, waitCalls)
			}
			if wrapped.calls != 0 {
				t.Fatalf("used global bundle enumeration %d times", wrapped.calls)
			}
		})
	}
}

func TestApplicationAudioInspectorRejectsMismatchedOrFailedDirectTargetReads(t *testing.T) {
	t.Run("actual PID mismatch", func(t *testing.T) {
		base := newCompatRouteTestInspector()
		base.input = audioroute.ProcessInput{Found: true, PID: 43, RunningInput: true, InputDeviceUIDs: []string{compatAudioDeviceUID}}
		wrapped := &applicationInspectorEnumerationFailure{
			compatAudioInspector: base,
			err:                  errors.New("global enumeration must not be used"),
		}
		i := &compatApplicationInspector{wrapped, "test.inputmethod", func(context.Context) (int32, error) { return 42, nil }}
		input, err := i.ProcessInput(42)
		if !errors.Is(err, errCompatRouteTargetMissing) || input.Found {
			t.Fatalf("mismatched actual target = %+v, %v", input, err)
		}
		if wrapped.calls != 0 {
			t.Fatalf("used global bundle enumeration %d times", wrapped.calls)
		}
	})

	t.Run("direct process query error", func(t *testing.T) {
		base := newCompatRouteTestInspector()
		queryError := errors.New("target process route read failed")
		enumeration := &applicationInspectorEnumerationFailure{
			compatAudioInspector: base,
			err:                  errors.New("global enumeration must not be used"),
		}
		query := &applicationInspectorProcessFailure{compatAudioInspector: enumeration, err: queryError}
		i := &compatApplicationInspector{query, "test.inputmethod", func(context.Context) (int32, error) { return 42, nil }}
		if _, err := i.ProcessInput(42); !errors.Is(err, queryError) {
			t.Fatalf("direct ProcessInput error = %v", err)
		}
		if query.calls != 1 || query.pid != 42 || enumeration.calls != 0 {
			t.Fatalf("direct process dispatch = calls:%d pid:%d global:%d", query.calls, query.pid, enumeration.calls)
		}
	})

	t.Run("direct readiness query error", func(t *testing.T) {
		base := newCompatRouteTestInspector()
		queryError := errors.New("target receiver wait failed")
		enumeration := &applicationInspectorEnumerationFailure{
			compatAudioInspector: base,
			err:                  errors.New("global enumeration must not be used"),
		}
		query := &applicationInspectorDirectWaitFailure{compatAudioInspector: enumeration, err: queryError}
		i := &compatApplicationInspector{query, "test.inputmethod", func(context.Context) (int32, error) { return 42, nil }}
		if err := i.WaitForInput(context.Background(), 42, compatAudioDeviceUID); !errors.Is(err, queryError) {
			t.Fatalf("direct WaitForInput error = %v", err)
		}
		if query.calls != 1 || query.pid != 42 || query.uid != compatAudioDeviceUID || enumeration.calls != 0 {
			t.Fatalf("direct readiness dispatch = calls:%d pid:%d uid:%q global:%d", query.calls, query.pid, query.uid, enumeration.calls)
		}
	})
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

func TestApplicationInspectorFactoryRetainsColdIdentityAndErrors(t *testing.T) {
	for _, bundleID := range []string{compatDoubaoBundleID, "com.tencent.inputmethod.wetype"} {
		t.Run(bundleID, func(t *testing.T) {
			base := newCompatRouteTestInspector()
			base.processes, base.input = nil, audioroute.ProcessInput{PID: 42}
			deps := defaultCompatAudioRouteDependencies()
			deps.newInspector = func() (compatAudioInspector, error) { return base, nil }
			deps = withCompatApplicationInspector(deps, bundleID, "/Applications/Test.app", "/Applications/Test.app/Contents/MacOS/Test")
			raw, err := deps.newInspector()
			if err != nil {
				t.Fatal(err)
			}
			wrapper, ok := raw.(*compatApplicationInspector)
			if !ok {
				t.Fatal("missing application identity wrapper")
			}
			wrapper.resolve = func(context.Context) (int32, error) { return 42, nil }
			processes, err := wrapper.InputsForBundle(bundleID)
			if err != nil || len(processes) != 1 || processes[0].PID != 42 {
				t.Fatalf("cold app discovery: %+v %v", processes, err)
			}
			want := errors.New("application identity failed")
			wrapper.resolve = func(context.Context) (int32, error) { return 0, want }
			if _, err := wrapper.InputsForBundle(bundleID); !errors.Is(err, want) {
				t.Fatalf("identity error became idle: %v", err)
			}
		})
	}
}
