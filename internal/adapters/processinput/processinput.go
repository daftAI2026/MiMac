// Package processinput composes a parked audio output queue with a process-tap
// input endpoint. The host remains responsible for the consumer's lifecycle.
package processinput

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/adapters/processtap"
)

var (
	ErrFactoryRequired    = errors.New("processinput: native factories are required")
	ErrOutputMissing      = errors.New("processinput: output queue is unavailable")
	ErrEndpointMissing    = errors.New("processinput: endpoint is unavailable")
	ErrInvalidLibraryPath = errors.New("processinput: an absolute non-NUL library path is required")
	ErrReceiverRequired   = errors.New("processinput: receiver wait callback is required")
	ErrNotPrepared        = errors.New("processinput: input has not been prepared")
	ErrReceiverNotReady   = errors.New("processinput: receiver has not accepted the input")
	ErrBusy               = errors.New("processinput: receiver wait is in progress")
	ErrClosed             = errors.New("processinput: sink is closing or closed")
	ErrFaulted            = errors.New("processinput: sink is faulted")
)

type output interface {
	Prepare(context.Context) error
	TryWrite([]int16) error
	Drain(context.Context) error
	Close() error
}

type endpoint interface {
	InputUID() string
	Close() error
}

type outputFactory func(audiooutput.Config) (output, error)
type endpointFactory func(string) (endpoint, error)

// Sink owns an output queue and, after Prepare, a process-tap endpoint. It
// performs synchronous native calls; Open, Prepare, Drain and Close belong
// on a worker, never the UI thread. It never starts a PCM producer or claims
// that a target stopped using
// the endpoint; its host must stop consumers before Close.
type Sink struct {
	mu sync.Mutex

	output       output
	endpoint     endpoint
	inputUID     string
	libraryPath  string
	waitReceiver func(context.Context, string) error
	openEndpoint endpointFactory

	prepared      bool
	receiverReady bool
	waitActive    bool
	closing       bool
	closed        bool
	fault         error
}

// Open allocates the output queue and retains it if native construction
// returns a partial sink. Prepare later parks that queue before opening the
// process-tap endpoint. Any non-nil returned Sink must be closed, including
// when Open returns an error.
func Open(config audiooutput.Config, libraryPath string, waitReceiver func(context.Context, string) error) (*Sink, error) {
	return openWith(config, libraryPath, waitReceiver, openAudioOutput, openProcessTap)
}

func openAudioOutput(config audiooutput.Config) (output, error) {
	sink, err := audiooutput.Open(config)
	if sink == nil {
		return nil, err
	}
	return sink, err
}

func openProcessTap(libraryPath string) (endpoint, error) {
	owner, err := processtap.Open(libraryPath)
	if owner == nil {
		return nil, err
	}
	return owner, err
}

func openWith(config audiooutput.Config, libraryPath string, waitReceiver func(context.Context, string) error, makeOutput outputFactory, makeEndpoint endpointFactory) (*Sink, error) {
	if waitReceiver == nil {
		return nil, ErrReceiverRequired
	}
	if !filepath.IsAbs(libraryPath) || strings.IndexByte(libraryPath, 0) >= 0 {
		return nil, ErrInvalidLibraryPath
	}
	if makeOutput == nil || makeEndpoint == nil {
		return nil, ErrFactoryRequired
	}

	owned := &Sink{
		libraryPath:  libraryPath,
		waitReceiver: waitReceiver,
		openEndpoint: makeEndpoint,
	}
	queue, err := makeOutput(config)
	if queue == nil {
		if err == nil {
			return nil, ErrOutputMissing
		}
		return nil, err
	}
	owned.output = queue
	if err != nil {
		owned.fault = err
		return owned, err
	}
	return owned, nil
}

// Prepare first parks the output queue, then opens the process-tap endpoint
// and caches its exact input UID. The output retains its existing bounded
// silent priming; this layer never calls TryWrite or resumes the parked queue.
func (s *Sink) Prepare(ctx context.Context) error {
	if s == nil {
		return ErrClosed
	}
	if ctx == nil {
		return errors.New("processinput: nil prepare context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.closed {
		return ErrClosed
	}
	if s.fault != nil {
		return errors.Join(ErrFaulted, s.fault)
	}
	if s.prepared {
		return nil
	}
	if s.output == nil {
		return s.failLocked(ErrOutputMissing)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.output.Prepare(ctx); err != nil {
		return s.failLocked(fmt.Errorf("processinput: prepare output queue: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return s.failLocked(err)
	}

	opened, openErr := s.openEndpoint(s.libraryPath)
	if opened != nil {
		s.endpoint = opened
	}
	if openErr != nil {
		return s.failLocked(fmt.Errorf("processinput: open endpoint: %w", openErr))
	}
	if opened == nil {
		return s.failLocked(ErrEndpointMissing)
	}
	if err := ctx.Err(); err != nil {
		return s.failLocked(err)
	}
	uid := opened.InputUID()
	if uid == "" {
		return s.failLocked(errors.New("processinput: endpoint returned no input UID"))
	}
	s.inputUID = uid
	s.prepared = true
	return nil
}

// InputUID returns the prepared endpoint identity until Close begins. It does
// not mean that a consumer has selected it or that PCM has been delivered.
func (s *Sink) InputUID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.prepared || s.closing || s.closed {
		return ""
	}
	return s.inputUID
}

// WaitReceiver asks the host's receiver integration to accept the exact
// process-tap input UID. The callback runs without the native-resource mutex;
// Close therefore reports ErrBusy and leaves resources owned while it runs.
func (s *Sink) WaitReceiver(ctx context.Context) error {
	if s == nil {
		return ErrClosed
	}
	if ctx == nil {
		return errors.New("processinput: nil receiver context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closing || s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.fault != nil {
		err := errors.Join(ErrFaulted, s.fault)
		s.mu.Unlock()
		return err
	}
	if !s.prepared || s.inputUID == "" {
		s.mu.Unlock()
		return ErrNotPrepared
	}
	if s.receiverReady {
		s.mu.Unlock()
		return nil
	}
	if s.waitActive {
		s.mu.Unlock()
		return ErrBusy
	}
	s.waitActive = true
	uid := s.inputUID
	wait := s.waitReceiver
	s.mu.Unlock()

	callbackErr := wait(ctx, uid)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitActive = false
	if s.closing || s.closed {
		return errors.Join(ErrClosed, callbackErr)
	}
	if callbackErr != nil {
		return callbackErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.receiverReady = true
	return nil
}

// TryWrite forwards PCM only after both endpoint preparation and receiver
// acceptance have completed.
func (s *Sink) TryWrite(pcm []int16) error {
	if s == nil {
		return ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.readyLocked(); err != nil {
		return err
	}
	err := s.output.TryWrite(pcm)
	if err != nil && !errors.Is(err, audiooutput.ErrBackpressure) && !errors.Is(err, audiooutput.ErrBlockTooLarge) {
		s.rememberFaultLocked(err)
	}
	return err
}

// Drain waits for local output completion after the receiver has accepted the
// endpoint. It does not report speech-tool acknowledgement.
func (s *Sink) Drain(ctx context.Context) error {
	if s == nil {
		return ErrClosed
	}
	if ctx == nil {
		return errors.New("processinput: nil drain context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.readyLocked(); err != nil {
		return err
	}
	err := s.output.Drain(ctx)
	if err != nil {
		s.rememberFaultLocked(err)
	}
	return err
}

// Close prevents further use, closes the output queue first, then closes the
// endpoint. On either failure it retains that resource for a later retry. The
// endpoint's own active-I/O check remains authoritative; the host still owns
// the responsibility to stop consumers before calling Close.
func (s *Sink) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closing = true
	s.receiverReady = false
	s.inputUID = ""
	if s.waitActive {
		return ErrBusy
	}
	if s.output != nil {
		if err := s.output.Close(); err != nil {
			return fmt.Errorf("processinput: close output queue: %w", err)
		}
		s.output = nil
	}
	if s.endpoint != nil {
		if err := s.endpoint.Close(); err != nil {
			return fmt.Errorf("processinput: close endpoint: %w", err)
		}
		s.endpoint = nil
	}
	s.closed = true
	return nil
}

func (s *Sink) readyLocked() error {
	if s.closing || s.closed {
		return ErrClosed
	}
	if s.fault != nil {
		return errors.Join(ErrFaulted, s.fault)
	}
	if !s.prepared {
		return ErrNotPrepared
	}
	if !s.receiverReady {
		return ErrReceiverNotReady
	}
	if s.output == nil {
		return ErrOutputMissing
	}
	return nil
}

func (s *Sink) failLocked(err error) error {
	s.rememberFaultLocked(err)
	return err
}

func (s *Sink) rememberFaultLocked(err error) {
	if err != nil && s.fault == nil {
		s.fault = err
	}
}
