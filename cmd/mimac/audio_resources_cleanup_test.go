package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

type cleanupTestSource struct {
	closeErr   error
	closeCalls atomic.Int32
	order      *[]string
	orderMu    *sync.Mutex
}

func (*cleanupTestSource) Read(ctx context.Context, _ []int16) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}
func (*cleanupTestSource) Stop(context.Context) error { return nil }
func (s *cleanupTestSource) Close() error {
	s.closeCalls.Add(1)
	if s.order != nil && s.orderMu != nil {
		s.orderMu.Lock()
		*s.order = append(*s.order, "source")
		s.orderMu.Unlock()
	}
	return s.closeErr
}

type cleanupTestSink struct {
	closeErr   error
	closeCalls atomic.Int32
	order      *[]string
	orderMu    *sync.Mutex
}

func (*cleanupTestSink) TryWrite([]int16) error      { return nil }
func (*cleanupTestSink) Drain(context.Context) error { return nil }
func (s *cleanupTestSink) Close() error {
	s.closeCalls.Add(1)
	if s.order != nil && s.orderMu != nil {
		s.orderMu.Lock()
		*s.order = append(*s.order, "sink")
		s.orderMu.Unlock()
	}
	return s.closeErr
}

func newCleanupTestRuntime(t *testing.T, source *cleanupTestSource, sink *cleanupTestSink) *audiosession.Runtime {
	t.Helper()
	runner, err := audiosession.New(func(context.Context, bridge.SessionToken) (audiosession.Resources, error) {
		return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Submit(bridge.PrepareSourceEffect{Token: preparedAudioToken(t)}); err != nil {
		t.Fatal(err)
	}
	select {
	case completion := <-runner.Completions():
		if completion.Err != nil {
			t.Fatalf("audio preparation failed: %v", completion.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("audio resource preparation did not complete")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = runner.Close(ctx)
	})
	return runner
}

func TestAudioSessionHostClosesIdlePreparedConnection(t *testing.T) {
	runner, err := audiosession.New(func(context.Context, bridge.SessionToken) (audiosession.Resources, error) {
		t.Fatal("idle runtime must not prepare a voice session")
		return audiosession.Resources{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var closeCalls atomic.Int32
	host := &audioSessionHost{
		runtime: runner,
		closeResources: func(context.Context) error {
			closeCalls.Add(1)
			return nil
		},
	}

	if err := host.close(context.Background()); err != nil {
		t.Fatalf("close idle host: %v", err)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("connection close calls=%d, want 1 for an idle prepared connection", got)
	}
}

func TestAudioSessionHostWaitsForRuntimeCleanupBeforeClosingConnection(t *testing.T) {
	var order []string
	var orderMu sync.Mutex
	source := &cleanupTestSource{order: &order, orderMu: &orderMu}
	sink := &cleanupTestSink{order: &order, orderMu: &orderMu}
	runner := newCleanupTestRuntime(t, source, sink)
	host := &audioSessionHost{
		runtime: runner,
		closeResources: func(context.Context) error {
			orderMu.Lock()
			order = append(order, "connection")
			orderMu.Unlock()
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := host.close(ctx); err != nil {
		t.Fatalf("host close: %v", err)
	}
	orderMu.Lock()
	got := append([]string(nil), order...)
	orderMu.Unlock()
	want := []string{"source", "sink", "connection"}
	if len(got) != len(want) {
		t.Fatalf("cleanup order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cleanup order = %v, want %v", got, want)
		}
	}
}

func TestAudioSessionHostDoesNotCloseConnectionAfterRuntimeCleanupFailure(t *testing.T) {
	runtimeErr := errors.New("source close failed")
	source := &cleanupTestSource{closeErr: runtimeErr}
	sink := &cleanupTestSink{}
	runner := newCleanupTestRuntime(t, source, sink)
	var connectionCloseCalls atomic.Int32
	host := &audioSessionHost{
		runtime: runner,
		closeResources: func(context.Context) error {
			connectionCloseCalls.Add(1)
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := host.close(ctx); !errors.Is(err, runtimeErr) {
		t.Fatalf("host close error = %v, want runtime cleanup error", err)
	}
	if got := connectionCloseCalls.Load(); got != 0 {
		t.Fatalf("connection closed %d times after runtime cleanup failed", got)
	}
}

func TestAudioSessionHostRetriesConnectionCloseAndCachesSuccess(t *testing.T) {
	firstErr := errors.New("connection close failed")
	var calls atomic.Int32
	host := &audioSessionHost{
		closeResources: func(context.Context) error {
			if calls.Add(1) == 1 {
				return firstErr
			}
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := host.close(ctx); !errors.Is(err, firstErr) {
		t.Fatalf("first host close = %v, want connection close error", err)
	}
	if host.resourcesClosed.Load() {
		t.Fatal("failed connection cleanup was cached as complete")
	}
	if err := host.close(ctx); err != nil {
		t.Fatalf("retry host close: %v", err)
	}
	if !host.resourcesClosed.Load() || calls.Load() != 2 {
		t.Fatalf("successful retry state: closed=%v calls=%d", host.resourcesClosed.Load(), calls.Load())
	}

	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := host.close(cancelled); err != nil {
		t.Fatalf("cached cleanup with cancelled context = %v, want nil", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("cached close retried resources with cancelled context: calls=%d", calls.Load())
	}
}

func TestAudioSessionHostCancelledMainCheckDoesNotEnterResourceClose(t *testing.T) {
	var calls atomic.Int32
	block := make(chan struct{})
	host := &audioSessionHost{closeResources: func(context.Context) error {
		calls.Add(1)
		<-block
		return nil
	}}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- host.close(cancelled) }()
	select {
	case err := <-done:
		if !errors.Is(err, audiosession.ErrCleanupPending) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled close = %v, want pending cleanup plus cancellation", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(block)
		t.Fatal("cancelled close blocked on connection cleanup")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("cancelled main-thread check called resource close %d times", got)
	}
}

func TestAudioSessionHostConcurrentCloseReturnsPendingWithoutDoubleClosing(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	host := &audioSessionHost{closeResources: func(context.Context) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}}

	firstDone := make(chan error, 1)
	go func() { firstDone <- host.close(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first close did not enter resource cleanup")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- host.close(context.Background()) }()
	select {
	case err := <-secondDone:
		if !errors.Is(err, audiosession.ErrCleanupPending) {
			t.Fatalf("concurrent close = %v, want ErrCleanupPending", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(release)
		<-firstDone
		<-secondDone
		t.Fatal("concurrent close waited for an in-flight resource close")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent close entered resources %d times before release, want 1", got)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first close: %v", err)
	}
	if got := calls.Load(); got != 1 || !host.resourcesClosed.Load() {
		t.Fatalf("concurrent cleanup result: calls=%d resourcesClosed=%v", got, host.resourcesClosed.Load())
	}
}

var _ audiosession.Source = (*cleanupTestSource)(nil)
var _ audiosession.Sink = (*cleanupTestSink)(nil)
