package audiosession

import (
	"context"
	"errors"
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
