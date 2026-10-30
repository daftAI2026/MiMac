package main

import (
	"context"
	"math"
	"mimac.local/mimac/internal/bridge"
	"testing"
	"time"
)

// Native CoreSpeech oracle: 128-point periodic Hamming at 16kHz, a .25
// coherent sine at 375Hz, [200,500,...] boundaries, first read s=.5.
// 32767 Int16 quantization accounts for the small absolute tolerance.
func TestVoiceMeterReferenceMagnitudeRegression(t *testing.T) {
	var state voiceSpectrumState
	state.setEnabled(true)
	token := bridge.SessionToken{}
	observer := state.observer(context.Background(), token, 16000)
	now := time.Now()
	observer.Observe(testTone(512, 375, .25), now)
	first := state.snapshot(token, now)
	if math.Abs(float64(first.Magnitudes[1])-.09625) > .0002 {
		t.Fatalf("original Hamming magnitude expected .09625, got %.8f", first.Magnitudes[1])
	}
	second := state.snapshot(token, now)
	if math.Abs(float64(second.Magnitudes[1]-first.Magnitudes[1]*1.5)) > .000001 {
		t.Fatalf("second native-style read must advance .5 to .75 of raw: first=%v second=%v", first.Magnitudes[1], second.Magnitudes[1])
	}
	observer.Clear()
	if got := state.snapshot(token, now); got.Samples != 0 || got.Magnitudes[1] != 0 {
		t.Fatal("retired display retained old FFT data")
	}
}
