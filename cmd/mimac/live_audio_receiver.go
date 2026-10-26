package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"mimac.local/mimac/internal/platform/audioroute"
)

const compatReceiverCheckInterval = 100 * time.Millisecond

var (
	errCompatReceiverUnready = errors.New("mimac-compat route: receiver has not passed readiness")
	errCompatReceiverStopped = errors.New("mimac-compat route: captured target stopped receiving input")
)

// This adapter owns observation of the target, not the voice state machine.
// A fault stays latched for this resource generation. Runtime owns cancellation
// and joins WatchReceiver before releasing the source or sink.
type compatAudioReceiverSink struct {
	xiaomiPreparedSink
	inspector compatAudioInspector
	identity  compatProcessIdentity
	pid       int32
	deviceUID string

	mu        sync.Mutex
	ready     bool
	fault     error
	checkedAt time.Time
	now       func() time.Time
}

func (s *compatAudioReceiverSink) WaitReceiver(ctx context.Context) error {
	if s == nil || s.inspector == nil || s.identity == nil || s.pid <= 0 || s.deviceUID != compatAudioDeviceUID {
		return errors.New("mimac-compat route: receiver guard is incomplete")
	}
	if ctx == nil {
		return audioroute.ErrReadinessDeadlineRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.identity.Check(); err != nil {
		return fmt.Errorf("mimac-compat route: verify target before receiver wait: %w", err)
	}
	if err := s.inspector.WaitForInput(ctx, s.pid, s.deviceUID); err != nil {
		return err
	}
	if err := s.identity.Check(); err != nil {
		return fmt.Errorf("mimac-compat route: verify target after receiver wait: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != nil {
		return s.fault
	}
	s.ready = true
	return nil
}

func (s *compatAudioReceiverSink) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Native identity and input-property reads are serialized off the UI thread.
// Their result is a snapshot, not an atomic lease on a third-party recorder.
// An in-flight native query cannot be interrupted by a context deadline.
func (s *compatAudioReceiverSink) checkReceiver(force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != nil {
		return s.fault
	}
	if !s.ready {
		s.fault = errCompatReceiverUnready
		return s.fault
	}
	if !force && !s.checkedAt.IsZero() && s.clock().Before(s.checkedAt.Add(compatReceiverCheckInterval)) {
		return nil
	}
	s.fault = s.observeReceiver()
	if s.fault == nil {
		s.checkedAt = s.clock()
	}
	return s.fault
}

func (s *compatAudioReceiverSink) observeReceiver() error {
	if err := s.identity.Check(); err != nil {
		return fmt.Errorf("mimac-compat route: receiver identity: %w", err)
	}
	input, err := s.inspector.ProcessInput(s.pid)
	if err != nil {
		return fmt.Errorf("mimac-compat route: receiver input: %w", err)
	}
	if err := s.identity.Check(); err != nil {
		return fmt.Errorf("mimac-compat route: receiver identity after input query: %w", err)
	}
	if !input.Found || input.PID != s.pid {
		return errCompatRouteTargetMissing
	}
	if !input.RunningInput {
		return errCompatReceiverStopped
	}
	if len(input.InputDeviceUIDs) != 1 || input.InputDeviceUIDs[0] != s.deviceUID {
		return fmt.Errorf("%w: expected UID %q, got %q", audioroute.ErrInputRouteMismatch, s.deviceUID, input.InputDeviceUIDs)
	}
	return nil
}

// WatchReceiver exists only while this session is active, including a source
// Read with no PCM available. Idle/prepared routes have no polling worker.
func (s *compatAudioReceiverSink) WatchReceiver(ctx context.Context) error {
	if ctx == nil {
		return errors.New("mimac-compat route: receiver watch requires context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkReceiver(false); err != nil {
		return err
	}
	ticker := time.NewTicker(compatReceiverCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.checkReceiver(false); err != nil {
				return err
			}
		}
	}
}

func (s *compatAudioReceiverSink) TryWrite(pcm []int16) error {
	if err := s.checkReceiver(false); err != nil {
		return err
	}
	return s.xiaomiPreparedSink.TryWrite(pcm)
}

// Short utterances must not skip the health check just because the last normal
// write was recent. A target that stops during Drain cannot produce a success
// completion that Core would translate into another toggle.
func (s *compatAudioReceiverSink) Drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("mimac-compat route: drain requires context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkReceiver(true); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.xiaomiPreparedSink.Drain(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkReceiver(true)
}
