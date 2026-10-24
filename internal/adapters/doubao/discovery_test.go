package doubao

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

func TestDiscoverReturnsCancelledBeforeFilesystemAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := Discover(ctx, voicetools.Locator{})
	if got.State != voicetools.ReadError || got.Source != "cancelled" || len(got.Actions) != 0 {
		t.Fatalf("cancelled discovery = %#v", got)
	}
}

func TestDiscoverFindsVerifiedBundleAndMissingMMKV(t *testing.T) {
	requireDarwin(t)
	locator, appPath := doubaoFixtureLocator(t, bundleIdentifier, verifiedShortVersion, verifiedBuildVersion)
	settingsPath := doubaoMMKVPath(locator.Home)

	got := Discover(context.Background(), locator)
	if got.State != voicetools.ConfigMissing {
		t.Fatalf("state = %q, want config_missing: %#v", got.State, got)
	}
	if got.AppPath != appPath || got.Version != "1.0.2 (1)" || got.SettingsPath != settingsPath {
		t.Fatalf("bundle/config metadata = %#v", got)
	}
	if strings.Contains(got.Source, "not configured") || strings.Contains(got.Source, "未配置") {
		t.Fatalf("missing files must not claim the user has no configuration: %#v", got)
	}
}

func TestDiscoverPreservesOnlyWhitelistedShortcutValues(t *testing.T) {
	requireDarwin(t)
	locator, appPath := doubaoFixtureLocator(t, bundleIdentifier, verifiedShortVersion, verifiedBuildVersion)
	settingsPath := doubaoMMKVPath(locator.Home)
	writeMMKVFixture(t, settingsPath, []mmkvEntry{{
		key: []byte("d"),
		value: keyedArchiveFixture(t, map[string]interface{}{
			"asrShortcutKeyCode":                uint64(49),
			"asrShortcutModifierFlags":          uint64(4096),
			"asrShortcutKeyDisplay":             " Control+K ",
			"asrLongPressShortcutKeyCode":       uint64(12),
			"asrLongPressShortcutModifierFlags": uint64(2048),
			"asrLongPressShortcutKeyDisplay":    "Option+Space",
			"privateField":                      "UNRELATED_SECRET_SENTINEL",
		}),
	}, {
		key:   []byte("unrelated-mmkv-key"),
		value: []byte("OTHER_MMKV_VALUE_SENTINEL"),
	}})

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Ready || got.AppPath != appPath || got.SettingsPath != settingsPath {
		t.Fatalf("discovery = %#v", got)
	}
	if len(got.Actions) != 2 {
		t.Fatalf("actions = %#v, want exactly the two shortcut records", got.Actions)
	}
	for _, action := range got.Actions {
		if action.Scope != "unknown" || action.Gesture != "unverified" || len(action.Candidates) != 1 {
			t.Fatalf("action interpretation should remain unverified: %#v", action)
		}
		if action.Candidates[0].Supported {
			t.Fatalf("stored values must not be treated as executable shortcuts: %#v", action.Candidates[0])
		}
	}
	if got.Actions[0].Candidates[0].Shortcut != " Control+K " ||
		!strings.Contains(got.Actions[0].Candidates[0].Raw, `"asrShortcutKeyCode":49`) ||
		!strings.Contains(got.Actions[0].Candidates[0].Raw, `"asrShortcutModifierFlags":4096`) {
		t.Fatalf("single-press raw values were not preserved: %#v", got.Actions[0].Candidates[0])
	}
	if got.Actions[1].Candidates[0].Shortcut != "Option+Space" ||
		!strings.Contains(got.Actions[1].Candidates[0].Raw, `"asrLongPressShortcutKeyCode":12`) ||
		!strings.Contains(got.Actions[1].Candidates[0].Raw, `"asrLongPressShortcutModifierFlags":2048`) {
		t.Fatalf("long-press raw values were not preserved: %#v", got.Actions[1].Candidates[0])
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "UNRELATED_SECRET_SENTINEL") ||
		strings.Contains(string(encoded), "OTHER_MMKV_VALUE_SENTINEL") ||
		strings.Contains(string(encoded), "privateField") {
		t.Fatalf("snapshot included non-whitelisted settings: %s", encoded)
	}
}

func TestDiscoverIgnoresObsoletePlistLocations(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, verifiedShortVersion, verifiedBuildVersion)
	writeBundleTestFile(t, filepath.Join(locator.Home, "Library", "Application Support", "DoubaoIme", "UserSettings.plist"),
		[]byte("<plist><dict><key>asrShortcutKeyCode</key><integer>49</integer></dict></plist>"))
	writeBundleTestFile(t, filepath.Join(locator.Home, "Library", "Application Support", "OceanIme", "UserSettings.plist"),
		[]byte("<plist><dict><key>asrShortcutKeyCode</key><integer>49</integer></dict></plist>"))

	got := Discover(context.Background(), locator)
	if got.State != voicetools.ConfigMissing || got.SettingsPath != doubaoMMKVPath(locator.Home) || len(got.Actions) != 0 {
		t.Fatalf("obsolete plist files must not act as settings fallbacks: %#v", got)
	}
}

func TestDiscoverDoesNotInferShortcutFieldsWhenMMKVValueDiffers(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, verifiedShortVersion, verifiedBuildVersion)
	writeMMKVFixture(t, doubaoMMKVPath(locator.Home), []mmkvEntry{{
		key: []byte("d"),
		value: keyedArchiveFixture(t, map[string]interface{}{
			"unrelatedSetting": "UNRELATED_SECRET_SENTINEL",
		}),
	}})

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Unsupported || len(got.Actions) != 0 ||
		!strings.Contains(got.Source, "shortcut field layout is unverified") {
		t.Fatalf("unverified storage layout = %#v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "UNRELATED_SECRET_SENTINEL") || strings.Contains(string(encoded), "unrelatedSetting") {
		t.Fatalf("snapshot included a non-whitelisted setting: %s", encoded)
	}
}

func TestDiscoverRejectsUnverifiedBundleVersion(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, "1.0.3", verifiedBuildVersion)
	got := Discover(context.Background(), locator)
	if got.State != voicetools.Unsupported || len(got.Actions) != 0 || got.SettingsPath != "" {
		t.Fatalf("unverified bundle version = %#v", got)
	}
}

func TestDiscoverRejectsMultipleInstalledBundles(t *testing.T) {
	requireDarwin(t)
	home := t.TempDir()
	userDir := filepath.Join(home, "Library", "Input Methods")
	systemDir := filepath.Join(t.TempDir(), "Input Methods")
	writeBundle(t, filepath.Join(userDir, bundleName), bundleIdentifier, verifiedShortVersion, verifiedBuildVersion)
	writeBundle(t, filepath.Join(systemDir, bundleName), bundleIdentifier, verifiedShortVersion, verifiedBuildVersion)
	locator := voicetools.Locator{Home: home, InputMethodDirs: []string{userDir, systemDir}}

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Ambiguous || got.AppPath != "" || len(got.Actions) != 0 {
		t.Fatalf("multiple installed bundles must remain ambiguous: %#v", got)
	}
}

func TestDiscoverRejectsWrongBundleIdentifier(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, "wrong.identifier", verifiedShortVersion, verifiedBuildVersion)
	got := Discover(context.Background(), locator)
	if got.State != voicetools.Unsupported || len(got.Actions) != 0 {
		t.Fatalf("wrong bundle identity = %#v", got)
	}
}

func TestDiscoverRejectsMalformedMMKV(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, verifiedShortVersion, verifiedBuildVersion)
	writeMMKVRawFixture(t, doubaoMMKVPath(locator.Home), []byte("not an MMKV archive"))

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Unsupported || len(got.Actions) != 0 {
		t.Fatalf("malformed MMKV = %#v", got)
	}
}

func TestDiscoverRejectsUnexpectedWhitelistFieldType(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, verifiedShortVersion, verifiedBuildVersion)
	writeMMKVFixture(t, doubaoMMKVPath(locator.Home), []mmkvEntry{{
		key: []byte("d"),
		value: keyedArchiveFixture(t, map[string]interface{}{
			"asrShortcutKeyCode": "49",
		}),
	}})

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Unsupported || len(got.Actions) != 0 ||
		!strings.Contains(got.Source, "unsupported type") {
		t.Fatalf("wrong whitelist field type = %#v", got)
	}
}

func doubaoFixtureLocator(t *testing.T, identifier, version, build string) (voicetools.Locator, string) {
	t.Helper()
	home := t.TempDir()
	inputMethods := filepath.Join(home, "Library", "Input Methods")
	appPath := filepath.Join(inputMethods, bundleName)
	writeBundle(t, appPath, identifier, version, build)
	return voicetools.Locator{Home: home, InputMethodDirs: []string{inputMethods}}, appPath
}

func writeBundle(t *testing.T, appPath, identifier, version, build string) {
	t.Helper()
	infoDir := filepath.Join(appPath, "Contents")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` +
		`<plist version="1.0"><dict>` +
		`<key>CFBundleIdentifier</key><string>` + identifier + `</string>` +
		`<key>CFBundleShortVersionString</key><string>` + version + `</string>` +
		`<key>CFBundleVersion</key><string>` + build + `</string>` +
		`</dict></plist>`
	if err := os.WriteFile(filepath.Join(infoDir, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeBundleTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("Doubao discovery requires the macOS input method bundle")
	}
}
