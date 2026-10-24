package main

import (
	"context"
	"errors"
	"sync"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

var (
	errPreparedAudioNil          = errors.New("prepared audio owner is nil")
	errPreparedAudioClosed       = errors.New("prepared audio owner is closed")
	errPreparedAudioAlreadyStart = errors.New("prepared audio owner already started")
	errPreparedAudioNotReady     = errors.New("prepared audio is not ready")
	errPreparedAudioInvalidToken = errors.New("prepared audio session token is invalid")
	errPreparedAudioPrepareNil   = errors.New("prepared audio factory is nil")
)

type preparedAudioState uint8

const (
	preparedAudioIdle preparedAudioState = iota
	preparedAudioPreparing
	preparedAudioReady
	preparedAudioLeased
	preparedAudioFailed
	preparedAudioClosed
)

// preparedAudioOwner keeps one asynchronously prepared resource group ready
// for the next Runtime session. A lease transfers close responsibility to
// audiosession.Runtime; only successful closure of both resources permits the
// owner to prepare another group.
type preparedAudioOwner struct {
	mu      sync.Mutex
	closeMu sync.Mutex // Serializes native Source/Sink Close calls.
	changes chan struct{}

	prepare func(context.Context) (audiosession.Resources, error)
	state   preparedAudioState
	started bool
	closed  bool

	prepareCancel context.CancelFunc
	prepareDone   chan struct{}
	current       audiosession.Resources
	generation    uint64
	sourceClosed  bool
	sinkClosed    bool
	leaseDone     chan struct{}

	cleanupDone chan struct{}
	cleanupErr  error
	lastErr     error
}

func newPreparedAudioOwner(prepare func(context.Context) (audiosession.Resources, error)) (*preparedAudioOwner, error) {
	if prepare == nil {
		return nil, errPreparedAudioPrepareNil
	}
	return &preparedAudioOwner{prepare: prepare, state: preparedAudioIdle, changes: make(chan struct{}, 1)}, nil
}

// Changes returns a coalescing notification channel. Consumers should reread
// Ready and Error after each signal; preparation work never calls UI code.
func (o *preparedAudioOwner) Changes() <-chan struct{} {
	if o == nil {
		return nil
	}
	return o.changes
}

func (o *preparedAudioOwner) signalChangeLocked() {
	select {
	case o.changes <- struct{}{}:
	default:
	}
}

func (o *preparedAudioOwner) setStateLocked(state preparedAudioState) {
	if o.state == state {
		return
	}
	o.state = state
	o.signalChangeLocked()
}

// Start launches initial preparation and never waits for native audio work.
func (o *preparedAudioOwner) Start() error {
	if o == nil {
		return errPreparedAudioNil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errPreparedAudioClosed
	}
	if o.started {
		return errPreparedAudioAlreadyStart
	}
	o.started = true
	o.startPrepareLocked()
	return nil
}

// Ready is a nonblocking preflight for voiceAudioBinding. It reports only
// whether the current armed Source and prepared Sink can be transferred now.
func (o *preparedAudioOwner) Ready() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	if o.closed || o.state != preparedAudioReady {
		o.mu.Unlock()
		return false
	}
	generation := o.generation
	o.mu.Unlock()
	if o.sourceError(generation) != nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return !o.closed && o.generation == generation && o.state == preparedAudioReady
}

// Error exposes asynchronous preparation or cleanup failure to the host.
func (o *preparedAudioOwner) Error() error {
	if o == nil {
		return errPreparedAudioNil
	}
	o.mu.Lock()
	generation, source, err := o.generation, o.current.Source, o.lastErr
	checkSource := !o.closed && source != nil && (o.state == preparedAudioReady || o.state == preparedAudioFailed)
	o.mu.Unlock()
	if checkSource {
		if sourceErr := preparedAudioSourceError(source); sourceErr != nil {
			o.recordSourceError(generation, sourceErr)
			if !errors.Is(err, sourceErr) {
				err = errors.Join(err, sourceErr)
			}
		}
	}
	return err
}

// Take transfers the one ready resource group to Runtime exactly once.
func (o *preparedAudioOwner) Take(token bridge.SessionToken) (audiosession.Resources, error) {
	if o == nil {
		return audiosession.Resources{}, errPreparedAudioNil
	}
	if token.ID() == 0 || token.Device().Generation() == 0 {
		return audiosession.Resources{}, errPreparedAudioInvalidToken
	}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return audiosession.Resources{}, errPreparedAudioClosed
	}
	if o.state != preparedAudioReady {
		err := errors.Join(errPreparedAudioNotReady, o.lastErr)
		o.mu.Unlock()
		return audiosession.Resources{}, err
	}
	generation := o.generation
	o.mu.Unlock()
	if err := o.sourceError(generation); err != nil {
		return audiosession.Resources{}, errors.Join(errPreparedAudioNotReady, err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return audiosession.Resources{}, errPreparedAudioClosed
	}
	if o.generation != generation || o.state != preparedAudioReady {
		return audiosession.Resources{}, errors.Join(errPreparedAudioNotReady, o.lastErr)
	}
	o.state = preparedAudioLeased
	o.signalChangeLocked()
	o.leaseDone = make(chan struct{})
	return audiosession.Resources{
		Source:     preparedAudioSource{owner: o, generation: o.generation, inner: o.current.Source},
		Sink:       preparedAudioSink{owner: o, generation: o.generation, inner: o.current.Sink},
		SampleRate: o.current.SampleRate,
	}, nil
}

func (o *preparedAudioOwner) sourceError(generation uint64) error {
	o.mu.Lock()
	if o.generation != generation || o.current.Source == nil {
		o.mu.Unlock()
		return nil
	}
	source := o.current.Source
	o.mu.Unlock()
	err := preparedAudioSourceError(source)
	if err != nil {
		o.recordSourceError(generation, err)
	}
	return err
}

func (o *preparedAudioOwner) recordSourceError(generation uint64, err error) {
	if err == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.generation != generation || o.current.Source == nil {
		return
	}
	if !errors.Is(o.lastErr, err) {
		o.lastErr = errors.Join(o.lastErr, err)
		o.signalChangeLocked()
	}
	if o.state == preparedAudioReady {
		o.setStateLocked(preparedAudioFailed)
	}
}

func preparedAudioSourceError(source audiosession.Source) error {
	if source == nil {
		return nil
	}
	if healthy, ok := source.(interface{ Err() error }); ok {
		return healthy.Err()
	}
	return nil
}

// Prepare adapts Take to audiosession.Prepare without waiting. The caller
// should use Ready as its HID preflight; a race to Close still fails closed.
func (o *preparedAudioOwner) Prepare(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
	if ctx == nil {
		return audiosession.Resources{}, errors.New("prepared audio context is nil")
	}
	if err := ctx.Err(); err != nil {
		return audiosession.Resources{}, err
	}
	return o.Take(token)
}

func (o *preparedAudioOwner) startPrepareLocked() {
	if o.closed || !o.started || o.prepareDone != nil || o.current.Source != nil || o.current.Sink != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	o.generation++
	generation := o.generation
	o.prepareCancel = cancel
	o.prepareDone = done
	o.setStateLocked(preparedAudioPreparing)
	o.lastErr = nil
	go o.runPrepare(ctx, generation, done)
}

func (o *preparedAudioOwner) runPrepare(ctx context.Context, generation uint64, done chan struct{}) {
	resources, err := o.prepare(ctx)
	if err == nil && (resources.Source == nil || resources.Sink == nil || (resources.SampleRate != 8000 && resources.SampleRate != 16000)) {
		err = errors.New("prepared audio factory returned incomplete resources or an unsupported PCM rate")
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}

	o.mu.Lock()
	if o.generation == generation {
		// Even a cancelled/failed factory may return live native handles. Keep
		// them owned so Close can retry cleanup rather than dropping them.
		o.current = resources
		o.sourceClosed = resources.Source == nil
		o.sinkClosed = resources.Sink == nil
		o.prepareCancel = nil
		o.prepareDone = nil
		o.lastErr = err
		if err == nil && !o.closed {
			o.setStateLocked(preparedAudioReady)
		} else if o.closed {
			o.setStateLocked(preparedAudioClosed)
		} else {
			o.setStateLocked(preparedAudioFailed)
		}
	}
	close(done)
	o.mu.Unlock()
}

func (o *preparedAudioOwner) closePart(generation uint64, source bool) error {
	o.closeMu.Lock()
	defer o.closeMu.Unlock()

	o.mu.Lock()
	if generation != o.generation || (o.current.Source == nil && o.current.Sink == nil) {
		o.mu.Unlock()
		return nil
	}
	closed := o.sinkClosed
	var closeFn func() error
	if source {
		closed = o.sourceClosed
		if o.current.Source != nil {
			closeFn = o.current.Source.Close
		}
	} else if o.current.Sink != nil {
		closeFn = o.current.Sink.Close
	}
	o.mu.Unlock()

	var err error
	if !closed && closeFn != nil {
		err = closeFn()
	}

	o.mu.Lock()
	if generation == o.generation {
		if err == nil {
			if source {
				o.sourceClosed = true
			} else {
				o.sinkClosed = true
			}
		} else {
			if !errors.Is(o.lastErr, err) {
				o.lastErr = errors.Join(o.lastErr, err)
				o.signalChangeLocked()
			}
		}
		if o.sourceClosed && o.sinkClosed {
			wasLeased := o.state == preparedAudioLeased
			o.current = audiosession.Resources{}
			o.sourceClosed = false
			o.sinkClosed = false
			o.lastErr = nil
			if o.closed {
				o.setStateLocked(preparedAudioClosed)
			} else {
				o.setStateLocked(preparedAudioIdle)
			}
			if wasLeased && o.leaseDone != nil {
				close(o.leaseDone)
				o.leaseDone = nil
			}
			if !o.closed {
				o.startPrepareLocked()
			}
		}
	}
	o.mu.Unlock()
	return err
}

// Close cancels preparation and cleans unclaimed resources asynchronously.
// Leased resources remain Runtime-owned; Close waits for their wrappers to
// confirm both closes. A timed-out call leaves all remaining handles owned so
// the host can retry after Runtime cleanup completes.
func (o *preparedAudioOwner) Close(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("prepared audio close context is nil")
	}
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		o.signalChangeLocked()
	}
	if o.prepareCancel != nil {
		o.prepareCancel()
	}
	prepareDone := o.prepareDone
	o.mu.Unlock()

	if prepareDone != nil {
		select {
		case <-prepareDone:
		case <-ctx.Done():
			return errors.Join(audiosession.ErrCleanupPending, ctx.Err())
		}
	}

	for {
		o.mu.Lock()
		if o.prepareDone != nil {
			done := o.prepareDone
			o.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return errors.Join(audiosession.ErrCleanupPending, ctx.Err())
			}
		}
		if o.current.Source == nil && o.current.Sink == nil {
			o.setStateLocked(preparedAudioClosed)
			o.mu.Unlock()
			return nil
		}
		if o.state == preparedAudioLeased {
			done := o.leaseDone
			o.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return errors.Join(audiosession.ErrCleanupPending, ctx.Err())
			}
		}
		if done := o.cleanupDone; done != nil {
			o.mu.Unlock()
			select {
			case <-done:
				o.mu.Lock()
				err := o.cleanupErr
				o.mu.Unlock()
				if err != nil {
					return err
				}
				continue
			case <-ctx.Done():
				return errors.Join(audiosession.ErrCleanupPending, ctx.Err())
			}
		}
		done := make(chan struct{})
		o.cleanupDone = done
		generation := o.generation
		o.mu.Unlock()
		go func() {
			err := o.closeUnleased(generation)
			o.mu.Lock()
			o.cleanupErr = err
			o.cleanupDone = nil
			close(done)
			o.mu.Unlock()
		}()
		select {
		case <-done:
			o.mu.Lock()
			err := o.cleanupErr
			o.mu.Unlock()
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return errors.Join(audiosession.ErrCleanupPending, ctx.Err())
		}
	}
}

func (o *preparedAudioOwner) closeUnleased(generation uint64) error {
	var err error
	o.mu.Lock()
	sourceClosed, sinkClosed := o.sourceClosed, o.sinkClosed
	o.mu.Unlock()
	if !sourceClosed {
		err = errors.Join(err, o.closePart(generation, true))
	}
	if !sinkClosed {
		err = errors.Join(err, o.closePart(generation, false))
	}
	return err
}

type preparedAudioSource struct {
	owner      *preparedAudioOwner
	generation uint64
	inner      audiosession.Source
}

func (s preparedAudioSource) Read(ctx context.Context, pcm []int16) (int, error) {
	return s.inner.Read(ctx, pcm)
}

func (s preparedAudioSource) Stop(ctx context.Context) error { return s.inner.Stop(ctx) }

func (s preparedAudioSource) Close() error {
	if s.owner == nil {
		return nil
	}
	return s.owner.closePart(s.generation, true)
}

type preparedAudioSink struct {
	owner      *preparedAudioOwner
	generation uint64
	inner      audiosession.Sink
}

func (s preparedAudioSink) TryWrite(pcm []int16) error { return s.inner.TryWrite(pcm) }

func (s preparedAudioSink) Drain(ctx context.Context) error { return s.inner.Drain(ctx) }

func (s preparedAudioSink) Close() error {
	if s.owner == nil {
		return nil
	}
	return s.owner.closePart(s.generation, false)
}

var _ audiosession.Prepare = (*preparedAudioOwner)(nil).Prepare
