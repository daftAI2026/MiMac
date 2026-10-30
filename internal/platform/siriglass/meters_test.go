package siriglass

import (
	"math"
	"testing"

	"mimac.local/mimac/internal/audiometer"
)

func TestMetersPreserveFrequencyAndAmplitudeIdentity(t *testing.T) {
	s := audiometer.Spectrum{Count: 2, StopFrequenciesHz: [audiometer.MaxBands]float32{1000, 8000}, RMS: [audiometer.MaxBands]float32{.1, .5}}
	m := MetersFromSpectrum(s)
	if m.Count != 2 || m.Amplitudes != s.RMS || m.StopFrequenciesHz != s.StopFrequenciesHz {
		t.Fatal("frequency identity was replaced by scalar")
	}
	if err := m.validate(); err != nil {
		t.Fatal(err)
	}
	quiet := MetersFromSpectrum(audiometer.Spectrum{})
	if quiet.Count < 2 || quiet.Amplitudes != ([audiometer.MaxBands]float32{}) {
		t.Fatal("missing data fell back to scalar or invented audio")
	}
	for _, bad := range []float32{-1, 1.1, float32(math.NaN()), float32(math.Inf(1))} {
		m.Amplitudes[0] = bad
		if m.validate() == nil {
			t.Fatalf("accepted amplitude %v", bad)
		}
	}
}
