package audiosession

import (
	"context"
	"errors"
	"testing"
	"time"

	"mimac.local/mimac/internal/bridge"
)

func TestRuntimeStopErrorCancelsBlockedReadAndPreservesCause(t *testing.T) {
	stopErr := errors.New("source stop failed")
	source, sink := newSourceStub(), newSinkStub()
	transcript := &transcriptSinkStub{Sink: sink}
	source.stopErr = stopErr
	runtime := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{Source: source, Sink: transcript, SampleRate: 16000}, nil
	})
	core, device := newCoreForSessionTest(t, "audio-session-stop-error")
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
	waitSignal(t, source.stopStarted)

	failed := waitCompletion(t, runtime)
	if !errors.Is(failed.Err, stopErr) {
		t.Fatalf("Stop failure cause = %v, want original error %v", failed.Err, stopErr)
	}
	if failed.Transcript != nil || transcript.resultCalls.Load() != 0 {
		t.Fatalf("failed source completion carried a transcript: %#v", failed.Transcript)
	}
	effects := applyExpectedFailure(t, core, failed)
	if !hasAudioEffect[bridge.CloseGateEffect](effects) || !hasAudioEffect[bridge.StopSourceEffect](effects) {
		t.Fatalf("Stop failure did not report SourceFailed to Core: %#v", effects)
	}
	applySourceEffects(t, runtime, effects)

	ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("close after Stop failure: %v", err)
	}
	if got := source.closeCalls.Load(); got != 1 {
		t.Fatalf("source close calls = %d, want 1", got)
	}
	if got := sink.closeCalls.Load(); got != 1 {
		t.Fatalf("sink close calls = %d, want 1", got)
	}
	select {
	case extra := <-runtime.Completions():
		t.Fatalf("unexpected completion after SourceFailed: %#v", extra)
	default:
	}
}

type stopIgnoringCancellationSource struct {
	*sourceStub
	entered chan struct{}
	release chan struct{}
}

func (s *stopIgnoringCancellationSource) Stop(context.Context) error {
	signal(s.entered)
	<-s.release // Model a native Stop that ignores its deadline.
	return nil
}

func TestRuntimeStopDeadlineRetainsResourcesUntilStopReturns(t *testing.T) {
	base := newSourceStub()
	source := &stopIgnoringCancellationSource{
		sourceStub: base,
		entered:    make(chan struct{}, 1),
		release:    make(chan struct{}, 1),
	}
	sink := newSinkStub()
	transcript := &transcriptSinkStub{Sink: sink}
	runtime, err := New(func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{Source: source, Sink: transcript, SampleRate: 16000}, nil
	})
	if err != nil {
		t.Fatalf("New runtime: %v", err)
	}
	t.Cleanup(func() {
		signal(source.release)
		ctx, cancel := context.WithTimeout(context.Background(), audioSessionTestTimeout)
		defer cancel()
		if err := runtime.Close(ctx); err != nil {
			t.Errorf("cleanup runtime: %v", err)
		}
	})

	core, device := newCoreForSessionTest(t, "audio-session-stop-timeout")
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
	waitSignal(t, source.entered)

	var failed Completion
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	select {
	case failed = <-runtime.Completions():
	case <-timer.C:
		t.Fatal("timed out waiting for the Stop deadline failure")
	}
	if !errors.Is(failed.Err, context.DeadlineExceeded) {
		t.Fatalf("Stop deadline completion error = %v, want context deadline exceeded", failed.Err)
	}
	if failed.Transcript != nil || transcript.resultCalls.Load() != 0 {
		t.Fatalf("failed source completion carried a transcript: %#v", failed.Transcript)
	}
	effects := applyExpectedFailure(t, core, failed)
	if !hasAudioEffect[bridge.StopSourceEffect](effects) {
		t.Fatalf("Stop deadline did not report SourceFailed to Core: %#v", effects)
	}
	applySourceEffects(t, runtime, effects)

	closeCtx, cancelClose := context.WithCancel(context.Background())
	cancelClose()
	closeErr := runtime.Close(closeCtx)
	if !errors.Is(closeErr, ErrCleanupPending) || !errors.Is(closeErr, context.Canceled) {
		t.Fatalf("Close with canceled context = %v, want cleanup pending and context canceled", closeErr)
	}
	if got := source.closeCalls.Load(); got != 0 {
		t.Fatalf("source closed while Stop was still blocked: calls=%d", got)
	}
	if got := sink.closeCalls.Load(); got != 0 {
		t.Fatalf("sink closed while Stop was still blocked: calls=%d", got)
	}

	signal(source.release)
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer cancelRetry()
	if err := runtime.Close(retryCtx); err != nil {
		t.Fatalf("Close retry after Stop returned: %v", err)
	}
	if got := source.closeCalls.Load(); got != 1 {
		t.Fatalf("source close calls after retry = %d, want 1", got)
	}
	if got := sink.closeCalls.Load(); got != 1 {
		t.Fatalf("sink close calls after retry = %d, want 1", got)
	}
	select {
	case extra := <-runtime.Completions():
		t.Fatalf("unexpected completion after Stop deadline failure: %#v", extra)
	default:
	}
}
