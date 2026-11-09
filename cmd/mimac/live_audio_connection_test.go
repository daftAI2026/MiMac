package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

type liveAudioConnectionTestRoute struct {
	mu sync.Mutex

	startErr    error
	currentErr  error
	ready       bool
	changes     chan struct{}
	closeErrors []error
	closeEnter  chan struct{}
	closeBlock  <-chan struct{}

	startCalls   int
	prepareCalls int
	closeCalls   int
}

func newLiveAudioConnectionTestRoute() *liveAudioConnectionTestRoute {
	return &liveAudioConnectionTestRoute{changes: make(chan struct{}, 1)}
}

func (r *liveAudioConnectionTestRoute) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startCalls++
	if r.startErr == nil {
		r.ready = true
	}
	return r.startErr
}

func (r *liveAudioConnectionTestRoute) Ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready
}

func (r *liveAudioConnectionTestRoute) Error() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentErr
}

func (r *liveAudioConnectionTestRoute) Changes() <-chan struct{} { return r.changes }

func (r *liveAudioConnectionTestRoute) Prepare(context.Context, bridge.SessionToken) (audiosession.Resources, error) {
	r.mu.Lock()
	r.prepareCalls++
	r.mu.Unlock()
	return audiosession.Resources{SampleRate: 16000}, nil
}

func (r *liveAudioConnectionTestRoute) Close(context.Context) error {
	r.mu.Lock()
	r.closeCalls++
	entered, unblock := r.closeEnter, r.closeBlock
	r.closeEnter = nil
	var err error
	if len(r.closeErrors) == 0 {
		r.mu.Unlock()
	} else {
		err = r.closeErrors[0]
		r.closeErrors = r.closeErrors[1:]
		r.mu.Unlock()
	}
	if entered != nil {
		close(entered)
	}
	if unblock != nil {
		<-unblock
	}
	return err
}

func (r *liveAudioConnectionTestRoute) calls() (start, prepare, close int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startCalls, r.prepareCalls, r.closeCalls
}

func TestLiveAudioConnectionWaitsForDoneAndForwardsRoute(t *testing.T) {
	route := newLiveAudioConnectionTestRoute()
	factoryEntered := make(chan struct{})
	releaseFactory := make(chan struct{})
	connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) {
		close(factoryEntered)
		<-releaseFactory
		return route, nil
	})
	<-factoryEntered

	if connection.Ready() {
		t.Fatal("Ready() returned true before factory completion")
	}
	if err := connection.Err(); err != nil {
		t.Fatalf("Err() before factory completion = %v, want nil", err)
	}
	if connection.Changes() != nil {
		t.Fatal("Changes() returned a route channel before factory completion")
	}

	prepared := make(chan error, 1)
	go func() {
		_, err := connection.Prepare(context.Background(), bridge.SessionToken{})
		prepared <- err
	}()
	select {
	case err := <-prepared:
		t.Fatalf("Prepare returned before Done: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFactory)
	waitLiveAudioDone(t, connection)
	if err := <-prepared; err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !connection.Ready() {
		t.Fatal("Ready() did not forward the route state")
	}
	if connection.Changes() != route.Changes() {
		t.Fatal("Changes() did not forward the route channel")
	}
	if _, prepareCalls, _ := route.calls(); prepareCalls != 1 {
		t.Fatalf("route Prepare calls = %d, want 1", prepareCalls)
	}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	waitLiveAudioClosed(t, connection)
}

func TestLiveAudioConnectionCloseCancelsAndClosesPartialRoute(t *testing.T) {
	setupErr := errors.New("partial native owner setup")
	route := newLiveAudioConnectionTestRoute()
	factoryEntered := make(chan struct{})
	connection := newLiveAudioConnection(func(ctx context.Context) (liveAudioRoute, error) {
		close(factoryEntered)
		<-ctx.Done()
		return route, setupErr
	})
	<-factoryEntered
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	waitLiveAudioDone(t, connection)
	waitLiveAudioClosed(t, connection)
	startCalls, _, closeCalls := route.calls()
	if startCalls != 0 {
		t.Fatalf("route Start calls = %d, want 0 after factory error", startCalls)
	}
	if closeCalls != 1 {
		t.Fatalf("route Close calls = %d, want 1 for partial route", closeCalls)
	}
	if !errors.Is(connection.Err(), setupErr) {
		t.Fatalf("Err() = %v, want setup error", connection.Err())
	}
}

func TestLiveAudioConnectionCloseWaitTimeoutRetainsFactoryResultForRetry(t *testing.T) {
	route := newLiveAudioConnectionTestRoute()
	factoryEntered := make(chan struct{})
	releaseFactory := make(chan struct{})
	connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) {
		close(factoryEntered)
		<-releaseFactory // Deliberately finishes after cancellation to simulate a native call.
		return route, nil
	})
	<-factoryEntered

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := connection.Close(ctx); !errors.Is(err, audiosession.ErrCleanupPending) {
		t.Fatalf("Close() error = %v, want ErrCleanupPending", err)
	}
	if channelIsClosed(connection.Closed()) {
		t.Fatal("Closed() signaled before startup and cleanup completed")
	}
	if _, _, closeCalls := route.calls(); closeCalls != 0 {
		t.Fatalf("route Close calls before Done = %d, want 0", closeCalls)
	}

	close(releaseFactory)
	waitLiveAudioDone(t, connection)
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
	waitLiveAudioClosed(t, connection)
	startCalls, _, closeCalls := route.calls()
	if startCalls != 0 {
		t.Fatalf("route Start calls = %d, want 0 after cancellation", startCalls)
	}
	if closeCalls != 1 {
		t.Fatalf("route Close calls = %d, want 1", closeCalls)
	}
}

func TestLiveAudioConnectionCloseFailureCanBeRetried(t *testing.T) {
	closeErr := errors.New("route cleanup failed")
	route := newLiveAudioConnectionTestRoute()
	route.closeErrors = []error{closeErr, nil}
	connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) {
		return route, nil
	})
	waitLiveAudioDone(t, connection)

	if err := connection.Close(context.Background()); !errors.Is(err, closeErr) {
		t.Fatalf("first Close() error = %v, want route cleanup error", err)
	}
	if channelIsClosed(connection.Closed()) {
		t.Fatal("Closed() signaled after failed route cleanup")
	}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
	waitLiveAudioClosed(t, connection)
	if _, _, calls := route.calls(); calls != 2 {
		t.Fatalf("route Close calls = %d, want 2", calls)
	}
}

func TestLiveAudioConnectionConcurrentCloseWaitHonorsContext(t *testing.T) {
	// Virtual time separates the close deadline from host scheduler stalls.
	synctest.Test(t, func(t *testing.T) {
		route := newLiveAudioConnectionTestRoute()
		closeEntered := make(chan struct{})
		closeRelease := make(chan struct{})
		route.closeEnter = closeEntered
		route.closeBlock = closeRelease
		connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) {
			return route, nil
		})
		waitLiveAudioDone(t, connection)

		firstClose := make(chan error, 1)
		go func() { firstClose <- connection.Close(context.Background()) }()
		<-closeEntered

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		secondClose := make(chan error, 1)
		go func() { secondClose <- connection.Close(ctx) }()

		var secondErr error
		select {
		case secondErr = <-secondClose:
		case <-time.After(200 * time.Millisecond):
			close(closeRelease)
			<-firstClose
			<-secondClose
			t.Fatal("second Close(ctx) stayed blocked on serialization past its deadline")
		}
		close(closeRelease)
		if err := <-firstClose; err != nil {
			t.Fatalf("first Close() error = %v", err)
		}
		if !errors.Is(secondErr, context.DeadlineExceeded) || !errors.Is(secondErr, audiosession.ErrCleanupPending) {
			t.Fatalf("second Close(ctx) error = %v, want deadline and cleanup-pending errors", secondErr)
		}
		waitLiveAudioClosed(t, connection)
	})
}

func TestLiveAudioConnectionRetainsRouteWhenStartFails(t *testing.T) {
	startErr := errors.New("route start failed")
	route := newLiveAudioConnectionTestRoute()
	route.startErr = startErr
	connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) {
		return route, nil
	})
	waitLiveAudioDone(t, connection)
	if !errors.Is(connection.Err(), startErr) {
		t.Fatalf("Err() = %v, want route Start error", connection.Err())
	}
	if connection.Ready() {
		t.Fatal("Ready() returned true after route Start failure")
	}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, _, calls := route.calls(); calls != 1 {
		t.Fatalf("route Close calls = %d, want 1 after Start failure", calls)
	}
}

func TestLiveAudioConnectionRejectsNilFactoryAndCloses(t *testing.T) {
	connection := newLiveAudioConnection(nil)
	waitLiveAudioDone(t, connection)
	if !errors.Is(connection.Err(), errLiveAudioFactoryNil) {
		t.Fatalf("Err() = %v, want nil-factory error", connection.Err())
	}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	waitLiveAudioClosed(t, connection)
}

func waitLiveAudioDone(t *testing.T, connection *liveAudioConnection) {
	t.Helper()
	select {
	case <-connection.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live audio factory")
	}
}

func waitLiveAudioClosed(t *testing.T, connection *liveAudioConnection) {
	t.Helper()
	select {
	case <-connection.Closed():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live audio close")
	}
}

func channelIsClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

type startCheckedRoute struct {
	*liveAudioConnectionTestRoute
	before func(context.Context) error
}

func (r *startCheckedRoute) BeforeStart(ctx context.Context) error { return r.before(ctx) }

func TestLiveAudioConnectionBeforeStartOnlyForReadyOpenRoute(t *testing.T) {
	calls := 0
	marker := errors.New("selection rejected")
	route := &startCheckedRoute{newLiveAudioConnectionTestRoute(), func(context.Context) error { calls++; return marker }}
	release := make(chan struct{})
	connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) { <-release; return route, nil })
	if err := connection.BeforeStart(context.Background()); !errors.Is(err, errLiveAudioNotReady) {
		t.Fatalf("cold preflight = %v", err)
	}
	close(release)
	waitLiveAudioDone(t, connection)
	if err := connection.BeforeStart(context.Background()); !errors.Is(err, marker) {
		t.Fatalf("ready preflight = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := connection.BeforeStart(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preflight = %v", err)
	}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := connection.BeforeStart(context.Background()); err == nil {
		t.Fatal("closed preflight accepted")
	}
	if calls != 1 {
		t.Fatalf("target requests = %d, want 1", calls)
	}
}

func TestLiveAudioConnectionBeforeStartDoesNotWaitOnClose(t *testing.T) {
	route := &startCheckedRoute{newLiveAudioConnectionTestRoute(), func(context.Context) error { t.Error("start during close"); return nil }}
	entered, release := make(chan struct{}), make(chan struct{})
	route.closeEnter, route.closeBlock = entered, release
	connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) { return route, nil })
	waitLiveAudioDone(t, connection)
	done := make(chan error, 1)
	go func() { done <- connection.Close(context.Background()) }()
	<-entered
	if err := connection.BeforeStart(context.Background()); !errors.Is(err, errLiveAudioClosed) {
		t.Fatalf("concurrent preflight = %v", err)
	}
	// The channel retained below is the sole close owner.
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
