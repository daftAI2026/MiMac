package audiosession

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

var errPCMStageFull = errors.New("audiosession: startup PCM stage is full")

// pcmStage is a bounded FIFO shared by one startup Reader and Runtime's
// existing copyPCM consumer. Its data/space notifications are fixed one-slot
// channels, so the 20 ms PCM loop does not allocate channels.
type pcmStage struct {
	mu             sync.Mutex
	samples        []int16
	head           int
	count          int
	deliveryOpen   bool
	terminal       error
	terminalSet    bool
	dataReady      chan struct{}
	spaceAvailable chan struct{}
}

func newPCMStage(sampleRate int) *pcmStage {
	seconds := int(receiverWaitBudget / time.Second)
	return &pcmStage{
		samples:        make([]int16, sampleRate*seconds),
		dataReady:      make(chan struct{}, 1),
		spaceAvailable: make(chan struct{}, 1),
	}
}

func (s *pcmStage) signalLocked(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *pcmStage) signalData() {
	select {
	case s.dataReady <- struct{}{}:
	default:
	}
}

func (s *pcmStage) freeLocked() int { return len(s.samples) - s.count }

func (s *pcmStage) readCapacity(max int) (free int, terminalSet bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return min(max, s.freeLocked()), s.terminalSet
}

func (s *pcmStage) waitForSpace(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.terminalSet || s.freeLocked() > 0 {
			s.mu.Unlock()
			return nil
		}
		wake := s.spaceAvailable
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

func (s *pcmStage) openDelivery() {
	s.mu.Lock()
	s.deliveryOpen = true
	s.signalLocked(s.dataReady)
	s.signalLocked(s.spaceAvailable)
	s.mu.Unlock()
}

func (s *pcmStage) append(pcm []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.appendLocked(pcm)
	if err == nil && len(pcm) > 0 {
		s.signalLocked(s.dataReady)
	}
	return err
}

func (s *pcmStage) appendLocked(pcm []int16) error {
	if len(pcm) > s.freeLocked() {
		return errPCMStageFull
	}
	if len(pcm) == 0 {
		return nil
	}
	tail := (s.head + s.count) % len(s.samples)
	first := copy(s.samples[tail:], pcm)
	copy(s.samples, pcm[first:])
	s.count += len(pcm)
	return nil
}

func (s *pcmStage) finishEOF(pcm []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendLocked(pcm); err != nil {
		return err
	}
	s.terminal = io.EOF
	s.terminalSet = true
	s.signalLocked(s.dataReady)
	return nil
}

// finishFault atomically decides whether this fault happened before delivery.
// Pre-gate faults clear the FIFO; post-gate faults keep valid n>0 PCM ahead of
// the terminal error so copyPCM preserves its established write-before-error
// behavior.
func (s *pcmStage) finishFault(pcm []int16, err error) (preGate bool, terminal error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	preGate = !s.deliveryOpen
	if preGate {
		s.clearLocked()
	} else if appendErr := s.appendLocked(pcm); appendErr != nil {
		s.clearLocked()
		err = errors.Join(err, appendErr)
	}
	s.terminal = err
	s.terminalSet = true
	s.signalLocked(s.dataReady)
	return preGate, err
}

func (s *pcmStage) read(ctx context.Context, done <-chan struct{}, result func() error, dst []int16) (n int, readErr error) {
	return s.readWithEmptyHook(ctx, done, result, dst, nil)
}

func (s *pcmStage) readWithEmptyHook(ctx context.Context, done <-chan struct{}, result func() error, dst []int16, beforeWait func()) (n int, readErr error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		s.mu.Lock()
		if s.count > 0 {
			n = s.popLocked(dst)
			s.signalLocked(s.spaceAvailable)
			empty := s.count == 0
			if empty && s.terminalSet {
				err := s.terminal
				s.mu.Unlock()
				return n, err
			}
			s.mu.Unlock()
			if empty {
				select {
				case <-done:
					s.mu.Lock()
					if s.count == 0 {
						err := result()
						s.mu.Unlock()
						return n, err
					}
					s.mu.Unlock()
				default:
				}
			}
			return n, nil
		}
		if s.terminalSet {
			err := s.terminal
			s.mu.Unlock()
			return 0, err
		}
		select {
		case <-done:
			// done only means the producer stopped; recheck the FIFO under its
			// lock before returning its terminal result.
			if s.count != 0 {
				s.mu.Unlock()
				continue
			}
			err := result()
			s.mu.Unlock()
			if err != nil {
				return 0, err
			}
			return 0, nil
		default:
		}
		wake := s.dataReady
		s.mu.Unlock()
		if beforeWait != nil {
			beforeWait()
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-done:
			// Completion is only a wake signal. Recheck the FIFO and terminal
			// under the lock; the producer may have appended its last block first.
			continue
		case <-wake:
		}
	}
}

func (s *pcmStage) popLocked(dst []int16) int {
	first := min(s.count, len(dst), len(s.samples)-s.head)
	n := copy(dst[:first], s.samples[s.head:s.head+first])
	remaining := min(len(dst)-n, s.count-n)
	if remaining > 0 {
		n += copy(dst[n:n+remaining], s.samples[:remaining])
	}
	s.head = (s.head + n) % len(s.samples)
	s.count -= n
	return n
}

func (s *pcmStage) clearLocked() {
	clear(s.samples)
	s.head = 0
	s.count = 0
	s.terminal = nil
	s.terminalSet = false
}

// release drops the bounded startup buffer after the session reader has
// joined. It is safe to call repeatedly during cleanup and only releases the
// reference; it does not promise immediate RSS reduction.
func (s *pcmStage) release() {
	s.mu.Lock()
	s.clearLocked()
	s.samples = nil
	s.deliveryOpen = false
	s.mu.Unlock()
}

// startupReader is the single Source.Read owner for an active session. It
// first fills only the bounded startup stage, then keeps moving PCM from the
// source into that same FIFO while Runtime's existing copyPCM consumes it.
type startupReader struct {
	runtime *Runtime
	session *resource
	stage   *pcmStage
	done    chan struct{}
	result  error // Written by run and read only after done closes.
}

func newStartupReader(runtime *Runtime, session *resource) *startupReader {
	return &startupReader{
		runtime: runtime,
		session: session,
		stage:   newPCMStage(session.resources.SampleRate),
		done:    make(chan struct{}),
	}
}

func (r *startupReader) start() { go r.run() }

func (r *startupReader) openDelivery() { r.stage.openDelivery() }

func (r *startupReader) read(dst []int16) (int, error) {
	return r.stage.read(r.session.ctx, r.done, func() error { return r.result }, dst)
}

func (r *startupReader) run() {
	defer func() {
		close(r.done)
		r.stage.signalData()
	}()
	var storage [320]int16
	pcm := storage[:r.session.resources.SampleRate/50]
	for {
		if err := r.session.ctx.Err(); err != nil {
			r.result = err
			return
		}
		free, terminalSet := r.stage.readCapacity(len(pcm))
		if terminalSet {
			r.result = errors.New("audiosession: startup reader observed a terminal FIFO")
			return
		}
		if free == 0 {
			if err := r.stage.waitForSpace(r.session.ctx); err != nil {
				r.result = err
				return
			}
			continue
		}
		n, readErr := r.session.resources.Source.Read(r.session.ctx, pcm[:free])
		if n < 0 || n > free {
			readErr = errors.New("audiosession: invalid PCM block length")
			n = 0
		}
		if err := r.session.ctx.Err(); err != nil {
			r.result = err
			return
		}
		if readErr == io.EOF {
			// Publish the source barrier before EOF can wake the copy worker.
			r.stage.mu.Lock()
			queued := r.stage.count + n
			r.stage.mu.Unlock()
			r.runtime.sourceReachedEOF(r.session, queued)
			if err := r.stage.finishEOF(pcm[:n]); err != nil {
				r.finishFault(err)
				return
			}
			r.result = io.EOF
			return
		}
		if readErr != nil {
			r.finishFaultWithPCM(pcm[:n], readErr)
			return
		}
		if n == 0 {
			r.finishFault(io.ErrNoProgress)
			return
		}
		if err := r.stage.append(pcm[:n]); err != nil {
			r.finishFault(err)
			return
		}
	}
}

func (r *startupReader) finishFault(err error) {
	r.finishFaultWithPCM(nil, err)
}

func (r *startupReader) finishFaultWithPCM(pcm []int16, err error) {
	preGate, terminal := r.stage.finishFault(pcm, err)
	r.result = terminal
	if preGate {
		r.runtime.fail(r.session, terminal)
	}
}
