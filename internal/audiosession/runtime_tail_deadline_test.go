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

const tailDeadlineTestTimeout = 6 * time.Second

func TestRuntimeEOFBeforeStopReturnGetsTailDeadline(t *testing.T) {
	source := newTailDeadlineSource(320, true, true)
	sink := newTailDeadlineSink(false)
	runtime, core, press := startTailDeadlineSession(t, source, sink)

	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)
	stopDeadline := waitTailDeadlineValue(t, source.stopStarted, "Source.Stop start")

	source.readRelease <- struct{}{}
	waitTailDeadlineSignal(t, source.eofReturned, "Source.Read EOF")
	waitTailDeadlineSignal(t, sink.writeStarted, "queued EOF PCM write")
	select {
	case <-sink.drainStarted:
		t.Fatal("Sink.Drain started before Source.Stop returned")
	default:
	}

	source.stopRelease <- struct{}{}
	if err := waitTailDeadlineValue(t, source.stopReturned, "Source.Stop return"); err != nil {
		t.Fatalf("Source.Stop failed: %v", err)
	}
	drain := waitTailDeadlineValue(t, sink.drainStarted, "Sink.Drain start")
	if !drain.hasDeadline {
		t.Fatal("Sink.Drain received no tail deadline")
	}
	if !drain.deadline.After(stopDeadline) {
		t.Fatalf("tail deadline %s did not replace stop deadline %s", drain.deadline, stopDeadline)
	}
	if drain.entryErr != nil {
		t.Fatalf("Sink.Drain began with context error: %v", drain.entryErr)
	}

	completion := waitTailDeadlineCompletion(t, runtime)
	effects := applyCompletion(t, core, completion)
	applySourceEffects(t, runtime, effects)
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("successful tail drain retained Core voice lease: %+v", snapshot)
	}
	waitTailDeadlineSignal(t, source.closeStarted, "Source.Close")
	waitTailDeadlineSignal(t, sink.closeStarted, "Sink.Close")
	closeTailDeadlineRuntime(t, runtime)
}

func TestRuntimeTailDrainHonorsExplicitCoreCancellation(t *testing.T) {
	source := newTailDeadlineSource(0, false, true)
	sink := newTailDeadlineSink(true)
	runtime, core, press := startTailDeadlineSession(t, source, sink)

	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)
	if err := waitTailDeadlineValue(t, source.stopReturned, "Source.Stop return"); err != nil {
		t.Fatalf("Source.Stop failed: %v", err)
	}
	source.readRelease <- struct{}{}
	waitTailDeadlineSignal(t, source.eofReturned, "Source.Read EOF")
	drain := waitTailDeadlineValue(t, sink.drainStarted, "tail Sink.Drain start")
	if !drain.hasDeadline || drain.entryErr != nil {
		t.Fatalf("tail Drain did not enter with a live deadline: %+v", drain)
	}

	effects, err := core.Step(bridge.RouteAvailable(false))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, effects)
	if drainErr := waitTailDeadlineValue(t, sink.drainReturned, "cancelled Sink.Drain"); !errors.Is(drainErr, context.Canceled) {
		t.Fatalf("Sink.Drain after Core cancellation = %v, want context canceled", drainErr)
	}
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("explicit cancellation retained Core voice lease: %+v", snapshot)
	}
	waitTailDeadlineSignal(t, source.closeStarted, "Source.Close")
	waitTailDeadlineSignal(t, sink.closeStarted, "Sink.Close")
	closeTailDeadlineRuntime(t, runtime)
}

func TestRuntimeStopAckWithoutReaderEOFKeepsControlDeadline(t *testing.T) {
	source := newTailDeadlineSource(0, false, false)
	sink := newTailDeadlineSink(false)
	runtime, core, press := startTailDeadlineSession(t, source, sink)

	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)
	if err := waitTailDeadlineValue(t, source.stopReturned, "Source.Stop ACK"); err != nil {
		t.Fatalf("Source.Stop ACK failed: %v", err)
	}

	completion := waitTailDeadlineCompletion(t, runtime)
	if !errors.Is(completion.Err, context.DeadlineExceeded) {
		t.Fatalf("completion without Reader EOF = %v, want control deadline exceeded", completion.Err)
	}
	if got := sink.drainCalls.Load(); got != 0 {
		t.Fatalf("Sink.Drain called %d times before Reader EOF", got)
	}
	applySourceEffects(t, runtime, applyExpectedFailure(t, core, completion))
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("Reader EOF timeout retained Core voice lease: %+v", snapshot)
	}
	waitTailDeadlineSignal(t, source.closeStarted, "Source.Close after timeout")
	waitTailDeadlineSignal(t, sink.closeStarted, "Sink.Close after timeout")
	closeTailDeadlineRuntime(t, runtime)
}

type tailDeadlineSource struct {
	pcm          []int16
	readRelease  chan struct{}
	stopRelease  chan struct{}
	readStarted  chan struct{}
	eofReturned  chan struct{}
	stopStarted  chan time.Time
	stopReturned chan error
	closeStarted chan struct{}
	closeCalls   atomic.Int32
}

func newTailDeadlineSource(sampleCount int, waitForStop, waitForEOF bool) *tailDeadlineSource {
	source := &tailDeadlineSource{
		pcm:          make([]int16, sampleCount),
		readRelease:  make(chan struct{}, 1),
		readStarted:  make(chan struct{}, 1),
		eofReturned:  make(chan struct{}, 1),
		stopStarted:  make(chan time.Time, 1),
		stopReturned: make(chan error, 1),
		closeStarted: make(chan struct{}, 1),
	}
	if waitForStop {
		source.stopRelease = make(chan struct{}, 1)
	}
	if waitForEOF {
		// EOF is released by the test after the Core has requested Stop.
		source.readRelease = make(chan struct{}, 1)
	} else {
		// A nil channel remains blocked until the session's reader context ends.
		source.readRelease = nil
	}
	return source
}

func (s *tailDeadlineSource) Read(ctx context.Context, dst []int16) (int, error) {
	signal(s.readStarted)
	select {
	case <-s.readRelease:
		n := copy(dst, s.pcm)
		signal(s.eofReturned)
		return n, io.EOF
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (s *tailDeadlineSource) Stop(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		err := errors.New("Source.Stop received no deadline")
		s.stopReturned <- err
		return err
	}
	s.stopStarted <- deadline
	if s.stopRelease == nil {
		s.stopReturned <- nil
		return nil
	}
	select {
	case <-s.stopRelease:
		if err := ctx.Err(); err != nil {
			s.stopReturned <- err
			return err
		}
		s.stopReturned <- nil
		return nil
	case <-ctx.Done():
		err := ctx.Err()
		s.stopReturned <- err
		return err
	}
}

func (s *tailDeadlineSource) Close() error {
	s.closeCalls.Add(1)
	signal(s.closeStarted)
	return nil
}

type tailDeadlineDrain struct {
	deadline    time.Time
	hasDeadline bool
	entryErr    error
}

type tailDeadlineSink struct {
	waitForCancel bool
	writeStarted  chan struct{}
	drainStarted  chan tailDeadlineDrain
	drainReturned chan error
	closeStarted  chan struct{}
	drainCalls    atomic.Int32
	closeCalls    atomic.Int32
}

func newTailDeadlineSink(waitForCancel bool) *tailDeadlineSink {
	return &tailDeadlineSink{
		waitForCancel: waitForCancel,
		writeStarted:  make(chan struct{}, 1),
		drainStarted:  make(chan tailDeadlineDrain, 1),
		drainReturned: make(chan error, 1),
		closeStarted:  make(chan struct{}, 1),
	}
}

func (s *tailDeadlineSink) TryWrite([]int16) error {
	signal(s.writeStarted)
	return nil
}

func (s *tailDeadlineSink) Drain(ctx context.Context) error {
	s.drainCalls.Add(1)
	deadline, hasDeadline := ctx.Deadline()
	s.drainStarted <- tailDeadlineDrain{deadline: deadline, hasDeadline: hasDeadline, entryErr: ctx.Err()}
	if s.waitForCancel {
		<-ctx.Done()
		err := ctx.Err()
		s.drainReturned <- err
		return err
	}
	s.drainReturned <- nil
	return nil
}

func (s *tailDeadlineSink) Close() error {
	s.closeCalls.Add(1)
	signal(s.closeStarted)
	return nil
}

func startTailDeadlineSession(t *testing.T, source *tailDeadlineSource, sink *tailDeadlineSink) (*Runtime, *bridge.Core, bridge.PressToken) {
	t.Helper()
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-tail-deadline")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	prepared := waitCompletion(t, runtime)
	if prepared.Err != nil {
		t.Fatalf("prepare source: %v", prepared.Err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, prepared))
	waitTailDeadlineSignal(t, source.readStarted, "Source.Read start")
	return runtime, core, press
}

func waitTailDeadlineSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(tailDeadlineTestTimeout):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitTailDeadlineValue[T any](t *testing.T, ch <-chan T, name string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(tailDeadlineTestTimeout):
		var zero T
		t.Fatalf("timed out waiting for %s", name)
		return zero
	}
}

func waitTailDeadlineCompletion(t *testing.T, runtime *Runtime) Completion {
	t.Helper()
	select {
	case completion, ok := <-runtime.Completions():
		if !ok {
			t.Fatal("completion channel closed unexpectedly")
		}
		return completion
	case <-time.After(tailDeadlineTestTimeout):
		t.Fatal("timed out waiting for Runtime completion")
		return Completion{}
	}
}

func closeTailDeadlineRuntime(t *testing.T, runtime *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), tailDeadlineTestTimeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("close Runtime: %v", err)
	}
}
