//go:build darwin && arm64

package siriglass

import (
	"math"
	"testing"
)

func TestRendererParametersMapsSourceDBFSToNativeDisplayRange(t *testing.T) {
	tests := []struct {
		name             string
		sourceDBFS       float32
		wantNativeDB     float32
		wantNativeEffect float32
	}{
		{name: "below source floor clamps", sourceDBFS: -100, wantNativeDB: -40, wantNativeEffect: 0},
		{name: "source floor maps to native effect floor", sourceDBFS: -60, wantNativeDB: -40, wantNativeEffect: 0},
		{name: "low speech range", sourceDBFS: -50, wantNativeDB: -35, wantNativeEffect: 0.125},
		{name: "observed sustained speech peak", sourceDBFS: -43.617, wantNativeDB: -31.8085, wantNativeEffect: 0.2047875},
		{name: "mid range", sourceDBFS: -40, wantNativeDB: -30, wantNativeEffect: 0.25},
		{name: "native ceiling", sourceDBFS: -20, wantNativeDB: -20, wantNativeEffect: 0.5},
		{name: "source ceiling clamps", sourceDBFS: 0, wantNativeDB: -20, wantNativeEffect: 0.5},
	}

	var previousNativeDB float32 = -1000
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parameters := rendererParameters(7, true, tt.sourceDBFS)
			if !parameters.Visible {
				t.Fatal("visible renderer parameters marked hidden")
			}
			if !closeFloat32(parameters.AudioPowerDB, tt.wantNativeDB, 0.0001) {
				t.Errorf("native display dB = %v, want %v", parameters.AudioPowerDB, tt.wantNativeDB)
			}
			if parameters.AudioPowerDB < -40 || parameters.AudioPowerDB > -20 {
				t.Errorf("native display dB %v is outside [-40,-20]", parameters.AudioPowerDB)
			}
			if got := legacyScalarEffectForTest(parameters.AudioPowerDB); !closeFloat32(got, tt.wantNativeEffect, 0.0001) {
				t.Errorf("original scalar effect = %v, want %v", got, tt.wantNativeEffect)
			}
			if parameters.AudioPowerDB < previousNativeDB {
				t.Errorf("mapping is not monotonic: %v followed %v", parameters.AudioPowerDB, previousNativeDB)
			}
			previousNativeDB = parameters.AudioPowerDB
		})
	}
}

func TestRendererParametersHiddenIgnoresSourceLevel(t *testing.T) {
	for _, sourceDBFS := range []float32{-60, -43.617, -20, 0} {
		parameters := rendererParameters(8, false, sourceDBFS)
		if parameters.Visible {
			t.Errorf("source dBFS %v: hidden renderer parameters marked visible", sourceDBFS)
		}
		if parameters.AudioPowerDB != initialDB {
			t.Errorf("source dBFS %v: hidden native dB = %v, want initialDB %v", sourceDBFS, parameters.AudioPowerDB, initialDB)
		}
	}
}

func legacyScalarEffectForTest(nativeDisplayDB float32) float32 {
	if nativeDisplayDB < -40 {
		return 0
	}
	return float32((math.Min(float64(nativeDisplayDB), -20) + 40) / 40)
}

func closeFloat32(got, want, tolerance float32) bool {
	return math.Abs(float64(got-want)) <= float64(tolerance)
}
