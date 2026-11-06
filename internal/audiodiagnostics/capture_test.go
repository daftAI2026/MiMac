package audiodiagnostics

import (
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCaptureTakePreservesCompleteChunksAndStats(t *testing.T) {
	capture, err := NewCapture(8)
	if err != nil {
		t.Fatal(err)
	}

	first := []int16{-32768, 0, 32767}
	second := []int16{10, -10}
	firstCopy := append([]int16(nil), first...)
	secondCopy := append([]int16(nil), second...)
	at := time.Unix(100, 0)
	capture.Observe(first, at)
	capture.Observe(second, at.Add(25*time.Millisecond))

	if !reflect.DeepEqual(first, firstCopy) || !reflect.DeepEqual(second, secondCopy) {
		t.Fatal("Observe modified its borrowed PCM block")
	}

	got, stats, err := capture.Take()
	if err != nil {
		t.Fatal(err)
	}
	want := []int16{-32768, 0, 32767, 10, -10}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Take samples = %v, want %v", got, want)
	}
	if cap(got) != len(got) {
		t.Fatalf("Take capacity = %d, want bounded transferred capacity %d", cap(got), len(got))
	}
	if stats.ObservedBlocks != 2 || stats.ObservedSamples != 5 || stats.RetainedBlocks != 2 || stats.RetainedSamples != 5 {
		t.Fatalf("block/sample counts = %+v", stats)
	}
	if stats.OverflowBlocks != 0 || stats.OverflowSamples != 0 {
		t.Fatalf("unexpected overflow counts = %+v", stats)
	}
	if stats.ZeroSamples != 1 || stats.ClippedSamples != 2 || stats.Peak != 32768 {
		t.Fatalf("sample extrema = %+v", stats)
	}
	if math.Abs(stats.RMS-math.Sqrt((32768*32768.0+32767*32767.0+10*10.0+10*10.0)/5)) > 1e-9 {
		t.Fatalf("RMS = %.12f", stats.RMS)
	}
	if !stats.FirstObservedAt.Equal(at) || !stats.LastObservedAt.Equal(at.Add(25*time.Millisecond)) || stats.MaxBlockInterval != 25*time.Millisecond {
		t.Fatalf("timestamps = %+v", stats)
	}

	if _, _, err := capture.Take(); !errors.Is(err, ErrCaptureClosed) {
		t.Fatalf("second Take error = %v, want ErrCaptureClosed", err)
	}
}

func TestCaptureObserveDoesNotRetainInputSlice(t *testing.T) {
	capture, err := NewCapture(4)
	if err != nil {
		t.Fatal(err)
	}

	input := []int16{12, -34, 56}
	capture.Observe(input, time.Unix(200, 0))
	input[0], input[1], input[2] = 1000, 1001, 1002

	got, _, err := capture.Take()
	if err != nil {
		t.Fatal(err)
	}
	if want := []int16{12, -34, 56}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Take samples = %v, want independent copy %v", got, want)
	}
}

func TestCaptureOverflowRejectsWholeBlocksAndTakeRefusesPartialPCM(t *testing.T) {
	capture, err := NewCapture(4)
	if err != nil {
		t.Fatal(err)
	}
	backing := capture.buffer
	capture.Observe([]int16{1, 2}, time.Unix(300, 0))
	capture.Observe([]int16{3, 4, 5}, time.Unix(300, 1)) // Does not fit: reject all three.
	capture.Observe([]int16{6}, time.Unix(300, 2))       // Do not resume after a gap.

	got, stats, err := capture.Take()
	if !errors.Is(err, ErrCaptureOverflow) {
		t.Fatalf("Take error = %v, want ErrCaptureOverflow", err)
	}
	if got != nil {
		t.Fatalf("Take returned partial PCM %v on overflow", got)
	}
	if stats.ObservedBlocks != 3 || stats.ObservedSamples != 6 || stats.RetainedBlocks != 1 || stats.RetainedSamples != 2 {
		t.Fatalf("block/sample counts = %+v", stats)
	}
	if stats.OverflowBlocks != 2 || stats.OverflowSamples != 4 {
		t.Fatalf("overflow counts = %+v", stats)
	}
	for i, sample := range backing {
		if sample != 0 {
			t.Fatalf("internal sample %d remains %d after failed Take", i, sample)
		}
	}
}

func TestCaptureReleaseIsIdempotentAndZerosOwnedBuffer(t *testing.T) {
	capture, err := NewCapture(4)
	if err != nil {
		t.Fatal(err)
	}
	backing := capture.buffer
	capture.Observe([]int16{7, -8, 9}, time.Unix(400, 0))
	capture.Release()
	capture.Release()
	capture.Clear()

	for i, sample := range backing {
		if sample != 0 {
			t.Fatalf("internal sample %d remains %d after Release", i, sample)
		}
	}
	if capture.buffer != nil {
		t.Fatal("Release retained its internal buffer")
	}
	if _, _, err := capture.Take(); !errors.Is(err, ErrCaptureClosed) {
		t.Fatalf("Take after Release error = %v, want ErrCaptureClosed", err)
	}
}

func TestCaptureTakeAndReleaseSerializeWithObserve(t *testing.T) {
	capture, err := NewCapture(1024)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var observers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		observers.Add(1)
		go func(worker int) {
			defer observers.Done()
			<-start
			block := []int16{int16(worker + 1), int16(-worker - 1)}
			for i := 0; i < 32; i++ {
				capture.Observe(block, time.Unix(500, int64(worker*32+i)))
			}
		}(worker)
	}

	type takeResult struct {
		samples []int16
		stats   Stats
		err     error
	}
	result := make(chan takeResult, 1)
	var closers sync.WaitGroup
	closers.Add(2)
	go func() {
		defer closers.Done()
		<-start
		samples, stats, err := capture.Take()
		result <- takeResult{samples: samples, stats: stats, err: err}
	}()
	go func() {
		defer closers.Done()
		<-start
		capture.Release()
	}()

	close(start)
	observers.Wait()
	closers.Wait()
	firstTake := <-result
	if firstTake.err != nil && !errors.Is(firstTake.err, ErrCaptureClosed) {
		t.Fatalf("concurrent Take error = %v", firstTake.err)
	}
	if firstTake.err == nil {
		if len(firstTake.samples)%2 != 0 || uint64(len(firstTake.samples)) != firstTake.stats.RetainedSamples {
			t.Fatalf("transferred samples/stats diverge: len=%d stats=%+v", len(firstTake.samples), firstTake.stats)
		}
		if firstTake.stats.ObservedSamples < firstTake.stats.RetainedSamples {
			t.Fatalf("observed samples below retained samples: %+v", firstTake.stats)
		}
	}

	if _, _, err := capture.Take(); !errors.Is(err, ErrCaptureClosed) {
		t.Fatalf("Take after concurrent close error = %v, want ErrCaptureClosed", err)
	}
	capture.Release()
}

func TestNewCaptureRejectsInvalidCapacity(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, capacity := range []int{0, -1, maxInt/2 + 1} {
		if capture, err := NewCapture(capacity); !errors.Is(err, ErrInvalidCapacity) || capture != nil {
			t.Fatalf("NewCapture(%d) = (%v, %v), want nil and ErrInvalidCapacity", capacity, capture, err)
		}
	}
}
