package shandian

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

const (
	fixtureBundleShortVersion = "99.0.0"
	fixtureBundleBuildVersion = "99999"
)

func TestDecodeConfigRetainsOnlyShortcutAndActionAllowlist(t *testing.T) {
	data := []byte(`{
		"config_version": 3,
		"recording_hotkey": null,
		"freemode_hotkey": null,
		"unified_hotkey": {"keys":["RCmd"],"block_keys":true,"future_secret":"LEAK_ME"},
		"short_press_action": "VoiceInput",
		"long_press_action": "VoiceAssistant",
		"esc_cancel_enabled": null,
		"api_key": "LEAK_ME",
		"model_provider": {"token":"LEAK_ME"}
	}`)

	got, err := decodeConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	want := Candidate{
		ConfigVersion:    3,
		UnifiedHotkey:    UnifiedHotkey{Keys: []string{"RCmd"}, BlockKeys: true},
		ShortPressAction: "VoiceInput",
		LongPressAction:  "VoiceAssistant",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate = %#v, want %#v", got, want)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{"LEAK_ME", "recording_hotkey", "freemode_hotkey", "esc_cancel_enabled", "api_key", "model_provider"} {
		if strings.Contains(string(encoded), excluded) {
			t.Fatalf("candidate retained excluded field %q: %s", excluded, encoded)
		}
	}
}

func TestDecodeConfigRequiresVerifiedSchema(t *testing.T) {
	for _, input := range []string{
		`{"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		`{"config_version":null,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		`{"config_version":2,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		`{"config_version":4,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		`{"config_version":3.0,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		`{"config_version":"3","unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
	} {
		got, err := decodeConfig([]byte(input))
		if !errors.Is(err, ErrUnsupportedSchema) {
			t.Errorf("decodeConfig(%s) error = %v, want ErrUnsupportedSchema", input, err)
		}
		if !reflect.DeepEqual(got, Candidate{}) {
			t.Errorf("unsupported schema returned partial candidate: %#v", got)
		}
	}
}

func TestDecodeConfigPreservesExplicitlyEmptyHotkey(t *testing.T) {
	data := []byte(`{"config_version":3,"unified_hotkey":{"keys":[],"block_keys":false},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`)
	got, err := decodeConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnifiedHotkey.Keys == nil || len(got.UnifiedHotkey.Keys) != 0 || got.UnifiedHotkey.BlockKeys {
		t.Fatalf("explicit empty binding = %#v", got.UnifiedHotkey)
	}
}

func TestDecodeConfigRejectsMalformedOrIncompleteAllowlist(t *testing.T) {
	valid := `"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"`
	cases := map[string]string{
		"malformed JSON":         `{"config_version":3,` + valid + `,}`,
		"trailing JSON":          `{"config_version":3,` + valid + `}{}`,
		"non-object root":        `[]`,
		"missing unified hotkey": `{"config_version":3,"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"null unified hotkey":    `{"config_version":3,"unified_hotkey":null,"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"missing keys":           `{"config_version":3,"unified_hotkey":{"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"null keys":              `{"config_version":3,"unified_hotkey":{"keys":null,"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"null key element":       `{"config_version":3,"unified_hotkey":{"keys":[null],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"empty key element":      `{"config_version":3,"unified_hotkey":{"keys":[""],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"blank key element":      `{"config_version":3,"unified_hotkey":{"keys":[" \t "],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"wrong key type":         `{"config_version":3,"unified_hotkey":{"keys":[1],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"missing block flag":     `{"config_version":3,"unified_hotkey":{"keys":["RCmd"]},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"null block flag":        `{"config_version":3,"unified_hotkey":{"keys":["RCmd"],"block_keys":null},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
		"null short action":      `{"config_version":3,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":null,"long_press_action":"VoiceAssistant"}`,
		"empty short action":     `{"config_version":3,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"","long_press_action":"VoiceAssistant"}`,
		"wrong long action type": `{"config_version":3,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":true}`,
		"blank long action":      `{"config_version":3,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":" \t "}`,
		"duplicate action":       `{"config_version":3,` + valid + `,"short_press_action":"VoiceAssistant"}`,
		"duplicate hotkey key":   `{"config_version":3,"unified_hotkey":{"keys":["RCmd"],"keys":["LCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := decodeConfig([]byte(input))
			if !errors.Is(err, ErrMalformedConfig) {
				t.Fatalf("error = %v, want ErrMalformedConfig", err)
			}
			if !reflect.DeepEqual(got, Candidate{}) {
				t.Fatalf("malformed config returned partial candidate: %#v", got)
			}
		})
	}
}

func TestDecodeConfigRejectsInvalidUTF8(t *testing.T) {
	data := []byte(`{"config_version":3,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`)
	data[len(data)-2] = 0xff
	if got, err := decodeConfig(data); !errors.Is(err, ErrMalformedConfig) || !reflect.DeepEqual(got, Candidate{}) {
		t.Fatalf("invalid UTF-8 result = %#v, %v", got, err)
	}
}

func TestReadInstalledUsesFutureReleaseBundleAndLocatorHome(t *testing.T) {
	requireDarwin(t)
	locator, configPath, appPath := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
	writeConfig(t, configPath, []byte(validConfig))

	got, err := ReadInstalled(context.Background(), locator)
	if err != nil {
		t.Fatal(err)
	}
	if got.BundleIdentifier != bundleIdentifier || got.BundleVersion != fixtureBundleShortVersion || got.BundleBuild != fixtureBundleBuildVersion {
		t.Fatalf("bundle identity and diagnostic release = %#v", got)
	}
	if got.ConfigVersion != verifiedSchema || got.ShortPressAction != "VoiceInput" || got.LongPressAction != "VoiceAssistant" ||
		!reflect.DeepEqual(got.UnifiedHotkey, UnifiedHotkey{Keys: []string{"RCmd"}, BlockKeys: true}) {
		t.Fatalf("installed candidate = %#v", got)
	}
	if appPath == "" || got.BundleIdentifier != bundleIdentifier {
		t.Fatalf("fixture bundle was not used: %#v", got)
	}
}

func TestReadInstalledReportsMissingConfig(t *testing.T) {
	requireDarwin(t)
	locator, _, _ := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
	if got, err := ReadInstalled(context.Background(), locator); !errors.Is(err, ErrConfigMissing) || !reflect.DeepEqual(got, Candidate{}) {
		t.Fatalf("missing config result = %#v, %v", got, err)
	}
}

func TestReadInstalledRejectsWrongBundleIdentifier(t *testing.T) {
	requireDarwin(t)
	locator, _, appPath := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
	infoPath := filepath.Join(appPath, "Contents", "Info.plist")
	data, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatal(err)
	}
	wrongIdentity := strings.Replace(string(data), bundleIdentifier, "wrong.identifier", 1)
	if err := os.WriteFile(infoPath, []byte(wrongIdentity), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadInstalled(context.Background(), locator); !errors.Is(err, ErrBundleUnavailable) || !reflect.DeepEqual(got, Candidate{}) {
		t.Fatalf("wrong bundle identity result = %#v, %v", got, err)
	}
}

func TestReadInstalledRejectsAmbiguousBundle(t *testing.T) {
	requireDarwin(t)
	home := t.TempDir()
	first := filepath.Join(home, "first")
	second := filepath.Join(home, "second")
	writeBundle(t, filepath.Join(first, bundleName), fixtureBundleShortVersion, fixtureBundleBuildVersion)
	writeBundle(t, filepath.Join(second, bundleName), fixtureBundleShortVersion, fixtureBundleBuildVersion)
	locator := voicetools.Locator{Home: home, ApplicationDirs: []string{first, second}}

	if got, err := ReadInstalled(context.Background(), locator); !errors.Is(err, ErrAmbiguousBundle) || !reflect.DeepEqual(got, Candidate{}) {
		t.Fatalf("ambiguous bundle result = %#v, %v", got, err)
	}
}

func TestReadInstalledRejectsUnstableFileTypesAndOversize(t *testing.T) {
	requireDarwin(t)
	t.Run("symlink", func(t *testing.T) {
		locator, configPath, _ := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
		target := filepath.Join(t.TempDir(), "target.json")
		writeConfig(t, target, []byte(validConfig))
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, configPath); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadInstalled(context.Background(), locator); !errors.Is(err, ErrUnsupportedConfig) || !reflect.DeepEqual(got, Candidate{}) {
			t.Fatalf("symlink result = %#v, %v", got, err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		locator, configPath, _ := shandianFixture(t, fixtureBundleShortVersion, fixtureBundleBuildVersion)
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, make([]byte, maxConfigBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadInstalled(context.Background(), locator); !errors.Is(err, ErrUnsupportedConfig) || !reflect.DeepEqual(got, Candidate{}) {
			t.Fatalf("oversized result = %#v, %v", got, err)
		}
	})
}

func TestConfigReadErrorMapsStaleSnapshot(t *testing.T) {
	if !errors.Is(configReadError(context.Background(), voicetools.ErrStaleFile), ErrConfigChanged) {
		t.Fatal("stale read must be surfaced as a changed snapshot")
	}
}

const validConfig = `{"config_version":3,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`

func shandianFixture(t *testing.T, version, build string) (voicetools.Locator, string, string) {
	t.Helper()
	home := t.TempDir()
	applications := filepath.Join(home, "Applications")
	appPath := filepath.Join(applications, bundleName)
	writeBundle(t, appPath, version, build)
	configPath := filepath.Join(home, filepath.FromSlash(configRelativePath))
	return voicetools.Locator{Home: home, ApplicationDirs: []string{applications}}, configPath, appPath
}

func writeBundle(t *testing.T, appPath, version, build string) {
	t.Helper()
	contents := filepath.Join(appPath, "Contents")
	if err := os.MkdirAll(contents, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + bundleIdentifier + `</string>
<key>CFBundleShortVersionString</key><string>` + version + `</string>
<key>CFBundleVersion</key><string>` + build + `</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, path string, data []byte) {
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
		t.Skip("bundle inspection uses macOS plutil")
	}
}
