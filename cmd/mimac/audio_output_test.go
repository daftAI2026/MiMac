package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
)

type audioOutputOwnerTestSink struct {
	writes      [][]int16
	writeErr    error
	drainErr    error
	closeErrors []error
	drainCalls  int
	closeCalls  int
	lastContext context.Context
	drainStart  chan struct{}
	drainResume chan struct{}
}

type audioOutputTestContextKey struct{}

func (s *audioOutputOwnerTestSink) TryWrite(samples []int16) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.writes = append(s.writes, append([]int16(nil), samples...))
	return nil
}

func (s *audioOutputOwnerTestSink) Drain(ctx context.Context) error {
	s.drainCalls++
	s.lastContext = ctx
	if s.drainStart != nil {
		close(s.drainStart)
		select {
		case <-s.drainResume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.drainErr
}

func (s *audioOutputOwnerTestSink) Close() error {
	s.closeCalls++
	if len(s.closeErrors) == 0 {
		return nil
	}
	err := s.closeErrors[0]
	s.closeErrors = s.closeErrors[1:]
	return err
}

func TestAudioOutputOwnerEnableIsLazyAndUsesExplicitConfig(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "test-output", SampleRate: 16000}
	sink := &audioOutputOwnerTestSink{}
	owner := newAudioOutputOwner()
	factoryCalls := 0
	var gotConfig audiooutput.Config
	if err := owner.open(config); !errors.Is(err, errAudioOutputOwnerInactive) {
		t.Fatalf("default owner open = %v", err)
	}

	if err := owner.enable(func(got audiooutput.Config) (audioOutputSink, error) {
		factoryCalls++
		gotConfig = got
		return sink, nil
	}); err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 0 || owner.sink != nil {
		t.Fatal("enabling the owner opened a device")
	}
	if err := owner.open(config); err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 1 || gotConfig != config || owner.sink != sink {
		t.Fatalf("explicit open: calls=%d config=%+v sink=%p", factoryCalls, gotConfig, owner.sink)
	}
	if err := owner.open(config); !errors.Is(err, errAudioOutputAlreadyOpen) || factoryCalls != 1 {
		t.Fatalf("second open = %v, calls=%d", err, factoryCalls)
	}

	pcm := []int16{1, -2, 3}
	if err := owner.tryWrite(pcm); err != nil {
		t.Fatal(err)
	}
	pcm[0] = 99
	if !reflect.DeepEqual(sink.writes, [][]int16{{1, -2, 3}}) {
		t.Fatalf("write was not delegated as one unchanged frame: %v", sink.writes)
	}

	ctx := context.WithValue(context.Background(), audioOutputTestContextKey{}, "drain")
	if err := owner.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if sink.drainCalls != 1 || sink.lastContext != ctx {
		t.Fatal("Drain did not receive the caller context")
	}
	if err := owner.disable(); err != nil {
		t.Fatal(err)
	}
	if owner.sink != nil || owner.enabled || owner.factory != nil || sink.closeCalls != 1 {
		t.Fatalf("disable did not close and clear owner: %+v closeCalls=%d", owner, sink.closeCalls)
	}
	if err := owner.disable(); err != nil {
		t.Fatalf("idempotent disable: %v", err)
	}
	if err := owner.open(config); !errors.Is(err, errAudioOutputOwnerInactive) {
		t.Fatalf("open after disable = %v", err)
	}
}

func TestAudioOutputOwnerRetainsPartialOpenAndFailedClose(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "test-output", SampleRate: 8000}
	partialErr := errors.New("partial open cleanup failed")
	closeErr := errors.New("dispose failed")
	partial := &audioOutputOwnerTestSink{closeErrors: []error{closeErr, nil}}
	ready := &audioOutputOwnerTestSink{}
	factoryCalls := 0
	owner := newAudioOutputOwner()
	if err := owner.enable(func(got audiooutput.Config) (audioOutputSink, error) {
		factoryCalls++
		if got != config {
			t.Fatalf("factory config = %+v, want %+v", got, config)
		}
		if factoryCalls == 1 {
			return partial, partialErr
		}
		return ready, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := owner.open(config); !errors.Is(err, partialErr) {
		t.Fatalf("partial open = %v", err)
	}
	if owner.sink != partial || owner.ready || factoryCalls != 1 {
		t.Fatal("partial resource was not retained in a non-writable state")
	}
	if err := owner.tryWrite([]int16{1}); !errors.Is(err, partialErr) {
		t.Fatalf("partial sink accepted writes: %v", err)
	}
	if err := owner.open(config); !errors.Is(err, errAudioOutputAlreadyOpen) || factoryCalls != 1 {
		t.Fatalf("reopen with partial resource = %v, calls=%d", err, factoryCalls)
	}
	if err := owner.close(); !errors.Is(err, closeErr) {
		t.Fatalf("failed close = %v", err)
	}
	if owner.sink != partial || factoryCalls != 1 {
		t.Fatal("failed close discarded the native cleanup handle")
	}
	if err := owner.open(config); !errors.Is(err, errAudioOutputAlreadyOpen) || factoryCalls != 1 {
		t.Fatalf("reopen while close failed = %v, calls=%d", err, factoryCalls)
	}
	if err := owner.close(); err != nil {
		t.Fatalf("cleanup retry = %v", err)
	}
	if owner.sink != nil {
		t.Fatal("successful cleanup retry did not clear the handle")
	}
	if err := owner.open(config); err != nil {
		t.Fatal(err)
	}
	if owner.sink != ready || factoryCalls != 2 {
		t.Fatal("owner did not permit a new sink after cleanup succeeded")
	}
}

func TestAudioOutputOwnerStopFailureRetainsFactoryAndSink(t *testing.T) {
	closeErr := errors.New("close failed")
	sink := &audioOutputOwnerTestSink{closeErrors: []error{closeErr, nil}}
	config := audiooutput.Config{DeviceUID: "test-output", SampleRate: 16000}
	owner := newAudioOutputOwner()
	if err := owner.enable(func(audiooutput.Config) (audioOutputSink, error) { return sink, nil }); err != nil {
		t.Fatal(err)
	}
	if err := owner.open(config); err != nil {
		t.Fatal(err)
	}
	if err := owner.disable(); !errors.Is(err, closeErr) {
		t.Fatalf("first stop = %v", err)
	}
	if !owner.enabled || owner.factory == nil || owner.sink != sink {
		t.Fatal("failed Stop reported inactive or dropped its resource")
	}
	if err := owner.open(config); !errors.Is(err, errAudioOutputAlreadyOpen) {
		t.Fatalf("open while cleanup failed = %v", err)
	}
	if err := owner.disable(); err != nil {
		t.Fatalf("stop cleanup retry = %v", err)
	}
	if owner.enabled || owner.factory != nil || owner.sink != nil || sink.closeCalls != 2 {
		t.Fatal("successful Stop retry did not clear the owner")
	}
}

func TestAudioOutputOwnerDrainAndWriteFailuresRequireClose(t *testing.T) {
	writeErr := errors.New("queue stopped")
	drainErr := errors.New("drain timed out")
	sink := &audioOutputOwnerTestSink{}
	second := &audioOutputOwnerTestSink{drainErr: drainErr}
	factoryCalls := 0
	config := audiooutput.Config{DeviceUID: "test-output", SampleRate: 8000}
	owner := newAudioOutputOwner()
	if err := owner.enable(func(audiooutput.Config) (audioOutputSink, error) {
		factoryCalls++
		if factoryCalls == 1 {
			return sink, nil
		}
		return second, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := owner.open(config); err != nil {
		t.Fatal(err)
	}

	sink.writeErr = audiooutput.ErrBackpressure
	if err := owner.tryWrite([]int16{1}); !errors.Is(err, audiooutput.ErrBackpressure) {
		t.Fatalf("backpressure = %v", err)
	}
	if owner.fault != nil || owner.sink != sink {
		t.Fatal("bounded backpressure faulted or dropped the sink")
	}

	sink.writeErr = writeErr
	if err := owner.tryWrite([]int16{2}); !errors.Is(err, writeErr) {
		t.Fatalf("write failure = %v", err)
	}
	if err := owner.tryWrite([]int16{3}); !errors.Is(err, writeErr) {
		t.Fatalf("write retried a faulted sink = %v", err)
	}
	if owner.sink != sink {
		t.Fatal("write failure discarded the sink handle")
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}

	if err := owner.open(config); err != nil {
		t.Fatal(err)
	}
	if owner.sink != second {
		t.Fatal("second session did not receive a fresh sink")
	}
	if err := owner.drain(context.Background()); !errors.Is(err, drainErr) {
		t.Fatalf("drain failure = %v", err)
	}
	if err := owner.tryWrite([]int16{4}); !errors.Is(err, drainErr) {
		t.Fatalf("write after drain failure = %v", err)
	}
	if owner.sink != second {
		t.Fatal("drain failure discarded the sink handle")
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
}

func TestAudioOutputOwnerRejectsNilFactoryAndNilSink(t *testing.T) {
	owner := newAudioOutputOwner()
	if err := owner.enable(nil); !errors.Is(err, errAudioOutputFactoryNil) {
		t.Fatalf("nil factory = %v", err)
	}
	if err := owner.enable(func(audiooutput.Config) (audioOutputSink, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := owner.open(audiooutput.Config{DeviceUID: "test-output", SampleRate: 16000}); !errors.Is(err, errAudioOutputSinkNil) {
		t.Fatalf("nil sink = %v", err)
	}
	if owner.sink != nil {
		t.Fatal("nil sink created owned state")
	}
}

func TestOpenSystemAudioOutputReturnsNilInterfaceForNilConcreteSink(t *testing.T) {
	sink, err := openSystemAudioOutput(audiooutput.Config{})
	if err == nil || sink != nil {
		t.Fatalf("invalid config returned sink=%v err=%v", sink, err)
	}
}

func TestAudioOutputOwnerCloseAndReopenDoNotAcceptLateDrainFault(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "first", SampleRate: 8000}
	lateDrainErr := errors.New("old drain completed late")
	oldSink := &audioOutputOwnerTestSink{
		drainErr:    lateDrainErr,
		drainStart:  make(chan struct{}),
		drainResume: make(chan struct{}),
	}
	newSink := &audioOutputOwnerTestSink{}
	factoryCalls := 0
	owner := newAudioOutputOwner()
	if err := owner.enable(func(audiooutput.Config) (audioOutputSink, error) {
		factoryCalls++
		if factoryCalls == 1 {
			return oldSink, nil
		}
		return newSink, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := owner.open(config); err != nil {
		t.Fatal(err)
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	drainResult := make(chan error, 1)
	go func() { drainResult <- owner.drain(drainCtx) }()
	select {
	case <-oldSink.drainStart:
	case <-drainCtx.Done():
		t.Fatal("Drain did not start")
	}

	if err := owner.close(); err != nil {
		t.Fatalf("Close blocked or failed during Drain: %v", err)
	}
	if err := owner.open(audiooutput.Config{DeviceUID: "second", SampleRate: 16000}); err != nil {
		t.Fatalf("open after close: %v", err)
	}
	if owner.sink != newSink || factoryCalls != 2 {
		t.Fatal("new session did not own the second sink")
	}

	close(oldSink.drainResume)
	select {
	case err := <-drainResult:
		if !errors.Is(err, lateDrainErr) {
			t.Fatalf("late Drain = %v", err)
		}
	case <-drainCtx.Done():
		t.Fatal("late Drain did not return")
	}
	if owner.fault != nil || owner.sink != newSink {
		t.Fatal("late Drain faulted or replaced the new sink")
	}
	if err := owner.tryWrite([]int16{7}); err != nil {
		t.Fatalf("new sink was faulted by stale Drain: %v", err)
	}
}
