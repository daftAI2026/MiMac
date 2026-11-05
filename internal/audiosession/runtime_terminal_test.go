package audiosession

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/bridge"
)

// terminalHangDrainSink models a synchronous native Drain/Stop call that does
// not unwind when its Go context expires. Only the test releases the call.
type terminalHangDrainSink struct {
	started   chan struct{}
	release   <-chan struct{}
	closeErr  error
	drainOnce sync.Once
	closeCall atomic.Int32
}

func (s *terminalHangDrainSink) TryWrite([]int16) error { return nil }

func (s *terminalHangDrainSink) Drain(context.Context) error {
	s.drainOnce.Do(func() { close(s.started) })
	<-s.release
	return nil
}

func (s *terminalHangDrainSink) Close() error {
	if s.closeCall.Add(1) == 1 {
		return s.closeErr
	}
	return nil
}

func newAfterDrainCore(t *testing.T, id string) (*bridge.Core, bridge.Device) {
	t.Helper()
	key, err := bridge.NewKey("space")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bridge.NewBinding(bridge.PolicyAfterDrain, key)
	if err != nil {
		t.Fatal(err)
	}
	core, err := bridge.NewCore(binding)
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{
		bridge.DeviceConnected(device),
		bridge.RouteAvailable(true),
		bridge.ScopeAvailable(true),
	} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	return core, device
}

func TestRuntimeDeadlineFailsOnceWhileNativeDrainIsBlocked(t *testing.T) {
	firstSource, nextSource := newSourceStub(), newSourceStub()
	drainRelease := make(chan struct{})
	closeErr := errors.New("native sink cleanup failed")
	firstSink := &terminalHangDrainSink{started: make(chan struct{}), release: drainRelease, closeErr: closeErr}
	nextSink := newSinkStub()
	var prepareCalls atomic.Int32
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		switch prepareCalls.Add(1) {
		case 1:
			return Resources{Source: firstSource, Sink: firstSink, SampleRate: 16000}, nil
		case 2:
			return testResources(nextSource, nextSink), nil
		default:
			return Resources{}, errors.New("unexpected extra prepare")
		}
	})
	t.Cleanup(func() {
		select {
		case <-drainRelease:
		default:
			close(drainRelease)
		}
	})

	core, device := newAfterDrainCore(t, "blocked-native-drain-terminal")
	press1, token1, prepare1 := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare1); err != nil {
		t.Fatal(err)
	}
	prepared1 := waitCompletion(t, runtime)
	preparedEffects := applyCompletion(t, core, prepared1)
	if !hasAudioEffect[bridge.OpenGateEffect](preparedEffects) {
		t.Fatalf("first SourcePrepared did not open the Core gate: %#v", prepared1)
	}
	applySourceEffects(t, runtime, preparedEffects)
	waitSignal(t, firstSource.readStarted)
	ended1, err := core.Step(bridge.VoiceEnded(press1))
	if err != nil {
		t.Fatal(err)
	}
	if hasAudioEffect[bridge.KeyUpEffect](ended1) {
		t.Fatalf("AfterDrain released its key before a terminal completion: %#v", ended1)
	}
	applySourceEffects(t, runtime, ended1)
	waitSignal(t, firstSink.started)

	// Keep the production Stop/EOF budgets unchanged while shortening only this
	// already-entered Drain's timer to make the fake deterministic and quick.
	runtime.mu.Lock()
	active := runtime.active
	if active == nil || active.token != token1 {
		runtime.mu.Unlock()
		t.Fatal("blocked resource is not the active token")
	}
	runtime.armStopDeadlineLocked(active, 20*time.Millisecond)
	runtime.mu.Unlock()

	failed := waitCompletion(t, runtime)
	if failed.CleanupFailed || !errors.Is(failed.Err, context.DeadlineExceeded) {
		t.Fatalf("blocked Drain terminal result = %#v", failed)
	}
	failureEffects := applyExpectedFailure(t, core, failed)
	if !hasAudioEffect[bridge.KeyUpEffect](failureEffects) {
		t.Fatalf("deadline failure did not release the held AfterDrain key: %#v", failureEffects)
	}
	applySourceEffects(t, runtime, failureEffects)
	if got := firstSource.closeCalls.Load(); got != 0 {
		t.Fatalf("source closed while native Drain still owns the worker: %d", got)
	}
	if got := firstSink.closeCall.Load(); got != 0 {
		t.Fatalf("sink closed while native Drain still owns the worker: %d", got)
	}

	// A fresh Core session may be queued, but its factory cannot run while the
	// first session's synchronous native call still owns the resources.
	_, token2, prepare2 := beginVoiceSession(t, core, device, 2)
	if err := runtime.Submit(prepare2); err != nil {
		t.Fatalf("queue next session behind owned resource: %v", err)
	}
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("next factory ran before blocked Drain returned: calls=%d", got)
	}

	close(drainRelease) // The fake returns nil after its deadline failure.
	cleanupFailure := waitCompletion(t, runtime)
	if !cleanupFailure.CleanupFailed || !errors.Is(cleanupFailure.Err, closeErr) {
		t.Fatalf("resource cleanup fault was not kept on CleanupFailed path: %#v", cleanupFailure)
	}
	if effects, err := core.Step(cleanupFailure.Event); err != nil || len(effects) != 0 {
		t.Fatalf("old cleanup report changed the newer Core session: effects=%#v err=%v", effects, err)
	}
	pendingFailure := waitCompletion(t, runtime)
	if pendingFailure.CleanupFailed || !errors.Is(pendingFailure.Err, ErrResourceBusy) {
		t.Fatalf("pending session after cleanup fault = %#v", pendingFailure)
	}
	if !hasAudioEffect[bridge.KeyUpEffect](applyExpectedFailure(t, core, pendingFailure)) {
		t.Fatal("rejected pending session did not release its held key")
	}
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("rejected pending session reached factory: calls=%d (token=%d)", got, token2.ID())
	}

	select {
	case late := <-runtime.Completions():
		t.Fatalf("late nil Drain emitted another completion: %#v", late)
	default:
	}
	if got := firstSource.closeCalls.Load(); got != 1 {
		t.Fatalf("source cleanup count = %d, want one owner close", got)
	}
	if got := firstSink.closeCall.Load(); got != 1 {
		t.Fatalf("sink cleanup count = %d, want one owner close", got)
	}
}

func TestRuntimeCloseWaitsForBlockedDrainWithoutReleasingOwnership(t *testing.T) {
	source := newSourceStub()
	drainRelease := make(chan struct{})
	sink := &terminalHangDrainSink{started: make(chan struct{}), release: drainRelease}
	var prepareCalls atomic.Int32
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		prepareCalls.Add(1)
		return Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	t.Cleanup(func() {
		select {
		case <-drainRelease:
		default:
			close(drainRelease)
		}
	})

	core, device := newAfterDrainCore(t, "close-waits-native-drain")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	prepared := waitCompletion(t, runtime)
	applySourceEffects(t, runtime, applyCompletion(t, core, prepared))
	waitSignal(t, source.readStarted)
	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)
	waitSignal(t, sink.started)

	cancelEffects, err := core.Step(bridge.RouteAvailable(false))
	if err != nil {
		t.Fatal(err)
	}
	if !hasAudioEffect[bridge.KeyUpEffect](cancelEffects) {
		t.Fatalf("Core cancellation did not release its held key: %#v", cancelEffects)
	}
	applySourceEffects(t, runtime, cancelEffects)

	closeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	closeErr := runtime.Close(closeCtx)
	cancel()
	if !errors.Is(closeErr, ErrCleanupPending) {
		t.Fatalf("Close while native Drain is blocked = %v, want ErrCleanupPending", closeErr)
	}
	if got := source.closeCalls.Load(); got != 0 {
		t.Fatalf("source closed before owned Drain returned: %d", got)
	}
	if got := sink.closeCall.Load(); got != 0 {
		t.Fatalf("sink closed before owned Drain returned: %d", got)
	}
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("factory count changed while Close was pending: %d", got)
	}

	close(drainRelease)
	ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("Close after native Drain returned: %v", err)
	}
	if got := source.closeCalls.Load(); got != 1 {
		t.Fatalf("source owner Close calls = %d, want 1", got)
	}
	if got := sink.closeCall.Load(); got != 1 {
		t.Fatalf("sink owner Close calls = %d, want 1", got)
	}
	otherCore, otherDevice := newAfterDrainCore(t, "closed-runtime-reopen-rejected")
	_, _, nextPrepare := beginVoiceSession(t, otherCore, otherDevice, 1)
	if err := runtime.Submit(nextPrepare); !errors.Is(err, ErrClosed) {
		t.Fatalf("Prepare while Close owns pending cleanup = %v, want ErrClosed", err)
	}
	select {
	case completion := <-runtime.Completions():
		t.Fatalf("explicit cancellation emitted a terminal completion: %#v", completion)
	default:
	}
}

func TestDrainTerminalClaimHandlesExpiredDeadlineBeforeCallbackAndRaces(t *testing.T) {
	for _, name := range []string{"expired callback pending", "failure watcher race"} {
		t.Run(name, func(t *testing.T) {
			core, device := newAfterDrainCore(t, "terminal-claim-"+name)
			_, token, _ := beginVoiceSession(t, core, device, 1)
			runtime, err := New(func(context.Context, bridge.SessionToken) (Resources, error) {
				return Resources{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
				defer cancel()
				if err := runtime.Close(ctx); err != nil {
					t.Errorf("close test runtime: %v", err)
				}
			})
			sessionCtx, sessionCancel := context.WithCancel(context.Background())
			defer sessionCancel()
			stopCtx, stopCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer stopCancel()
			s := &resource{token: token, ctx: sessionCtx, cancel: sessionCancel, stopCtx: stopCtx}

			if name == "expired callback pending" {
				if runtime.claimDrain(s, stopCtx, Completion{Event: bridge.SourceDrained(token)}) {
					t.Fatal("expired phase context claimed success")
				}
			} else {
				start := make(chan struct{})
				var workers sync.WaitGroup
				workers.Add(2)
				go func() {
					defer workers.Done()
					<-start
					runtime.fail(s, errors.New("watcher failure"))
				}()
				go func() {
					defer workers.Done()
					<-start
					runtime.claimDrain(s, stopCtx, Completion{Event: bridge.SourceDrained(token)})
				}()
				close(start)
				workers.Wait()
			}

			completion := waitCompletion(t, runtime)
			if completion.Err == nil || completion.CleanupFailed {
				t.Fatalf("terminal claim = %#v, want one terminal SourceFailed", completion)
			}
			if name == "expired callback pending" && !errors.Is(completion.Err, context.DeadlineExceeded) {
				t.Fatalf("expired phase context error = %v", completion.Err)
			}
			select {
			case duplicate := <-runtime.Completions():
				t.Fatalf("terminal claim emitted more than once: %#v", duplicate)
			default:
			}
			if !s.terminalSent {
				t.Fatal("terminal event was emitted without claiming the resource")
			}
		})
	}
}

// Assert this test's reader fake retains the ordinary context-aware behavior;
// only the fake synchronous native Drain above intentionally ignores context.
var _ Source = (*sourceStub)(nil)
var _ Sink = (*terminalHangDrainSink)(nil)
