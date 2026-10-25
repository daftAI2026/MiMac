package wetype

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

func TestDiscoverReturnsCancelledStatusWithoutReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := Discover(ctx, voicetools.Locator{})
	if got.State != voicetools.ReadError || got.Source != "cancelled" || len(got.Actions) != 0 {
		t.Fatalf("cancelled discovery = %#v", got)
	}
}

func TestDiscoverReadsAllowlistedFieldsWithoutExecutablePlan(t *testing.T) {
	requireDarwin(t)
	locator, appPath, settingsPath := wetypeDiscoveryFixture(t, []testEntry{
		{key: "voicePTTShortcut_keyCodes", value: mmkvStringValue("[63,49]")},
		{key: "voicePTTShortcut_modifiers", value: mmkvInt32Value(7)},
		{key: "voicePTTShortcut_tapCount", value: mmkvInt32Value(1)},
		{key: "voiceToggleShortcut_keyCodes", value: mmkvStringValue("[70]")},
		{key: "unrelated.private.setting", value: []byte("PRIVATE_SENTINEL")},
	})

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Ready || got.AppPath != appPath || got.Version != "3.0.0 (670)" || got.SettingsPath != settingsPath {
		t.Fatalf("discovery metadata = %#v", got)
	}
	if len(got.Actions) != 2 {
		t.Fatalf("actions = %#v, want both known WeType shortcut groups", got.Actions)
	}
	if got.Actions[0].Name != "按住说话" || got.Actions[1].Name != "切换听写" {
		t.Fatalf("WeType action labels = %#v", got.Actions)
	}
	for _, action := range got.Actions {
		if action.Scope != "unknown" || action.Gesture != "unknown" || action.Control != nil || len(action.Candidates) != 1 {
			t.Fatalf("action claims unverified behavior: %#v", action)
		}
		candidate := action.Candidates[0]
		if candidate.Supported || candidate.Shortcut != "" {
			t.Fatalf("raw shortcut became executable: %#v", candidate)
		}
		if _, err := voicetools.ResolveAction(voicetools.ActionRef{
			ToolID: "wetype", ActionID: action.ID, CandidateRaw: candidate.Raw,
		}, 1, got); err == nil {
			t.Fatalf("unverified action %q resolved to a key plan", action.ID)
		}
	}
	if !strings.Contains(got.Actions[0].Candidates[0].Raw, `"voicePTTShortcut_keyCodes"`) ||
		!strings.Contains(got.Actions[0].Candidates[0].Raw, `"value":[63,49]`) {
		t.Fatalf("PTT raw candidate lost allowlisted values: %#v", got.Actions[0].Candidates[0])
	}
	if strings.Contains(got.Actions[0].Candidates[0].Raw, "PRIVATE_SENTINEL") {
		t.Fatal("non-allowlisted MMKV data escaped into candidate")
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("snapshot is not serializable: %v", err)
	}
}

func TestDiscoverReportsMissingBundleStoreFieldsAndConfiguration(t *testing.T) {
	requireDarwin(t)
	t.Run("bundle not found", func(t *testing.T) {
		home := t.TempDir()
		got := Discover(context.Background(), voicetools.Locator{
			Home: home, InputMethodDirs: []string{filepath.Join(home, "Applications")},
		})
		if got.State != voicetools.NotFound || len(got.Actions) != 0 {
			t.Fatalf("not found result = %#v", got)
		}
	})
	t.Run("all MMKV fields missing", func(t *testing.T) {
		locator, appPath, settingsPath := wetypeDiscoveryFixture(t, nil)
		got := Discover(context.Background(), locator)
		if got.State != voicetools.ConfigMissing || got.AppPath != appPath || got.SettingsPath != settingsPath || len(got.Actions) != 0 {
			t.Fatalf("empty store result = %#v", got)
		}
	})
	t.Run("MMKV store absent", func(t *testing.T) {
		locator, appPath, settingsPath := wetypeDiscoveryFixture(t, nil)
		if err := os.Remove(settingsPath); err != nil {
			t.Fatal(err)
		}
		got := Discover(context.Background(), locator)
		if got.State != voicetools.ConfigMissing || got.AppPath != appPath || got.SettingsPath != settingsPath {
			t.Fatalf("missing store result = %#v", got)
		}
	})
}

func TestDiscoverMapsUnverifiedVersionAndReadErrors(t *testing.T) {
	requireDarwin(t)
	t.Run("unverified bundle version", func(t *testing.T) {
		locator, appPath, _ := wetypeDiscoveryFixture(t, nil)
		infoPath := filepath.Join(appPath, "Contents", "Info.plist")
		writeBundleInfo(t, infoPath, bundleIdentifier, verifiedShortVersion, "future-build")
		got := Discover(context.Background(), locator)
		if got.State != voicetools.Unsupported || got.AppPath != appPath || got.Version != "3.0.0 (future-build)" {
			t.Fatalf("unverified bundle result = %#v", got)
		}
	})
	t.Run("integrity failure", func(t *testing.T) {
		locator, _, settingsPath := wetypeDiscoveryFixture(t, []testEntry{{
			key: "voicePTTShortcut_keyCodes", value: mmkvStringValue("[63]"),
		}})
		metadata, err := os.ReadFile(settingsPath + ".crc")
		if err != nil {
			t.Fatal(err)
		}
		metadata[0] ^= 0xff
		if err := os.WriteFile(settingsPath+".crc", metadata, 0o600); err != nil {
			t.Fatal(err)
		}
		got := Discover(context.Background(), locator)
		if got.State != voicetools.ReadError || len(got.Actions) != 0 {
			t.Fatalf("integrity error result = %#v", got)
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
		{name: "bundle missing", err: ErrBundleUnavailable, state: voicetools.NotFound, want: voicetools.NotFound},
		{name: "wrong bundle", err: ErrBundleUnavailable, state: voicetools.Unsupported, want: voicetools.Unsupported},
		{name: "bundle read error", err: ErrBundleUnavailable, state: voicetools.ReadError, want: voicetools.ReadError},
		{name: "ambiguous", err: ErrAmbiguousBundle, state: voicetools.Ambiguous, want: voicetools.Ambiguous},
		{name: "config missing", err: voicetools.ErrFileMissing, state: voicetools.Ready, want: voicetools.ConfigMissing},
		{name: "unsupported format", err: ErrUnsupportedFormat, state: voicetools.Ready, want: voicetools.Unsupported},
		{name: "integrity read failure", err: ErrIntegrity, state: voicetools.Ready, want: voicetools.ReadError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := discoveryErrorState(test.err, test.state); got != test.want {
				t.Fatalf("state = %q, want %q", got, test.want)
			}
		})
	}
}

func wetypeDiscoveryFixture(t *testing.T, entries []testEntry) (voicetools.Locator, string, string) {
	t.Helper()
	home := t.TempDir()
	applications := filepath.Join(home, "Applications")
	appPath := filepath.Join(applications, bundleName)
	infoPath := filepath.Join(appPath, "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeBundleInfo(t, infoPath, bundleIdentifier, verifiedShortVersion, verifiedBuildVersion)
	settingsPath := filepath.Join(home, "Library", "Application Support", storeDirectory, storeName)
	metadata, data := makeStoreFixture(entries)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath+".crc", metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	return voicetools.Locator{Home: home, InputMethodDirs: []string{applications}}, appPath, settingsPath
}

func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("bundle inspection uses macOS plutil")
	}
}
