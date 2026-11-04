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

// A failed receiver must release the control lease even when native Read has
// not returned. That does not permit closing memory still used by the reader.
func TestReceiverFailureReleasesCoreBeforeIgnoredReadCancellation(t *testing.T) {
	source := &rootBlockedReadSource{
		sourceStub: newSourceStub(), entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	sink := newSinkStub()
	observer := &rootClearObserver{}
	receiverEntered, receiverRelease := make(chan struct{}, 1), make(chan struct{})
	var receiverOnce sync.Once
	releaseReceiver := func() { receiverOnce.Do(func() { close(receiverRelease) }) }
	fault := errors.New("receiver failed while native read is pending")
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{Source: source, Sink: sink, Observer: observer, SampleRate: 16000,
			WaitReceiver: func(context.Context) error {
				signal(receiverEntered)
				<-receiverRelease
				return fault
			}}, nil
	})
	// Cleanup runs in reverse registration order, including a failed assertion.
	t.Cleanup(source.unblock)
	t.Cleanup(releaseReceiver)
	core, device := newCoreForSessionTest(t, "root-receiver-failure-pending-read")
	_, token, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, receiverEntered)
	waitSignal(t, source.entered)
	if len(core.Snapshot().HeldKeys) == 0 {
		t.Fatal("positive control: target key was never held")
	}
	releaseReceiver()
	failed := waitCompletion(t, r)
	if !errors.Is(failed.Err, fault) || failed.CleanupFailed || failed.Transcript != nil {
		t.Fatalf("unexpected failure completion: %+v", failed)
	}
	applySourceEffects(t, r, applyExpectedFailure(t, core, failed))
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("failure retained the target lease while Read blocked: %+v", snapshot)
	}
	if _, ok := r.InputLevel(token); ok {
		t.Fatal("pre-gate failure exposed an input level")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Close(cancelled); !errors.Is(err, ErrCleanupPending) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Close while Read ignores cancellation = %v", err)
	}
	if source.closeCalls.Load() != 0 || sink.closeCalls.Load() != 0 || observer.clears.Load() != 0 {
		t.Fatalf("cleanup before Read joined: source=%d sink=%d observer=%d",
			source.closeCalls.Load(), sink.closeCalls.Load(), observer.clears.Load())
	}
	if sink.writeCalls.Load() != 0 || observer.observes.Load() != 0 || sink.drainCalls.Load() != 0 {
		t.Fatal("receiver failure produced PCM, observation, or drain")
	}
	source.unblock()
	ctx, done := context.WithTimeout(context.Background(), audioSessionTestTimeout)
	defer done()
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if source.closedWhileReading.Load() || source.closeCalls.Load() != 1 || sink.closeCalls.Load() != 1 || observer.clears.Load() == 0 {
		t.Fatalf("cleanup after join: raced=%v source=%d sink=%d observer=%d",
			source.closedWhileReading.Load(), source.closeCalls.Load(), sink.closeCalls.Load(), observer.clears.Load())
	}
}

type rootBlockedReadSource struct {
	*sourceStub
	entered            chan struct{}
	release            chan struct{}
	releaseOnce        sync.Once
	reading            atomic.Bool
	closedWhileReading atomic.Bool
}

func (s *rootBlockedReadSource) Read(ctx context.Context, _ []int16) (int, error) {
	s.reading.Store(true)
	defer s.reading.Store(false)
	signal(s.entered)
	<-s.release
	return 0, ctx.Err()
}

func (s *rootBlockedReadSource) unblock() { s.releaseOnce.Do(func() { close(s.release) }) }

func (s *rootBlockedReadSource) Close() error {
	s.closedWhileReading.Store(s.reading.Load())
	return s.sourceStub.Close()
}

type rootClearObserver struct{ clears, observes atomic.Int32 }

func (o *rootClearObserver) Observe([]int16, time.Time) { o.observes.Add(1) }
func (o *rootClearObserver) Clear()                     { o.clears.Add(1) }
