package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
)

func TestReadHostPreferencesMissingDefaultsToHiddenDockAndVoiceOverlay(t *testing.T) {
	got, err := readHostPreferences(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != preferencesVersion || got.ShowInDock || !got.ShowVoiceOverlay || got.Appearance != mygo.ThemeSystem {
		t.Fatalf("default preferences = %+v, want version %d, hidden Dock, voice overlay enabled, and system appearance", got, preferencesVersion)
	}
}

func TestSaveAndReadHostPreferencesUsesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MiMac", "preferences.json")
	want := hostPreferences{Version: preferencesVersion, ShowInDock: true, ShowVoiceOverlay: true, Appearance: mygo.ThemeDark}
	if err := saveHostPreferences(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readHostPreferences(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("read preferences = %+v, want %+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("preferences mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestReadVersionOnePreferencesWithoutAppearanceDefaultsToSystemWithoutMigrationWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	original := []byte("{\"version\":1,\"show_in_dock\":true}\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readHostPreferences(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != (hostPreferences{Version: preferencesVersion, ShowInDock: true, ShowVoiceOverlay: true, Appearance: mygo.ThemeSystem}) {
		t.Fatalf("read legacy preferences = %+v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("reading legacy preferences performed a migration write: got %q, want %q", after, original)
	}
}

func TestReadHostPreferencesRejectsUnknownAppearanceAndPreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	original := []byte("{\"version\":1,\"show_in_dock\":true,\"appearance\":\"sepia\"}\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHostPreferences(path); err == nil {
		t.Fatal("readHostPreferences accepted unknown appearance")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("invalid preferences changed: got %q, want %q", after, original)
	}
}

func TestReadHostPreferencesRejectsAndPreservesMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	original := []byte("{\"version\":99,\"show_in_dock\":true}\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHostPreferences(path); err == nil {
		t.Fatal("readHostPreferences accepted an unsupported version")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("malformed preferences changed: got %q, want %q", after, original)
	}
}

func TestSaveHostPreferencesRejectsUnknownVersionWithoutReplacingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	original := []byte("keep me\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveHostPreferences(path, hostPreferences{Version: preferencesVersion + 1, ShowInDock: true, ShowVoiceOverlay: true, Appearance: mygo.ThemeSystem}); err == nil {
		t.Fatal("saveHostPreferences accepted an unsupported version")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("existing preferences changed: got %q, want %q", after, original)
	}
}

func TestSaveHostPreferencesRenameFailureLeavesDestinationUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("keep me\n")
	sentinel := filepath.Join(path, "sentinel")
	if err := os.WriteFile(sentinel, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveHostPreferences(path, hostPreferences{Version: preferencesVersion, ShowInDock: true, ShowVoiceOverlay: true, Appearance: mygo.ThemeSystem}); err == nil {
		t.Fatal("saveHostPreferences replaced a directory")
	}
	after, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("existing destination changed: got %q, want %q", after, original)
	}
}

func TestApplyDockPreferenceDoesNotOverwriteUnreadablePreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	original := []byte("{broken json\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	s := shell{preferencesState: preferencesState{
		value:      defaultHostPreferences(),
		path:       path,
		loadFailed: true,
		errorText:  "设置读取失败，原文件已保留",
	}}
	if err := s.applyDockPreference(true); err == nil {
		t.Fatal("applyDockPreference accepted preferences after a failed load")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("unreadable preferences changed: got %q, want %q", after, original)
	}
}

func TestSavePreferencesRetainsIndependentSettingsAcrossChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	s := shell{preferencesState: preferencesState{value: defaultHostPreferences(), path: path}}
	first := s.preferencesState.value
	first.ShowInDock = true
	if err := s.savePreferences(first); err != nil {
		t.Fatal(err)
	}
	second := s.preferencesState.value
	second.Appearance = mygo.ThemeLight
	if err := s.savePreferences(second); err != nil {
		t.Fatal(err)
	}
	got, err := readHostPreferences(path)
	if err != nil {
		t.Fatal(err)
	}
	want := hostPreferences{Version: preferencesVersion, ShowInDock: true, ShowVoiceOverlay: true, Appearance: mygo.ThemeLight}
	if got != want || s.preferencesState.value != want {
		t.Fatalf("preferences after independent changes = %+v, in-memory=%+v, want %+v", got, s.preferencesState.value, want)
	}
}

func TestApplyAppearancePreferenceWriteFailureRetainsStoredValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("keep me\n")
	if err := os.WriteFile(filepath.Join(path, "sentinel"), original, 0600); err != nil {
		t.Fatal(err)
	}
	want := hostPreferences{Version: preferencesVersion, ShowInDock: true, ShowVoiceOverlay: true, Appearance: mygo.ThemeLight}
	s := shell{preferencesState: preferencesState{value: want, path: path}}
	if err := s.applyAppearancePreference(mygo.ThemeDark); err == nil {
		t.Fatal("applyAppearancePreference succeeded while destination is a directory")
	}
	if s.preferencesState.value != want {
		t.Fatalf("failed appearance save changed stored state: got %+v, want %+v", s.preferencesState.value, want)
	}
	if s.preferencesState.errorText != "保存失败，设置未更改" {
		t.Fatalf("user error text = %q", s.preferencesState.errorText)
	}
	after, err := os.ReadFile(filepath.Join(path, "sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("failed appearance save changed existing data: got %q, want %q", after, original)
	}
}

func TestApplyVoiceOverlayPreferenceWriteFailureRetainsStoredValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	want := defaultHostPreferences()
	want.ShowVoiceOverlay = true
	s := shell{preferencesState: preferencesState{value: want, path: path}}
	if err := s.applyVoiceOverlayPreference(false); err == nil {
		t.Fatal("applyVoiceOverlayPreference succeeded while destination is a directory")
	}
	if s.preferencesState.value != want {
		t.Fatalf("failed voice overlay save changed stored value: got %+v, want %+v", s.preferencesState.value, want)
	}
	if s.preferencesState.errorText != "保存失败，设置未更改" {
		t.Fatalf("user error text = %q", s.preferencesState.errorText)
	}
}

type fakeLoginItemController struct {
	enabled      bool
	setCalls     int
	setError     error
	ignoreChange bool
}

func (f *fakeLoginItemController) OpenAtLogin() bool { return f.enabled }

func (f *fakeLoginItemController) SetOpenAtLogin(enabled bool) error {
	f.setCalls++
	if f.setError != nil {
		return f.setError
	}
	if !f.ignoreChange {
		f.enabled = enabled
	}
	return nil
}

func TestSetLoginAtStartupUsesActualStateAndAvoidsDuplicateUpdates(t *testing.T) {
	controller := &fakeLoginItemController{}
	if err := setLoginAtStartup(controller, false); err != nil || controller.setCalls != 0 {
		t.Fatalf("unchanged login state: err=%v calls=%d", err, controller.setCalls)
	}
	if err := setLoginAtStartup(controller, true); err != nil || !controller.OpenAtLogin() || controller.setCalls != 1 {
		t.Fatalf("enabled login state: err=%v enabled=%v calls=%d", err, controller.enabled, controller.setCalls)
	}
}

func TestSetLoginAtStartupRequiresSuccessfulReadback(t *testing.T) {
	controller := &fakeLoginItemController{setError: errors.New("registration failed")}
	if err := setLoginAtStartup(controller, true); err == nil || controller.enabled || controller.setCalls != 1 {
		t.Fatalf("failed update: err=%v enabled=%v calls=%d", err, controller.enabled, controller.setCalls)
	}
	controller = &fakeLoginItemController{ignoreChange: true}
	if err := setLoginAtStartup(controller, true); err == nil || controller.enabled || controller.setCalls != 1 {
		t.Fatalf("unverified update: err=%v enabled=%v calls=%d", err, controller.enabled, controller.setCalls)
	}
}

func TestTextSizePreferencePersistsAndRejectsInvalidReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	want := defaultHostPreferences()
	want.TextSize = 1
	want.ShowInDock = true
	if err := saveHostPreferences(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readHostPreferences(path)
	if err != nil || got != want {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	invalid := want
	invalid.TextSize = 3
	if err := saveHostPreferences(path, invalid); err == nil {
		t.Fatal("accepted unsupported text size")
	}
	got, err = readHostPreferences(path)
	if err != nil || got != want {
		t.Fatalf("invalid replacement changed valid settings: %+v %v", got, err)
	}
}
