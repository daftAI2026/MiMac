package shandian

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestDiscoverReturnsCancelledStatusWithoutReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := Discover(ctx, voicetools.Locator{})
	if got.State != voicetools.ReadError || got.Source != "cancelled" || len(got.Actions) != 0 {
		t.Fatalf("cancelled discovery = %#v", got)
	}
}

func TestDiscoverReadsKnownActionsWithoutInferringPressTiming(t *testing.T) {
	requireDarwin(t)
	locator, configPath, appPath := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
	writeConfig(t, configPath, []byte(strings.ReplaceAll(validConfig,
		`"long_press_action":"VoiceAssistant"`,
		`"long_press_action":"VoiceAssistant","future_secret":"LEAK_ME"`,
	)))

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Ready || got.AppPath != appPath || got.Version != fixtureBundleShortVersion+" ("+fixtureBundleBuildVersion+")" || got.SettingsPath != configPath {
		t.Fatalf("discovery metadata = %#v", got)
	}
	if len(got.Actions) != 2 || got.Actions[0].ID != "short_press_action" ||
		got.Actions[0].Name != "语音输入" || got.Actions[1].ID != "long_press_action" ||
		got.Actions[1].Name != "语音助手" {
		t.Fatalf("configured actions = %#v", got.Actions)
	}
	for _, action := range got.Actions {
		if action.Scope != "unknown" || action.Gesture != "unknown" || action.Control != nil || len(action.Candidates) != 1 {
			t.Fatalf("action claims unverified behavior: %#v", action)
		}
		candidate := action.Candidates[0]
		wantRawAction := `"raw_action":"VoiceInput"`
		if action.ID == "long_press_action" {
			wantRawAction = `"raw_action":"VoiceAssistant"`
		}
		if candidate.Supported || candidate.Shortcut != "" ||
			!strings.Contains(candidate.Raw, wantRawAction) ||
			!strings.Contains(candidate.Raw, `"keys":["RCmd"]`) ||
			!strings.Contains(candidate.Raw, `"block_keys":true`) || strings.Contains(candidate.Raw, "LEAK_ME") {
			t.Fatalf("hotkey candidate = %#v", candidate)
		}
		if _, err := voicetools.ResolveAction(voicetools.ActionRef{
			ToolID: "shandian", ActionID: action.ID, CandidateRaw: candidate.Raw,
		}, 1, got); err == nil {
			t.Fatalf("unverified action %q resolved to a key plan", action.ID)
		}
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("snapshot is not serializable: %v", err)
	}
}

func TestDiscoverPreservesUnknownActionNames(t *testing.T) {
	requireDarwin(t)
	locator, configPath, _ := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
	unknownConfig := strings.ReplaceAll(validConfig, "VoiceInput", "FutureMode")
	unknownConfig = strings.ReplaceAll(unknownConfig, "VoiceAssistant", "NextFutureMode")
	writeConfig(t, configPath, []byte(unknownConfig))

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Ready || len(got.Actions) != 2 ||
		got.Actions[0].Name != "未识别的语音动作" || got.Actions[1].Name != "未识别的语音动作" {
		t.Fatalf("unknown action names were translated: %#v", got)
	}
	if !strings.Contains(got.Actions[0].Candidates[0].Raw, `"raw_action":"FutureMode"`) ||
		!strings.Contains(got.Actions[1].Candidates[0].Raw, `"raw_action":"NextFutureMode"`) {
		t.Fatalf("unknown raw action values were not retained for diagnostics: %#v", got.Actions)
	}
}

func TestDiscoverCandidateRawChangesWhenConfiguredActionChanges(t *testing.T) {
	requireDarwin(t)
	locator, configPath, _ := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
	writeConfig(t, configPath, []byte(validConfig))
	first := Discover(context.Background(), locator)
	firstRaw := first.Actions[0].Candidates[0].Raw

	changedConfig := strings.Replace(validConfig, "VoiceInput", "FutureVoiceInput", 1)
	writeConfig(t, configPath, []byte(changedConfig))
	second := Discover(context.Background(), locator)
	secondRaw := second.Actions[0].Candidates[0].Raw
	if first.State != voicetools.Ready || second.State != voicetools.Ready || firstRaw == secondRaw ||
		!strings.Contains(secondRaw, `"raw_action":"FutureVoiceInput"`) {
		t.Fatalf("candidate raw did not track action change: first=%q second=%q", firstRaw, secondRaw)
	}
}

func TestDiscoverOmitsCandidatesForExplicitlyEmptyHotkey(t *testing.T) {
	requireDarwin(t)
	locator, configPath, _ := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
	emptyHotkeyConfig := strings.Replace(validConfig, `"keys":["RCmd"]`, `"keys":[]`, 1)
	writeConfig(t, configPath, []byte(emptyHotkeyConfig))

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Ready || len(got.Actions) != 2 {
		t.Fatalf("empty hotkey result = %#v", got)
	}
	for _, action := range got.Actions {
		if len(action.Candidates) != 0 {
			t.Fatalf("empty key list became a binding candidate: %#v", action)
		}
	}
}

func TestDiscoverMapsMissingAndUnsupportedConfiguration(t *testing.T) {
	requireDarwin(t)
	t.Run("bundle not found", func(t *testing.T) {
		home := t.TempDir()
		got := Discover(context.Background(), voicetools.Locator{
			Home: home, ApplicationDirs: []string{filepath.Join(home, "Applications")},
		})
		if got.State != voicetools.NotFound || len(got.Actions) != 0 {
			t.Fatalf("not found result = %#v", got)
		}
	})
	t.Run("configuration file missing", func(t *testing.T) {
		locator, configPath, appPath := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
		got := Discover(context.Background(), locator)
		if got.State != voicetools.ConfigMissing || got.AppPath != appPath || got.SettingsPath != configPath {
			t.Fatalf("missing config result = %#v", got)
		}
	})
	t.Run("schema unverified", func(t *testing.T) {
		locator, configPath, _ := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
		writeConfig(t, configPath, []byte(strings.Replace(validConfig, `"config_version":3`, `"config_version":4`, 1)))
		got := Discover(context.Background(), locator)
		if got.State != voicetools.Unsupported || len(got.Actions) != 0 {
			t.Fatalf("unsupported schema result = %#v", got)
		}
	})
}

func TestDiscoverAcceptsFutureReleaseAndPreservesAmbiguousStates(t *testing.T) {
	requireDarwin(t)
	t.Run("future release with supported schema", func(t *testing.T) {
		locator, configPath, appPath := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
		writeConfig(t, configPath, []byte(validConfig))
		writeBundle(t, appPath, "0.8.1", "future-build")
		got := Discover(context.Background(), locator)
		if got.State != voicetools.Ready || got.AppPath != appPath || got.Version != "0.8.1 (future-build)" || got.SettingsPath != configPath || len(got.Actions) != 2 {
			t.Fatalf("future release with supported schema result = %#v", got)
		}
	})
	t.Run("ambiguous bundles", func(t *testing.T) {
		locator, _, _ := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
		secondDir := filepath.Join(locator.Home, "other-applications")
		writeBundle(t, filepath.Join(secondDir, bundleName), fixtureBundleShortVersion, fixtureBundleBuildVersion)
		locator.ApplicationDirs = append(locator.ApplicationDirs, secondDir)
		got := Discover(context.Background(), locator)
		if got.State != voicetools.Ambiguous || len(got.Actions) != 0 {
			t.Fatalf("ambiguous result = %#v", got)
		}
	})
}

func TestDiscoveryErrorStateMapsReaderErrors(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		state voicetools.State
		want  voicetools.State
	}{
		{name: "unsupported platform", err: ErrUnsupportedPlatform, want: voicetools.Unsupported},
		{name: "missing home", err: ErrHomeUnavailable, want: voicetools.ReadError},
		{name: "bundle missing", err: ErrBundleUnavailable, state: voicetools.NotFound, want: voicetools.NotFound},
		{name: "wrong bundle", err: ErrBundleUnavailable, state: voicetools.Unsupported, want: voicetools.Unsupported},
		{name: "bundle read error", err: ErrBundleUnavailable, state: voicetools.ReadError, want: voicetools.ReadError},
		{name: "ambiguous bundle", err: ErrAmbiguousBundle, state: voicetools.Ambiguous, want: voicetools.Ambiguous},
		{name: "config missing", err: ErrConfigMissing, want: voicetools.ConfigMissing},
		{name: "unverified schema", err: ErrUnsupportedSchema, want: voicetools.Unsupported},
		{name: "malformed config", err: ErrMalformedConfig, want: voicetools.Unsupported},
		{name: "changed config", err: ErrConfigChanged, want: voicetools.ReadError},
		{name: "read error", err: ErrConfigRead, want: voicetools.ReadError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := discoveryErrorState(test.err, test.state); got != test.want {
				t.Fatalf("state = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadInstalledUsesLocatorHomePathForDiscovery(t *testing.T) {
	requireDarwin(t)
	locator, configPath, _ := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
	writeConfig(t, configPath, []byte(validConfig))
	got := Discover(context.Background(), locator)
	if got.SettingsPath != filepath.Join(locator.Home, filepath.FromSlash(configRelativePath)) ||
		got.SettingsPath != configPath {
		t.Fatalf("discovery used a different home directory: %#v", got)
	}
	if _, err := os.Stat(got.SettingsPath); err != nil {
		t.Fatalf("discovered config path does not exist: %v", err)
	}
}
