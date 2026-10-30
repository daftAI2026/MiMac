// Package spectral computes linear frequency-band magnitudes for the Siri
// display meter. It is a pure Go adapter and does not open audio devices.
package spectral

import (
	"errors"
	"math"
)

const (
	MaxFrameLength = 4096
	MaxBands       = 64

	DefaultSmoothingPreviousWeight float32 = 0.5
	MaxSmoothingPreviousWeight     float32 = 0.99
)

var (
	ErrUnsupportedSampleRate = errors.New("siriglass spectral: sample rate must be 8000 or 16000 Hz")
	ErrInvalidFrameLength    = errors.New("siriglass spectral: frame length must be a power of two from 2 through 4096")
	ErrTooManyBands          = errors.New("siriglass spectral: band count must be from 1 through 64")
	ErrInvalidStops          = errors.New("siriglass spectral: band stops must be non-empty, finite, positive, and strictly increasing")
	ErrInvalidSmoothing      = errors.New("siriglass spectral: previous-output weight must be finite and in [0,0.99]")
	ErrInvalidBuffer         = errors.New("siriglass spectral: buffer length does not match configuration")
)

// Config is explicit because sample rate, frame length, and band stops belong
// to the active display-meter configuration. Samples are accepted at 8 kHz or
// 16 kHz; Process expects normalized float32 values and ProcessInt16 divides
// signed PCM by 32768. Stops above Nyquist are capped by the FFT-bin boundary.
type Config struct {
	FrameLength             int
	SampleRateHz            int
	StopsHz                 []float32
	SmoothingPreviousWeight float32
}

// Analyzer retains the most recent FFT magnitudes and the native-style raw
// and smoothed band vectors. Construct one analyzer per display-meter session.
type Analyzer struct {
	n              int
	bandCount      int
	previousWeight float32
	stopsHz        []float32
	bounds         []int
	window         []float32
	real           []float32
	imag           []float32
	twiddleReal    []float32
	twiddleImag    []float32
	magnitudes     []float32 // Packed bins 0 through N/2-1; bin zero is omitted.
	rawBands       []float32
	outBands       []float32
}

// New validates and copies the explicit transform configuration. It accepts
// only an already-effective power-of-two N; callers should pass the effective
// frame length. Band stops become exclusive boundaries ceil(stop/(Fs/N)),
// capped at N/2. The smoothing coefficient weights the previous output.
func New(config Config) (*Analyzer, error) {
	if config.SampleRateHz != 8000 && config.SampleRateHz != 16000 {
		return nil, ErrUnsupportedSampleRate
	}
	n := config.FrameLength
	if n < 2 || n > MaxFrameLength || n&(n-1) != 0 {
		return nil, ErrInvalidFrameLength
	}
	if len(config.StopsHz) == 0 {
		return nil, ErrInvalidStops
	}
	if len(config.StopsHz) > MaxBands {
		return nil, ErrTooManyBands
	}
	if !validSmoothingWeight(config.SmoothingPreviousWeight) {
		return nil, ErrInvalidSmoothing
	}
	previous := float32(0)
	for i, stop := range config.StopsHz {
		if math.IsNaN(float64(stop)) || math.IsInf(float64(stop), 0) || stop <= 0 || (i > 0 && stop <= previous) {
			return nil, ErrInvalidStops
		}
		previous = stop
	}

	a := &Analyzer{
		n:              n,
		bandCount:      len(config.StopsHz),
		previousWeight: config.SmoothingPreviousWeight,
		stopsHz:        append([]float32(nil), config.StopsHz...),
		bounds:         make([]int, len(config.StopsHz)),
		window:         make([]float32, n),
		real:           make([]float32, n),
		imag:           make([]float32, n),
		twiddleReal:    make([]float32, n/2),
		twiddleImag:    make([]float32, n/2),
		magnitudes:     make([]float32, n/2),
		rawBands:       make([]float32, len(config.StopsHz)),
		outBands:       make([]float32, len(config.StopsHz)),
	}

	binWidth := float64(config.SampleRateHz) / float64(n)
	for i, stop := range a.stopsHz {
		bound := n / 2
		// Cap in floating point before converting: a finite float32 stop can
		// still exceed the integer range once expressed in FFT bins.
		if bins := float64(stop) / binWidth; bins < float64(bound) {
			bound = int(math.Ceil(bins))
		}
		a.bounds[i] = bound
	}

	// Periodic Hamming window: 0.54 - 0.46*cos(2*pi*n/N).
	pi := float32(math.Pi)
	for i := range a.window {
		phase := (float32(2) * pi) * float32(i) / float32(n)
		a.window[i] = float32(0.54) - float32(0.46)*float32(math.Cos(float64(phase)))
	}
	for k := range a.twiddleReal {
		phase := -float32(2) * pi * float32(k) / float32(n)
		a.twiddleReal[k] = float32(math.Cos(float64(phase)))
		a.twiddleImag[k] = float32(math.Sin(float64(phase)))
	}
	return a, nil
}

func validSmoothingWeight(weight float32) bool {
	return !math.IsNaN(float64(weight)) && !math.IsInf(float64(weight), 0) &&
		weight >= 0 && weight <= MaxSmoothingPreviousWeight
}

func (a *Analyzer) FrameLength() int {
	if a == nil {
		return 0
	}
	return a.n
}

func (a *Analyzer) BandCount() int {
	if a == nil {
		return 0
	}
	return a.bandCount
}

// Process analyzes a normalized float32 mono buffer. Buffers shorter than N
// leave the previous magnitudes unchanged; longer buffers use only their last
// N samples. Input samples are not modified. Calls with a valid analyzer and
// buffer perform no heap allocation.
func (a *Analyzer) Process(samples []float32) error {
	if a == nil {
		return ErrInvalidBuffer
	}
	if len(samples) < a.n {
		return nil
	}
	samples = samples[len(samples)-a.n:]
	for i, sample := range samples {
		a.real[i] = sample * a.window[i]
		a.imag[i] = 0
	}
	a.fftAndMagnitudes()
	return nil
}

// ProcessInt16 analyzes signed mono PCM after the preserved sample/32768
// normalization. It has the same short-buffer and final-N behavior as Process.
func (a *Analyzer) ProcessInt16(samples []int16) error {
	if a == nil {
		return ErrInvalidBuffer
	}
	if len(samples) < a.n {
		return nil
	}
	samples = samples[len(samples)-a.n:]
	for i, sample := range samples {
		a.real[i] = (float32(sample) / 32768) * a.window[i]
		a.imag[i] = 0
	}
	a.fftAndMagnitudes()
	return nil
}

// Raw aggregates the latest bins into linear band sums without advancing the
// smoothed output. Callers may publish raw values and smooth them at their own
// read cadence. raw must have BandCount entries.
func (a *Analyzer) Raw(raw []float32) error {
	if a == nil || len(raw) != a.bandCount {
		return ErrInvalidBuffer
	}
	a.aggregate()
	copy(raw, a.rawBands)
	return nil
}

// Get aggregates current bins and advances smoothed output on every call:
// out=(1-s)*raw+s*previous. raw and smoothed must be separate caller-owned
// buffers, each with BandCount entries. Repeated calls without new PCM advance
// smoothing again, matching the native getter lifecycle.
func (a *Analyzer) Get(raw, smoothed []float32) error {
	if a == nil || len(raw) != a.bandCount || len(smoothed) != a.bandCount {
		return ErrInvalidBuffer
	}
	a.aggregate()
	currentWeight := float32(1) - a.previousWeight
	for i, current := range a.rawBands {
		a.outBands[i] = currentWeight*current + a.previousWeight*a.outBands[i]
	}
	copy(raw, a.rawBands)
	copy(smoothed, a.outBands)
	return nil
}

// Reset clears raw and smoothed band vectors while preserving FFT magnitudes.
// A subsequent Raw or Get reaggregates the preserved bins.
func (a *Analyzer) Reset() {
	if a == nil {
		return
	}
	clear(a.rawBands)
	clear(a.outBands)
}

// SmoothInto computes a caller-paced smoothing step using the same previous
// output weight as Analyzer.Get. Reusable output storage avoids per-read
// allocation. The inputs are read-only unless the caller aliases out with one.
func SmoothInto(current, previous, out []float32, previousWeight float32) error {
	if len(current) == 0 || len(current) != len(previous) || len(current) != len(out) {
		return ErrInvalidBuffer
	}
	if !validSmoothingWeight(previousWeight) {
		return ErrInvalidSmoothing
	}
	currentWeight := float32(1) - previousWeight
	for i := range current {
		out[i] = currentWeight*current[i] + previousWeight*previous[i]
	}
	return nil
}

func (a *Analyzer) fftAndMagnitudes() {
	a.fft()
	clear(a.magnitudes)
	// The real FFT's split packing yields ordinary positive-bin magnitudes
	// equivalent to 2*|DFT|/N after the native 1/N scale. Packed bin zero
	// contains the DC/Nyquist pair and is skipped by aggregation.
	scale := float32(2) / float32(a.n)
	for k := 1; k < a.n/2; k++ {
		re := a.real[k] * scale
		im := a.imag[k] * scale
		a.magnitudes[k] = float32(math.Sqrt(float64(re*re + im*im)))
	}
}

func (a *Analyzer) aggregate() {
	lower := 1
	for band, upper := range a.bounds {
		if upper < lower {
			upper = lower
		}
		var sum float32
		for k := lower; k < upper; k++ {
			sum += a.magnitudes[k]
		}
		a.rawBands[band] = sum
		lower = upper
	}
}

func (a *Analyzer) fft() {
	n := a.n
	j := 0
	for i := 1; i < n; i++ {
		bit := n >> 1
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
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		stride := n / size
		for start := 0; start < n; start += size {
			for offset := 0; offset < half; offset++ {
				w := offset * stride
				upper := start + offset
				lower := upper + half
				productReal := a.real[lower]*a.twiddleReal[w] - a.imag[lower]*a.twiddleImag[w]
				productImag := a.real[lower]*a.twiddleImag[w] + a.imag[lower]*a.twiddleReal[w]
				upperReal, upperImag := a.real[upper], a.imag[upper]
				a.real[upper] = upperReal + productReal
				a.imag[upper] = upperImag + productImag
				a.real[lower] = upperReal - productReal
				a.imag[lower] = upperImag - productImag
			}
		}
	}
}
