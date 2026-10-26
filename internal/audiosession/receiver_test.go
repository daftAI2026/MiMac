package audiosession

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"

	"mimac.local/mimac/internal/bridge"
	"testing"
	"time"
)

func TestReceiverReadyPrecedesSourcePreparedAndPCM(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	entered, release := make(chan struct{}), make(chan struct{})
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		v := testResources(source, sink)
		v.WaitReceiver = func(ctx context.Context) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return v, nil
	})
	core, device := newCoreForSessionTest(t, "receiver-ready")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case c := <-r.Completions():
		t.Fatalf("published preparation before receiver: %+v", c)
	case <-time.After(audioSessionTestTimeout):
		t.Fatal("receiver never entered")
	}
	select {
	case c := <-r.Completions():
		t.Fatalf("premature completion: %+v", c)
	default:
	}
	select {
	case <-source.readStarted:
		t.Fatal("read before receiver ready")
	default:
	}
	close(release)
	applySourceEffects(t, r, applyCompletion(t, core, waitCompletion(t, r)))
	source.reads <- readResult{pcm: []int16{11, 22, 33}}
	select {
	case got := <-sink.writes:
		if len(got) != 3 || got[0] != 11 {
			t.Fatalf("first write was not source PCM: %v", got)
		}
	case <-time.After(audioSessionTestTimeout):
		t.Fatal("no PCM after readiness")
	}
}

func TestReceiverWaitReleaseCancelsWithoutPCM(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	entered := make(chan struct{})
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		v := testResources(source, sink)
		v.WaitReceiver = func(ctx context.Context) error { close(entered); <-ctx.Done(); return nil }
		return v, nil
	})
	core, device := newCoreForSessionTest(t, "receiver-release")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, entered)
	effects, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, r, effects)
	waitSignal(t, source.closeStart)
	waitSignal(t, sink.closeStart)
	select {
	case <-source.readStarted:
		t.Fatal("cancelled receiver started source")
	default:
	}
	select {
	case c := <-r.Completions():
		t.Fatalf("late ready escaped cancellation: %+v", c)
	default:
	}
}

func TestReceiverFailureClosesWithoutPCM(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	fault := errors.New("receiver selected wrong input")
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		v := testResources(source, sink)
		v.WaitReceiver = func(context.Context) error { return fault }
		return v, nil
	})
	core, device := newCoreForSessionTest(t, "receiver-failed")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	completion := waitCompletion(t, r)
	if !errors.Is(completion.Err, fault) {
		t.Fatalf("failure lost: %+v", completion)
	}
	applySourceEffects(t, r, applyExpectedFailure(t, core, completion))
	waitSignal(t, source.closeStart)
	waitSignal(t, sink.closeStart)
	select {
	case <-source.readStarted:
		t.Fatal("failed receiver started source")
	default:
	}
}

func TestReceiverDeadlineDoesNotTreatLateNilAsReady(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		v := testResources(source, sink)
		v.WaitReceiver = func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("missing deadline")
			}
			<-ctx.Done()
			return nil
		}
		return v, nil
	})
	core, device := newCoreForSessionTest(t, "receiver-timeout")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-r.Completions():
		if !errors.Is(c.Err, context.DeadlineExceeded) {
			t.Fatalf("late nil became ready: %+v", c)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("receiver timeout did not finish")
	}
	waitSignal(t, source.closeStart)
	waitSignal(t, sink.closeStart)
	select {
	case <-source.readStarted:
		t.Fatal("timeout started source")
	default:
	}
}

func TestReceiverIgnoringCancellationRetainsOwnedResources(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	entered, release := make(chan struct{}), make(chan struct{})
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		v := testResources(source, sink)
		v.WaitReceiver = func(context.Context) error { close(entered); <-release; return nil }
		return v, nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	core, device := newCoreForSessionTest(t, "receiver-late")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Close(ctx); !errors.Is(err, ErrCleanupPending) {
		t.Fatalf("Close did not retain active waiter: %v", err)
	}
	if source.closeCalls.Load() != 0 || sink.closeCalls.Load() != 0 {
		t.Fatal("closed resources beneath active waiter")
	}
	close(release)
	waitSignal(t, source.closeStart)
	waitSignal(t, sink.closeStart)
	select {
	case <-source.readStarted:
		t.Fatal("late waiter reopened source")
	default:
	}
}

type receiverHealthSource struct {
	*sourceStub
	health func() error
}

func (s *receiverHealthSource) Err() error { return s.health() }

func TestReceiverGateRejectsSourceFaultBeforePublishingPrepared(t *testing.T) {
	for _, stage := range []string{"before-wait", "during-wait", "healthy"} {
		t.Run(stage, func(t *testing.T) {
			source, sink := newSourceStub(), newSinkStub()
			fault := errors.New("source disconnected or buffer overflowed")
			var healthErr error
			if stage == "before-wait" {
				healthErr = fault
			}
			calls := 0
			checked := &receiverHealthSource{sourceStub: source, health: func() error { return healthErr }}
			r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
				return Resources{Source: checked, Sink: sink, SampleRate: 16000, WaitReceiver: func(context.Context) error {
					calls++
					if stage == "during-wait" {
						healthErr = fault
					}
					return nil
				}}, nil
			})
			core, device := newCoreForSessionTest(t, "receiver-health-"+stage)
			_, _, prepare := beginVoiceSession(t, core, device, 1)
			if err := r.Submit(prepare); err != nil {
				t.Fatal(err)
			}
			completion := waitCompletion(t, r)
			if stage == "healthy" {
				if completion.Err != nil || calls != 1 {
					t.Fatalf("healthy receiver: %+v calls=%d", completion, calls)
				}
				applySourceEffects(t, r, applyCompletion(t, core, completion))
				waitSignal(t, source.readStarted)
				return
			}
			if !errors.Is(completion.Err, fault) {
				t.Fatalf("faulted source published preparation: %+v", completion)
			}
			if stage == "before-wait" && calls != 0 {
				t.Fatalf("faulted source triggered receiver %d times", calls)
			}
			applySourceEffects(t, r, applyExpectedFailure(t, core, completion))
			waitSignal(t, source.closeStart)
			waitSignal(t, sink.closeStart)
			select {
			case <-source.readStarted:
				t.Fatal("faulted source started PCM")
			default:
			}
		})
	}
}

func TestReceiverWatchFailureCancelsBlockedReadWithoutSecondToggle(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	watchEntered, watchExited, stopTarget := make(chan struct{}), make(chan struct{}), make(chan struct{})
	fault := errors.New("target stopped while PCM was being captured")
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		v := testResources(source, sink)
		v.WaitReceiver = func(context.Context) error { return nil }
		v.WatchReceiver = func(ctx context.Context) error {
			defer close(watchExited)
			close(watchEntered)
			select {
			case <-stopTarget:
				return fault
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return v, nil
	})

	core, device := newToggleCoreForReceiverTest(t, "receiver-watch-toggle")
	_, prepare := beginToggleSessionForReceiverTest(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	prepared := waitCompletion(t, r)
	if prepared.Err != nil {
		t.Fatalf("preparation failed before receiver watch: %v", prepared.Err)
	}
	open, err := core.Step(prepared.Event)
	if err != nil {
		t.Fatal(err)
	}
	if err := submitReceiverEffects(r, open); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, source.readStarted)
	waitSignal(t, watchEntered)

	close(stopTarget)
	failed := waitCompletion(t, r)
	if !errors.Is(failed.Err, fault) {
		t.Fatalf("receiver watch failure was lost: %+v", failed)
	}
	effects, err := core.Step(failed.Event)
	if err != nil {
		t.Fatal(err)
	}
	if got := receiverTargetTaps(effects); got != 0 {
		t.Fatalf("watch failure sent an end toggle: effects=%#v", effects)
	}
	if snapshot := core.Snapshot(); snapshot.HasSession || !reflect.DeepEqual(snapshot.RecoveryKeys, []bridge.Key{"space"}) {
		t.Fatalf("toggle cancellation did not retain uncertainty: %+v", snapshot)
	}
	if err := submitReceiverEffects(r, effects); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, source.closeStart)
	waitSignal(t, sink.closeStart)
	waitSignal(t, watchExited)
	if sink.writeCalls.Load() != 0 {
		t.Fatalf("blocked source read unexpectedly wrote PCM: writes=%d", sink.writeCalls.Load())
	}
}

func TestReceiverWatchIsJoinedBeforeNormalDrainCompletion(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	watchEntered, watchRelease, watchExited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		v := testResources(source, sink)
		v.WaitReceiver = func(context.Context) error { return nil }
		v.WatchReceiver = func(context.Context) error {
			close(watchEntered)
			<-watchRelease // models a native query that finishes only after it returns
			close(watchExited)
			return errors.New("late receiver result after normal drain")
		}
		return v, nil
	})
	t.Cleanup(func() {
		select {
		case <-watchRelease:
		default:
			close(watchRelease)
		}
	})
	core, device := newToggleCoreForReceiverTest(t, "receiver-watch-normal")
	press, prepare := beginToggleSessionForReceiverTest(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	prepared := waitCompletion(t, r)
	open, err := core.Step(prepared.Event)
	if err != nil {
		t.Fatal(err)
	}
	if err := submitReceiverEffects(r, open); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, source.readStarted)
	waitSignal(t, watchEntered)

	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	if err := submitReceiverEffects(r, ended); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, source.stopStarted)
	waitSignal(t, sink.drainStarted)
	select {
	case completion := <-r.Completions():
		t.Fatalf("reported drained before the receiver watcher joined: %+v", completion)
	case <-time.After(20 * time.Millisecond):
	}
	if source.closeCalls.Load() != 0 || sink.closeCalls.Load() != 0 {
		t.Fatal("closed resources while the receiver watcher was still running")
	}
	close(watchRelease)
	drained := waitCompletion(t, r)
	if drained.Err != nil {
		t.Fatalf("late watcher error polluted normal drain: %+v", drained)
	}
	waitSignal(t, watchExited)
	finish, err := core.Step(drained.Event)
	if err != nil {
		t.Fatal(err)
	}
	if got := receiverTargetTaps(finish); got != 1 {
		t.Fatalf("normal toggle completion emitted %d end taps, want exactly one: %#v", got, finish)
	}
	if err := submitReceiverEffects(r, finish); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, source.closeStart)
	waitSignal(t, sink.closeStart)
}

func TestReceiverWatchIgnoringCancellationRetainsResourcesUntilJoined(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	watchEntered, watchRelease, watchExited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	fault := errors.New("late native receiver query error")
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		v := testResources(source, sink)
		v.WaitReceiver = func(context.Context) error { return nil }
		v.WatchReceiver = func(context.Context) error {
			close(watchEntered)
			<-watchRelease // deliberately ignores ctx, like a synchronous native query
			close(watchExited)
			return fault
		}
		return v, nil
	})
	t.Cleanup(func() {
		select {
		case <-watchRelease:
		default:
			close(watchRelease)
		}
	})
	core, device := newCoreForSessionTest(t, "receiver-watch-timeout")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	prepared := waitCompletion(t, r)
	open, err := core.Step(prepared.Event)
	if err != nil {
		t.Fatal(err)
	}
	if err := submitReceiverEffects(r, open); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, source.readStarted)
	waitSignal(t, watchEntered)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err = r.Close(ctx)
	cancel()
	if !errors.Is(err, ErrCleanupPending) {
		t.Fatalf("Close while native receiver query is blocked = %v, want cleanup pending", err)
	}
	if source.closeCalls.Load() != 0 || sink.closeCalls.Load() != 0 {
		t.Fatal("closed audio resources beneath an unjoined receiver query")
	}
	close(watchRelease)
	waitSignal(t, watchExited)
	ctx, cancel = context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Fatalf("Close after receiver query returned: %v", err)
	}
	if source.closeCalls.Load() != 1 || sink.closeCalls.Load() != 1 {
		t.Fatalf("resource close counts after join=(%d,%d), want (1,1)", source.closeCalls.Load(), sink.closeCalls.Load())
	}
}

func TestLateReceiverWatchResultCannotFailNextSession(t *testing.T) {
	firstSource, firstSink := newSourceStub(), newSinkStub()
	secondSource, secondSink := newSourceStub(), newSinkStub()
	watchEntered, watchRelease, watchExited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	lateFault := errors.New("stale process metadata result")
	var prepares atomic.Int32
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		if prepares.Add(1) == 1 {
			v := testResources(firstSource, firstSink)
			v.WaitReceiver = func(context.Context) error { return nil }
			v.WatchReceiver = func(context.Context) error {
				close(watchEntered)
				<-watchRelease
				close(watchExited)
				return lateFault
			}
			return v, nil
		}
		return testResources(secondSource, secondSink), nil
	})
	t.Cleanup(func() {
		select {
		case <-watchRelease:
		default:
			close(watchRelease)
		}
	})
	core, device := newCoreForSessionTest(t, "receiver-watch-generation")
	press1, _, prepare1 := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare1); err != nil {
		t.Fatal(err)
	}
	prepared1 := waitCompletion(t, r)
	open1, err := core.Step(prepared1.Event)
	if err != nil {
		t.Fatal(err)
	}
	if err := submitReceiverEffects(r, open1); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, firstSource.readStarted)
	waitSignal(t, watchEntered)

	cancelled, err := core.Step(bridge.RouteAvailable(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := submitReceiverEffects(r, cancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(bridge.VoiceEnded(press1)); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(bridge.RouteAvailable(true)); err != nil {
		t.Fatal(err)
	}
	_, _, prepare2 := beginVoiceSession(t, core, device, 2)
	if err := r.Submit(prepare2); err != nil {
		t.Fatalf("next session rejected while canceled watcher is still owned: %v", err)
	}
	if prepares.Load() != 1 {
		t.Fatalf("next session opened before old watcher join: prepare calls=%d", prepares.Load())
	}
	close(watchRelease)
	waitSignal(t, watchExited)
	prepared2 := waitCompletion(t, r)
	if prepared2.Err != nil {
		t.Fatalf("late watcher error polluted next session: %+v", prepared2)
	}
	opened2, err := core.Step(prepared2.Event)
	if err != nil {
		t.Fatal(err)
	}
	openFound := false
	for _, effect := range opened2 {
		if open, ok := effect.(bridge.OpenGateEffect); ok && open.Token == prepare2.Token {
			openFound = true
		}
	}
	if !openFound {
		t.Fatalf("next session did not receive its own prepared result: %#v", opened2)
	}
	if err := submitReceiverEffects(r, opened2); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, secondSource.readStarted)
	if secondSink.writeCalls.Load() != 0 {
		t.Fatalf("next session wrote unprovided PCM: %d", secondSink.writeCalls.Load())
	}
}

func TestReceiverWatchReturningNilBeforeCancellationFailsClosed(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		v := testResources(source, sink)
		v.WaitReceiver = func(context.Context) error { return nil }
		v.WatchReceiver = func(context.Context) error { return nil }
		return v, nil
	})
	core, device := newToggleCoreForReceiverTest(t, "receiver-watch-nil")
	_, prepare := beginToggleSessionForReceiverTest(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	completion := waitCompletion(t, r)
	if completion.Err == nil {
		open, err := core.Step(completion.Event)
		if err != nil {
			t.Fatal(err)
		}
		if err := submitReceiverEffects(r, open); err != nil {
			t.Fatal(err)
		}
		completion = waitCompletion(t, r)
	}
	if !errors.Is(completion.Err, errReceiverWatchReturned) {
		t.Fatalf("nil receiver watcher return was treated as ready: %+v", completion)
	}
	effects, err := core.Step(completion.Event)
	if err != nil {
		t.Fatal(err)
	}
	if got := receiverTargetTaps(effects); got != 0 {
		t.Fatalf("nil watcher return emitted a second toggle: %#v", effects)
	}
	if err := submitReceiverEffects(r, effects); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, source.closeStart)
	waitSignal(t, sink.closeStart)
}

func newToggleCoreForReceiverTest(t *testing.T, deviceID string) (*bridge.Core, bridge.Device) {
	t.Helper()
	binding, err := bridge.NewBinding(bridge.PolicyToggle, "space")
	if err != nil {
		t.Fatal(err)
	}
	core, err := bridge.NewCore(binding)
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice(deviceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.RouteAvailable(true), bridge.ScopeAvailable(true)} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	return core, device
}

func beginToggleSessionForReceiverTest(t *testing.T, core *bridge.Core, device bridge.Device, pressID uint64) (bridge.PressToken, bridge.PrepareSourceEffect) {
	t.Helper()
	press, err := bridge.NewPressToken(device, "voice", pressID)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := core.Step(bridge.VoiceBegan(press))
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range effects {
		if prepare, ok := effect.(bridge.PrepareSourceEffect); ok {
			return press, prepare
		}
	}
	t.Fatalf("toggle VoiceBegan lacks PrepareSourceEffect: %#v", effects)
	return bridge.PressToken{}, bridge.PrepareSourceEffect{}
}

func receiverTargetTaps(effects []bridge.Effect) int {
	count := 0
	for _, effect := range effects {
		if _, ok := effect.(bridge.TargetTapEffect); ok {
			count++
		}
	}
	return count
}

func submitReceiverEffects(runtime *Runtime, effects []bridge.Effect) error {
	for _, effect := range effects {
		switch effect.(type) {
		case bridge.OpenGateEffect, bridge.StopSourceEffect, bridge.CloseGateEffect, bridge.DisposeResourceEffect:
			if err := runtime.Submit(effect); err != nil {
				return err
			}
		}
	}
	return nil
}
