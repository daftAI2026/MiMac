// Package audiosession executes the bridge's audio effects. Core remains the
// only voice state machine; this package owns cancellable I/O and its resources.
package audiosession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/bridge"
)

// Source must permit Stop concurrently with Read. Stop requests a normal end;
// Read returns io.EOF only after the source's verified tail boundary. Read and
// Stop must respond to cancellation. Close owns native callback quiescence and
// must retain anything still live when it returns an error.
// An optional Err() error method reports asynchronous transport faults without
// starting capture or blocking; Runtime checks it around receiver preparation.
type Source interface {
	Read(context.Context, []int16) (int, error)
	Stop(context.Context) error
	Close() error
}

type Sink interface {
	TryWrite([]int16) error
	Drain(context.Context) error
	Close() error
}

// SessionSink defers expensive consumer preparation until a physical session.
// The source can already be armed without retaining a recognizer while idle.
type SessionSink interface{ StartSession(context.Context) error }

// TranscriptSink reports only completed output. Delivery belongs to the host,
// which must reject a result for a cancelled or retired SessionToken.
type TranscriptSink interface{ Transcript() (string, bool, error) }

type Transcript struct {
	Token bridge.SessionToken
	Text  string
}

// PCMObserver is an optional, bounded, in-process display tap. Calls are
// serialized on the existing PCM worker, outside Runtime.mu. Observe must not
// modify or retain its borrowed slice, block, do I/O, or dispatch to the UI.
// A bounded analysis window may own a copy; no sample history is persisted.
// Clear runs before drain and on every worker exit. It is idempotent.
// This tap neither owns the source nor acknowledges delivery to a receiver.
type PCMObserver interface {
	Observe([]int16, time.Time)
	Clear()
}

type Resources struct {
	Source     Source
	Sink       Sink
	SampleRate int
	Observer   PCMObserver
	// WaitReceiver is supplied by an external-consumer adapter. It must verify
	// the session's exact input route and active consumer, not merely that a
	// shortcut was sent. Nil is for routes requiring no external readiness.
	// It runs before SourcePrepared and PCM delivery. Source.Read may already be
	// staging bounded PCM, but no output is delivered until Core opens the gate.
	// It must honor cancellation; a late return never opens the gate and the
	// resources remain owned.
	WaitReceiver func(context.Context) error
	// WatchReceiver runs for the rest of the session after WaitReceiver succeeds.
	// It should return a non-nil error when the verified receiver stops matching
	// this session. Returning nil before ctx is canceled is an error. Runtime
	// cancels and joins the watcher before closing Source or Sink.
	WatchReceiver func(context.Context) error
}

// Prepare may return partial resources with an error. Their ownership always
// transfers to Runtime, including when native preparation ignores cancellation.
type Prepare func(context.Context, bridge.SessionToken) (Resources, error)

type Completion struct {
	Event         bridge.Event
	Transcript    *Transcript
	Err           error
	CleanupFailed bool
}

var (
	ErrClosed                = errors.New("audiosession: runtime is closed")
	ErrResourceBusy          = errors.New("audiosession: previous resources are still owned")
	ErrCleanupPending        = errors.New("audiosession: cleanup has not completed")
	errReceiverWatchReturned = errors.New("audiosession: receiver watcher returned before cancellation")
)

const resourceID = "audio-stream"

const receiverWaitBudget = 2 * time.Second

// Runtime starts no goroutine until PrepareSource. The two-slot completion
// channel carries control events only. A consumer must process these on the
// Core's owner thread; it must not block PCM callbacks on that thread.
type Runtime struct {
	mu          sync.Mutex
	prepare     Prepare
	completions chan Completion
	ctx         context.Context
	cancel      context.CancelFunc
	active      *resource
	pending     *resource // One next request while successful disposal finishes.
	lastSession uint64
	closed      bool
}

type resource struct {
	token        bridge.SessionToken
	ctx          context.Context
	cancel       context.CancelFunc
	gate         chan struct{}
	ready        chan struct{}
	done         chan struct{}
	resources    Resources // Immutable until the Read/Stop tasks have finished.
	gateOpened   bool
	stopStarted  bool
	stopDone     chan struct{}
	stopCtx      context.Context
	stopCancel   context.CancelFunc
	stopWatch    func() bool
	sourceEOF    bool
	stopReturned bool
	tailSamples  int
	cause        error
	terminalSent bool // Protected by Runtime.mu; arbitrates Core terminal events.
	finished     bool
	cleanupErr   error
	inputLevel   InputLevel // Protected by Runtime.mu; never contains PCM.
}

type receiverWatch struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (w *receiverWatch) cancelAndWait() {
	if w == nil {
		return
	}
	w.cancel()
	<-w.done
}

func New(prepare Prepare) (*Runtime, error) {
	if prepare == nil {
		return nil, errors.New("audiosession: prepare factory is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{prepare: prepare, completions: make(chan Completion, 2), ctx: ctx, cancel: cancel}, nil
}

func (r *Runtime) Completions() <-chan Completion { return r.completions }

// Submit only accepts control requests; it never waits for native Open, Read,
// Stop, Drain or Close. Success means accepted, not completed or recognized.
func (r *Runtime) Submit(effect bridge.Effect) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e := effect.(type) {
	case bridge.PrepareSourceEffect:
		if r.closed {
			return ErrClosed
		}
		if e.Token.ID() == 0 || e.Token.Device().Generation() == 0 {
			return errors.New("audiosession: invalid session token")
		}
		if e.Token.ID() <= r.lastSession {
			return nil // A repeated/retired Core effect cannot reopen resources.
		}
		if r.active != nil && (r.active.ctx.Err() == nil || r.active.cleanupErr != nil || r.pending != nil) {
			return errors.Join(ErrResourceBusy, r.active.cleanupErr)
		}
		ctx, cancel := context.WithCancel(r.ctx)
		s := &resource{token: e.Token, ctx: ctx, cancel: cancel, gate: make(chan struct{}), ready: make(chan struct{}), done: make(chan struct{})}
		r.lastSession = e.Token.ID()
		if r.active != nil {
			r.pending = s
			return nil
		}
		r.active = s
		go r.run(s)
	case bridge.OpenGateEffect:
		if s := r.match(e.Token); s != nil && !s.finished && !s.gateOpened && s.ctx.Err() == nil {
			s.gateOpened = true
			close(s.gate)
		}
	case bridge.StopSourceEffect:
		if s := r.match(e.Token); s != nil && !s.finished && !s.stopStarted && s.ctx.Err() == nil {
			s.stopStarted = true
			s.stopDone = make(chan struct{})
			r.armStopDeadlineLocked(s, 2*time.Second)
			go r.stop(s)
		}
	case bridge.CloseGateEffect:
		if s := r.match(e.Token); s != nil {
			s.cancel()
			if r.pending == s {
				r.cancelPendingLocked()
			}
		}
	case bridge.DisposeResourceEffect:
		if s := r.match(e.Token); s != nil && e.ResourceID == resourceID {
			s.cancel()
			if r.pending == s {
				r.cancelPendingLocked()
				return nil
			}
			if s.finished && s.cleanupErr != nil {
				r.retryCleanupLocked(s)
			}
		}
	default:
		return fmt.Errorf("audiosession: unsupported effect %T", effect)
	}
	return nil
}

func (r *Runtime) match(token bridge.SessionToken) *resource {
	if r.active != nil && r.active.token == token {
		return r.active
	}
	if r.pending != nil && r.pending.token == token {
		return r.pending
	}
	return nil
}

func (r *Runtime) cancelPendingLocked() *resource {
	s := r.pending
	if s != nil {
		s.cancel()
		s.finished = true
		close(s.done)
		r.pending = nil
	}
	return s
}

func (r *Runtime) fail(s *resource, err error) {
	r.mu.Lock()
	if !s.finished && !s.terminalSent && s.ctx.Err() == nil && s.cause == nil {
		s.cause = err
		s.cancel()
	}
	completion, claimed := r.claimFailureLocked(s)
	r.mu.Unlock()
	if claimed {
		r.emit(completion)
	}
}

// claimFailureLocked is the single arbiter for terminal SourceFailed events.
// Resource cleanup faults remain a separate CleanupFailed completion.
func (r *Runtime) claimFailureLocked(s *resource) (Completion, bool) {
	if s.finished || s.terminalSent || s.cause == nil {
		return Completion{}, false
	}
	s.terminalSent = true
	r.stopDeadlineLocked(s)
	return Completion{Event: bridge.SourceFailed(s.token), Err: s.cause}, true
}

func (r *Runtime) stopDeadlineLocked(s *resource) {
	if s.stopWatch != nil {
		s.stopWatch()
		s.stopWatch = nil
	}
	if s.stopCancel != nil {
		s.stopCancel()
		s.stopCancel = nil
	}
}

func (r *Runtime) startReceiverWatch(s *resource, watch func(context.Context) error) *receiverWatch {
	if watch == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	watcher := &receiverWatch{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(watcher.done)
		err := watch(ctx)
		// Native metadata calls can return after the session has already been
		// canceled. Such a late result belongs to this retired session only.
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errReceiverWatchReturned
		}
		r.fail(s, err)
	}()
	return watcher
}

func (r *Runtime) emit(c Completion) {
	select {
	case r.completions <- c:
	case <-r.ctx.Done(): // Cleanup never depends on a quitting UI consumer.
	}
}

func (r *Runtime) stop(s *resource) {
	defer close(s.stopDone)
	select {
	case <-s.ready:
	case <-s.ctx.Done():
		return
	}
	if s.ctx.Err() != nil || s.resources.Source == nil {
		return
	}
	if err := s.resources.Source.Stop(s.stopCtx); err != nil {
		if s.ctx.Err() == nil {
			r.fail(s, err)
		}
		return
	}
	r.mu.Lock()
	s.stopReturned = true
	completion, claimed := r.advanceStopDeadlineLocked(s)
	r.mu.Unlock()
	if claimed {
		r.emit(completion)
	}
}

// Each deadline belongs to one I/O phase. A retired timer must never fail the
// following phase, including when its callback was already queued.
func (r *Runtime) armStopDeadlineLocked(s *resource, budget time.Duration) {
	if s.stopWatch != nil {
		s.stopWatch()
	}
	if s.stopCancel != nil {
		s.stopCancel()
	}
	ctx, cancel := context.WithTimeout(s.ctx, budget)
	s.stopCtx, s.stopCancel = ctx, cancel
	s.stopWatch = context.AfterFunc(ctx, func() {
		r.mu.Lock()
		var completion Completion
		claimed := false
		if s.stopCtx == ctx && errors.Is(ctx.Err(), context.DeadlineExceeded) && !s.finished && !s.terminalSent && s.ctx.Err() == nil && s.cause == nil {
			s.cause = context.DeadlineExceeded
			s.cancel()
			completion, claimed = r.claimFailureLocked(s)
		}
		r.mu.Unlock()
		if claimed {
			r.emit(completion)
		}
	})
}

func (r *Runtime) sourceReachedEOF(s *resource, queued int) {
	r.mu.Lock()
	s.sourceEOF = true
	// The copy worker can have one block outside the FIFO. The sink's accepted
	// buffers and finalization remain covered by the existing I/O allowance.
	s.tailSamples = queued + s.resources.SampleRate/50
	completion, claimed := r.advanceStopDeadlineLocked(s)
	r.mu.Unlock()
	if claimed {
		r.emit(completion)
	}
}

func (r *Runtime) advanceStopDeadlineLocked(s *resource) (Completion, bool) {
	if !s.sourceEOF || !s.stopReturned || s.ctx.Err() != nil {
		return Completion{}, false
	}
	if err := s.stopCtx.Err(); err != nil {
		if s.cause == nil {
			s.cause = err
			s.cancel()
		}
		return r.claimFailureLocked(s)
	}
	playout := time.Duration(s.tailSamples) * time.Second / time.Duration(s.resources.SampleRate)
	r.armStopDeadlineLocked(s, playout+2*time.Second)
	return Completion{}, false
}

func (r *Runtime) run(s *resource) {
	resources, err := r.prepare(s.ctx, s.token)
	s.resources = resources
	close(s.ready)
	if err == nil && (resources.Source == nil || resources.Sink == nil || (resources.SampleRate != 8000 && resources.SampleRate != 16000)) {
		err = errors.New("audiosession: incomplete resources or unsupported PCM format")
	}
	if s.ctx.Err() == nil && err == nil {
		// The resources now belong to Runtime and have a valid negotiated PCM
		// format. Consume an armed source before sink setup or receiver readiness
		// can leave its bounded producer queue idle; the reader can only stage.
		err = sourceHealth(resources.Source)
	}
	var reader *startupReader
	if s.ctx.Err() == nil && err == nil {
		reader = newStartupReader(r, s)
		reader.start()
	}
	if s.ctx.Err() == nil && err == nil {
		if sink, ok := resources.Sink.(SessionSink); ok {
			err = sink.StartSession(s.ctx)
		}
	}
	if s.ctx.Err() == nil && err == nil && resources.WaitReceiver != nil {
		// This is a failure deadline, not a startup delay. Core decides whether
		// physical release cancels this wait or keeps the prepared source alive
		// long enough to open the gate and drain its captured tail.
		readyCtx, cancelReady := context.WithTimeout(s.ctx, receiverWaitBudget)
		err = resources.WaitReceiver(readyCtx)
		err = errors.Join(err, readyCtx.Err())
		cancelReady()
	}
	if s.ctx.Err() == nil && err == nil {
		// Native callbacks can fault a pre-armed source while the receiver waits.
		// Do not publish readiness for a source already known to have failed.
		err = sourceHealth(resources.Source)
	}
	var watcher *receiverWatch
	if s.ctx.Err() == nil && err == nil {
		r.emit(Completion{Event: bridge.SourcePrepared(s.token, resourceID)})
		watcher = r.startReceiverWatch(s, resources.WatchReceiver)
		select {
		case <-s.gate:
			if s.ctx.Err() == nil {
				// A source can fault asynchronously while the Core gate is closed.
				// Recheck it before the first staged sample can reach the sink.
				err = sourceHealth(resources.Source)
				if err == nil {
					reader.openDelivery()
					err = r.copyPCM(s, reader, watcher)
				}
			}
		case <-s.ctx.Done():
		}
	}
	r.mu.Lock()
	if !s.terminalSent && s.cause == nil && err != nil && s.ctx.Err() == nil {
		s.cause = err
	}
	completion, claimed := r.claimFailureLocked(s)
	r.mu.Unlock()
	// Cancel before notifying Core, so a blocked startup read cannot continue
	// producing while the failure event releases the target's held shortcut.
	s.cancel()
	if claimed {
		r.emit(completion)
	}
	// The reader owns Source.Read until it joins. Never wait under Runtime.mu.
	if reader != nil {
		<-reader.done
		reader.stage.release()
	}
	r.clearInputLevel(s)
	if resources.Observer != nil {
		// Includes failed preparation/receiver readiness and gate cancellation.
		resources.Observer.Clear()
	}
	watcher.cancelAndWait()
	r.mu.Lock()
	stopDone := s.stopDone
	r.mu.Unlock()
	if stopDone != nil {
		// A source that ignores cancellation remains owned; Close's caller
		// gets its deadline error rather than freeing memory still in use.
		<-stopDone
	}
	r.cleanup(s)
}

func (r *Runtime) copyPCM(s *resource, reader *startupReader, watcher *receiverWatch) error {
	defer r.clearInputLevel(s)
	if reader == nil {
		return errors.New("audiosession: missing session PCM reader")
	}
	var storage [320]int16
	pcm := storage[:s.resources.SampleRate/50]
	for {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		n, readErr := reader.read(pcm)
		if n < 0 || n > len(pcm) {
			return errors.New("audiosession: invalid PCM block length")
		}
		if n > 0 {
			if err := s.ctx.Err(); err != nil {
				return err
			}
			r.recordInputLevel(s, pcm[:n])
			if s.resources.Observer != nil {
				s.resources.Observer.Observe(pcm[:n], time.Now())
			}
			if err := writePCM(s.ctx, s.resources.Sink, pcm[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return r.drainEOF(s, watcher)
		}
		if readErr != nil {
			return readErr
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
}

func (r *Runtime) drainEOF(s *resource, watcher *receiverWatch) error {
	// The source has ended even if the output still needs time to drain.
	r.clearInputLevel(s)
	if s.resources.Observer != nil {
		s.resources.Observer.Clear()
	}
	r.mu.Lock()
	stopDone := s.stopDone
	r.mu.Unlock()
	if stopDone == nil {
		return errors.New("audiosession: source ended without a stop request")
	}
	select {
	case <-stopDone:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	// Stop may complete after the reader reaches EOF. Read the final phase's
	// context only after joining Stop, rather than retaining the retired timer.
	r.mu.Lock()
	stopCtx := s.stopCtx
	r.mu.Unlock()
	if err := s.resources.Sink.Drain(stopCtx); err != nil {
		return err
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	watcher.cancelAndWait()
	completion := Completion{Event: bridge.SourceDrained(s.token)}
	if sink, ok := s.resources.Sink.(TranscriptSink); ok {
		text, present, err := sink.Transcript()
		if err != nil {
			return err
		}
		if present {
			completion.Transcript = &Transcript{Token: s.token, Text: text}
		}
	}
	if !r.claimDrain(s, stopCtx, completion) {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		r.mu.Lock()
		cause := s.cause
		r.mu.Unlock()
		if cause != nil {
			return cause
		}
		return errors.New("audiosession: terminal result was already claimed")
	}
	<-s.ctx.Done() // Core decides when to close its gate and resource.
	return nil
}

// claimDrain serializes success with deadline/watch failures. In particular,
// it checks the phase context while holding Runtime.mu so a late nil Drain
// cannot win merely because context.AfterFunc has not run yet.
func (r *Runtime) claimDrain(s *resource, stopCtx context.Context, completion Completion) bool {
	r.mu.Lock()
	if s.finished || s.terminalSent {
		r.mu.Unlock()
		return false
	}
	if s.cause != nil {
		failed, claimed := r.claimFailureLocked(s)
		r.mu.Unlock()
		if claimed {
			r.emit(failed)
		}
		return false
	}
	if s.ctx.Err() != nil {
		r.mu.Unlock()
		return false
	}
	if stopCtx.Err() != nil {
		s.cause = stopCtx.Err()
		s.cancel()
		failed, claimed := r.claimFailureLocked(s)
		r.mu.Unlock()
		if claimed {
			r.emit(failed)
		}
		return false
	}
	s.terminalSent = true
	r.stopDeadlineLocked(s)
	r.mu.Unlock()
	r.emit(completion)
	return true
}

func sourceHealth(source Source) error {
	if health, ok := source.(interface{ Err() error }); ok {
		return health.Err()
	}
	return nil
}

func writePCM(ctx context.Context, sink Sink, pcm []int16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sink.TryWrite(pcm); !errors.Is(err, audiooutput.ErrBackpressure) {
		return err
	}
	// The steady path allocates no timer. Retry timing exists only while the
	// bounded native queue is full, and cannot extend indefinitely.
	deadline := time.NewTimer(250 * time.Millisecond)
	ticker := time.NewTicker(2 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			return context.DeadlineExceeded
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := sink.TryWrite(pcm); !errors.Is(err, audiooutput.ErrBackpressure) {
				return err
			}
		}
	}
}

func (r *Runtime) cleanup(s *resource) {
	var closeErr error
	if s.resources.Source != nil {
		if err := s.resources.Source.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		} else {
			s.resources.Source = nil
		}
	}
	if s.resources.Sink != nil {
		if err := s.resources.Sink.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		} else {
			s.resources.Sink = nil
		}
	}
	r.mu.Lock()
	if s.stopWatch != nil {
		s.stopWatch()
	}
	if s.stopCancel != nil {
		s.stopCancel()
	}
	s.cleanupErr = closeErr
	s.finished = true
	var rejected *resource
	if closeErr != nil {
		rejected = r.cancelPendingLocked()
	}
	if closeErr == nil && r.active == s {
		r.active = nil
		if r.pending != nil && !r.closed {
			next := r.pending
			r.pending = nil
			r.active = next
			go r.run(next)
		}
	}
	close(s.done)
	r.mu.Unlock()
	if closeErr != nil {
		r.emit(Completion{Event: bridge.SourceFailed(s.token), Err: closeErr, CleanupFailed: true})
		if rejected != nil {
			r.emit(Completion{Event: bridge.SourceFailed(rejected.token), Err: errors.Join(ErrResourceBusy, closeErr)})
		}
	}
}

func (r *Runtime) retryCleanupLocked(s *resource) {
	s.finished = false
	s.done = make(chan struct{})
	go r.cleanup(s)
}

// Close cancels first, then waits on the caller's context. Call it from a host
// shutdown task, never its UI thread. A timeout leaves native resources owned;
// a late Open result is cleaned locally even if nobody consumes completions.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.cancelPendingLocked()
	s := r.active
	if s == nil {
		r.mu.Unlock()
		return nil
	}
	s.cancel()
	if s.finished && s.cleanupErr != nil {
		r.retryCleanupLocked(s)
	}
	done := s.done
	r.mu.Unlock()
	// Prefer a completed cleanup even if the caller's context is already
	// cancelled (the synchronous plugin-stop path uses exactly that check).
	select {
	case <-done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return s.cleanupErr
	default:
	}
	select {
	case <-done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return s.cleanupErr
	case <-ctx.Done():
		return errors.Join(ErrCleanupPending, ctx.Err())
	}
}
