package audiooutput

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeQueue struct {
	returned                                                                              func(int)
	pcm                                                                                   [][]int16
	slots                                                                                 []int
	starts, checks, disposes                                                              int
	pauses, primes                                                                        int
	stops                                                                                 []bool
	running                                                                               bool
	stayStopped                                                                           bool
	enqueueError, primeError, startError, pauseError, stopError, checkError, disposeError error
	onStop                                                                                func(bool)
	events                                                                                []string
	primeEntered, startEntered, disposeCalled                                             chan struct{}
	primeRelease, startRelease                                                            chan struct{}
	disposeOnce                                                                           sync.Once
}

func (q *fakeQueue) Enqueue(slot int, pcm []int16) error {
	q.events = append(q.events, "enqueue")
	if q.enqueueError != nil {
		return q.enqueueError
	}
	q.slots = append(q.slots, slot)
	q.pcm = append(q.pcm, append([]int16(nil), pcm...))
	return nil
}
func (q *fakeQueue) Prime() error {
	q.events = append(q.events, "prime")
	q.primes++
	if q.primeEntered != nil {
		close(q.primeEntered)
		<-q.primeRelease
	}
	return q.primeError
}
func (q *fakeQueue) Start() error {
	q.events = append(q.events, "start")
	q.starts++
	if q.startEntered != nil {
		close(q.startEntered)
		<-q.startRelease
	}
	q.running = q.startError == nil && !q.stayStopped
	return q.startError
}
func (q *fakeQueue) Pause() error {
	q.events = append(q.events, "pause")
	q.pauses++
	if q.pauseError == nil {
		q.running = false
	}
	return q.pauseError
}
func (q *fakeQueue) Stop(immediate bool) error {
	q.events = append(q.events, "stop")
	q.stops = append(q.stops, immediate)
	if q.onStop != nil {
		q.onStop(immediate)
	}
	return q.stopError
}
func (q *fakeQueue) Check() (bool, error) {
	q.events = append(q.events, "check")
	q.checks++
	return q.running, q.checkError
}
func (q *fakeQueue) Dispose() error {
	q.events = append(q.events, "dispose")
	q.disposes++
	if q.disposeCalled != nil {
		q.disposeOnce.Do(func() { close(q.disposeCalled) })
	}
	return q.disposeError
}
func testSink(t *testing.T) (*Sink, *fakeQueue) {
	t.Helper()
	q := &fakeQueue{}
	s, err := openWith(Config{DeviceUID: "test-only", SampleRate: 16000}, func(_ Config, returned func(int)) (nativeQueue, error) { q.returned = returned; return q, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.disposeError = nil; _ = s.Close() })
	return s, q
}

type queueWithoutPreparation struct{ q *fakeQueue }

func (q queueWithoutPreparation) Enqueue(slot int, pcm []int16) error { return q.q.Enqueue(slot, pcm) }
func (q queueWithoutPreparation) Start() error                        { return q.q.Start() }
func (q queueWithoutPreparation) Stop(immediate bool) error           { return q.q.Stop(immediate) }
func (q queueWithoutPreparation) Check() (bool, error)                { return q.q.Check() }
func (q queueWithoutPreparation) Dispose() error                      { return q.q.Dispose() }

func TestRejectConfigurationBeforeNativeOpen(t *testing.T) {
	for _, cfg := range []Config{{}, {DeviceUID: "   ", SampleRate: 16000}, {DeviceUID: "a\x00b", SampleRate: 16000}, {DeviceUID: "test", SampleRate: 44100}} {
		s, err := openWith(cfg, func(Config, func(int)) (nativeQueue, error) {
			t.Fatal("native open on invalid config")
			return nil, nil
		})
		if err == nil || s != nil {
			t.Fatalf("cfg=%+v sink=%v err=%v", cfg, s, err)
		}
	}
}

func TestBoundedWritesAreAllOrNothingOnBackpressure(t *testing.T) {
	s, q := testSink(t)
	pcm := make([]int16, 3*320)
	for i := range pcm {
		pcm[i] = int16(i - 200)
	}
	if err := s.TryWrite(pcm); err != nil {
		t.Fatal(err)
	}
	if len(q.pcm) != 3 || q.starts != 1 {
		t.Fatalf("buffers=%d starts=%d", len(q.pcm), q.starts)
	}
	if !reflect.DeepEqual(q.pcm[1], pcm[320:640]) {
		t.Fatal("PCM changed")
	}
	pcm[0] = 9999
	if q.pcm[0][0] == 9999 {
		t.Fatal("borrowed caller PCM after return")
	}
	if err := s.TryWrite(make([]int16, 321)); !errors.Is(err, ErrBackpressure) {
		t.Fatal(err)
	}
	if len(q.pcm) != 3 {
		t.Fatal("partial enqueue on backpressure")
	}
	q.returned(q.slots[0])
	if err := s.TryWrite(make([]int16, 321)); err != nil {
		t.Fatal(err)
	}
	if len(q.pcm) != 5 || len(q.pcm[4]) != 1 || q.starts != 1 {
		t.Fatal("buffer reuse/partial final buffer/start")
	}
}

func TestOversizeAndEmptyWritesDoNotStart(t *testing.T) {
	s, q := testSink(t)
	if err := s.TryWrite(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.TryWrite(make([]int16, 1281)); !errors.Is(err, ErrBlockTooLarge) {
		t.Fatal(err)
	}
	if q.starts != 0 || len(q.pcm) != 0 {
		t.Fatal("invalid write started queue")
	}
}

func TestPreparePrimesPausesAndResumesSameQueue(t *testing.T) {
	s, q := testSink(t)
	if err := s.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if q.primes != 1 || q.starts != 1 || q.pauses != 1 || len(q.pcm) != bufferCount {
		t.Fatalf("primes=%d starts=%d pauses=%d buffers=%d", q.primes, q.starts, q.pauses, len(q.pcm))
	}
	for i, silence := range q.pcm {
		if len(silence) != 320 {
			t.Fatalf("silence[%d] frames=%d", i, len(silence))
		}
		for _, sample := range silence {
			if sample != 0 {
				t.Fatalf("silence[%d] contains %d", i, sample)
			}
		}
	}
	want := []string{"enqueue", "enqueue", "enqueue", "enqueue", "check", "prime", "check", "start", "check", "pause"}
	if !reflect.DeepEqual(q.events, want) {
		t.Fatalf("events=%v want=%v", q.events, want)
	}

	// The first write resumes before reporting backpressure, even while all four
	// prepared buffers still belong to the native queue.
	pcm := []int16{12, -34}
	if err := s.TryWrite(pcm); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("first write=%v", err)
	}
	if q.starts != 2 || len(q.pcm) != bufferCount {
		t.Fatalf("resume starts=%d buffers=%d", q.starts, len(q.pcm))
	}
	q.returned(q.slots[0])
	if err := s.TryWrite(pcm); err != nil {
		t.Fatal(err)
	}
	if q.starts != 2 || len(q.pcm) != bufferCount+1 || !reflect.DeepEqual(q.pcm[len(q.pcm)-1], pcm) {
		t.Fatalf("queue was rebuilt or PCM changed: starts=%d buffers=%d", q.starts, len(q.pcm))
	}
}

func TestPrepareIsIdempotentAndRequiresFreshQueue(t *testing.T) {
	s, q := testSink(t)
	if err := s.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if q.primes != 1 || q.starts != 1 || q.pauses != 1 {
		t.Fatalf("repeat prepare changed native queue: primes=%d starts=%d pauses=%d", q.primes, q.starts, q.pauses)
	}
	if err := s.TryWrite([]int16{1}); !errors.Is(err, ErrBackpressure) {
		t.Fatal(err)
	}
	if err := s.Prepare(context.Background()); err != nil {
		t.Fatalf("idempotent prepare after use: %v", err)
	}

	other, otherQ := testSink(t)
	if err := other.TryWrite([]int16{1}); err != nil {
		t.Fatal(err)
	}
	if err := other.Prepare(context.Background()); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatalf("prepare after ordinary write=%v", err)
	}
	if otherQ.primes != 0 {
		t.Fatal("already-used queue was primed")
	}
}

func TestPreparePreservesTwentyMillisecondBuffersAtBothRates(t *testing.T) {
	for _, test := range []struct {
		rate   int
		frames int
	}{{rate: 8000, frames: 160}, {rate: 16000, frames: 320}} {
		t.Run(fmt.Sprint(test.rate), func(t *testing.T) {
			q := &fakeQueue{}
			s, err := openWith(Config{DeviceUID: "test-only", SampleRate: test.rate}, func(_ Config, returned func(int)) (nativeQueue, error) {
				q.returned = returned
				return q, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(q.pcm) != bufferCount {
				t.Fatalf("buffers=%d", len(q.pcm))
			}
			for _, pcm := range q.pcm {
				if len(pcm) != test.frames {
					t.Fatalf("frames=%d want=%d", len(pcm), test.frames)
				}
			}
		})
	}
}

func TestPrepareIsOptionalForLegacyNativeQueues(t *testing.T) {
	q := &fakeQueue{}
	s, err := openWith(Config{DeviceUID: "test-only", SampleRate: 16000}, func(_ Config, returned func(int)) (nativeQueue, error) {
		q.returned = returned
		return queueWithoutPreparation{q: q}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Prepare(context.Background()); !errors.Is(err, ErrPrepareUnsupported) {
		t.Fatal(err)
	}
	if err := s.TryWrite([]int16{7}); err != nil {
		t.Fatalf("ordinary path changed: %v", err)
	}
}

func TestPreparedIdleDrainReturnsWithoutPlayingSilence(t *testing.T) {
	s, q := testSink(t)
	if err := s.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(q.stops) != 0 {
		t.Fatalf("idle prepared Drain requested a non-immediate stop: %v", q.stops)
	}
	if err := s.TryWrite([]int16{1}); !errors.Is(err, ErrDraining) {
		t.Fatal(err)
	}
}

func TestPrepareCancellationBeforeWorkLeavesQueueUntouched(t *testing.T) {
	s, q := testSink(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(q.events) != 0 || q.disposes != 0 {
		t.Fatalf("canceled prepare touched queue: events=%v disposes=%d", q.events, q.disposes)
	}
	if err := s.TryWrite([]int16{1}); err != nil {
		t.Fatal("pre-cancel did not poison fresh queue:", err)
	}
}

func TestPrepareCancellationWhileWaitingPausesButKeepsOwnership(t *testing.T) {
	s, q := testSink(t)
	q.stayStopped = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := s.Prepare(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if q.pauses != 1 || q.disposes != 0 {
		t.Fatalf("pauses=%d disposes=%d", q.pauses, q.disposes)
	}
	if err := s.TryWrite([]int16{1}); !errors.Is(err, ErrFaulted) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if q.disposes != 1 {
		t.Fatal("Close did not release the retained queue")
	}
}

func TestPrepareCancellationCannotInterruptStartAndCloseRetainsQueue(t *testing.T) {
	s, q := testSink(t)
	q.startEntered = make(chan struct{})
	q.startRelease = make(chan struct{})
	q.disposeCalled = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Prepare(ctx) }()
	<-q.startEntered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Prepare returned while native Start was blocked: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-q.disposeCalled:
		t.Fatal("canceled Prepare released native ownership")
	default:
	}
	close(q.startRelease)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if q.pauses != 1 || q.disposes != 0 {
		t.Fatalf("pauses=%d disposes=%d", q.pauses, q.disposes)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	<-q.disposeCalled
}

func TestPrepareAndCloseAreMutuallyExclusive(t *testing.T) {
	s, q := testSink(t)
	q.primeEntered = make(chan struct{})
	q.primeRelease = make(chan struct{})
	q.disposeCalled = make(chan struct{})
	prepareDone := make(chan error, 1)
	go func() { prepareDone <- s.Prepare(context.Background()) }()
	<-q.primeEntered
	closeAttempted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeAttempted)
		closeDone <- s.Close()
	}()
	<-closeAttempted
	select {
	case <-q.disposeCalled:
		t.Fatal("Close disposed queue during Prepare")
	case <-time.After(20 * time.Millisecond):
	}
	close(q.primeRelease)
	if err := <-prepareDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q.events, []string{"enqueue", "enqueue", "enqueue", "enqueue", "check", "prime", "check", "start", "check", "pause", "stop", "dispose"}) {
		t.Fatalf("native operations crossed Close: %v", q.events)
	}
}

func TestPreparePartialNativeFailuresStayOwnedAndFaulted(t *testing.T) {
	for _, where := range []string{"enqueue", "prime", "start", "check", "pause"} {
		t.Run(where, func(t *testing.T) {
			s, q := testSink(t)
			failure := errors.New(where + " failed")
			switch where {
			case "enqueue":
				q.enqueueError = failure
			case "prime":
				q.primeError = failure
			case "start":
				q.startError = failure
			case "check":
				q.checkError = failure
			case "pause":
				q.pauseError = failure
			}
			if err := s.Prepare(context.Background()); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if err := s.TryWrite([]int16{1}); !errors.Is(err, ErrFaulted) {
				t.Fatal(err)
			}
			if q.disposes != 0 {
				t.Fatal("Prepare disposed the queue after a partial failure")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if q.disposes != 1 {
				t.Fatal("Close did not dispose the retained queue")
			}
		})
	}
}

func TestDrainWaitsForBothReturnedBuffersAndStoppedQueue(t *testing.T) {
	s, q := testSink(t)
	if err := s.TryWrite([]int16{1, 2}); err != nil {
		t.Fatal(err)
	}
	q.onStop = func(immediate bool) {
		if immediate {
			return
		}
		q.returned(q.slots[0])
		// Returned memory is not proof of playout. Keep running until timeout.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if q.disposes != 1 || !reflect.DeepEqual(q.stops, []bool{false, true}) {
		t.Fatalf("stops=%v disposes=%d", q.stops, q.disposes)
	}
}

func TestDrainRequiresBufferReturnsEvenWhenStopped(t *testing.T) {
	s, q := testSink(t)
	_ = s.TryWrite([]int16{1})
	q.onStop = func(bool) { q.running = false }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestNormalDrainAndIdempotentClose(t *testing.T) {
	s, q := testSink(t)
	_ = s.TryWrite([]int16{1})
	q.onStop = func(bool) { q.running = false; q.returned(q.slots[0]) }
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.TryWrite([]int16{1}); !errors.Is(err, ErrDraining) {
		t.Fatal(err)
	}
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if q.disposes != 1 {
		t.Fatal(q.disposes)
	}
	q.returned(q.slots[0]) // A retired callback cannot resurrect a closed sink.
	if err := s.TryWrite([]int16{1}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestEmptyDrainDoesNotStartOrStop(t *testing.T) {
	s, q := testSink(t)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if q.starts != 0 || len(q.stops) != 0 {
		t.Fatal("empty queue started/stopped")
	}
}

func TestCancelClosesAndPreservesCleanupError(t *testing.T) {
	s, q := testSink(t)
	_ = s.TryWrite([]int16{1})
	failure := errors.New("dispose failed")
	q.disposeError = failure
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Drain(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err = s.TryWrite([]int16{1}); !errors.Is(err, ErrFaulted) {
		t.Fatal(err)
	}
	q.disposeError = nil
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if q.disposes != 2 {
		t.Fatal(q.disposes)
	}
}

func TestNativeFailuresLockWrites(t *testing.T) {
	for _, where := range []string{"check", "enqueue", "start"} {
		t.Run(where, func(t *testing.T) {
			s, q := testSink(t)
			failure := errors.New(where)
			switch where {
			case "check":
				q.checkError = failure
			case "enqueue":
				q.enqueueError = failure
			case "start":
				q.startError = failure
			}
			if err := s.TryWrite([]int16{1}); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if err := s.TryWrite([]int16{1}); !errors.Is(err, ErrFaulted) {
				t.Fatal(err)
			}
		})
	}
}

func TestDrainRouteFailureClosesWithoutClaimingSuccess(t *testing.T) {
	s, q := testSink(t)
	_ = s.TryWrite([]int16{1})
	failure := errors.New("route moved")
	q.checkError = failure
	if err := s.Drain(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if q.disposes != 1 {
		t.Fatal("failed drain leaked queue")
	}
}

func TestUnexpectedStopRejectsFurtherPCM(t *testing.T) {
	s, q := testSink(t)
	_ = s.TryWrite([]int16{1})
	_ = s.TryWrite([]int16{2}) // Observe the completed asynchronous start.
	q.running = false
	if err := s.TryWrite([]int16{3}); err == nil {
		t.Fatal("stopped queue accepted PCM")
	}
	if len(q.pcm) != 2 {
		t.Fatal("PCM enqueued after unexpected stop")
	}
	if err := s.TryWrite([]int16{3}); !errors.Is(err, ErrFaulted) {
		t.Fatal(err)
	}
}

func TestAsyncStartPendingAcceptsBoundedPCM(t *testing.T) {
	s, q := testSink(t)
	if err := s.TryWrite([]int16{1}); err != nil {
		t.Fatal(err)
	}
	q.running = false // AudioQueueStart can return before IsRunning changes.
	if err := s.TryWrite([]int16{2}); err != nil {
		t.Fatalf("pending start rejected: %v", err)
	}
	q.running = true
	if err := s.TryWrite([]int16{3}); err != nil {
		t.Fatal(err)
	}
	q.running = false
	if err := s.TryWrite([]int16{4}); err == nil {
		t.Fatal("post-start stop accepted")
	}
}

func TestPartialOpenRetainsFailedCleanup(t *testing.T) {
	opening, closing := errors.New("allocate failed"), errors.New("dispose failed")
	q := &fakeQueue{disposeError: closing}
	s, err := openWith(Config{DeviceUID: "test", SampleRate: 8000}, func(Config, func(int)) (nativeQueue, error) { return q, errors.Join(opening, closing) })
	if s == nil || !errors.Is(err, opening) || !errors.Is(err, closing) {
		t.Fatalf("sink=%v err=%v", s, err)
	}
	if err := s.TryWrite([]int16{1}); !errors.Is(err, ErrFaulted) {
		t.Fatal(err)
	}
	q.disposeError = nil
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidCallbackFaultsWithoutBlocking(t *testing.T) {
	s, q := testSink(t)
	q.returned(99)
	if err := s.TryWrite([]int16{1}); !errors.Is(err, ErrFaulted) {
		t.Fatal(err)
	}
}

func TestEightKBufferCapacity(t *testing.T) {
	q := &fakeQueue{}
	s, err := openWith(Config{DeviceUID: "test", SampleRate: 8000}, func(_ Config, cb func(int)) (nativeQueue, error) { q.returned = cb; return q, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.TryWrite(make([]int16, 640)); err != nil {
		t.Fatal(err)
	}
	if len(q.pcm) != 4 || len(q.pcm[0]) != 160 {
		t.Fatal("8k buffer duration differs")
	}
}
