//go:build darwin

package chatgpt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestDiscoverUsesVerifiedDefaultsWhenKeymapIsMissing(t *testing.T) {
	locator, appPath := chatGPTFixtureLocator(t, bundleIdentifier)

	got := discoverFixture(context.Background(), locator)
	if got.State != voicetools.Ready {
		t.Fatalf("state = %q, want ready: %#v", got.State, got)
	}
	if got.AppPath != appPath || got.Version != "26.1007.71326 (20398)" {
		t.Fatalf("verified app metadata = %#v", got)
	}
	if got.SettingsPath != filepath.Join(filepath.Join(locator.Home, ".codex"), "keybindings.json") || len(got.Actions) != 2 {
		t.Fatalf("missing keymap result = %#v", got)
	}
	if len(got.Source) < len("defaults:") || got.Source[:len("defaults:")] != "defaults:" {
		t.Fatalf("source = %q, want explicit defaults prefix", got.Source)
	}
	voice := got.Actions[0]
	if voice.ID != "composer.startVoiceMode" || len(voice.Candidates) != 1 ||
		voice.Candidates[0] != (voicetools.Candidate{Raw: "Ctrl+Shift+V", Shortcut: "Control+Shift+V", Supported: false}) {
		t.Fatalf("default ChatGPT voice action = %#v", voice)
	}
	if got.Actions[1].ID != "realtimeVoice.endCall" || len(got.Actions[1].Candidates) != 0 {
		t.Fatalf("end action should have no default shortcut: %#v", got.Actions[1])
	}
}

func TestDiscoverReadsAllowlistedKeymapRowsOnly(t *testing.T) {
	locator, appPath := chatGPTFixtureLocator(t, bundleIdentifier)
	if err := os.MkdirAll(filepath.Join(locator.Home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("[" +
		"{\"command\":\"composer.startVoiceMode\",\"key\":\"Ctrl+K\"}," +
		"{\"command\":\"composer.startVoiceMode\",\"key\":\"Control+Shift+V\"}," +
		"{\"command\":\"realtimeVoice.endCall\",\"key\":\"Cmd+E\"}," +
		"{\"command\":\"realtimeVoice\",\"key\":\"Ctrl+G\",\"private\":\"DROP_ME\"}," +
		"{\"command\":\"globalDictationHold\",\"key\":\"Fn\"}," +
		"{\"command\":\"unrelatedCommand\",\"key\":\"DROP_ME\",\"token\":\"DROP_ME\"}]")
	if err := os.WriteFile(filepath.Join(filepath.Join(locator.Home, ".codex"), "keybindings.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	got := discoverFixture(context.Background(), locator)
	if got.State != voicetools.Ready || got.AppPath != appPath || len(got.Actions) != 2 {
		t.Fatalf("discovery = %#v", got)
	}
	if got.Actions[0].ID != "composer.startVoiceMode" || got.Actions[0].Scope != "app" || got.Actions[0].Gesture != "toggle" || len(got.Actions[0].Candidates) != 2 ||
		got.Actions[0].Candidates[0] != (voicetools.Candidate{Raw: "Ctrl+K", Shortcut: "Control+K", Supported: false}) ||
		got.Actions[0].Candidates[1] != (voicetools.Candidate{Raw: "Control+Shift+V", Shortcut: "Control+Shift+V", Supported: false}) {
		t.Fatalf("ChatGPT voice action = %#v", got.Actions[0])
	}
	if got.Actions[1].ID != "realtimeVoice.endCall" || got.Actions[1].Scope != "app" || got.Actions[1].Gesture != "end" ||
		len(got.Actions[1].Candidates) != 1 || got.Actions[1].Candidates[0] != (voicetools.Candidate{Raw: "Cmd+E", Shortcut: "Command+E", Supported: false}) {
		t.Fatalf("ChatGPT end action = %#v", got.Actions[1])
	}
	if len(got.Source) >= len("defaults:") && got.Source[:len("defaults:")] == "defaults:" {
		t.Fatalf("explicit voice override should not report default source: %q", got.Source)
	}
}

func TestDiscoverDoesNotUseDefaultsForMalformedKeymap(t *testing.T) {
	locator, _ := chatGPTFixtureLocator(t, bundleIdentifier)
	if err := os.MkdirAll(filepath.Join(locator.Home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Join(locator.Home, ".codex"), "keybindings.json"), []byte(`[{"command":"composer.startVoiceMode","key":42}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := discoverFixture(context.Background(), locator)
	if got.State != voicetools.Unsupported || len(got.Actions) != 0 || got.Source == "" {
		t.Fatalf("malformed keymap result = %#v, want unsupported without defaults", got)
	}
}

func TestDiscoverRejectsWrongBundleIdentity(t *testing.T) {
	locator, _ := chatGPTFixtureLocator(t, "wrong.identifier")
	got := discoverFixture(context.Background(), locator)
	if got.State != voicetools.Unsupported || len(got.Actions) != 0 {
		t.Fatalf("wrong bundle identity result = %#v", got)
	}
}

func TestDiscoverFindsChatGPTInUserApplications(t *testing.T) {
	locator, legacyPath := chatGPTFixtureLocator(t, bundleIdentifier)
	appPath := filepath.Join(locator.Home, "Applications", "ChatGPT.app")
	if err := os.Rename(legacyPath, appPath); err != nil {
		t.Fatal(err)
	}
	got := discoverFixture(context.Background(), locator)
	if got.State != voicetools.Ready || got.AppPath != appPath {
		t.Fatalf("missed ChatGPT in user Applications: %+v", got)
	}
}

func chatGPTFixtureLocator(t *testing.T, identifier string) (voicetools.Locator, string) {
	t.Helper()
	home := t.TempDir()
	apps := filepath.Join(home, "Applications")
	appPath := filepath.Join(apps, "Codex.app")
	infoDir := filepath.Join(appPath, "Contents")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" +
		"<plist version=\"1.0\"><dict>" +
		"<key>CFBundleIdentifier</key><string>" + identifier + "</string>" +
		"<key>CFBundleShortVersionString</key><string>26.1007.71326</string>" +
		"<key>CFBundleVersion</key><string>20398</string>" +
		"</dict></plist>"
	if err := os.WriteFile(filepath.Join(infoDir, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	return voicetools.Locator{
		Home:            home,
		ApplicationDirs: []string{apps},
	}, appPath
}

func discoverFixture(ctx context.Context, locator voicetools.Locator) voicetools.Snapshot {
	return discover(ctx, locator, func(context.Context, string) (string, voicetools.State, error) {
		return filepath.Join(locator.Home, ".codex"), voicetools.Ready, nil
	})
}

func TestDiscoverRequiresResolvedRunningTarget(t *testing.T) {
	for _, test := range []struct {
		name, path string
		state      voicetools.State
		err        error
		want       voicetools.State
	}{
		{"stopped", "", voicetools.NotRunning, nil, voicetools.NotRunning},
		{"multiple", "", voicetools.Ambiguous, nil, voicetools.Ambiguous},
		{"permission", "", voicetools.ReadError, errors.New("unavailable"), voicetools.ReadError},
		{"unsupported", "", voicetools.Unsupported, errors.New("unsupported"), voicetools.Unsupported},
		{"relative", "relative", voicetools.Ready, nil, voicetools.Unsupported},
		{"empty", "", voicetools.Ready, nil, voicetools.Unsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator, appPath := chatGPTFixtureLocator(t, bundleIdentifier)
			got := discover(context.Background(), locator, func(_ context.Context, path string) (string, voicetools.State, error) {
				if path != appPath {
					t.Fatal("resolver did not receive verified bundle")
				}
				return test.path, test.state, test.err
			})
			if got.State != test.want || got.SettingsPath != "" || len(got.Actions) != 0 {
				t.Fatalf("unresolved target produced actions: %+v", got)
			}
		})
	}
}

func TestDiscoverUsesTargetDirectoryInsteadOfHostEnvironment(t *testing.T) {
	locator, _ := chatGPTFixtureLocator(t, bundleIdentifier)
	host, target := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", host)
	for path, shortcut := range map[string]string{host: "Ctrl+H", target: "Ctrl+T"} {
		data := []byte(`[{"command":"composer.startVoiceMode","key":"` + shortcut + `"}]`)
		if err := os.WriteFile(filepath.Join(path, "keybindings.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := discover(context.Background(), locator, func(context.Context, string) (string, voicetools.State, error) {
		return target, voicetools.Ready, nil
	})
	if got.State != voicetools.Ready || got.SettingsPath != filepath.Join(target, "keybindings.json") ||
		len(got.Actions) != 2 || len(got.Actions[0].Candidates) != 1 || got.Actions[0].Candidates[0].Shortcut != "Control+T" {
		t.Fatalf("did not use target path: %+v", got)
	}
}
