package siriglass

import "mimac.local/mimac/internal/platform/siriglass/spectral"

// This is the recovered Campo/DesignLibrary meter profile. It is separate
// from device PCM settings and from the presentation scale in meters.go.
const SpectrumWindowSize = 128

func SpectrumStopFrequencies() [6]float32 {
	return [6]float32{200, 500, 1200, 2400, 4800, 8000}
}

func NewSpectrumAnalyzer(sampleRate int) (*spectral.Analyzer, error) {
	stops := SpectrumStopFrequencies()
	return spectral.New(spectral.Config{FrameLength: SpectrumWindowSize, SampleRateHz: sampleRate, StopsHz: stops[:], SmoothingPreviousWeight: spectral.DefaultSmoothingPreviousWeight})
}
