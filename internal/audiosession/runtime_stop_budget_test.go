package audiosession

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/bridge"
)

// Source.Stop and Sink.Drain share the StopSourceEffect deadline. Leave a
// substantial remainder after Stop so this test checks the combined budget
// without depending on a 100 ms scheduling window.
func TestRuntimeStopAndDrainShareTotalDeadline(t *testing.T) {
	source := newStopDrainBudgetSource()
	sink := newStopDrainBudgetSink()
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-stop-drain-budget")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	prepared := waitCompletion(t, runtime)
	if prepared.Err != nil {
		t.Fatalf("prepare source: %v", prepared.Err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, prepared))
	waitSignal(t, source.readStarted)

	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)

	stopDeadline := waitStopDrainValue(t, source.stopStarted, "Source.Stop")
	if err := waitStopDrainValue(t, source.stopReturned, "Source.Stop return"); err != nil {
		t.Fatalf("Source.Stop returned before its reserved drain window: %v", err)
	}
	drain := waitStopDrainValue(t, sink.drainStarted, "Sink.Drain")
	if !drain.hasDeadline {
		t.Fatal("Sink.Drain received no deadline")
	}
	if !drain.deadline.Equal(stopDeadline) {
		t.Fatalf("Source.Stop deadline %s != Sink.Drain deadline %s", stopDeadline, drain.deadline)
	}
	if drain.entryErr != nil {
		t.Fatalf("Sink.Drain started after the shared deadline: %v", drain.entryErr)
	}
	if got := source.closeCalls.Load(); got != 0 {
		t.Fatalf("source closed before Sink.Drain returned: calls=%d", got)
	}
	if got := sink.closeCalls.Load(); got != 0 {
		t.Fatalf("sink closed before Sink.Drain returned: calls=%d", got)
	}

	if drainErr := waitStopDrainValue(t, sink.drainReturned, "Sink.Drain deadline"); !errors.Is(drainErr, context.DeadlineExceeded) {
		t.Fatalf("Sink.Drain return = %v, want shared deadline exceeded", drainErr)
	}
	failed := waitCompletion(t, runtime)
	if !errors.Is(failed.Err, context.DeadlineExceeded) {
		t.Fatalf("completion after shared Stop/Drain deadline = %v, want deadline exceeded", failed.Err)
	}
	effects := applyExpectedFailure(t, core, failed)
	applySourceEffects(t, runtime, effects)
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("shared deadline failure retained the Core voice lease: %+v", snapshot)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancel()
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatalf("close after shared deadline failure: %v", err)
	}
	if got := source.closeCalls.Load(); got != 1 {
		t.Fatalf("source close calls = %d, want 1", got)
	}
	if got := sink.closeCalls.Load(); got != 1 {
		t.Fatalf("sink close calls = %d, want 1", got)
	}
	if got := sink.drainCalls.Load(); got != 1 {
		t.Fatalf("sink drain calls = %d, want 1", got)
	}
}

type stopDrainBudgetSource struct {
	readStarted  chan struct{}
	stopStarted  chan time.Time
	stopReturned chan error
	stopped      chan struct{}
	closeCalls   atomic.Int32
}

func newStopDrainBudgetSource() *stopDrainBudgetSource {
	return &stopDrainBudgetSource{
		readStarted:  make(chan struct{}, 1),
		stopStarted:  make(chan time.Time, 1),
		stopReturned: make(chan error, 1),
		stopped:      make(chan struct{}),
	}
}

func (s *stopDrainBudgetSource) Read(ctx context.Context, _ []int16) (int, error) {
	signal(s.readStarted)
	select {
	case <-s.stopped:
		return 0, io.EOF
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (s *stopDrainBudgetSource) Stop(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		err := errors.New("Source.Stop received no deadline")
		s.stopReturned <- err
		return err
	}
	s.stopStarted <- deadline

	const drainReserve = 700 * time.Millisecond
	wait := time.Until(deadline) - drainReserve
	if wait <= 0 {
		err := errors.New("Source.Stop had no reserved drain window")
		s.stopReturned <- err
		return err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		err := ctx.Err()
		s.stopReturned <- err
		return err
	case <-timer.C:
		if err := ctx.Err(); err != nil {
			s.stopReturned <- err
			return err
		}
		close(s.stopped)
		s.stopReturned <- nil
		return nil
	}
}

func (s *stopDrainBudgetSource) Close() error {
	s.closeCalls.Add(1)
	return nil
}

type stopDrainBudgetObservation struct {
	deadline    time.Time
	hasDeadline bool
	entryErr    error
}

type stopDrainBudgetSink struct {
	drainStarted  chan stopDrainBudgetObservation
	drainReturned chan error
	drainCalls    atomic.Int32
	closeCalls    atomic.Int32
}

func newStopDrainBudgetSink() *stopDrainBudgetSink {
	return &stopDrainBudgetSink{
		drainStarted:  make(chan stopDrainBudgetObservation, 1),
		drainReturned: make(chan error, 1),
	}
}

func (*stopDrainBudgetSink) TryWrite([]int16) error { return nil }

func (s *stopDrainBudgetSink) Drain(ctx context.Context) error {
	s.drainCalls.Add(1)
	deadline, hasDeadline := ctx.Deadline()
	s.drainStarted <- stopDrainBudgetObservation{deadline: deadline, hasDeadline: hasDeadline, entryErr: ctx.Err()}
	<-ctx.Done()
	err := ctx.Err()
	s.drainReturned <- err
	return err
}

func (s *stopDrainBudgetSink) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func waitStopDrainValue[T any](t *testing.T, ch <-chan T, name string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", name)
		return zero
	}
}
