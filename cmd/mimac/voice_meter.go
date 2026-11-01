package main

import (
	"context"
	"sync"
	"time"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/siriglass"
	"mimac.local/mimac/internal/platform/siriglass/spectral"
)

const voiceSpectrumFreshness = 100 * time.Millisecond

// Only raw display bands cross this mailbox. PCM and the session FSM retain
// their existing owners. Smoothing advances when the UI reads, not on writes.
type voiceSpectrumState struct {
	mu       sync.Mutex
	enabled  bool
	epoch    uint64
	token    bridge.SessionToken
	ctx      context.Context
	value    siriglass.Spectrum
	quiet    siriglass.Spectrum
	smoothed [siriglass.MaxMeterBands]float32
	at       time.Time
	trace    *voiceOverlayTrace // Immutable opt-in report owner; UI sample entries stay read-side.
}

func (m *voiceSpectrumState) setEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled != enabled {
		m.enabled = enabled
		m.epoch++
		m.value, m.at = siriglass.Spectrum{}, time.Time{}
		m.smoothed = [siriglass.MaxMeterBands]float32{}
	}
}

func (m *voiceSpectrumState) snapshot(token bridge.SessionToken, now time.Time) (result siriglass.Spectrum) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.trace != nil {
		defer func() { m.trace.sample(m, token, now, result) }()
	}
	if !m.enabled || token != m.token || m.ctx == nil || m.ctx.Err() != nil {
		return siriglass.Spectrum{}
	}
	if m.at.IsZero() || now.Before(m.at) || now.Sub(m.at) > voiceSpectrumFreshness {
		m.smoothed = [siriglass.MaxMeterBands]float32{}
		return m.quiet
	}
	out := m.value
	if err := spectral.SmoothInto(out.Magnitudes[:out.Count], m.smoothed[:out.Count], m.smoothed[:out.Count], spectral.DefaultSmoothingPreviousWeight); err != nil {
		return m.quiet
	}
	copy(out.Magnitudes[:], m.smoothed[:])
	return out
}

func (m *voiceSpectrumState) observer(ctx context.Context, token bridge.SessionToken, rate int) audiosession.PCMObserver {
	if ctx.Err() != nil || (rate != 8000 && rate != 16000) {
		return nil
	}
	m.mu.Lock()
	m.token, m.ctx, m.value, m.at = token, ctx, siriglass.Spectrum{}, time.Time{}
	m.smoothed = [siriglass.MaxMeterBands]float32{}
	stops := siriglass.SpectrumStopFrequencies()
	m.quiet = siriglass.Spectrum{SampleRate: rate, Count: len(stops)}
	copy(m.quiet.StopFrequenciesHz[:], stops[:])
	m.mu.Unlock()
	return &voicePCMObserver{state: m, ctx: ctx, token: token, rate: rate}
}

type voicePCMObserver struct {
	state    *voiceSpectrumState
	ctx      context.Context
	token    bridge.SessionToken
	rate     int
	epoch    uint64
	analyzer *spectral.Analyzer // Existing PCM worker only; UI reads the mailbox.
	tail     [siriglass.SpectrumWindowSize]int16
	used     int
	sequence uint64
	lastAt   time.Time
}

func (o *voicePCMObserver) discard() {
	// Native reset preserves old FFT bins. Retire the analyzer and partial input
	// entirely so a new token/enable cannot render a previous session's sound.
	o.analyzer = nil
	o.tail = [siriglass.SpectrumWindowSize]int16{}
	o.used, o.sequence = 0, 0
	o.lastAt = time.Time{}
}

func (o *voicePCMObserver) Observe(pcm []int16, at time.Time) {
	m := o.state
	m.mu.Lock()
	enabled, epoch := m.enabled && m.token == o.token && o.ctx.Err() == nil, m.epoch
	m.mu.Unlock()
	if !enabled {
		o.discard()
		return
	}
	if o.epoch != epoch {
		o.discard()
		o.epoch = epoch
	}
	if o.analyzer == nil {
		var err error
		o.analyzer, err = siriglass.NewSpectrumAnalyzer(o.rate)
		if err != nil {
			return
		} // Display failure never fails capture.
	}
	if len(pcm) == 0 {
		return
	}
	if !o.lastAt.IsZero() && (at.Before(o.lastAt) || at.Sub(o.lastAt) > voiceSpectrumFreshness) {
		// A resumed short read cannot form a window with pre-gap samples. Clear
		// read-side smoothing too, even if the UI did not draw during the gap.
		o.used = 0
		m.mu.Lock()
		if m.epoch == epoch && m.token == o.token {
			m.value, m.at = siriglass.Spectrum{}, time.Time{}
			m.smoothed = [siriglass.MaxMeterBands]float32{}
		}
		m.mu.Unlock()
	}
	o.lastAt = at
	// The reference processor consumes the last N samples of a callback. Our
	// transport can split a callback into shorter reads: keep only a bounded
	// trailing window in this display adapter, never delay or modify delivery.
	if len(pcm) >= len(o.tail) {
		copy(o.tail[:], pcm[len(pcm)-len(o.tail):])
		o.used = len(o.tail)
	} else {
		keep := min(o.used, len(o.tail)-len(pcm))
		copy(o.tail[:keep], o.tail[o.used-keep:o.used])
		copy(o.tail[keep:], pcm)
		o.used = keep + len(pcm)
	}
	if o.used < len(o.tail) {
		return
	}
	if err := o.analyzer.ProcessInt16(o.tail[:]); err != nil {
		return
	}
	stops := siriglass.SpectrumStopFrequencies()
	o.sequence++
	value := siriglass.Spectrum{SampleRate: o.rate, Samples: len(o.tail), Sequence: o.sequence, Count: len(stops)}
	copy(value.StopFrequenciesHz[:], stops[:])
	if err := o.analyzer.Raw(value.Magnitudes[:value.Count]); err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled && m.epoch == epoch && m.token == o.token && o.ctx.Err() == nil {
		m.value, m.at = value, at
	}
}

func (o *voicePCMObserver) Clear() {
	o.discard()
	m := o.state
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == o.token {
		m.value, m.at = siriglass.Spectrum{}, time.Time{}
		m.smoothed = [siriglass.MaxMeterBands]float32{}
	}
}
