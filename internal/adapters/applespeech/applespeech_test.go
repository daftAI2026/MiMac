package applespeech

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenWriteDrainAndClose(t *testing.T) {
	owner := newFakeOwner(nativeReady)
	bridge := &fakeBridge{owner: owner}
	sink, err := openWith(context.Background(), testConfig(false), func(string) (nativeBridge, error) { return bridge, nil })
	if err != nil {
		t.Fatal(err)
	}
	if bridge.installAssets {
		t.Fatal("default Open unexpectedly enabled asset installation")
	}
	if _, err := sink.FinalText(); !errors.Is(err, ErrNotDrained) {
		t.Fatalf("FinalText before Drain error = %v, want ErrNotDrained", err)
	}
	if err := sink.TryWrite(make([]int16, MaxBlockSamples)); err != nil {
		t.Fatal(err)
	}
	if got := owner.writtenSamples(); got != MaxBlockSamples {
		t.Fatalf("native received %d samples, want %d", got, MaxBlockSamples)
	}
	if err := sink.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := sink.FinalText(); err != nil || got != owner.text {
		t.Fatalf("FinalText() = %q, %v; want %q", got, err, owner.text)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if !owner.wasReleased() {
		t.Fatal("native owner was not released after a completed drain")
	}
	if got, err := sink.FinalText(); err != nil || got != owner.text {
		t.Fatalf("FinalText() after Close = %q, %v; want retained final text", got, err)
	}
}

func TestTryWriteRejectsInvalidBlocksAndPropagatesFullQueue(t *testing.T) {
	owner := newFakeOwner(nativeReady)
	sink, err := openWith(context.Background(), testConfig(false), func(string) (nativeBridge, error) {
		return &fakeBridge{owner: owner}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.TryWrite(nil); !errors.Is(err, ErrEmptyBlock) {
		t.Fatalf("empty block error = %v, want ErrEmptyBlock", err)
	}
	if err := sink.TryWrite(make([]int16, MaxBlockSamples+1)); !errors.Is(err, ErrBlockTooLarge) {
		t.Fatalf("oversized block error = %v, want ErrBlockTooLarge", err)
	}
	owner.setWriteError(ErrQueueFull)
	if err := sink.TryWrite([]int16{1}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full queue error = %v, want ErrQueueFull", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallAssetsIsExplicit(t *testing.T) {
	owner := newFakeOwner(nativeReady)
	bridge := &fakeBridge{owner: owner}
	cfg := testConfig(true)
	sink, err := openWith(context.Background(), cfg, func(string) (nativeBridge, error) { return bridge, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !bridge.installAssets {
		t.Fatal("explicit asset installation request was not passed to the native bridge")
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCancellationKeepsUnjoinedOwnerForRetry(t *testing.T) {
	owner := newFakeOwner(nativePreparing)
	owner.terminateOnCancel = false
	bridge := &fakeBridge{owner: owner, created: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type openResult struct {
		sink *Sink
		err  error
	}
	result := make(chan openResult, 1)
	go func() {
		sink, err := openWith(ctx, testConfig(false), func(string) (nativeBridge, error) { return bridge, nil })
		result <- openResult{sink: sink, err: err}
	}()
	<-bridge.created
	cancel()
	opened := <-result
	if opened.sink == nil || !errors.Is(opened.err, ErrCleanupPending) {
		t.Fatalf("Open cancellation = (%v, %v), want a retained sink and ErrCleanupPending", opened.sink, opened.err)
	}
	if owner.wasReleased() {
		t.Fatal("Open released an owner before its task joined")
	}
	owner.markTerminated()
	if err := opened.sink.Close(); err != nil {
		t.Fatal(err)
	}
	if !owner.wasReleased() {
		t.Fatal("retry Close did not release the joined native owner")
	}
}

func TestCloseCancelsConcurrentDrainAndJoins(t *testing.T) {
	owner := newFakeOwner(nativeReady)
	owner.holdDrain = true
	sink, err := openWith(context.Background(), testConfig(false), func(string) (nativeBridge, error) {
		return &fakeBridge{owner: owner}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	drainResult := make(chan error, 1)
	go func() { drainResult <- sink.Drain(context.Background()) }()
	select {
	case <-owner.drainStarted:
	case <-time.After(time.Second):
		t.Fatal("Drain did not reach native finish")
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-drainResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("concurrent Drain error = %v, want context.Canceled", err)
	}
	if !owner.wasReleased() {
		t.Fatal("Close did not release the joined native owner")
	}
	if _, err := sink.FinalText(); !errors.Is(err, ErrNotDrained) {
		t.Fatalf("cancelled FinalText error = %v, want ErrNotDrained", err)
	}
}

func TestFinalTextOverflowIsExplicitAndNotPublished(t *testing.T) {
	owner := newFakeOwner(nativeReady)
	owner.text = strings.Repeat("x", MaxFinalTextBytes+1)
	sink, err := openWith(context.Background(), testConfig(false), func(string) (nativeBridge, error) {
		return &fakeBridge{owner: owner}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Drain(context.Background()); !errors.Is(err, ErrFinalTextOverflow) {
		t.Fatalf("Drain overflow error = %v, want ErrFinalTextOverflow", err)
	}
	if _, err := sink.FinalText(); !errors.Is(err, ErrNotDrained) {
		t.Fatalf("overflow FinalText error = %v, want ErrNotDrained", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMissingAssetsIsClassifiable(t *testing.T) {
	owner := newFakeOwner(nativeFailed)
	owner.failure = "assets unavailable: install the Apple Speech model"
	owner.terminated = true
	_, err := openWith(context.Background(), testConfig(false), func(string) (nativeBridge, error) {
		return &fakeBridge{owner: owner}, nil
	})
	if !errors.Is(err, ErrAssetsUnavailable) {
		t.Fatalf("Open error = %v, want ErrAssetsUnavailable", err)
	}
	if !owner.wasReleased() {
		t.Fatal("failed preparation owner was not released after join")
	}
}

func testConfig(installAssets bool) Config {
	return Config{
		LibraryPath:   "/fake/libMiMacAppleSpeech.dylib",
		Locale:        "zh-CN",
		SampleRate:    RequiredSampleRate,
		InstallAssets: installAssets,
	}
}

type fakeBridge struct {
	owner         *fakeOwner
	installAssets bool
	created       chan struct{}
}

func (*fakeBridge) isAvailable() (bool, error) { return true, nil }

func (b *fakeBridge) create(_ string, _ int, installAssets bool) (nativeOwner, error) {
	b.installAssets = installAssets
	if b.created != nil {
		close(b.created)
	}
	return b.owner, nil
}

type fakeOwner struct {
	mu sync.Mutex

	state              nativeState
	terminated         bool
	terminateOnCancel  bool
	holdDrain          bool
	drainStarted       chan struct{}
	changed            chan struct{}
	text               string
	failure            string
	writeErr           error
	written            int
	released           bool
	cancelRequestCount int
}

func newFakeOwner(state nativeState) *fakeOwner {
	return &fakeOwner{
		state:             state,
		terminateOnCancel: true,
		drainStarted:      make(chan struct{}, 1),
		changed:           make(chan struct{}),
		text:              "识别结果",
	}
}

func (o *fakeOwner) signalLocked() {
	close(o.changed)
	o.changed = make(chan struct{})
}

func (o *fakeOwner) wait(timeout time.Duration, done func(nativeState) bool) nativeState {
	deadline := time.Now().Add(timeout)
	for {
		o.mu.Lock()
		state := o.state
		changed := o.changed
		ready := done(state)
		o.mu.Unlock()
		if ready || timeout <= 0 {
			return state
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return state
		}
		select {
		case <-changed:
		case <-time.After(remaining):
			return state
		}
	}
}

func (o *fakeOwner) waitReady(timeout time.Duration) (nativeState, error) {
	return o.wait(timeout, func(state nativeState) bool { return state != nativePreparing }), nil
}

func (o *fakeOwner) tryWrite(samples []int16) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.writeErr != nil {
		return o.writeErr
	}
	o.written += len(samples)
	return nil
}

func (o *fakeOwner) beginDrain() error {
	o.mu.Lock()
	if !o.holdDrain {
		o.state = nativeDrained
		o.terminated = true
	}
	o.signalLocked()
	o.mu.Unlock()
	o.drainStarted <- struct{}{}
	return nil
}

func (o *fakeOwner) waitDone(timeout time.Duration) (nativeState, error) {
	return o.wait(timeout, func(state nativeState) bool {
		return state == nativeDrained || state == nativeCancelled || state == nativeFailed
	}), nil
}

func (o *fakeOwner) requestCancel() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cancelRequestCount++
	if o.state != nativeDrained && o.state != nativeFailed {
		o.state = nativeCancelled
	}
	if o.terminateOnCancel {
		o.terminated = true
	}
	o.signalLocked()
	return nil
}

func (o *fakeOwner) waitTerminated(timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		o.mu.Lock()
		if o.terminated {
			o.mu.Unlock()
			return true, nil
		}
		changed := o.changed
		o.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		select {
		case <-changed:
		case <-time.After(remaining):
			return false, nil
		}
	}
}

func (o *fakeOwner) finalText() (string, error) { return o.text, nil }

func (o *fakeOwner) errorMessage() string { return o.failure }

func (o *fakeOwner) release() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.terminated {
		return errors.New("release before join")
	}
	o.released = true
	return nil
}

func (o *fakeOwner) writtenSamples() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.written
}

func (o *fakeOwner) setWriteError(err error) {
	o.mu.Lock()
	o.writeErr = err
	o.mu.Unlock()
}

func (o *fakeOwner) markTerminated() {
	o.mu.Lock()
	o.terminated = true
	o.signalLocked()
	o.mu.Unlock()
}

func (o *fakeOwner) wasReleased() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.released
}
