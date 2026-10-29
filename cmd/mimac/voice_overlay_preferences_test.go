package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func overlayPreferenceJSON(t *testing.T, value hostPreferences) bool {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	enabled, exists := fields["show_voice_overlay"].(bool)
	if !exists {
		t.Fatal("voice overlay preference is missing from saved settings")
	}
	return enabled
}

func TestVoiceOverlayPreferencesDefaultAndLegacy(t *testing.T) {
	if !overlayPreferenceJSON(t, defaultHostPreferences()) {
		t.Fatal("new settings should show voice state")
	}
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"show_in_dock":true,"appearance":"system"}`), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := readHostPreferences(path)
	if err != nil {
		t.Fatal(err)
	}
	if !value.ShowInDock || !overlayPreferenceJSON(t, value) {
		t.Fatal("legacy preference did not preserve Dock and default voice state")
	}
}

func TestVoiceOverlayPreferenceExplicitOffRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"show_in_dock":false,"show_voice_overlay":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := readHostPreferences(path)
	if err != nil {
		t.Fatal(err)
	}
	if overlayPreferenceJSON(t, value) {
		t.Fatal("explicit off was replaced by default")
	}
	if err := saveHostPreferences(path, value); err != nil {
		t.Fatal(err)
	}
	again, err := readHostPreferences(path)
	if err != nil || overlayPreferenceJSON(t, again) {
		t.Fatalf("off did not round-trip: %v", err)
	}
}
