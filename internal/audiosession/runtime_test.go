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

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/bridge"
)

const audioSessionTestTimeout = 2 * time.Second

var errTestRead = errors.New("test PCM read failed")

type delayedCloseSink struct {
	Sink
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *delayedCloseSink) Close() error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return s.Sink.Close()
}

func TestRuntimeQueuesOneNextSessionDuringSuccessfulDisposal(t *testing.T) {
	firstSource, secondSource := newSourceStub(), newSourceStub()
	firstSink := &delayedCloseSink{Sink: newSinkStub(), started: make(chan struct{}), release: make(chan struct{})}
	secondSink := newSinkStub()
	var calls atomic.Int32
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		if calls.Add(1) == 1 {
			return Resources{Source: firstSource, Sink: firstSink, SampleRate: 16000}, nil
		}
		return testResources(secondSource, secondSink), nil
	})
	t.Cleanup(func() {
		select {
		case <-firstSink.release:
		default:
			close(firstSink.release)
		}
	})
	core, device := newCoreForSessionTest(t, "successive-sessions")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	ended, _ := core.Step(bridge.VoiceEnded(press))
	applySourceEffects(t, runtime, ended)
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	waitSignal(t, firstSink.started)
	_, _, next := beginVoiceSession(t, core, device, 2)
	if err := runtime.Submit(next); err != nil {
		t.Fatalf("normal cleanup rejected next session: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("opened two simultaneous resource groups")
	}
	close(firstSink.release)
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	waitSignal(t, secondSource.readStarted)
	if calls.Load() != 2 {
		t.Fatal("next session never prepared after disposal")
	}
}

func TestRuntimeDoesNotPreparePendingSessionReleasedBeforeOldDispose(t *testing.T) {
	firstSource, secondSource := newSourceStub(), newSourceStub()
	firstSinkBase := newSinkStub()
	firstSink := &delayedCloseSink{Sink: firstSinkBase, started: make(chan struct{}), release: make(chan struct{})}
	secondSink := newSinkStub()
	var prepareCalls atomic.Int32
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		if prepareCalls.Add(1) == 1 {
			return Resources{Source: firstSource, Sink: firstSink, SampleRate: 16000}, nil
		}
		return testResources(secondSource, secondSink), nil
	})
	t.Cleanup(func() {
		select {
		case <-firstSink.release:
		default:
			close(firstSink.release)
		}
	})
	core, device := newCoreForSessionTest(t, "pending-release-before-dispose")
	press1, _, prepare1 := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare1); err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	ended1, err := core.Step(bridge.VoiceEnded(press1))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended1)
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	waitSignal(t, firstSink.started)

	press2, _, prepare2 := beginVoiceSession(t, core, device, 2)
	if err := runtime.Submit(prepare2); err != nil {
		t.Fatalf("pending preparation rejected during clean disposal: %v", err)
	}
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("pending resource opened before old Dispose completed: calls=%d", got)
	}
	runtime.mu.Lock()
	oldResource := runtime.active
	runtime.mu.Unlock()
	if oldResource == nil {
		t.Fatal("old resource disappeared before Dispose completed")
	}
	ended2, err := core.Step(bridge.VoiceEnded(press2))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended2)
	close(firstSink.release)
	waitSignal(t, oldResource.done)
	runtime.mu.Lock()
	active, pending := runtime.active, runtime.pending
	runtime.mu.Unlock()
	if active != nil || pending != nil {
		t.Fatalf("released pending session survived old disposal: active=%v pending=%v", active, pending)
	}
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("released pending press still called Prepare after old Dispose: calls=%d", got)
	}
	if got := secondSource.closeCalls.Load(); got != 0 {
		t.Fatalf("pending source was unexpectedly opened and closed: calls=%d", got)
	}
	select {
	case completion := <-runtime.Completions():
		t.Fatalf("released pending session emitted a completion: %#v", completion)
	default:
	}
}

func TestRuntimeDisposeFailureRejectsPendingAndReopen(t *testing.T) {
	closeErr := errors.New("old AudioQueue Dispose failed")
	firstSource, secondSource := newSourceStub(), newSourceStub()
	firstSinkBase := newSinkStub()
	firstSinkBase.closeErrs = []error{closeErr, nil}
	firstSink := &delayedCloseSink{Sink: firstSinkBase, started: make(chan struct{}), release: make(chan struct{})}
	secondSink := newSinkStub()
	var prepareCalls atomic.Int32
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		if prepareCalls.Add(1) == 1 {
			return Resources{Source: firstSource, Sink: firstSink, SampleRate: 16000}, nil
		}
		return testResources(secondSource, secondSink), nil
	})
	t.Cleanup(func() {
		select {
		case <-firstSink.release:
		default:
			close(firstSink.release)
		}
	})
	core, device := newCoreForSessionTest(t, "pending-dispose-failure")
	press1, token1, prepare1 := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare1); err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	ended1, err := core.Step(bridge.VoiceEnded(press1))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended1)
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	waitSignal(t, firstSink.started)

	press2, token2, prepare2 := beginVoiceSession(t, core, device, 2)
	if err := runtime.Submit(prepare2); err != nil {
		t.Fatalf("pending preparation rejected before cleanup outcome: %v", err)
	}
	runtime.mu.Lock()
	oldResource := runtime.active
	runtime.mu.Unlock()
	if oldResource == nil {
		t.Fatal("old resource disappeared before Dispose completed")
	}
	close(firstSink.release)
	waitSignal(t, oldResource.done)
	runtime.mu.Lock()
	active, pending := runtime.active, runtime.pending
	var cleanupErr error
	if active != nil {
		cleanupErr = active.cleanupErr
	}
	runtime.mu.Unlock()
	if active == nil || active.token != token1 || !errors.Is(cleanupErr, closeErr) || pending != nil {
		t.Fatalf("failed Dispose did not retain only the old handle: active=%v cleanupErr=%v pending=%v", active, cleanupErr, pending)
	}
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("pending Prepare ran despite old Dispose failure: calls=%d", got)
	}
	if got := secondSource.closeCalls.Load(); got != 0 {
		t.Fatalf("rejected pending source was unexpectedly opened: calls=%d", got)
	}
	oldFailure := waitCompletion(t, runtime)
	if !oldFailure.CleanupFailed || !errors.Is(oldFailure.Err, closeErr) {
		t.Fatalf("old Dispose failure completion = %#v", oldFailure)
	}
	applyExpectedFailure(t, core, oldFailure)
	pendingFailure := waitCompletion(t, runtime)
	if pendingFailure.CleanupFailed || !errors.Is(pendingFailure.Err, closeErr) || !errors.Is(pendingFailure.Err, ErrResourceBusy) {
		t.Fatalf("pending rejection did not report the cleanup cause: %#v", pendingFailure)
	}
	if effects := applyExpectedFailure(t, core, pendingFailure); !hasAudioEffect[bridge.KeyUpEffect](effects) {
		t.Fatalf("pending Core session was not canceled: %#v", effects)
	}
	if _, err := core.Step(bridge.VoiceEnded(press2)); err != nil {
		t.Fatal(err)
	}
	_, _, prepare3 := beginVoiceSession(t, core, device, 3)
	if err := runtime.Submit(prepare3); !errors.Is(err, ErrResourceBusy) {
		t.Fatalf("reopen with retained old handle = %v, want ErrResourceBusy (pending token %d)", err, token2.ID())
	}
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("reopen called Prepare with retained handle: calls=%d", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("retry old disposal: %v", err)
	}
}

type readResult struct {
	pcm []int16
	err error
}

type sourceStub struct {
	reads       chan readResult
	readStarted chan struct{}
	readLengths chan int
	stopStarted chan struct{}
	stopSignal  chan struct{}
	closeStart  chan struct{}
	stopOnce    sync.Once
	stopCalls   atomic.Int32
	closeCalls  atomic.Int32
	stopErr     error
	closeMu     sync.Mutex
	closeErrs   []error
}

func newSourceStub() *sourceStub {
	return &sourceStub{
		reads:       make(chan readResult, 8),
		readStarted: make(chan struct{}, 8),
		readLengths: make(chan int, 8),
		stopStarted: make(chan struct{}, 1),
		stopSignal:  make(chan struct{}),
		closeStart:  make(chan struct{}, 1),
	}
}

func (s *sourceStub) Read(ctx context.Context, dst []int16) (int, error) {
	signal(s.readStarted)
	select {
	case s.readLengths <- len(dst):
	default:
	}
	select {
	case <-s.stopSignal:
		return 0, io.EOF
	default:
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.stopSignal:
		return 0, io.EOF
	case result := <-s.reads:
		if result.err != nil {
			return 0, result.err
		}
		return copy(dst, result.pcm), nil
	}
}

func (s *sourceStub) Stop(context.Context) error {
	s.stopCalls.Add(1)
	signal(s.stopStarted)
	s.stopOnce.Do(func() { close(s.stopSignal) })
	return s.stopErr
}

func (s *sourceStub) Close() error {
	index := int(s.closeCalls.Add(1)) - 1
	signal(s.closeStart)
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if index < len(s.closeErrs) {
		return s.closeErrs[index]
	}
	return nil
}

type sinkStub struct {
	writes          chan []int16
	writeCalls      atomic.Int32
	writeMu         sync.Mutex
	writeErrs       []error
	defaultWriteErr error
	drainStarted    chan struct{}
	drainRelease    <-chan struct{}
	drainErr        error
	drainCalls      atomic.Int32
	closeCalls      atomic.Int32
	closeStart      chan struct{}
	closeMu         sync.Mutex
	closeErrs       []error
}

func newSinkStub() *sinkStub {
	return &sinkStub{writes: make(chan []int16, 8), drainStarted: make(chan struct{}, 1), closeStart: make(chan struct{}, 1)}
}

func (s *sinkStub) TryWrite(pcm []int16) error {
	index := int(s.writeCalls.Add(1)) - 1
	s.writeMu.Lock()
	var err error
	if index < len(s.writeErrs) {
		err = s.writeErrs[index]
	} else {
		err = s.defaultWriteErr
	}
	s.writeMu.Unlock()
	if err != nil {
		return err
	}
	s.writes <- append([]int16(nil), pcm...)
	return nil
}

func (s *sinkStub) Drain(ctx context.Context) error {
	s.drainCalls.Add(1)
	signal(s.drainStarted)
	if s.drainRelease == nil {
		return s.drainErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.drainRelease:
		return s.drainErr
	}
}

func (s *sinkStub) Close() error {
	index := int(s.closeCalls.Add(1)) - 1
	signal(s.closeStart)
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if index < len(s.closeErrs) {
		return s.closeErrs[index]
	}
	return nil
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(audioSessionTestTimeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		t.Fatal("timed out waiting for channel signal")
	}
}

func waitCompletion(t *testing.T, runtime *Runtime) Completion {
	t.Helper()
	timer := time.NewTimer(audioSessionTestTimeout)
	defer timer.Stop()
	select {
	case result, ok := <-runtime.Completions():
		if !ok {
			t.Fatal("completion channel closed unexpectedly")
		}
		return result
	case <-timer.C:
		t.Fatal("timed out waiting for audio session completion")
		return Completion{}
	}
}

func newRuntimeForTest(t *testing.T, prepare Prepare) *Runtime {
	t.Helper()
	runtime, err := New(prepare)
	if err != nil {
		t.Fatalf("New runtime: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
		defer cancel()
		if err := runtime.Close(ctx); err != nil {
			t.Errorf("cleanup runtime: %v", err)
		}
	})
	return runtime
}

func newCoreForSessionTest(t *testing.T, deviceID string) (*bridge.Core, bridge.Device) {
	t.Helper()
	key, err := bridge.NewKey("space")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bridge.NewBinding(bridge.PolicyExactEdge, key)
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

func beginVoiceSession(t *testing.T, core *bridge.Core, device bridge.Device, pressID uint64) (bridge.PressToken, bridge.SessionToken, bridge.PrepareSourceEffect) {
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
		if prepared, ok := effect.(bridge.PrepareSourceEffect); ok {
			return press, prepared.Token, prepared
		}
	}
	t.Fatalf("VoiceBegan effects lack PrepareSourceEffect: %#v", effects)
	return bridge.PressToken{}, bridge.SessionToken{}, bridge.PrepareSourceEffect{}
}

func applySourceEffects(t *testing.T, runtime *Runtime, effects []bridge.Effect) {
	t.Helper()
	for _, effect := range effects {
		switch effect.(type) {
		case bridge.OpenGateEffect, bridge.StopSourceEffect, bridge.CloseGateEffect, bridge.DisposeResourceEffect:
			if err := runtime.Submit(effect); err != nil {
				t.Fatalf("submit %T: %v", effect, err)
			}
		}
	}
}

func applyCompletion(t *testing.T, core *bridge.Core, completion Completion) []bridge.Effect {
	t.Helper()
	if completion.Err != nil {
		t.Fatalf("unexpected completion error: %v", completion.Err)
	}
	effects, err := core.Step(completion.Event)
	if err != nil {
		t.Fatalf("apply completion: %v", err)
	}
	return effects
}

func applyExpectedFailure(t *testing.T, core *bridge.Core, completion Completion) []bridge.Effect {
	t.Helper()
	if completion.Err == nil {
		t.Fatal("expected failed source completion without an error")
	}
	effects, err := core.Step(completion.Event)
	if err != nil {
		t.Fatalf("apply failed completion: %v", err)
	}
	return effects
}

func testResources(source *sourceStub, sink *sinkStub) Resources {
	return Resources{Source: source, Sink: sink, SampleRate: 16000}
}

func TestRuntimeIsLazyUntilCoreRequestsPreparation(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	var prepareCalls atomic.Int32
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		prepareCalls.Add(1)
		return testResources(source, sink), nil
	})
	if got := prepareCalls.Load(); got != 0 {
		t.Fatalf("New started preparation eagerly: calls=%d", got)
	}
	core, device := newCoreForSessionTest(t, "audio-session-remote")
	_, token, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	completion := waitCompletion(t, runtime)
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("PrepareSourceEffect called Prepare %d times", got)
	}
	if effects := applyCompletion(t, core, completion); !hasAudioEffect[bridge.OpenGateEffect](effects) {
		t.Fatalf("SourcePrepared did not open the core gate: %#v", effects)
	}
	if got := completion.Event; got == nil || token.ID() == 0 {
		t.Fatal("runtime returned no prepared event or invalid token")
	}
}

func TestRuntimeWaitsForOpenGateThenForwardsBoundedPCM(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	sink.drainRelease = closedSignal()
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return testResources(source, sink), nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-gate")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	prepared := waitCompletion(t, runtime)
	openEffects := applyCompletion(t, core, prepared)
	select {
	case <-source.readStarted:
		t.Fatal("source read began before OpenGate")
	default:
	}
	applySourceEffects(t, runtime, openEffects)
	waitSignal(t, source.readStarted)
	select {
	case frames := <-source.readLengths:
		if frames != 320 {
			t.Fatalf("Read buffer = %d frames, want 20 ms / 320 frames", frames)
		}
	default:
		t.Fatal("source did not receive a bounded read buffer")
	}
	source.reads <- readResult{pcm: []int16{4, -7, 11}}
	select {
	case got := <-sink.writes:
		if !reflect.DeepEqual(got, []int16{4, -7, 11}) {
			t.Fatalf("written PCM = %v", got)
		}
	case <-time.After(audioSessionTestTimeout):
		t.Fatal("PCM was not forwarded to the sink")
	}
	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)
	drained := waitCompletion(t, runtime)
	cleanup := applyCompletion(t, core, drained)
	applySourceEffects(t, runtime, cleanup)
	ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeReportsDrainedOnlyAfterReadEOFAndSinkDrain(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	drainRelease := make(chan struct{})
	sink.drainRelease = drainRelease
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return testResources(source, sink), nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-drain")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	waitSignal(t, source.readStarted)
	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)
	waitSignal(t, source.stopStarted)
	waitSignal(t, sink.drainStarted)
	select {
	case completion := <-runtime.Completions():
		t.Fatalf("completion preceded local drain: %#v", completion)
	default:
	}
	close(drainRelease)
	drained := waitCompletion(t, runtime)
	cleanup := applyCompletion(t, core, drained)
	if !hasAudioEffect[bridge.DisposeResourceEffect](cleanup) || !hasAudioEffect[bridge.CloseGateEffect](cleanup) {
		t.Fatalf("drained event did not finish and dispose the resource: %#v", cleanup)
	}
	applySourceEffects(t, runtime, cleanup)
}

func TestRuntimeCleansPartialPreparationAndReportsFailure(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	openErr := errors.New("source preparation failed")
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return testResources(source, sink), openErr
	})
	core, device := newCoreForSessionTest(t, "audio-session-partial")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	completion := waitCompletion(t, runtime)
	if !errors.Is(completion.Err, openErr) || completion.CleanupFailed {
		t.Fatalf("partial open completion = %#v", completion)
	}
	waitSignal(t, source.closeStart)
	waitSignal(t, sink.closeStart)
	if got := source.closeCalls.Load(); got != 1 {
		t.Fatalf("partial source close calls = %d", got)
	}
	if got := sink.closeCalls.Load(); got != 1 {
		t.Fatalf("partial sink close calls = %d", got)
	}
	if effects := applyExpectedFailure(t, core, completion); hasAudioEffect[bridge.OpenGateEffect](effects) {
		t.Fatalf("failed preparation opened gate: %#v", effects)
	}
}

func TestRuntimeRetainsPartialResourcesWhenCleanupFails(t *testing.T) {
	cleanupErr := errors.New("first close failed")
	source, sink := newSourceStub(), newSinkStub()
	sink.closeErrs = []error{cleanupErr, nil}
	var prepareCalls atomic.Int32
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		prepareCalls.Add(1)
		return testResources(source, sink), errTestRead
	})
	core, device := newCoreForSessionTest(t, "audio-session-cleanup-fail")
	_, _, firstPrepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(firstPrepare); err != nil {
		t.Fatal(err)
	}
	failed := waitCompletion(t, runtime)
	if failed.CleanupFailed || !errors.Is(failed.Err, errTestRead) {
		t.Fatalf("partial preparation failure completion = %#v", failed)
	}
	applyExpectedFailure(t, core, failed)
	cleanupFailure := waitCompletion(t, runtime)
	if !cleanupFailure.CleanupFailed || !errors.Is(cleanupFailure.Err, cleanupErr) {
		t.Fatalf("cleanup failure not retained in completion: %#v", cleanupFailure)
	}
	applyExpectedFailure(t, core, cleanupFailure)
	if _, err := core.Step(bridge.VoiceEnded(mustPressToken(t, device, "voice", 1))); err != nil {
		t.Fatal(err)
	}
	_, token2, secondPrepare := beginVoiceSession(t, core, device, 2)
	if err := runtime.Submit(secondPrepare); err == nil {
		t.Fatal("new preparation started while old native resource still needs cleanup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("retry retained cleanup: %v", err)
	}
	if got := sink.closeCalls.Load(); got != 2 {
		t.Fatalf("sink cleanup retry count = %d, want 2 (token2=%d)", got, token2.ID())
	}
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("blocked second prepare reached source: calls=%d", got)
	}
}

func TestRuntimeCancelsLateNoninterruptibleOpenAndClosesItsOwnResources(t *testing.T) {
	openStarted, allowOpen := make(chan struct{}, 1), make(chan struct{})
	source, sink := newSourceStub(), newSinkStub()
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		signal(openStarted)
		<-allowOpen // deliberately models a native Open that ignores cancellation
		return testResources(source, sink), nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-late-open")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, openStarted)
	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)
	close(allowOpen)
	ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("close after late open: %v", err)
	}
	if got := source.closeCalls.Load(); got != 1 {
		t.Fatalf("late source close calls = %d", got)
	}
	if got := sink.closeCalls.Load(); got != 1 {
		t.Fatalf("late sink close calls = %d", got)
	}
	if got := sink.drainCalls.Load(); got != 0 {
		t.Fatalf("canceled late open was reported/driven as a normal drain: %d", got)
	}
	select {
	case completion := <-runtime.Completions():
		t.Fatalf("late canceled open emitted a completion: %#v", completion)
	default:
	}
}

func TestRuntimeCloseTimeoutDoesNotReleasePendingPreparation(t *testing.T) {
	openStarted, allowOpen := make(chan struct{}, 1), make(chan struct{})
	source, sink := newSourceStub(), newSinkStub()
	var prepareCalls atomic.Int32
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		prepareCalls.Add(1)
		if prepareCalls.Load() == 1 {
			signal(openStarted)
			<-allowOpen
		}
		return testResources(source, sink), nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-close-timeout")
	press1, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, openStarted)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runtime.Close(ctx); err == nil {
		t.Fatal("Close reported success while native preparation was blocked")
	}
	ended, endErr := core.Step(bridge.VoiceEnded(press1))
	if endErr != nil {
		t.Fatal(endErr)
	}
	applySourceEffects(t, runtime, ended)
	_, secondToken, secondPrepare := beginVoiceSession(t, core, device, 2)
	if err := runtime.Submit(secondPrepare); err == nil {
		t.Fatalf("new token %d prepared while old Open still owns the runtime", secondToken.ID())
	}
	close(allowOpen)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cleanupCancel()
	if err := runtime.Close(cleanupCtx); err != nil {
		t.Fatalf("Close retry after blocked Open returned: %v", err)
	}
	if got := source.closeCalls.Load(); got != 1 {
		t.Fatalf("late resource was not closed after Open returned: %d", got)
	}
}

func TestRuntimeCloseFailureRetainsHandleForRetry(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	closeErr := errors.New("native dispose failed")
	sink.closeErrs = []error{closeErr, nil}
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return testResources(source, sink), nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-close-retry")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	if got := waitCompletion(t, runtime); got.Err != nil {
		t.Fatal(got.Err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancel()
	if err := runtime.Close(ctx); !errors.Is(err, closeErr) {
		t.Fatalf("first Close = %v, want dispose error", err)
	}
	newCore, newDevice := newCoreForSessionTest(t, "audio-session-close-new")
	_, _, newPrepare := beginVoiceSession(t, newCore, newDevice, 1)
	if err := runtime.Submit(newPrepare); err == nil {
		t.Fatal("runtime accepted a new session after failed cleanup")
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("Close retry: %v", err)
	}
	if got := sink.closeCalls.Load(); got != 2 {
		t.Fatalf("sink close attempts = %d, want failed close plus retry", got)
	}
}

func TestRuntimeRetriesBackpressureWithoutLosingPCM(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	sink.writeErrs = []error{audiooutput.ErrBackpressure, nil}
	sink.drainRelease = closedSignal()
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return testResources(source, sink), nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-backpressure")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	waitSignal(t, source.readStarted)
	pcm := []int16{13, -21, 34}
	source.reads <- readResult{pcm: pcm}
	select {
	case got := <-sink.writes:
		if !reflect.DeepEqual(got, pcm) {
			t.Fatalf("retried PCM = %v, want %v", got, pcm)
		}
	case <-time.After(audioSessionTestTimeout):
		t.Fatal("backpressured PCM was not retried")
	}
	if got := sink.writeCalls.Load(); got != 2 {
		t.Fatalf("TryWrite calls = %d, want one backpressure retry", got)
	}
	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, ended)
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
}

func TestRuntimeReadAndDrainErrorsReportSourceFailure(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		source, sink := newSourceStub(), newSinkStub()
		runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
			return testResources(source, sink), nil
		})
		core, device := newCoreForSessionTest(t, "audio-session-read-error")
		_, _, prepare := beginVoiceSession(t, core, device, 1)
		if err := runtime.Submit(prepare); err != nil {
			t.Fatal(err)
		}
		applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
		waitSignal(t, source.readStarted)
		source.reads <- readResult{err: errTestRead}
		completion := waitCompletion(t, runtime)
		if !errors.Is(completion.Err, errTestRead) {
			t.Fatalf("read failure completion error = %v", completion.Err)
		}
		if effects := applyExpectedFailure(t, core, completion); hasAudioEffect[bridge.CloseGateEffect](effects) == false {
			t.Fatalf("read failure did not cancel the session: %#v", effects)
		}
	})

	t.Run("drain", func(t *testing.T) {
		source, sink := newSourceStub(), newSinkStub()
		drainErr := errors.New("local sink drain failed")
		sink.drainErr = drainErr
		sink.drainRelease = closedSignal()
		runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
			return testResources(source, sink), nil
		})
		core, device := newCoreForSessionTest(t, "audio-session-drain-error")
		press, _, prepare := beginVoiceSession(t, core, device, 1)
		if err := runtime.Submit(prepare); err != nil {
			t.Fatal(err)
		}
		applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
		waitSignal(t, source.readStarted)
		ended, err := core.Step(bridge.VoiceEnded(press))
		if err != nil {
			t.Fatal(err)
		}
		applySourceEffects(t, runtime, ended)
		completion := waitCompletion(t, runtime)
		if !errors.Is(completion.Err, drainErr) {
			t.Fatalf("drain failure completion error = %v", completion.Err)
		}
		if effects := applyExpectedFailure(t, core, completion); hasAudioEffect[bridge.CloseGateEffect](effects) == false {
			t.Fatalf("drain failure did not cancel the session: %#v", effects)
		}
	})
}

func TestRuntimeBackpressureHasABoundedRetryWindow(t *testing.T) {
	source, sink := newSourceStub(), newSinkStub()
	sink.defaultWriteErr = audiooutput.ErrBackpressure
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return testResources(source, sink), nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-backpressure-timeout")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, runtime, applyCompletion(t, core, waitCompletion(t, runtime)))
	waitSignal(t, source.readStarted)
	source.reads <- readResult{pcm: []int16{1, 2, 3}}
	started := time.Now()
	completion := waitCompletion(t, runtime)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("backpressure retry exceeded its bounded window: %s", elapsed)
	}
	if !errors.Is(completion.Err, context.DeadlineExceeded) {
		t.Fatalf("persistent backpressure completion error = %v", completion.Err)
	}
	if got := sink.writeCalls.Load(); got < 2 {
		t.Fatalf("persistent backpressure was not retried: calls=%d", got)
	}
	if effects := applyExpectedFailure(t, core, completion); !hasAudioEffect[bridge.CloseGateEffect](effects) {
		t.Fatalf("backpressure failure did not cancel the session: %#v", effects)
	}
}

func TestRuntimeCancelDoesNotDrainAndIgnoresOldTokenEffects(t *testing.T) {
	sources := []*sourceStub{newSourceStub(), newSourceStub()}
	sinks := []*sinkStub{newSinkStub(), newSinkStub()}
	var prepares atomic.Int32
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		index := int(prepares.Add(1)) - 1
		return testResources(sources[index], sinks[index]), nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-old-token")
	press1, token1, prepare1 := beginVoiceSession(t, core, device, 1)
	if err := runtime.Submit(prepare1); err != nil {
		t.Fatal(err)
	}
	prepared1 := waitCompletion(t, runtime)
	applySourceEffects(t, runtime, applyCompletion(t, core, prepared1))
	cancelEffects, err := core.Step(bridge.RouteAvailable(false))
	if err != nil {
		t.Fatal(err)
	}
	var staleDispose bridge.DisposeResourceEffect
	for _, effect := range cancelEffects {
		if dispose, ok := effect.(bridge.DisposeResourceEffect); ok {
			staleDispose = dispose
		}
	}
	applySourceEffects(t, runtime, cancelEffects)
	waitSignal(t, sources[0].closeStart)
	waitSignal(t, sinks[0].closeStart)
	select {
	case completion := <-runtime.Completions():
		t.Fatalf("cancel produced a completion (it must not report Drained): %#v", completion)
	default:
	}
	if _, err := core.Step(bridge.RouteAvailable(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(bridge.VoiceEnded(press1)); err != nil {
		t.Fatal(err)
	}
	_, token2, prepare2 := beginVoiceSession(t, core, device, 2)
	if err := runtime.Submit(prepare2); err != nil {
		t.Fatal(err)
	}
	prepared2 := waitCompletion(t, runtime)
	applySourceEffects(t, runtime, applyCompletion(t, core, prepared2))
	if err := runtime.Submit(bridge.StopSourceEffect{Token: token1}); err != nil {
		t.Fatal(err)
	}
	if staleDispose.ResourceID == "" {
		t.Fatal("cancel effects did not carry the first resource identity")
	}
	if err := runtime.Submit(staleDispose); err != nil {
		t.Fatal(err)
	}
	if got := sources[1].stopCalls.Load(); got != 0 {
		t.Fatalf("old StopSource reached current session: calls=%d", got)
	}
	if got := sources[1].closeCalls.Load(); got != 0 {
		t.Fatalf("old Dispose closed current source: calls=%d", got)
	}
	if got := sinks[1].closeCalls.Load(); got != 0 {
		t.Fatalf("old Dispose closed current sink (token2=%d): calls=%d", token2.ID(), got)
	}
}

func hasAudioEffect[T bridge.Effect](effects []bridge.Effect) bool {
	for _, effect := range effects {
		if _, ok := effect.(T); ok {
			return true
		}
	}
	return false
}

func closedSignal() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func mustPressToken(t *testing.T, device bridge.Device, button string, pressID uint64) bridge.PressToken {
	t.Helper()
	press, err := bridge.NewPressToken(device, button, pressID)
	if err != nil {
		t.Fatal(err)
	}
	return press
}
