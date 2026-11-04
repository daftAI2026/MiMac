package audiosession

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/bridge"
)

func TestPCMStageDoneWakeRechecksLastDataBeforeEOFOrCompletion(t *testing.T) {
	for _, test := range []struct {
		name     string
		terminal error
	}{
		{name: "EOF", terminal: io.EOF},
		{name: "nil completion"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stage := newPCMStage(8000)
			done := make(chan struct{})
			result := test.terminal
			emptyObserved := make(chan struct{})
			resume := make(chan struct{})
			got := make(chan struct {
				n   int
				err error
				pcm []int16
			}, 1)
			go func() {
				dst := make([]int16, 8)
				n, err := stage.readWithEmptyHook(context.Background(), done, func() error { return result }, dst, func() {
					close(emptyObserved)
					<-resume
				})
				got <- struct {
					n   int
					err error
					pcm []int16
				}{n: n, err: err, pcm: append([]int16(nil), dst[:n]...)}
			}()
			waitSignal(t, emptyObserved)
			wantPCM := []int16{17, -31, 255, -1024}
			if test.terminal == io.EOF {
				if err := stage.finishEOF(wantPCM); err != nil {
					t.Fatal(err)
				}
			} else if err := stage.append(wantPCM); err != nil {
				t.Fatal(err)
			}
			close(done)
			close(resume)
			first := <-got
			if first.n != len(wantPCM) || !reflect.DeepEqual(first.pcm, wantPCM) {
				t.Fatalf("last staged block = %v, want %v", first.pcm, wantPCM)
			}
			if test.terminal != nil {
				if !errors.Is(first.err, io.EOF) {
					t.Fatalf("last n>0+EOF result = %v, want EOF", first.err)
				}
				return
			}
			if first.err != nil {
				t.Fatalf("last n>0+nil result = %v, want data before completion", first.err)
			}
			n, err := stage.read(context.Background(), done, func() error { return result }, make([]int16, 8))
			if n != 0 || err != nil {
				t.Fatalf("empty completed stage = (%d,%v), want nil completion", n, err)
			}
		})
	}
}

func TestRuntimeWritesQueuedPCMBeforePostGateReadError(t *testing.T) {
	fault := errors.New("source failed on third read")
	source := &orderedFaultSource{
		reads:        make(chan readResult, 8),
		readStarted:  make(chan struct{}, 8),
		readReturned: make(chan struct{}, 8),
		closeStart:   make(chan struct{}, 1),
	}
	baseSink := newSinkStub()
	sink := &firstWriteBarrierSink{
		sinkStub: baseSink,
		entered:  make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
	observer := &countingPCMObserver{}
	waitEntered, releaseReceiver := make(chan struct{}), make(chan struct{})
	var releaseReceiverOnce sync.Once
	openReceiver := func() { releaseReceiverOnce.Do(func() { close(releaseReceiver) }) }
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{
			Source: source, Sink: sink, Observer: observer, SampleRate: 16000,
			WaitReceiver: func(ctx context.Context) error {
				close(waitEntered)
				select {
				case <-releaseReceiver:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		}, nil
	})
	var releaseSinkOnce sync.Once
	releaseFirstWrite := func() { releaseSinkOnce.Do(func() { close(sink.release) }) }
	t.Cleanup(releaseFirstWrite)
	t.Cleanup(openReceiver)
	core, device := newCoreForSessionTest(t, "post-gate-read-error-order")
	_, token, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, waitEntered)
	waitSignal(t, source.readStarted)
	a, b, c := orderedPCMBlock(100), orderedPCMBlock(500), orderedPCMBlock(900)
	source.reads <- readResult{pcm: a}
	waitSignal(t, source.readStarted)
	source.reads <- readResult{pcm: b}
	waitSignal(t, source.readStarted) // B is staged; Reader is in the third Read.

	openReceiver()
	prepared := waitCompletion(t, runtime)
	if prepared.Err != nil {
		t.Fatalf("receiver completion: %v", prepared.Err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, prepared))
	waitSignal(t, sink.entered) // copyPCM has taken A; B remains queued in the FIFO.
	if baseSink.writeCalls.Load() != 0 {
		t.Fatal("barrier Sink passed A before it was released")
	}
	source.setErr(fault)
	source.reads <- readResult{pcm: c, err: fault}
	waitSignal(t, source.readReturned)
	releaseFirstWrite()

	waitForSinkWrite(t, baseSink, a)
	waitForSinkWrite(t, baseSink, b)
	waitForSinkWrite(t, baseSink, c)
	failed := waitCompletion(t, runtime)
	if !errors.Is(failed.Err, fault) {
		t.Fatalf("post-gate Source.Read failure = %+v, want %v", failed, fault)
	}
	if got := observer.observations.Load(); got != 3 {
		t.Fatalf("Observer saw %d blocks before SourceFailed, want A/B/C", got)
	}
	if _, ok := runtime.InputLevel(token); ok {
		t.Fatal("failed session retained input level")
	}
	applySourceEffects(t, runtime, applyExpectedFailure(t, core, failed))
	waitSignal(t, source.closeStart)
	waitSignal(t, baseSink.closeStart)
}

func TestRuntimePreGateReadFaultDiscardsStageWithoutOutput(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	observer := &countingPCMObserver{}
	fault := errors.New("synthetic pre-gate source fault")
	waitEntered := make(chan struct{})
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{
			Source: source, Sink: sink, Observer: observer, SampleRate: 16000,
			WaitReceiver: func(ctx context.Context) error {
				close(waitEntered)
				<-ctx.Done()
				return ctx.Err()
			},
		}, nil
	})
	core, device := newCoreForSessionTest(t, "pre-gate-source-fault")
	_, token, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, waitEntered)
	waitSignal(t, source.readStarted)
	source.reads <- readResult{pcm: make([]int16, 320)}
	waitSignal(t, source.readStarted)
	source.reads <- readResult{err: fault}
	completion := waitCompletion(t, runtime)
	if !errors.Is(completion.Err, fault) {
		t.Fatalf("pre-gate read fault completion = %+v", completion)
	}
	if sink.writeCalls.Load() != 0 || observer.observations.Load() != 0 {
		t.Fatalf("known pre-gate source fault leaked output: sink=%d observer=%d", sink.writeCalls.Load(), observer.observations.Load())
	}
	if _, ok := runtime.InputLevel(token); ok {
		t.Fatal("pre-gate source fault invented an input level")
	}
	applySourceEffects(t, runtime, applyExpectedFailure(t, core, completion))
	waitSignal(t, source.closeStart)
	waitSignal(t, sink.closeStart)
}

func TestRuntimeUnexpectedEOFWaitsForCoreGateAndDoesNotDrain(t *testing.T) {
	source := &eofOnceSource{pcm: []int16{5, -8, 13, -21}}
	sink := newSinkStub()
	observer := &countingPCMObserver{}
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{Source: source, Sink: sink, Observer: observer, SampleRate: 16000}, nil
	})
	core, device := newCoreForSessionTest(t, "unexpected-eof")
	_, token, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	prepared := waitCompletion(t, runtime)
	if prepared.Err != nil {
		t.Fatalf("unexpected EOF skipped SourcePrepared: %+v", prepared)
	}
	if sink.writeCalls.Load() != 0 || observer.observations.Load() != 0 {
		t.Fatal("EOF samples escaped before Core opened the gate")
	}
	if _, ok := runtime.InputLevel(token); ok {
		t.Fatal("pre-gate EOF invented an input level")
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, prepared))
	failed := waitCompletion(t, runtime)
	if failed.Err == nil || failed.Err.Error() != "audiosession: source ended without a stop request" {
		t.Fatalf("unexpected EOF result = %+v", failed)
	}
	if got := sink.drainCalls.Load(); got != 0 {
		t.Fatalf("unexpected EOF drained sink %d times", got)
	}
	if got := sink.snapshotWrites(); !reflect.DeepEqual(got, []int16{5, -8, 13, -21}) {
		t.Fatalf("n>0 EOF samples = %v", got)
	}
	applySourceEffects(t, runtime, applyExpectedFailure(t, core, failed))
}

func TestRuntimeReadsWhileSessionSinkStartSessionIsBlocked(t *testing.T) {
	source, baseSink := newSourceStub(), newSinkStub()
	sink := &blockingSessionTestSink{sinkStub: baseSink, entered: make(chan struct{}), release: make(chan struct{})}
	observer := &countingPCMObserver{}
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{Source: source, Sink: sink, Observer: observer, SampleRate: 16000}, nil
	})
	core, device := newCoreForSessionTest(t, "start-session-prefetch")
	var releaseOnce sync.Once
	var releaseSession = func() { releaseOnce.Do(func() { close(sink.release) }) }
	t.Cleanup(releaseSession)
	press, token, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, sink.entered)
	waitSignal(t, source.readStarted)
	source.reads <- readResult{pcm: []int16{1, 2, 3, 4}}
	waitSignal(t, source.readStarted)
	if baseSink.writeCalls.Load() != 0 || observer.observations.Load() != 0 {
		t.Fatal("Reader observed or wrote while StartSession was still blocked")
	}
	releaseSession()
	prepared := waitCompletion(t, runtime)
	if prepared.Err != nil {
		t.Fatalf("SourcePrepared after StartSession: %v", prepared.Err)
	}
	if baseSink.writeCalls.Load() != 0 {
		t.Fatal("SourcePrepared alone delivered staged PCM")
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, prepared))
	waitForSinkWrite(t, baseSink, []int16{1, 2, 3, 4})
	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)
	waitSignal(t, source.stopStarted)
	drained := waitCompletion(t, runtime)
	if drained.Err != nil {
		t.Fatalf("drain after early read: %v", drained.Err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, drained))
	if _, ok := runtime.InputLevel(token); ok {
		t.Fatal("completed session retained an input level")
	}
}

func TestRuntimeInvalidStartupReadFailsWithoutOutput(t *testing.T) {
	source := &invalidStartupReadSource{started: make(chan struct{}, 1), release: make(chan struct{}), closeStart: make(chan struct{}, 1)}
	sink := newSinkStub()
	observer := &countingPCMObserver{}
	waitEntered := make(chan struct{})
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{
			Source: source, Sink: sink, Observer: observer, SampleRate: 16000,
			WaitReceiver: func(ctx context.Context) error {
				close(waitEntered)
				<-ctx.Done()
				return ctx.Err()
			},
		}, nil
	})
	t.Cleanup(func() {
		select {
		case <-source.release:
		default:
			close(source.release)
		}
	})
	core, device := newCoreForSessionTest(t, "invalid-startup-read")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, waitEntered)
	waitSignal(t, source.started)
	close(source.release)
	completion := waitCompletion(t, runtime)
	if completion.Err == nil || completion.Err.Error() != "audiosession: invalid PCM block length" {
		t.Fatalf("invalid startup read result = %+v", completion)
	}
	if sink.writeCalls.Load() != 0 || observer.observations.Load() != 0 {
		t.Fatal("invalid startup read reached output")
	}
	applySourceEffects(t, runtime, applyExpectedFailure(t, core, completion))
	waitSignal(t, source.closeStart)
}

type orderedFaultSource struct {
	reads        chan readResult
	readStarted  chan struct{}
	readReturned chan struct{}
	closeStart   chan struct{}
	errMu        sync.Mutex
	err          error
}

func (s *orderedFaultSource) Read(ctx context.Context, dst []int16) (int, error) {
	signal(s.readStarted)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case result := <-s.reads:
		n := copy(dst, result.pcm)
		signal(s.readReturned)
		return n, result.err
	}
}

func (s *orderedFaultSource) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

func (s *orderedFaultSource) setErr(err error) {
	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
}

func (*orderedFaultSource) Stop(context.Context) error { return nil }
func (s *orderedFaultSource) Close() error {
	signal(s.closeStart)
	return nil
}

type firstWriteBarrierSink struct {
	*sinkStub
	entered   chan struct{}
	release   chan struct{}
	firstCall atomic.Bool
}

func (s *firstWriteBarrierSink) TryWrite(pcm []int16) error {
	if s.firstCall.CompareAndSwap(false, true) {
		signal(s.entered)
		<-s.release
	}
	return s.sinkStub.TryWrite(pcm)
}

type countingPCMObserver struct{ observations atomic.Int64 }

func (o *countingPCMObserver) Observe([]int16, time.Time) { o.observations.Add(1) }
func (*countingPCMObserver) Clear()                       {}

type eofOnceSource struct {
	pcm        []int16
	readOnce   sync.Once
	closeCalls atomic.Int32
}

func (s *eofOnceSource) Read(ctx context.Context, dst []int16) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var n int
	s.readOnce.Do(func() { n = copy(dst, s.pcm) })
	if n > 0 {
		return n, io.EOF
	}
	return 0, io.EOF
}

func (*eofOnceSource) Stop(context.Context) error { return nil }
func (s *eofOnceSource) Close() error             { s.closeCalls.Add(1); return nil }

type blockingSessionTestSink struct {
	*sinkStub
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSessionTestSink) StartSession(ctx context.Context) error {
	signal(s.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return nil
	}
}

type invalidStartupReadSource struct {
	started    chan struct{}
	release    chan struct{}
	closeStart chan struct{}
}

func (s *invalidStartupReadSource) Read(_ context.Context, dst []int16) (int, error) {
	signal(s.started)
	<-s.release
	return len(dst) + 1, nil
}

func (*invalidStartupReadSource) Stop(context.Context) error { return nil }
func (s *invalidStartupReadSource) Close() error {
	signal(s.closeStart)
	return nil
}

func waitForSinkWrite(t *testing.T, sink *sinkStub, want []int16) {
	t.Helper()
	select {
	case got := <-sink.writes:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sink write = %v, want %v", got, want)
		}
	case <-time.After(audioSessionTestTimeout):
		t.Fatal("PCM never reached sink")
	}
}

func orderedPCMBlock(first int16) []int16 {
	pcm := make([]int16, 320)
	for i := range pcm {
		pcm[i] = first + int16(i)
	}
	return pcm
}

func (s *sinkStub) snapshotWrites() []int16 {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var pcm []int16
	for {
		select {
		case block := <-s.writes:
			pcm = append(pcm, block...)
		default:
			return pcm
		}
	}
}

var _ Source = (*orderedFaultSource)(nil)
var _ Sink = (*firstWriteBarrierSink)(nil)
var _ SessionSink = (*blockingSessionTestSink)(nil)
