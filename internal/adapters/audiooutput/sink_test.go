package audiooutput

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeQueue struct {
	returned                                                      func(int)
	pcm                                                           [][]int16
	slots                                                         []int
	starts, checks, disposes                                      int
	stops                                                         []bool
	running                                                       bool
	enqueueError, startError, stopError, checkError, disposeError error
	onStop                                                        func(bool)
}

func (q *fakeQueue) Enqueue(slot int, pcm []int16) error {
	if q.enqueueError != nil {
		return q.enqueueError
	}
	q.slots = append(q.slots, slot)
	q.pcm = append(q.pcm, append([]int16(nil), pcm...))
	return nil
}
func (q *fakeQueue) Start() error { q.starts++; q.running = q.startError == nil; return q.startError }
func (q *fakeQueue) Stop(immediate bool) error {
	q.stops = append(q.stops, immediate)
	if q.onStop != nil {
		q.onStop(immediate)
	}
	return q.stopError
}
func (q *fakeQueue) Check() (bool, error) { q.checks++; return q.running, q.checkError }
func (q *fakeQueue) Dispose() error       { q.disposes++; return q.disposeError }
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
