//go:build darwin && arm64

package siriglass

import (
	"encoding/json"
	"testing"
)

func TestTypedSpectrumSelectsNativeArrayBranch(t *testing.T) {
	meters := MetersFromSpectrum(Spectrum{SampleRate: 16000, Samples: 128, Sequence: 41, Count: 2, StopFrequenciesHz: [MaxMeterBands]float32{1000, 8000}, Magnitudes: [MaxMeterBands]float32{.125, .0625}})
	p := rendererMeterParameters(5, true, -36, meters)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		AudioMeters *audioMetersInput `json:"audioMeters"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.AudioMeters == nil || len(decoded.AudioMeters.NormalizedPowerAmplitudes) != 2 || decoded.AudioMeters.NormalizedPowerAmplitudes[1] != .3125 || decoded.AudioMeters.StopFrequenciesHz[1] != 8000 {
		t.Fatalf("native array branch missing from JSON: %s", data)
	}
	loud := Spectrum{SampleRate: 16000, Samples: 128, Sequence: 42, Count: 2, StopFrequenciesHz: [MaxMeterBands]float32{1000, 8000}, Magnitudes: [MaxMeterBands]float32{.25, .0625}}
	loudParameters := rendererMeterParameters(8, true, -36, MetersFromSpectrum(loud))
	loudData, err := json.Marshal(loudParameters)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(loudData, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.AudioMeters == nil || decoded.AudioMeters.NormalizedPowerAmplitudes[0] != 1.25 {
		t.Fatalf("native array branch changed original amplitude 1.25: %s", loudData)
	}
	quiet := rendererMeterParameters(6, true, -60, MetersFromSpectrum(Spectrum{}))
	if quiet.AudioMeters == nil {
		t.Fatal("silence switched to scalar branch")
	}
	hidden := rendererMeterParameters(7, false, -20, meters)
	if hidden.AudioMeters != nil || hidden.AudioPowerDB != initialDB {
		t.Fatal("hidden frame retained audio")
	}
}
