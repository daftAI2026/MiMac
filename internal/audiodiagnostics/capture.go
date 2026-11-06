// Package audiodiagnostics contains bounded, explicitly owned diagnostics for
// audio samples. It does not persist PCM or participate in session control.
package audiodiagnostics

import (
	"errors"
	"math"
	"sync"
	"time"
)

var (
	ErrInvalidCapacity = errors.New("audiodiagnostics: max samples must be positive and addressable")
	ErrCaptureClosed   = errors.New("audiodiagnostics: capture is closed")
	ErrCaptureOverflow = errors.New("audiodiagnostics: capture overflowed; complete PCM unavailable")
)

// Stats summarizes all nonempty blocks observed by a Capture. Sample metrics
// include blocks rejected after overflow; retained counts include only whole
// blocks copied into the bounded buffer. ClippedSamples counts samples at the
// int16 rails (-32768 or 32767). RMS is expressed in int16 sample units (not normalized to 0..1).
type Stats struct {
	ObservedBlocks   uint64
	ObservedSamples  uint64
	RetainedBlocks   uint64
	RetainedSamples  uint64
	OverflowBlocks   uint64
	OverflowSamples  uint64
	ZeroSamples      uint64
	ClippedSamples   uint64
	Peak             uint16
	RMS              float64
	FirstObservedAt  time.Time
	LastObservedAt   time.Time
	MaxBlockInterval time.Duration
}

// Capture owns one preallocated flat PCM buffer until Take transfers it or
// Release clears it. It never writes PCM to disk, changes the source samples,
// or retains an Observe argument. Each block is accepted whole or rejected
// whole; once one block overflows, later blocks are counted as overflow too so
// the stored prefix can never be mistaken for a complete stream.
//
// Observe has the same method shape as audiosession.PCMObserver for explicit
// in-process diagnostic mirroring. Do not attach Capture directly as a
// display Resources.Observer: that lifecycle calls Clear before Drain and
// would discard the capture before Take. Clear is an idempotent Release alias.
type Capture struct {
	mu sync.Mutex

	buffer      []int16
	retained    int
	closed      bool
	overflowed  bool
	hasObserved bool
	stats       Stats
	sumSquares  float64
}

// NewCapture reserves maxSamples int16 samples for a single diagnostic capture.
func NewCapture(maxSamples int) (*Capture, error) {
	maxInt := int(^uint(0) >> 1)
	if maxSamples <= 0 || maxSamples > maxInt/2 {
		return nil, ErrInvalidCapacity
	}
	return &Capture{buffer: make([]int16, maxSamples)}, nil
}

// Observe copies a whole source block into the capture when it fits. Calls
// after Take or Release are ignored. Empty blocks do not affect statistics.
func (c *Capture) Observe(pcm []int16, at time.Time) {
	if c == nil || len(pcm) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}

	c.recordStatsLocked(pcm, at)
	if c.overflowed || len(pcm) > len(c.buffer)-c.retained {
		c.overflowed = true
		c.stats.OverflowBlocks = addCount(c.stats.OverflowBlocks, 1)
		c.stats.OverflowSamples = addCount(c.stats.OverflowSamples, uint64(len(pcm)))
		return
	}

	copy(c.buffer[c.retained:], pcm)
	c.retained += len(pcm)
	c.stats.RetainedBlocks = addCount(c.stats.RetainedBlocks, 1)
	c.stats.RetainedSamples = addCount(c.stats.RetainedSamples, uint64(len(pcm)))
}

// Take closes the capture and transfers ownership of the captured samples
// exactly once. If any block overflowed, it returns ErrCaptureOverflow and no
// samples; the unusable internal prefix is cleared before returning.
func (c *Capture) Take() ([]int16, Stats, error) {
	if c == nil {
		return nil, Stats{}, ErrCaptureClosed
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, c.statsSnapshotLocked(), ErrCaptureClosed
	}

	c.closed = true
	stats := c.statsSnapshotLocked()
	if c.overflowed {
		c.clearLocked()
		return nil, stats, ErrCaptureOverflow
	}

	var samples []int16
	if c.retained != 0 {
		samples = c.buffer[:c.retained:c.retained]
	}
	c.buffer = nil
	c.retained = 0
	return samples, stats, nil
}

// Release closes the capture, zeroes its owned buffer, and releases it. It is
// safe to call repeatedly and after Take; a transferred slice remains owned by
// the caller and is never cleared here.
func (c *Capture) Release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.clearLocked()
}

// Clear implements the optional observer cleanup shape. For Capture it has
// Release semantics, so use it only when ending the diagnostic capture.
func (c *Capture) Clear() { c.Release() }

func (c *Capture) recordStatsLocked(pcm []int16, at time.Time) {
	c.stats.ObservedBlocks = addCount(c.stats.ObservedBlocks, 1)
	c.stats.ObservedSamples = addCount(c.stats.ObservedSamples, uint64(len(pcm)))
	if c.hasObserved {
		if gap := at.Sub(c.stats.LastObservedAt); gap > c.stats.MaxBlockInterval {
			c.stats.MaxBlockInterval = gap
		}
	} else {
		c.stats.FirstObservedAt = at
		c.hasObserved = true
	}
	c.stats.LastObservedAt = at

	for _, sample := range pcm {
		value := int32(sample)
		magnitude := value
		if magnitude < 0 {
			magnitude = -magnitude
		}
		if uint16(magnitude) > c.stats.Peak {
			c.stats.Peak = uint16(magnitude)
		}
		if sample == math.MinInt16 || sample == math.MaxInt16 {
			c.stats.ClippedSamples = addCount(c.stats.ClippedSamples, 1)
		}
		if sample == 0 {
			c.stats.ZeroSamples = addCount(c.stats.ZeroSamples, 1)
		}
		c.sumSquares += float64(value) * float64(value)
	}
}

func (c *Capture) statsSnapshotLocked() Stats {
	stats := c.stats
	if stats.ObservedSamples != 0 {
		stats.RMS = math.Sqrt(c.sumSquares / float64(stats.ObservedSamples))
	}
	return stats
}

func (c *Capture) clearLocked() {
	clear(c.buffer)
	c.buffer = nil
	c.retained = 0
}

func addCount(value, increment uint64) uint64 {
	if math.MaxUint64-value < increment {
		return math.MaxUint64
	}
	return value + increment
}
