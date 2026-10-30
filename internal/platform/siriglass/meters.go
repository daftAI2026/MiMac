package siriglass

import (
	"errors"
	"math"

	"mimac.local/mimac/internal/audiometer"
)

// Meters is an owned, comparable render value. No slice or PCM buffer survives
// a native call. Count == 0 selects the legacy scalar API only for callers
// explicitly using Update; the voice overlay always supplies array meters.
type Meters struct {
	Count             int
	Amplitudes        [audiometer.MaxBands]float32
	StopFrequenciesHz [audiometer.MaxBands]float32
}

// MetersFromSpectrum currently uses measured full-scale linear band RMS.
// This is deliberately not described as the original Siri normalization or
// band configuration; those upstream contracts are still being recovered.
// No data uses quiet array input, rather than switching back to legacy scalar.
func MetersFromSpectrum(s audiometer.Spectrum) Meters {
	if s.Count == 0 {
		return Meters{Count: 6, StopFrequenciesHz: [audiometer.MaxBands]float32{200, 500, 1200, 2400, 4800, 8000}}
	}
	return Meters{Count: s.Count, Amplitudes: s.RMS, StopFrequenciesHz: s.StopFrequenciesHz}
}

func (m Meters) validate() error {
	if m.Count < 0 || m.Count > len(m.Amplitudes) {
		return errors.New("siriglass: invalid meter count")
	}
	var previous float32
	for i := 0; i < m.Count; i++ {
		a, f := float64(m.Amplitudes[i]), float64(m.StopFrequenciesHz[i])
		if math.IsNaN(a) || math.IsInf(a, 0) || a < 0 || a > 1 ||
			math.IsNaN(f) || math.IsInf(f, 0) || float32(f) <= previous {
			return errors.New("siriglass: invalid amplitude or frequency")
		}
		previous = float32(f)
	}
	return nil
}
