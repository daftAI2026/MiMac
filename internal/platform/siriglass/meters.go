package siriglass

import (
	"errors"
	"math"
)

const MaxMeterBands = 16

// Spectrum carries linear FFT band magnitudes. The host mailbox smooths them
// at read time before render conversion. SampleRate, Samples, and Sequence
// retain source metadata, while Count selects the populated bands.
type Spectrum struct {
	SampleRate        int
	Samples           int
	Sequence          uint64
	Count             int
	StopFrequenciesHz [MaxMeterBands]float32
	Magnitudes        [MaxMeterBands]float32
}

// Meters is an owned, comparable render value. No slice or PCM buffer survives
// a native call. Count == 0 selects the legacy scalar API only for callers
// explicitly using Update; the voice overlay always supplies array meters.
type Meters struct {
	Count             int
	Amplitudes        [MaxMeterBands]float32
	StopFrequenciesHz [MaxMeterBands]float32
}

// MetersFromSpectrum applies Campo's original 5x consumer conversion once to
// the producer's smoothed linear magnitudes. Values may exceed one; the native
// renderer receives them unchanged. No data uses quiet array input, rather
// than switching back to the legacy scalar path.
func MetersFromSpectrum(s Spectrum) Meters {
	if s.Count == 0 {
		return Meters{Count: 6, StopFrequenciesHz: [MaxMeterBands]float32{200, 500, 1200, 2400, 4800, 8000}}
	}
	meters := Meters{Count: s.Count, StopFrequenciesHz: s.StopFrequenciesHz}
	if s.Count < 0 || s.Count > MaxMeterBands {
		return meters
	}
	for i := 0; i < s.Count; i++ {
		meters.Amplitudes[i] = s.Magnitudes[i] * 5
	}
	return meters
}

func (m Meters) validate() error {
	if m.Count < 0 || m.Count > MaxMeterBands {
		return errors.New("siriglass: invalid meter count")
	}
	var previous float32
	for i := 0; i < m.Count; i++ {
		a, f := float64(m.Amplitudes[i]), float64(m.StopFrequenciesHz[i])
		if math.IsNaN(a) || math.IsInf(a, 0) || a < 0 ||
			math.IsNaN(f) || math.IsInf(f, 0) || float32(f) <= previous {
			return errors.New("siriglass: invalid amplitude or frequency")
		}
		previous = float32(f)
	}
	return nil
}
