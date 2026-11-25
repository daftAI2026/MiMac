package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/applespeech"
	"mimac.local/mimac/internal/voicetools"
)

func prepareAppleSpeechSettingsTest(t *testing.T, locale string) (string, string) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "userdata")
	resourcesDir := filepath.Join(t.TempDir(), "resources")
	mygo.App.SetPath(mygo.PathUserData, dataDir)
	mygo.App.SetPath(mygo.PathResources, resourcesDir)
	path := filepath.Join(dataDir, appleSpeechPreferencesFile)
	if err := saveAppleSpeechPreferences(path, appleSpeechPreferences{Version: 1, Locale: locale}); err != nil {
		t.Fatal(err)
	}
	appleSpeechPreferenceCache.Delete(path)
	library := filepath.Join(resourcesDir, "libMiMacSpeech.dylib")
	appleSpeechLocaleCache.Store(library, appleSpeechLocaleCacheEntry{locales: []applespeech.Locale{
		{Identifier: "en-US", DisplayName: "English (United States)", Installed: true},
		{Identifier: "de-DE", DisplayName: "German (Germany)", Installed: false},
	}})
	t.Cleanup(func() {
		mygo.App.SetPath(mygo.PathUserData, "")
		mygo.App.SetPath(mygo.PathResources, "")
		appleSpeechPreferenceCache.Delete(path)
		appleSpeechLocaleCache.Delete(library)
	})
	return path, library
}

func TestAppleSpeechPreferencesDefaultAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), appleSpeechPreferencesFile)
	value, err := readAppleSpeechPreferences(path)
	if err != nil {
		t.Fatal(err)
	}
	if value != (appleSpeechPreferences{Version: 1, Locale: defaultAppleSpeechLocale}) {
		t.Fatalf("missing preferences = %+v", value)
	}

	want := appleSpeechPreferences{Version: 1, Locale: "en-US"}
	if err := saveAppleSpeechPreferences(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readAppleSpeechPreferences(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("saved preferences = %+v, want %+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("preferences mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestAppleSpeechPreferencesRejectMalformedWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), appleSpeechPreferencesFile)
	original := []byte("{\"version\":1,\"locale\":\"en-US\",\"other\":true}")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAppleSpeechPreferences(path); err == nil {
		t.Fatal("unknown preference field was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("invalid preferences were rewritten")
	}
}

func TestSetAppleSpeechLocaleSameLocaleIsNoOpEvenDuringSessionTransition(t *testing.T) {
	path, _ := prepareAppleSpeechSettingsTest(t, "en-US")
	s, _ := newToolShortcutsTestShell(t, voicetools.Snapshot{})
	tool := &voiceTool{id: appleSpeechToolID, active: false, sequence: 7}
	s.tools = []*voiceTool{tool}
	s.audioSession = &audioSessionHost{}
	s.liveAudio.connection = &liveAudioConnection{}
	s.liveAudio.toolID = appleSpeechToolID
	s.liveAudio.retiring = true
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.setAppleSpeechLocale(tool, "en-US"); err != nil {
		t.Fatalf("reselecting current locale returned an error: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("reselecting current locale rewrote its preferences")
	}
	if tool.sequence != 7 || tool.busy {
		t.Fatalf("reselecting current locale refreshed Apple tool: sequence=%d busy=%t", tool.sequence, tool.busy)
	}
	if s.liveAudio.connection == nil || s.liveAudio.toolID != appleSpeechToolID || !s.liveAudio.retiring {
		t.Fatal("reselecting current locale changed the active Apple route transition")
	}
}

func TestSetAppleSpeechLocalePreservesNonAppleLiveRoute(t *testing.T) {
	path, _ := prepareAppleSpeechSettingsTest(t, "en-US")
	s, _ := newToolShortcutsTestShell(t, voicetools.Snapshot{})
	tool := &voiceTool{id: appleSpeechToolID, active: false, sequence: 11}
	s.tools = []*voiceTool{tool}
	s.audioSession = &audioSessionHost{}
	connection := &liveAudioConnection{}
	s.liveAudio.connection = connection
	s.liveAudio.toolID = "voice-doubao"
	s.liveAudio.retiring = false
	postMain := make(chan func(), 1)
	s.remoteState.postMain = func(fn func()) { postMain <- fn }
	if err := s.setAppleSpeechLocale(tool, "de-DE"); err != nil {
		t.Fatalf("selecting a supported locale: %v", err)
	}
	if got, err := readAppleSpeechPreferences(path); err != nil || got.Locale != "de-DE" {
		t.Fatalf("selected Apple locale preferences=%+v err=%v", got, err)
	}
	if s.liveAudio.connection != connection || s.liveAudio.toolID != "voice-doubao" || s.liveAudio.retiring {
		t.Fatal("changing Apple locale retired or replaced the non-Apple live route")
	}
	if tool.sequence != 11 || tool.busy {
		t.Fatalf("inactive Apple tool unexpectedly refreshed: sequence=%d busy=%t", tool.sequence, tool.busy)
	}
}
