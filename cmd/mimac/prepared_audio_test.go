package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

type preparedAudioTestSource struct {
	mu         sync.Mutex
	closeCalls int
	closeErrs  []error
	err        error
}

func (*preparedAudioTestSource) Read(context.Context, []int16) (int, error) { return 0, io.EOF }
func (*preparedAudioTestSource) Stop(context.Context) error                 { return nil }
func (s *preparedAudioTestSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	if len(s.closeErrs) == 0 {
		return nil
	}
	err := s.closeErrs[0]
	s.closeErrs = s.closeErrs[1:]
	return err
}
func (s *preparedAudioTestSource) closes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}
func (s *preparedAudioTestSource) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *preparedAudioTestSource) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

type preparedAudioTestSink struct {
	mu         sync.Mutex
	closeCalls int
	closeErrs  []error
}

func (*preparedAudioTestSink) TryWrite([]int16) error      { return nil }
func (*preparedAudioTestSink) Drain(context.Context) error { return nil }
func (s *preparedAudioTestSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	if len(s.closeErrs) == 0 {
		return nil
	}
	err := s.closeErrs[0]
	s.closeErrs = s.closeErrs[1:]
	return err
}
func (s *preparedAudioTestSink) closes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

func preparedAudioToken(t *testing.T) bridge.SessionToken {
	t.Helper()
	key, err := bridge.NewKey("Space")
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
	device, err := bridge.NewDevice("prepared-audio-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{
		bridge.RouteAvailable(true),
		bridge.ScopeAvailable(true),
		bridge.DeviceConnected(device),
		bridge.VoiceBegan(press),
	} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := core.Snapshot()
	if !snapshot.HasSession {
		t.Fatal("test core did not create a session")
	}
	return snapshot.Session.Token
}

func waitPreparedAudio(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for prepared audio state")
}

func closePreparedAudio(t *testing.T, owner *preparedAudioOwner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := owner.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPreparedAudioOwnerReadyGateAndSingleTake(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var prepares atomic.Int32
	owner, err := newPreparedAudioOwner(func(context.Context) (audiosession.Resources, error) {
		prepares.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		return audiosession.Resources{Source: &preparedAudioTestSource{}, Sink: &preparedAudioTestSink{}, SampleRate: 16000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	<-started
	if owner.Ready() {
		t.Fatal("owner reported ready before preparation completed")
	}
	if _, err := owner.Take(preparedAudioToken(t)); !errors.Is(err, errPreparedAudioNotReady) {
		t.Fatalf("Take before readiness = %v", err)
	}
	close(release)
	waitPreparedAudio(t, owner.Ready)
	resources, err := owner.Take(preparedAudioToken(t))
	if err != nil {
		t.Fatal(err)
	}
	if resources.SampleRate != 16000 {
		t.Fatalf("SampleRate = %d", resources.SampleRate)
	}
	if _, err := owner.Take(preparedAudioToken(t)); !errors.Is(err, errPreparedAudioNotReady) {
		t.Fatalf("second Take = %v", err)
	}
	if prepares.Load() != 1 {
		t.Fatalf("prepares before lease cleanup = %d", prepares.Load())
	}
	if err := resources.Source.Close(); err != nil {
		t.Fatal(err)
	}
	if prepares.Load() != 1 {
		t.Fatalf("prepared next resources before Sink.Close: %d", prepares.Load())
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatal(err)
	}
	waitPreparedAudio(t, func() bool { return prepares.Load() == 2 && owner.Ready() })
	closePreparedAudio(t, owner)
}

func TestPreparedAudioOwnerChangesCoalesceAndNotifyReadyAndRearm(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var prepares atomic.Int32
	owner, err := newPreparedAudioOwner(func(context.Context) (audiosession.Resources, error) {
		call := prepares.Add(1)
		if call == 1 {
			startedOnce.Do(func() { close(started) })
			<-release
		}
		return audiosession.Resources{Source: &preparedAudioTestSource{}, Sink: &preparedAudioTestSink{}, SampleRate: 16000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	changes := owner.Changes()
	if cap(changes) != 1 {
		t.Fatalf("Changes capacity = %d, want 1", cap(changes))
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	<-started
	waitChange(t, changes) // Preparing.
	if owner.Ready() {
		t.Fatal("owner ready while initial preparation is blocked")
	}
	close(release)
	waitReadyChange(t, owner)
	if !owner.Ready() {
		t.Fatal("ready notification arrived without a ready owner")
	}
	if len(changes) != 0 {
		t.Fatalf("coalesced notifications left %d extra signals", len(changes))
	}

	resources, err := owner.Take(preparedAudioToken(t))
	if err != nil {
		t.Fatal(err)
	}
	waitChange(t, changes)
	if err := resources.Source.Close(); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatal("one resource close notified a completed lease")
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatal(err)
	}
	waitReadyChange(t, owner)
	if !owner.Ready() || prepares.Load() != 2 {
		t.Fatalf("rearm notification state: ready=%t prepares=%d", owner.Ready(), prepares.Load())
	}
	if len(changes) != 0 {
		t.Fatalf("rearm produced non-coalesced signals: %d pending", len(changes))
	}
	closePreparedAudio(t, owner)
}

func waitChange(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-changes:
	case <-timer.C:
		t.Fatal("timed out waiting for prepared audio change")
	}
}

func waitReadyChange(t *testing.T, owner *preparedAudioOwner) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for !owner.Ready() {
		select {
		case <-owner.Changes():
		case <-timer.C:
			t.Fatal("timed out waiting for Ready notification")
		}
	}
}

func TestPreparedAudioOwnerCloseDuringPrepareRetainsLateResources(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	source, sink := &preparedAudioTestSource{}, &preparedAudioTestSink{}
	owner, err := newPreparedAudioOwner(func(context.Context) (audiosession.Resources, error) {
		close(started)
		<-release // Simulate a native prepare call that cannot be interrupted.
		return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	<-started
	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = owner.Close(short)
	cancel()
	if !errors.Is(err, audiosession.ErrCleanupPending) {
		t.Fatalf("Close during prepare = %v", err)
	}
	if source.closes() != 0 || sink.closes() != 0 {
		t.Fatal("resources closed before native prepare returned them")
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := owner.Close(ctx); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if source.closes() != 1 || sink.closes() != 1 {
		t.Fatalf("late resources not cleaned: source=%d sink=%d", source.closes(), sink.closes())
	}
	if owner.Ready() {
		t.Fatal("closed owner became ready")
	}
}

func TestPreparedAudioOwnerPartialFailureRetainsHandlesForRetry(t *testing.T) {
	closeErr := errors.New("source close failed")
	source := &preparedAudioTestSource{closeErrs: []error{closeErr}}
	sink := &preparedAudioTestSink{}
	var prepares atomic.Int32
	owner, err := newPreparedAudioOwner(func(context.Context) (audiosession.Resources, error) {
		prepares.Add(1)
		return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, errors.New("prepare failed after native allocation")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	waitPreparedAudio(t, func() bool { return owner.Error() != nil })
	if owner.Ready() {
		t.Fatal("failed preparation reported ready")
	}
	if _, err := owner.Take(preparedAudioToken(t)); !errors.Is(err, errPreparedAudioNotReady) {
		t.Fatalf("Take after failed prepare = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err = owner.Close(ctx)
	cancel()
	if !errors.Is(err, closeErr) {
		t.Fatalf("first Close = %v", err)
	}
	if source.closes() != 1 || sink.closes() != 1 {
		t.Fatalf("first cleanup calls source=%d sink=%d", source.closes(), sink.closes())
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := owner.Close(ctx); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if source.closes() != 2 || sink.closes() != 1 {
		t.Fatalf("cleanup retry lost ownership or repeated success: source=%d sink=%d", source.closes(), sink.closes())
	}
	if prepares.Load() != 1 {
		t.Fatalf("failed preparation rearmed %d times", prepares.Load()-1)
	}
}

func TestPreparedAudioOwnerDoesNotRearmUntilBothLeaseClosesSucceed(t *testing.T) {
	source := &preparedAudioTestSource{}
	sink := &preparedAudioTestSink{closeErrs: []error{errors.New("sink close failed")}}
	var prepares atomic.Int32
	owner, err := newPreparedAudioOwner(func(context.Context) (audiosession.Resources, error) {
		prepares.Add(1)
		return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	waitPreparedAudio(t, owner.Ready)
	resources, err := owner.Take(preparedAudioToken(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := resources.Source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.Sink.Close(); err == nil {
		t.Fatal("expected first sink close to fail")
	}
	if prepares.Load() != 1 {
		t.Fatalf("rearmed while Sink.Close failed: %d", prepares.Load())
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatalf("retry Sink.Close: %v", err)
	}
	waitPreparedAudio(t, func() bool { return prepares.Load() == 2 && owner.Ready() })
	closePreparedAudio(t, owner)
}

func TestPreparedAudioOwnerRejectsReadySourceFault(t *testing.T) {
	for _, checkReadyFirst := range []bool{true, false} {
		name := "Take checks source health"
		if checkReadyFirst {
			name = "Ready checks source health"
		}
		t.Run(name, func(t *testing.T) {
			source, sink := &preparedAudioTestSource{}, &preparedAudioTestSink{}
			owner, err := newPreparedAudioOwner(func(context.Context) (audiosession.Resources, error) {
				return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			waitPreparedAudio(t, owner.Ready)
			sourceFault := errors.New("ATVV source ring overflow")
			source.setErr(sourceFault)
			if checkReadyFirst && owner.Ready() {
				t.Fatal("Ready remained true after the Source faulted")
			}
			if err := owner.Error(); !errors.Is(err, sourceFault) {
				t.Fatalf("Error() = %v, want source fault", err)
			}
			if _, err := owner.Take(preparedAudioToken(t)); !errors.Is(err, sourceFault) {
				t.Fatalf("Take after Source fault = %v", err)
			}
			if owner.Ready() {
				t.Fatal("faulted Source remained ready")
			}
			closePreparedAudio(t, owner)
		})
	}
}

func TestPreparedAudioOwnerCloseWaitsForRuntimeLeaseWithoutClosingIt(t *testing.T) {
	source, sink := &preparedAudioTestSource{}, &preparedAudioTestSink{}
	owner, err := newPreparedAudioOwner(func(context.Context) (audiosession.Resources, error) {
		return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	waitPreparedAudio(t, owner.Ready)
	resources, err := owner.Take(preparedAudioToken(t))
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = owner.Close(short)
	cancel()
	if !errors.Is(err, audiosession.ErrCleanupPending) {
		t.Fatalf("Close with active Runtime lease = %v", err)
	}
	if source.closes() != 0 || sink.closes() != 0 {
		t.Fatal("owner raced Runtime by closing a leased resource")
	}
	if err := resources.Source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := owner.Close(ctx); err != nil {
		t.Fatalf("Close after Runtime cleanup: %v", err)
	}
	if owner.Ready() {
		t.Fatal("owner rearmed after Close began")
	}
}

func TestPreparedAudioTakePreservesReceiverReadiness(t *testing.T) {
	var calls atomic.Int32
	fault := errors.New("receiver not selected")
	owner, err := newPreparedAudioOwner(func(context.Context) (audiosession.Resources, error) {
		return audiosession.Resources{Source: &preparedAudioTestSource{}, Sink: &preparedAudioTestSink{}, SampleRate: 16000, WaitReceiver: func(context.Context) error { calls.Add(1); return fault }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePreparedAudio(t, owner) })
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	waitPreparedAudio(t, owner.Ready)
	if calls.Load() != 0 {
		t.Fatal("background preparation waited for target before session")
	}
	resources, err := owner.Take(preparedAudioToken(t))
	if err != nil {
		t.Fatal(err)
	}
	if resources.WaitReceiver == nil {
		t.Fatal("resource transfer dropped receiver gate")
	}
	if err := resources.WaitReceiver(context.Background()); !errors.Is(err, fault) || calls.Load() != 1 {
		t.Fatalf("receiver gate changed: %v", err)
	}
	if err := resources.Source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedAudioLeasePreservesLiveSourceHealth(t *testing.T) {
	source, sink := &preparedAudioTestSource{}, &preparedAudioTestSink{}
	owner, err := newPreparedAudioOwner(func(context.Context) (audiosession.Resources, error) {
		return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.Start(); err != nil {
		t.Fatal(err)
	}
	waitPreparedAudio(t, owner.Ready)
	resources, err := owner.Take(preparedAudioToken(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { resources.Source.Close(); resources.Sink.Close(); closePreparedAudio(t, owner) }()
	health, ok := resources.Source.(interface{ Err() error })
	if !ok {
		t.Fatal("lease erased source health capability")
	}
	if err := health.Err(); err != nil {
		t.Fatalf("healthy source=%v", err)
	}
	fault := errors.New("source fault after lease transfer")
	source.setErr(fault)
	if err := health.Err(); !errors.Is(err, fault) {
		t.Fatalf("lease source health=%v", err)
	}
}
