package main

import (
	"context"
	"sync"
	"time"

	"mimac.local/mimac/internal/audiometer"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

// voiceSpectrumState is a latest-value display mailbox, not a session FSM.
// Core owns phase; the audio worker owns samples and analysis. No PCM crosses
// this mailbox, and disabling the display does not change audio routing.
type voiceSpectrumState struct {
	mu      sync.Mutex
	enabled bool
	epoch   uint64
	token   bridge.SessionToken
	ctx     context.Context
	value   audiometer.Spectrum
	quiet   audiometer.Spectrum
	at      time.Time
}

func (m *voiceSpectrumState) setEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled != enabled {
		m.enabled = enabled
		m.epoch++
		m.value, m.at = audiometer.Spectrum{}, time.Time{}
	}
}

func (m *voiceSpectrumState) snapshot(token bridge.SessionToken, now time.Time) audiometer.Spectrum {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled || token != m.token || m.ctx == nil || m.ctx.Err() != nil {
		return audiometer.Spectrum{}
	}
	if m.at.IsZero() || now.Before(m.at) || now.Sub(m.at) > 100*time.Millisecond {
		return m.quiet
	}
	return m.value
}

func (m *voiceSpectrumState) observer(ctx context.Context, token bridge.SessionToken, rate int) audiosession.PCMObserver {
	if ctx.Err() != nil || (rate != 8000 && rate != 16000) {
		return nil
	}
	m.mu.Lock()
	m.token, m.ctx, m.value, m.at = token, ctx, audiometer.Spectrum{}, time.Time{}
	stops := voiceMeterStops(rate)
	m.quiet = audiometer.Spectrum{SampleRate: rate, Count: len(stops)}
	copy(m.quiet.StopFrequenciesHz[:], stops)
	m.mu.Unlock()
	return &voicePCMObserver{state: m, ctx: ctx, token: token, rate: rate}
}

// These are MiMac's provisional measurement bands, not a recovered Siri
// session configuration. Keep them here, outside the reusable PCM analyzer.
// No display gain or automatic gain control is applied to source audio.
func voiceMeterStops(rate int) []float32 {
	if rate == 8000 {
		return []float32{200, 500, 1200, 2400, 4000}
	}
	return []float32{200, 500, 1200, 2400, 4800, 8000}
}

type voicePCMObserver struct {
	state    *voiceSpectrumState
	ctx      context.Context
	token    bridge.SessionToken
	rate     int
	epoch    uint64
	analyzer *audiometer.Analyzer // Only the existing PCM worker accesses this.
}

func (o *voicePCMObserver) Observe(pcm []int16, at time.Time) {
	m := o.state
	m.mu.Lock()
	enabled, epoch := m.enabled && m.token == o.token && o.ctx.Err() == nil, m.epoch
	m.mu.Unlock()
	if !enabled {
		if o.analyzer != nil {
			o.analyzer.Reset()
			o.analyzer = nil
		}
		return
	}
	if o.analyzer == nil {
		var err error
		o.analyzer, err = audiometer.New(o.rate, voiceMeterStops(o.rate))
		if err != nil {
			return
		} // Optional display failure never fails capture.
	}
	if o.epoch != epoch {
		o.analyzer.Reset()
		o.epoch = epoch
	}
	value, ready := o.analyzer.Write(pcm)
	if !ready {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled && m.epoch == epoch && m.token == o.token && o.ctx.Err() == nil {
		m.value, m.at = value, at
	}
}

func (o *voicePCMObserver) Clear() {
	if o.analyzer != nil {
		o.analyzer.Reset()
	}
	m := o.state
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == o.token {
		m.value, m.at = audiometer.Spectrum{}, time.Time{}
	}
}
