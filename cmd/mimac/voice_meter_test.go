package main

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/siriglass"
)

func testTone(n int, frequency, amplitude float64) []int16 {
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(math.Round(amplitude * 32767 * math.Sin(2*math.Pi*frequency*float64(i)/16000)))
	}
	return pcm
}

func TestVoiceSpectrumUsesRuntimePCMWithoutChangingDelivery(t *testing.T) {
	s, tasks, source, sink := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	s.voiceOverlay.meter.setEnabled(true)
	press := beginHostVoice(t, s)
	runHostTask(t, tasks)
	pcm := testTone(siriglass.SpectrumWindowSize, 375, .25)
	for _, block := range [][]int16{pcm[:64], pcm[64:]} {
		source.samples <- block
		select {
		case delivered := <-sink.writes:
			if !reflect.DeepEqual(delivered, block) {
				t.Fatal("display changed delivered PCM")
			}
		case <-time.After(time.Second):
			t.Fatal("PCM delivery stalled")
		}
	}
	f := s.voiceOverlayFrameAt(time.Now())
	if !f.Visible || f.Spectrum.Count != 6 || math.Abs(float64(f.Spectrum.Magnitudes[1])-.09625) > .0002 || f.Spectrum.Magnitudes[3] > .0001 {
		t.Fatalf("real worker frequency identity lost: %+v", f.Spectrum)
	}
	meters := siriglass.MetersFromSpectrum(f.Spectrum)
	if meters.Count != 6 || meters.Amplitudes[1] != f.Spectrum.Magnitudes[1]*5 {
		t.Fatal("spectrum did not reach array payload")
	}
	second := s.voiceOverlayFrameAt(time.Now())
	if math.Abs(float64(second.Spectrum.Magnitudes[1]-f.Spectrum.Magnitudes[1]*1.5)) > .000001 {
		t.Fatal("read-time smoothing did not advance without new PCM")
	}
	if stale := s.voiceOverlayFrameAt(time.Now().Add(time.Second)); stale.Spectrum.Samples != 0 {
		t.Fatal("stale spectrum remained active")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
	if f := s.voiceOverlayFrameAt(time.Now()); f.Visible || f.Spectrum.Count != 0 {
		t.Fatal("retired session retained spectrum")
	}
}

func TestVoiceSpectrumDisableCancelAndNewTokenIsolation(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	press := beginHostVoice(t, s)
	runHostTask(t, tasks)
	token := s.coordinator.core.Snapshot().Session.Token
	var state voiceSpectrumState
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := state.observer(ctx, token, 16000).(*voicePCMObserver)
	pcm, now := testTone(128, 3000, .25), time.Now()
	o.Observe(pcm, now)
	if o.analyzer != nil {
		t.Fatal("disabled display allocated analyzer")
	}
	state.setEnabled(true)
	o.Observe(pcm[:64], now)
	state.setEnabled(false)
	state.setEnabled(true)
	o.Observe(pcm[64:], now)
	if got := state.snapshot(token, now); got.Samples != 0 {
		t.Fatal("pre-disable partial window crossed enable boundary")
	}
	o.Observe(pcm[:64], now)
	if got := state.snapshot(token, now); got.Count != 6 || got.Magnitudes[4] < .12 {
		t.Fatalf("new window missing: %+v", got)
	}
	if got := state.snapshot(bridge.SessionToken{}, now); got.Count != 0 {
		t.Fatal("other token read spectrum")
	}
	cancel()
	if got := state.snapshot(token, now); got.Count != 0 {
		t.Fatal("cancelled spectrum remained visible")
	}
	o.Clear()
	if state.value != (siriglass.Spectrum{}) {
		t.Fatal("clear retained spectrum")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
}

func TestVoiceSpectrumQuietKeepsEightKHzSessionShape(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	press := beginHostVoice(t, s)
	runHostTask(t, tasks)
	token := s.coordinator.core.Snapshot().Session.Token
	var state voiceSpectrumState
	state.setEnabled(true)
	o := state.observer(context.Background(), token, 8000)
	now := time.Now()
	checkQuiet := func(at time.Time) {
		t.Helper()
		got := state.snapshot(token, at)
		if got.Count != 6 || got.SampleRate != 8000 || got.StopFrequenciesHz[5] != 8000 || got.Samples != 0 || got.Magnitudes != ([siriglass.MaxMeterBands]float32{}) {
			t.Fatalf("quiet frame changed session spectrum shape: %+v", got)
		}
	}
	checkQuiet(now)
	o.Observe(make([]int16, 512), now)
	if got := state.snapshot(token, now); got.Samples != siriglass.SpectrumWindowSize {
		t.Fatal("silent real frame missing")
	}
	checkQuiet(now.Add(time.Second))
	o.Clear()
	checkQuiet(now)
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
}

func TestVoiceSpectrumGapRetiresPartialWindowAndSmoothing(t *testing.T) {
	var state voiceSpectrumState
	state.setEnabled(true)
	token := bridge.SessionToken{}
	o := state.observer(context.Background(), token, 16000)
	now := time.Now()
	tone := testTone(128, 375, .25)
	o.Observe(tone, now)
	first := state.snapshot(token, now).Magnitudes[1]
	// No UI read during the gap: the worker must retire both old samples and
	// old smoothing before a short transport read resumes.
	resumed := now.Add(time.Second)
	o.Observe(tone[:64], resumed)
	if got := state.snapshot(token, resumed); got.Samples != 0 {
		t.Fatal("resumed partial read reused pre-gap PCM")
	}
	o.Observe(tone[64:], resumed.Add(time.Millisecond))
	if got := state.snapshot(token, resumed.Add(time.Millisecond)); math.Abs(float64(got.Magnitudes[1]-first)) > .000001 {
		t.Fatalf("resumed window retained old smoothing: first %v resumed %v", first, got.Magnitudes[1])
	}
}
