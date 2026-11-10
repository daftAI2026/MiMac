package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlanInitialSettingsKeepsDiagnosticsSeparateFromFirstRun(t *testing.T) {
	for _, tc := range []struct {
		name          string
		showSpecified bool
		showRequested bool
		firstRun      bool
		wantOpen      bool
		wantFirstRun  bool
	}{
		{name: "ordinary first run", firstRun: true, wantOpen: true, wantFirstRun: true},
		{name: "ordinary with saved configuration", firstRun: false},
		{name: "explicit false suppresses first run", showSpecified: true, firstRun: true},
		{name: "explicit true shows diagnostic window", showSpecified: true, showRequested: true, firstRun: true, wantOpen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := planInitialSettings(tc.showSpecified, tc.showRequested, tc.firstRun)
			if got.open != tc.wantOpen || got.firstRun != tc.wantFirstRun {
				t.Fatalf("plan = %+v, want open=%t firstRun=%t", got, tc.wantOpen, tc.wantFirstRun)
			}
		})
	}
}

func TestFirstRunSettingsPageUsesExistingDevicePage(t *testing.T) {
	if got := firstRunSettingsPage(initialSettingsPlan{open: true, firstRun: true}); got != pageDevice {
		t.Fatalf("first-run page = %q, want %q", got, pageDevice)
	}
	if got := firstRunSettingsPage(initialSettingsPlan{open: true}); got != "" {
		t.Fatalf("diagnostic page override = %q, want default page", got)
	}
}

func TestFirstRunSettingsRequiredRequiresNoSavedHostToolMappingOrAudioConfig(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "preferences.json"),
		filepath.Join(root, "tools.json"),
		filepath.Join(root, "mappings.json"),
		filepath.Join(root, "audio-backend.json"),
	}
	value := defaultHostPreferences()
	if !firstRunSettingsRequired(value, false, paths...) {
		t.Fatal("missing configuration should request the initial settings window")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			if firstRunSettingsRequired(value, false, paths...) {
				t.Fatal("existing user configuration should suppress automatic settings")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFirstRunSettingsRequiredHonorsMarkerAndUnreadablePreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	value := defaultHostPreferences()
	value.FirstRunSettingsShown = true
	if firstRunSettingsRequired(value, false) {
		t.Fatal("a shown first-run settings window should not appear again")
	}
	value.FirstRunSettingsShown = false
	if firstRunSettingsRequired(value, true) {
		t.Fatal("a failed preference load should not trigger an automatic write or window")
	}
	if firstRunSettingsRequired(value, false, path) {
		t.Fatal("existing host preferences should suppress automatic settings")
	}
}
