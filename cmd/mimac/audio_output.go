package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"mimac.local/mimac/internal/adapters/audiooutput"
)

const audioOutputPlugin = "audio-output"

var (
	errAudioOutputOwnerInactive = errors.New("audio output owner is inactive")
	errAudioOutputAlreadyActive = errors.New("audio output owner is already active")
	errAudioOutputAlreadyOpen   = errors.New("audio output sink is still owned")
	errAudioOutputNotOpen       = errors.New("audio output sink is not open")
	errAudioOutputFactoryNil    = errors.New("audio output factory is nil")
	errAudioOutputSinkNil       = errors.New("audio output factory returned a nil sink")
)

// audioOutputSink is the narrow per-session resource the host owns. The
// adapter bounds writes internally; the owner never retains PCM samples.
type audioOutputSink interface {
	TryWrite([]int16) error
	Drain(context.Context) error
	Close() error
}

type audioOutputOpenFunc func(audiooutput.Config) (audioOutputSink, error)

// audioOutputOwner is lazy: enabling its plugin installs a factory but does
// not open an AudioQueue. At most one session sink is owned at a time. A sink
// returned alongside an Open error is retained solely so Close can retry its
// partial cleanup. Open, TryWrite, Close, enable and disable are serialized by
// mu. Drain runs outside mu; a generation check prevents an old result from
// faulting a sink opened after the drained sink was closed.
type audioOutputOwner struct {
	mu         sync.Mutex
	factory    audioOutputOpenFunc
	sink       audioOutputSink
	generation uint64
	ready      bool
	fault      error
	enabled    bool
}

func newAudioOutputOwner() *audioOutputOwner {
	return &audioOutputOwner{}
}

// openSystemAudioOutput adapts the concrete pointer return to the narrow owner
// interface so tests can inject an in-memory sink.
func openSystemAudioOutput(config audiooutput.Config) (audioOutputSink, error) {
	sink, err := audiooutput.Open(config)
	if sink == nil {
		return nil, err
	}
	return sink, err
}

// enable is intended for the audio-output plugin Start callback. It records
// the factory only; callers must explicitly call open for a session.
func (o *audioOutputOwner) enable(factory audioOutputOpenFunc) error {
	if o == nil {
		return errAudioOutputOwnerInactive
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.enabled {
		return errAudioOutputAlreadyActive
	}
	if o.sink != nil {
		return errAudioOutputAlreadyOpen
	}
	if factory == nil {
		return errAudioOutputFactoryNil
	}
	o.factory = factory
	o.enabled = true
	return nil
}

// open creates the one per-session sink. An error with a non-nil sink keeps
// that handle owned and blocks another Open until cleanup succeeds.
func (o *audioOutputOwner) open(config audiooutput.Config) error {
	if o == nil {
		return errAudioOutputOwnerInactive
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.enabled || o.factory == nil {
		return errAudioOutputOwnerInactive
	}
	if o.sink != nil {
		return errors.Join(errAudioOutputAlreadyOpen, o.fault)
	}

	sink, err := o.factory(config)
	if sink != nil {
		o.sink = sink
		o.generation++
		o.ready = err == nil
	}
	if err != nil {
		if sink != nil {
			o.fault = err
		}
		return fmt.Errorf("open audio output: %w", err)
	}
	if sink == nil {
		return errAudioOutputSinkNil
	}
	o.ready = true
	o.fault = nil
	return nil
}

func (o *audioOutputOwner) tryWrite(pcm []int16) error {
	if o == nil {
		return errAudioOutputOwnerInactive
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.requireReadyLocked(); err != nil {
		return err
	}
	err := o.sink.TryWrite(pcm)
	if err != nil && !errors.Is(err, audiooutput.ErrBackpressure) {
		o.fault = err
	}
	return err
}

func (o *audioOutputOwner) drain(ctx context.Context) error {
	if o == nil {
		return errAudioOutputOwnerInactive
	}
	o.mu.Lock()
	if err := o.requireReadyLocked(); err != nil {
		o.mu.Unlock()
		return err
	}
	sink := o.sink
	generation := o.generation
	o.mu.Unlock()

	err := sink.Drain(ctx)
	if err != nil {
		o.mu.Lock()
		if o.generation == generation && o.sink != nil {
			o.ready = false
			o.fault = errors.Join(o.fault, err)
		}
		o.mu.Unlock()
	}
	return err
}

// close clears ownership only after the adapter confirms successful cleanup.
// The retained handle remains available for another cleanup attempt on error.
func (o *audioOutputOwner) close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closeLocked()
}

func (o *audioOutputOwner) closeLocked() error {
	if o.sink == nil {
		return nil
	}
	if err := o.sink.Close(); err != nil {
		o.ready = false
		o.fault = errors.Join(o.fault, err)
		return err
	}
	o.sink = nil
	o.generation++
	o.ready = false
	o.fault = nil
	return nil
}

// disable is intended for the audio-output plugin Stop callback. A failed
// close leaves the factory, enabled state, and sink intact for diagnosis; the
// plugin runtime will fault the module rather than report a clean stop.
func (o *audioOutputOwner) disable() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.closeLocked(); err != nil {
		return err
	}
	o.factory = nil
	o.enabled = false
	return nil
}

func (o *audioOutputOwner) requireReadyLocked() error {
	if !o.enabled || o.factory == nil {
		return errAudioOutputOwnerInactive
	}
	if o.fault != nil {
		return errors.Join(errAudioOutputNotOpen, o.fault)
	}
	if o.sink == nil || !o.ready {
		return errAudioOutputNotOpen
	}
	return nil
}
