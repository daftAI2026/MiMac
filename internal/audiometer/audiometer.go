// Package audiometer computes windowed, one-sided spectra for mono PCM.
package audiometer

import (
	"errors"
	"fmt"
	"math"
)

const (
	// WindowSize is the fixed number of samples in each non-overlapping frame.
	WindowSize = 512
	// MaxBands is the largest number of frequency bands returned in a Spectrum.
	MaxBands = 16

	halfSpectrumBins = WindowSize/2 + 1
)

var (
	ErrUnsupportedSampleRate = errors.New("audiometer: unsupported sample rate")
	ErrTooManyBands          = errors.New("audiometer: too many bands")
	ErrInvalidStopFrequency  = errors.New("audiometer: invalid stop frequency")
	ErrMissingNyquistStop    = errors.New("audiometer: final stop frequency must equal Nyquist")
)

// Spectrum is a value snapshot for one complete 512-sample window. Bands is a
// fixed-size pair of arrays so returned spectra do not share storage with the
// analyzer. StopFrequenciesHz contains each band's upper boundary, including
// Nyquist as the final entry. A band's lower boundary is zero for the first
// band and the previous stop for every later band.
type Spectrum struct {
	SampleRate        int
	Samples           int
	Sequence          uint64
	Count             int
	StopFrequenciesHz [MaxBands]float32
	RMS               [MaxBands]float32
}

// Analyzer incrementally turns signed 16-bit mono PCM into fixed-window
// spectra. Its mutable state is per-instance and Write is synchronous. Calls
// to Write and Reset on the same analyzer must not run concurrently.
type Analyzer struct {
	sampleRate        int
	bandCount         int
	stopFrequenciesHz [MaxBands]float32
	binBand           [halfSpectrumBins]uint8
	window            [WindowSize]float64
	windowPower       float64
	twiddleReal       [WindowSize / 2]float64
	twiddleImag       [WindowSize / 2]float64

	frame     [WindowSize]int16
	frameUsed int
	real      [WindowSize]float64
	imag      [WindowSize]float64
	sequence  uint64
}

// New creates an analyzer for 8 kHz or 16 kHz PCM. stopFrequenciesHz are
// copied and define every band's upper edge, including an explicit final
// Nyquist edge. Between one and MaxBands stops are required.
func New(sampleRate int, stopFrequenciesHz []float32) (*Analyzer, error) {
	if sampleRate != 8000 && sampleRate != 16000 {
		return nil, fmt.Errorf("%w: %d Hz", ErrUnsupportedSampleRate, sampleRate)
	}
	if len(stopFrequenciesHz) > MaxBands {
		return nil, fmt.Errorf("%w: got %d stops, maximum is %d", ErrTooManyBands, len(stopFrequenciesHz), MaxBands)
	}
	if len(stopFrequenciesHz) == 0 {
		return nil, ErrMissingNyquistStop
	}

	nyquist := float32(sampleRate) / 2
	previous := float32(0)
	for i, stop := range stopFrequenciesHz {
		if math.IsNaN(float64(stop)) || math.IsInf(float64(stop), 0) || stop < 0 || stop > nyquist || (i > 0 && stop <= previous) {
			return nil, fmt.Errorf("%w at index %d: %g Hz (Nyquist %g Hz)", ErrInvalidStopFrequency, i, stop, nyquist)
		}
		previous = stop
	}
	if stopFrequenciesHz[len(stopFrequenciesHz)-1] != nyquist {
		return nil, fmt.Errorf("%w: got %g Hz, want %g Hz", ErrMissingNyquistStop, stopFrequenciesHz[len(stopFrequenciesHz)-1], nyquist)
	}

	a := &Analyzer{sampleRate: sampleRate, bandCount: len(stopFrequenciesHz)}
	copy(a.stopFrequenciesHz[:], stopFrequenciesHz)

	for k := range a.binBand {
		frequency := float64(k) * float64(sampleRate) / WindowSize
		band := 0
		for band < len(stopFrequenciesHz)-1 && frequency >= float64(stopFrequenciesHz[band]) {
			band++
		}
		a.binBand[k] = uint8(band)
	}

	// A periodic Hann is the DFT-periodic form: its period is exactly one frame.
	for n := range a.window {
		w := 0.5 * (1 - math.Cos(2*math.Pi*float64(n)/WindowSize))
		a.window[n] = w
		a.windowPower += w * w
	}
	for k := range a.twiddleReal {
		angle := -2 * math.Pi * float64(k) / WindowSize
		a.twiddleReal[k] = math.Cos(angle)
		a.twiddleImag[k] = math.Sin(angle)
	}
	return a, nil
}

// Write consumes PCM without modifying it. Incomplete samples remain buffered
// until a full frame is available. If one call completes multiple frames, all
// are analyzed in order and the latest spectrum is returned; Sequence advances
// for every completed frame. The returned value is false only when the call
// did not complete any frame.
func (a *Analyzer) Write(pcm []int16) (Spectrum, bool) {
	if a == nil {
		return Spectrum{}, false
	}
	var latest Spectrum
	completed := false
	for _, sample := range pcm {
		a.frame[a.frameUsed] = sample
		a.frameUsed++
		if a.frameUsed == WindowSize {
			latest = a.analyzeFrame()
			a.frameUsed = 0
			completed = true
		}
	}
	return latest, completed
}

// Reset discards partial-frame and sequence history while preserving the
// configured sample rate, bands, window, and FFT tables.
func (a *Analyzer) Reset() {
	if a == nil {
		return
	}
	a.frame = [WindowSize]int16{}
	a.frameUsed = 0
	a.real = [WindowSize]float64{}
	a.imag = [WindowSize]float64{}
	a.sequence = 0
}

func (a *Analyzer) analyzeFrame() Spectrum {
	for n, sample := range a.frame {
		a.real[n] = float64(sample) / 32768 * a.window[n]
		a.imag[n] = 0
	}
	a.fft()

	var bandPower [MaxBands]float64
	for k := range a.binBand {
		magnitudeSquared := a.real[k]*a.real[k] + a.imag[k]*a.imag[k]
		oneSidedWeight := 2.0
		if k == 0 || k == WindowSize/2 {
			oneSidedWeight = 1
		}
		bandPower[a.binBand[k]] += oneSidedWeight * magnitudeSquared
	}

	a.sequence++
	out := Spectrum{
		SampleRate:        a.sampleRate,
		Samples:           WindowSize,
		Sequence:          a.sequence,
		Count:             a.bandCount,
		StopFrequenciesHz: a.stopFrequenciesHz,
	}
	denominator := float64(WindowSize) * a.windowPower
	for band := 0; band < a.bandCount; band++ {
		out.RMS[band] = float32(math.Sqrt(bandPower[band] / denominator))
	}
	return out
}

// fft performs an unscaled, in-place iterative radix-2 forward FFT.
func (a *Analyzer) fft() {
	j := 0
	for i := 1; i < WindowSize; i++ {
		bit := WindowSize >> 1
		for j&bit != 0 {
			j ^= bit
			bit >>= 1
		}
		j ^= bit
		if i < j {
			a.real[i], a.real[j] = a.real[j], a.real[i]
			a.imag[i], a.imag[j] = a.imag[j], a.imag[i]
		}
	}

	for size := 2; size <= WindowSize; size <<= 1 {
		half := size >> 1
		stride := WindowSize / size
		for start := 0; start < WindowSize; start += size {
			for offset := 0; offset < half; offset++ {
				twiddle := offset * stride
				upper := start + offset
				lower := upper + half
				productReal := a.real[lower]*a.twiddleReal[twiddle] - a.imag[lower]*a.twiddleImag[twiddle]
				productImag := a.real[lower]*a.twiddleImag[twiddle] + a.imag[lower]*a.twiddleReal[twiddle]
				upperReal, upperImag := a.real[upper], a.imag[upper]
				a.real[upper] = upperReal + productReal
				a.imag[upper] = upperImag + productImag
				a.real[lower] = upperReal - productReal
				a.imag[lower] = upperImag - productImag
			}
		}
	}
}
