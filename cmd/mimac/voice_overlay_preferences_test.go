package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
)

func readVoicePresentationJSON(t *testing.T, value hostPreferences) voicePresentationMode {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	mode, ok := fields["voice_presentation"].(string)
	if !ok {
		t.Fatalf("voice presentation preference is missing from saved settings: %s", data)
	}
	if _, exists := fields["show_voice_overlay"]; exists {
		t.Fatalf("saved settings retained the legacy boolean instead of one enum: %s", data)
	}
	return voicePresentationMode(mode)
}

func TestVoicePresentationPreferenceDefaultAndLegacyMigration(t *testing.T) {
	if got := readVoicePresentationJSON(t, defaultHostPreferences()); got != voicePresentationMiMacSphere {
		t.Fatalf("new settings presentation = %q, want MiMac sphere", got)
	}

	cases := []struct {
		name string
		json string
		want voicePresentationMode
	}{
		{name: "v1 missing value keeps original default", json: `{"version":1,"show_in_dock":true,"appearance":"system"}`, want: voicePresentationMiMacSphere},
		{name: "v1 explicit on keeps original display", json: `{"version":1,"show_in_dock":false,"show_voice_overlay":true}`, want: voicePresentationMiMacSphere},
		{name: "v1 explicit off follows voice tool", json: `{"version":1,"show_in_dock":true,"show_voice_overlay":false}`, want: voicePresentationFollowTool},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "preferences.json")
			original := []byte(tc.json)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := readHostPreferences(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != preferencesVersion || got.VoicePresentation != tc.want {
				t.Fatalf("migrated preference = %+v, want version %d mode %q", got, preferencesVersion, tc.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(original) {
				t.Fatalf("reading legacy settings wrote a migration: got %q, want %q", after, original)
			}
		})
	}
}

func TestVoicePresentationPreferenceThreeModeRoundTrip(t *testing.T) {
	modes := []voicePresentationMode{
		voicePresentationMiMacSphere,
		voicePresentationDotBar,
		voicePresentationFollowTool,
	}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "preferences.json")
			value := defaultHostPreferences()
			value.ShowInDock = true
			value.VoicePresentation = mode
			if err := saveHostPreferences(path, value); err != nil {
				t.Fatal(err)
			}
			if got := readVoicePresentationJSON(t, value); got != mode {
				t.Fatalf("serialized presentation = %q, want %q", got, mode)
			}
			got, err := readHostPreferences(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != value {
				t.Fatalf("round-trip preferences = %+v, want %+v", got, value)
			}
		})
	}
}

func TestLiveTextPreferenceDefaultsOffAndRemainsIndependent(t *testing.T) {
	if defaultHostPreferences().ShowLiveText {
		t.Fatal("live text should be opt-in")
	}
	for _, mode := range []voicePresentationMode{voicePresentationMiMacSphere, voicePresentationDotBar, voicePresentationFollowTool} {
		t.Run(string(mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "preferences.json")
			value := defaultHostPreferences()
			value.VoicePresentation = mode
			value.ShowLiveText = true
			if err := saveHostPreferences(path, value); err != nil {
				t.Fatal(err)
			}
			got, err := readHostPreferences(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != value {
				t.Fatalf("live text preference was coupled to presentation mode: got %+v, want %+v", got, value)
			}
		})
	}
}

func TestVoicePresentationNativeSelectionMapsThreeModesAndRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		mode  voicePresentationMode
		index int
	}{
		{mode: voicePresentationMiMacSphere, index: 0},
		{mode: voicePresentationDotBar, index: 1},
		{mode: voicePresentationFollowTool, index: 2},
	}
	for _, tc := range cases {
		index, ok := voicePresentationIndex(tc.mode)
		if !ok || index != tc.index {
			t.Fatalf("native index for %q = %d, %v; want %d, true", tc.mode, index, ok, tc.index)
		}
		mode, ok := voicePresentationModeForIndex(index)
		if !ok || mode != tc.mode {
			t.Fatalf("mode for native index %d = %q, %v; want %q, true", index, mode, ok, tc.mode)
		}
	}
	if _, ok := voicePresentationIndex("unknown"); ok {
		t.Fatal("unknown mode was accepted by the native selection mapping")
	}
	if _, ok := voicePresentationModeForIndex(3); ok {
		t.Fatal("out-of-range native selection index was accepted")
	}
}

func TestVoicePresentationPreferenceRejectsInvalidV2ModeWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	original := []byte(`{"version":2,"show_in_dock":false,"voice_presentation":"unknown","appearance":"system"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHostPreferences(path); err == nil {
		t.Fatal("readHostPreferences accepted an unknown voice presentation mode")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("invalid settings changed: got %q, want %q", after, original)
	}
}

func TestVoicePresentationPreferenceRejectsMixedV1AndV2Fields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	contents := []byte(`{"version":2,"show_in_dock":false,"voice_presentation":"mimac-sphere","show_voice_overlay":false}`)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHostPreferences(path); err == nil {
		t.Fatal("readHostPreferences accepted both new and legacy preference fields")
	}
}

func TestVoicePresentationPreferenceKeepsAppearanceAndDockWhileChangingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	want := hostPreferences{
		Version: preferencesVersion, ShowInDock: true,
		VoicePresentation: voicePresentationMiMacSphere, Appearance: mygo.ThemeDark, TextSize: 1,
	}
	if err := saveHostPreferences(path, want); err != nil {
		t.Fatal(err)
	}
	next := want
	next.VoicePresentation = voicePresentationDotBar
	if err := saveHostPreferences(path, next); err != nil {
		t.Fatal(err)
	}
	got, err := readHostPreferences(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != next {
		t.Fatalf("settings after presentation change = %+v, want %+v", got, next)
	}
}
