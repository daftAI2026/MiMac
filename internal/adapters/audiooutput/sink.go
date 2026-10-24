// Package audiooutput sends bounded mono PCM to one explicitly selected device.
// It owns output resources, not the bridge's voice-session state machine.
package audiooutput

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const bufferCount = 4

var (
	ErrBackpressure  = errors.New("audiooutput: buffers are full")
	ErrBlockTooLarge = errors.New("audiooutput: block exceeds 80 ms")
	ErrClosed        = errors.New("audiooutput: sink is closed")
	ErrDraining      = errors.New("audiooutput: sink is draining")
	ErrFaulted       = errors.New("audiooutput: sink is faulted")
)

// Config freezes an explicit route and the source's mono signed 16-bit format.
// System conversion handles the destination format. No default-device fallback,
// volume change, microphone capture or PCM persistence is performed here.
type Config struct {
	DeviceUID  string
	SampleRate int
}

// nativeQueue owns its buffers through a successful Dispose barrier. On partial
// creation failure the factory returns a non-nil queue only if disposal failed.
type nativeQueue interface {
	Enqueue(slot int, pcm []int16) error
	Start() error
	Stop(immediate bool) error
	Check() (running bool, err error)
	Dispose() error
}

// Sink permits concurrent calls but serializes native operations. Open/Drain/
// Close belong on a session worker, not the UI thread. No worker or timer exists
// while idle; Drain alone polls for at most two seconds. Native calls themselves
// must complete before their resources can safely be released.
type Sink struct {
	mu                                 sync.Mutex
	queue                              nativeQueue
	frames                             int
	free                               chan int
	busy                               [bufferCount]atomic.Bool
	callbackFault                      atomic.Bool
	retired                            atomic.Bool
	started, draining, drained, closed bool
	observedRunning                    bool
	fault                              error
}

// Open allocates a stopped queue. If partial construction cannot dispose its
// resources, it returns both a faulted Sink and an error; retain and Close it.
func Open(cfg Config) (*Sink, error) { return openWith(cfg, openNativeQueue) }

func openWith(cfg Config, factory func(Config, func(int)) (nativeQueue, error)) (*Sink, error) {
	if strings.TrimSpace(cfg.DeviceUID) == "" || strings.ContainsRune(cfg.DeviceUID, 0) || !utf8.ValidString(cfg.DeviceUID) {
		return nil, errors.New("audiooutput: explicit valid device UID is required")
	}
	if cfg.SampleRate != 8000 && cfg.SampleRate != 16000 {
		return nil, fmt.Errorf("audiooutput: unsupported source sample rate %d", cfg.SampleRate)
	}
	s := &Sink{frames: cfg.SampleRate / 50, free: make(chan int, bufferCount)}
	for i := range bufferCount {
		s.free <- i
	}
	q, err := factory(cfg, s.returned)
	if q == nil {
		return nil, errors.Join(err, errors.New("audiooutput: native queue unavailable"))
	}
	s.queue = q
	if err != nil {
		s.fault = err
		return s, err
	}
	return s, nil
}

// returned runs on the native queue thread. Never wait for the operation mutex,
// allocate, call a system API or invoke UI from this callback.
func (s *Sink) returned(slot int) {
	if s.retired.Load() {
		return
	}
	if slot < 0 || slot >= bufferCount || !s.busy[slot].Swap(false) {
		s.callbackFault.Store(true)
		return
	}
	select {
	case s.free <- slot:
	default:
		s.callbackFault.Store(true)
	}
}

func (s *Sink) writable() error {
	if s.closed {
		return ErrClosed
	}
	if s.callbackFault.Load() {
		s.fault = errors.Join(s.fault, errors.New("audiooutput: invalid buffer callback"))
	}
	if s.fault != nil {
		return errors.Join(ErrFaulted, s.fault)
	}
	if s.draining {
		return ErrDraining
	}
	return nil
}

// TryWrite copies the entire block before returning. Backpressure never submits
// a partial block. A native failure after enqueue may have submitted a prefix;
// it fault-locks this sink and the caller must end the session and Close it.
func (s *Sink) TryWrite(pcm []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return err
	}
	if len(pcm) == 0 {
		return nil
	}
	if len(pcm) > s.frames*bufferCount {
		return ErrBlockTooLarge
	}
	needed := (len(pcm) + s.frames - 1) / s.frames
	if len(s.free) < needed {
		return ErrBackpressure
	}
	running, err := s.queue.Check()
	if err != nil {
		s.fault = err
		return err
	}
	// AudioQueueStart acknowledges the request before IsRunning changes.
	// Only a later falling edge proves an unexpected stop. A start that never
	// completes stays bounded by four buffers and the Drain deadline.
	if s.observedRunning && !running {
		s.fault = errors.New("audiooutput: queue stopped unexpectedly")
		return s.fault
	}
	s.observedRunning = s.observedRunning || running
	var slots [bufferCount]int
	for i := range needed {
		slots[i] = <-s.free
		s.busy[slots[i]].Store(true)
	}
	for i, slot := range slots[:needed] {
		end := min((i+1)*s.frames, len(pcm))
		if err := s.queue.Enqueue(slot, pcm[i*s.frames:end]); err != nil {
			s.fault = err
			return err
		}
	}
	if !s.started {
		if err := s.queue.Start(); err != nil {
			s.fault = err
			return err
		}
		s.started = true
	}
	return nil
}

// Drain seals the sink, asks AudioQueue to play its accepted buffers, then waits
// for returned buffers AND a stopped queue. This is local playout completion,
// not acknowledgement from a speech tool. Failure performs immediate cleanup.
func (s *Sink) Drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("audiooutput: nil drain context")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.drained {
		s.mu.Unlock()
		return nil
	}
	if err := s.writable(); err != nil {
		s.mu.Unlock()
		if errors.Is(err, ErrDraining) {
			return err
		}
		return errors.Join(err, s.Close())
	}
	s.draining = true
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return errors.Join(err, s.Close())
	}
	if !s.started {
		s.drained = true
		s.mu.Unlock()
		return nil
	}
	err := s.queue.Stop(false)
	if err != nil {
		s.fault = err
	}
	s.mu.Unlock()
	if err != nil {
		return errors.Join(err, s.Close())
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return ErrClosed
		}
		running, checkErr := s.queue.Check()
		if s.callbackFault.Load() {
			checkErr = errors.Join(checkErr, errors.New("audiooutput: invalid buffer callback"))
		}
		if checkErr != nil {
			s.fault = checkErr
			s.mu.Unlock()
			return errors.Join(checkErr, s.Close())
		}
		if !running && len(s.free) == bufferCount {
			s.drained = true
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), s.Close())
		case <-ticker.C:
		}
	}
}

// Close aborts immediately. A failed Dispose preserves the queue and callback
// owner for a retry; resources are never reported released on an error alone.
func (s *Sink) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	stopErr := s.queue.Stop(true)
	disposeErr := s.queue.Dispose()
	err := errors.Join(stopErr, disposeErr)
	if disposeErr != nil {
		s.fault = err
		return err
	}
	s.retired.Store(true)
	s.closed = true
	s.queue = nil
	return err
}
