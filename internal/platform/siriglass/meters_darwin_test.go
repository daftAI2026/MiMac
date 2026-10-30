//go:build darwin && arm64

package siriglass

import (
	"encoding/json"
	"testing"

	"mimac.local/mimac/internal/audiometer"
)

func TestMeasuredSpectrumSelectsNativeArrayBranch(t *testing.T) {
	meters := MetersFromSpectrum(audiometer.Spectrum{Count: 2, StopFrequenciesHz: [audiometer.MaxBands]float32{1000, 8000}, RMS: [audiometer.MaxBands]float32{.05, .3}})
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
	if decoded.AudioMeters == nil || len(decoded.AudioMeters.NormalizedPowerAmplitudes) != 2 || decoded.AudioMeters.NormalizedPowerAmplitudes[1] != .3 || decoded.AudioMeters.StopFrequenciesHz[1] != 8000 {
		t.Fatalf("native array branch missing from JSON: %s", data)
	}
	quiet := rendererMeterParameters(6, true, -60, MetersFromSpectrum(audiometer.Spectrum{}))
	if quiet.AudioMeters == nil {
		t.Fatal("silence switched to scalar branch")
	}
	hidden := rendererMeterParameters(7, false, -20, meters)
	if hidden.AudioMeters != nil || hidden.AudioPowerDB != initialDB {
		t.Fatal("hidden frame retained audio")
	}
}
