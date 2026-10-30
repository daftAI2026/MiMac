package siriglass

import (
	"math"
	"testing"
)

func TestMetersFromSpectrumAppliesCampoGainOnce(t *testing.T) {
	s := Spectrum{SampleRate: 16000, Samples: 128, Sequence: 42, Count: 2, StopFrequenciesHz: [MaxMeterBands]float32{1000, 8000}, Magnitudes: [MaxMeterBands]float32{.125, .0625}}
	m := MetersFromSpectrum(s)
	if m.Count != 2 || m.Amplitudes[0] != .625 || m.Amplitudes[1] != .3125 || m.StopFrequenciesHz != s.StopFrequenciesHz {
		t.Fatalf("unexpected fivefold band conversion: %#v", m)
	}
	if err := m.validate(); err != nil {
		t.Fatal(err)
	}

	loudSpectrum := s
	loudSpectrum.Magnitudes[0] = .25
	loud := MetersFromSpectrum(loudSpectrum)
	if loud.Amplitudes[0] != 1.25 {
		t.Fatalf("Campo conversion changed magnitude 0.25 to %v, want 1.25", loud.Amplitudes[0])
	}
	if err := loud.validate(); err != nil {
		t.Fatalf("rejected original Siri amplitude 1.25: %v", err)
	}

	quiet := MetersFromSpectrum(Spectrum{})
	wantQuietStops := [MaxMeterBands]float32{200, 500, 1200, 2400, 4800, 8000}
	if quiet.Count != 6 || quiet.Amplitudes != ([MaxMeterBands]float32{}) || quiet.StopFrequenciesHz != wantQuietStops {
		t.Fatal("missing data fell back to scalar or invented audio")
	}
	for _, bad := range []float32{-0.01, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		invalid := m
		invalid.Amplitudes[0] = bad
		if invalid.validate() == nil {
			t.Fatalf("accepted amplitude %v", bad)
		}
	}
}
