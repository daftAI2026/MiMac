package typeless

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

const (
	fixtureBundleShortVersion = "99.0.0"
	fixtureBundleBuildVersion = "99999"
)

func TestDiscoverReturnsCancelledStatusWithoutReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := Discover(ctx, voicetools.Locator{})
	if got.State != voicetools.ReadError || got.Source != "cancelled" || len(got.Actions) != 0 {
		t.Fatalf("cancelled discovery = %#v", got)
	}
}

func TestDiscoverFindsConfigMissingForFutureReleaseApp(t *testing.T) {
	requireDarwin(t)
	locator, appPath := typelessFixtureLocator(t, "now.typeless.desktop")

	got := Discover(context.Background(), locator)
	if got.State != voicetools.ConfigMissing {
		t.Fatalf("state = %q, want config_missing: %#v", got.State, got)
	}
	if got.AppPath != appPath || got.Version != fixtureBundleShortVersion+" ("+fixtureBundleBuildVersion+")" {
		t.Fatalf("app identity and diagnostic release = %#v", got)
	}
	if got.SettingsPath != filepath.Join(locator.Home, typelessSettingsRelative) || len(got.Actions) != 0 {
		t.Fatalf("missing settings result = %#v", got)
	}
}

func TestDiscoverReadsOnlyKnownActionsForFutureReleaseWithoutDefaults(t *testing.T) {
	requireDarwin(t)
	locator, appPath := typelessFixtureLocator(t, "now.typeless.desktop")
	settingsPath := filepath.Join(locator.Home, typelessSettingsRelative)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("{\"featureShortcutBindings\":{" +
		"\"dictationMode\":[\"Ctrl+K\",\"Fn\"]," +
		"\"askAnythingMode\":[\"Ctrl+Space\"]," +
		"\"translationMode\":[\"Shift+T\"]," +
		"\"pasteLastTranscript\":[\"Ctrl+Cmd+V\"]," +
		"\"privateFutureAction\":[\"LEAK_ME\"]}," +
		"\"apiKey\":\"LEAK_ME\",\"history\":[\"LEAK_ME\"]}")
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Ready || got.AppPath != appPath {
		t.Fatalf("discovery = %#v", got)
	}
	if len(got.Actions) != 3 {
		t.Fatalf("actions = %#v, want only three voice actions", got.Actions)
	}
	if got.Actions[0].ID != "dictationMode" || got.Actions[0].Gesture != "tap-to-start-stop" {
		t.Fatalf("Dictate action = %#v", got.Actions[0])
	}
	if len(got.Actions[0].Candidates) != 2 ||
		got.Actions[0].Candidates[0] != (voicetools.Candidate{Raw: "Ctrl+K", Shortcut: "Control+K", Supported: true}) ||
		got.Actions[0].Candidates[1] != (voicetools.Candidate{Raw: "Fn", Supported: false}) {
		t.Fatalf("Dictate candidates = %#v", got.Actions[0].Candidates)
	}
	if got.Actions[1].Gesture != "tap-to-start; end via main Typeless shortcut" ||
		got.Actions[2].Gesture != "tap-to-start; end via main Typeless shortcut" {
		t.Fatalf("start/end gestures = %#v", got.Actions[1:])
	}

	encoded := string(mustJSON(t, got))
	for _, ignored := range []string{"pasteLastTranscript", "privateFutureAction", "LEAK_ME", "apiKey", "history"} {
		if contains(encoded, ignored) {
			t.Fatalf("snapshot leaked or included ignored field %q: %s", ignored, encoded)
		}
	}
}

func TestDiscoverRejectsWrongBundleIdentity(t *testing.T) {
	requireDarwin(t)
	locator, _ := typelessFixtureLocator(t, "wrong.identifier")
	got := Discover(context.Background(), locator)
	if got.State != voicetools.Unsupported || len(got.Actions) != 0 {
		t.Fatalf("wrong bundle identity result = %#v", got)
	}
}

func typelessFixtureLocator(t *testing.T, identifier string) (voicetools.Locator, string) {
	t.Helper()
	home := t.TempDir()
	apps := filepath.Join(home, "Applications")
	appPath := filepath.Join(apps, "Typeless.app")
	infoDir := filepath.Join(appPath, "Contents")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" +
		"<plist version=\"1.0\"><dict>" +
		"<key>CFBundleIdentifier</key><string>" + identifier + "</string>" +
		"<key>CFBundleShortVersionString</key><string>" + fixtureBundleShortVersion + "</string>" +
		"<key>CFBundleVersion</key><string>" + fixtureBundleBuildVersion + "</string>" +
		"</dict></plist>"
	if err := os.WriteFile(filepath.Join(infoDir, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	return voicetools.Locator{Home: home, ApplicationDirs: []string{apps}}, appPath
}

func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("bundle inspection uses the macOS plutil executable")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func contains(value, fragment string) bool {
	return strings.Contains(value, fragment)
}
