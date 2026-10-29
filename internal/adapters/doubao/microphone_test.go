package doubao

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestReadMicrophoneSelectionNarrowFieldValues(t *testing.T) {
	requireDarwin(t)
	for _, test := range []struct {
		name                 string
		fields               map[string]interface{}
		automatic            bool
		deviceUID            string
		selectedIDPresent    bool
		selectedMicrophoneID string
	}{
		{name: "absent", fields: map[string]interface{}{"unrelated": "ignored"}, automatic: true},
		{name: "null", fields: map[string]interface{}{"selectedMicrophoneId": nil}, automatic: true},
		{name: "empty", fields: map[string]interface{}{"selectedMicrophoneId": ""}, automatic: true, selectedIDPresent: true},
		{name: "explicit", fields: map[string]interface{}{"selectedMicrophoneId": "com.example.device.42"}, deviceUID: "com.example.device.42", selectedIDPresent: true, selectedMicrophoneID: "com.example.device.42"},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator, _ := doubaoFixtureLocator(t, bundleIdentifier, fixtureShortVersion, fixtureBuildVersion)
			writeMMKVFixture(t, doubaoMMKVPath(locator.Home), []mmkvEntry{{
				key:   []byte("d"),
				value: keyedArchiveFixture(t, test.fields),
			}})

			got, err := ReadMicrophoneSelection(context.Background(), locator)
			if err != nil {
				t.Fatalf("ReadMicrophoneSelection() error = %v", err)
			}
			if got.Automatic != test.automatic || got.DeviceUID != test.deviceUID {
				t.Fatalf("selection = %#v, want Automatic=%v DeviceUID=%q", got, test.automatic, test.deviceUID)
			}
			if test.selectedIDPresent {
				if got.SelectedMicrophoneID == nil || *got.SelectedMicrophoneID != test.selectedMicrophoneID {
					t.Fatalf("SelectedMicrophoneID = %v, want present %q", got.SelectedMicrophoneID, test.selectedMicrophoneID)
				}
			} else if got.SelectedMicrophoneID != nil {
				t.Fatalf("SelectedMicrophoneID = %q, want nil for absent/null field", *got.SelectedMicrophoneID)
			}
		})
	}
}

func TestMicrophoneSelectionOptionalStates(t *testing.T) {
	for _, test := range []struct {
		name                 string
		candidate            settingsCandidate
		automatic            bool
		deviceUID            string
		selectedIDPresent    bool
		selectedMicrophoneID string
	}{
		{name: "record absent", candidate: settingsCandidate{}, automatic: true},
		{name: "field absent", candidate: settingsCandidate{valuePresent: true, values: map[string]json.RawMessage{"unrelated": json.RawMessage(`"ignored"`)}}, automatic: true},
		{name: "null", candidate: settingsCandidate{valuePresent: true, values: map[string]json.RawMessage{microphoneSelectionField.name: json.RawMessage(`null`)}}, automatic: true},
		{name: "empty", candidate: settingsCandidate{valuePresent: true, values: map[string]json.RawMessage{microphoneSelectionField.name: json.RawMessage(`""`)}}, automatic: true, selectedIDPresent: true},
		{name: "UID", candidate: settingsCandidate{valuePresent: true, values: map[string]json.RawMessage{microphoneSelectionField.name: json.RawMessage(`"device.uid"`)}}, automatic: false, deviceUID: "device.uid", selectedIDPresent: true, selectedMicrophoneID: "device.uid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.candidate.microphoneSelection()
			if err != nil {
				t.Fatal(err)
			}
			if got.Automatic != test.automatic || got.DeviceUID != test.deviceUID {
				t.Fatalf("selection = %#v, want Automatic=%v DeviceUID=%q", got, test.automatic, test.deviceUID)
			}
			if test.selectedIDPresent {
				if got.SelectedMicrophoneID == nil || *got.SelectedMicrophoneID != test.selectedMicrophoneID {
					t.Fatalf("SelectedMicrophoneID = %v, want present %q", got.SelectedMicrophoneID, test.selectedMicrophoneID)
				}
				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				want, _ := json.Marshal(test.selectedMicrophoneID)
				if raw, ok := fields["selectedMicrophoneId"]; !ok || string(raw) != string(want) {
					t.Fatalf("serialized selectedMicrophoneId = %s (present %v), want %s", raw, ok, want)
				}
			} else if got.SelectedMicrophoneID != nil {
				t.Fatalf("SelectedMicrophoneID = %q, want nil for absent/null field", *got.SelectedMicrophoneID)
			}
		})
	}
}

func TestReadMicrophoneSelectionRejectsInvalidFieldType(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, fixtureShortVersion, fixtureBuildVersion)
	writeMMKVFixture(t, doubaoMMKVPath(locator.Home), []mmkvEntry{{
		key:   []byte("d"),
		value: keyedArchiveFixture(t, map[string]interface{}{"selectedMicrophoneId": uint64(42)}),
	}})

	if _, err := ReadMicrophoneSelection(context.Background(), locator); !errors.Is(err, errMicrophoneFieldType) {
		t.Fatalf("invalid microphone field error = %v, want errMicrophoneFieldType", err)
	}
}

func TestReadMicrophoneSelectionRequiresVerifiedCRC(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, fixtureShortVersion, fixtureBuildVersion)
	settingsPath := doubaoMMKVPath(locator.Home)
	writeMMKVFixture(t, settingsPath, []mmkvEntry{{
		key:   []byte("d"),
		value: keyedArchiveFixture(t, map[string]interface{}{"selectedMicrophoneId": "device.uid"}),
	}})
	metadataPath := settingsPath + ".crc"
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	metadata[0] ^= 0xff
	if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadMicrophoneSelection(context.Background(), locator); !errors.Is(err, voicetools.ErrUnsupportedFile) {
		t.Fatalf("bad CRC error = %v, want ErrUnsupportedFile", err)
	}
}

func TestReadMicrophoneSelectionRequiresLocatorPath(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, fixtureShortVersion, fixtureBuildVersion)
	wrongPath := filepath.Join(locator.Home, "Library", "Application Support", "DoubaoIme", "UserSettings.plist")
	if err := os.MkdirAll(filepath.Dir(wrongPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrongPath, []byte("not used"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMicrophoneSelection(context.Background(), locator); !errors.Is(err, voicetools.ErrFileMissing) {
		t.Fatalf("selection without exact MMKV locator file error = %v, want ErrFileMissing", err)
	}
}

func TestMicrophoneAllowlistDoesNotChangeShortcutDiscovery(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, fixtureShortVersion, fixtureBuildVersion)
	writeMMKVFixture(t, doubaoMMKVPath(locator.Home), []mmkvEntry{{
		key: []byte("d"),
		value: keyedArchiveFixture(t, map[string]interface{}{
			"asrShortcutKeyCode":       uint64(61),
			"asrShortcutModifierFlags": uint64(0),
			"asrShortcutKeyDisplay":    "rightoption",
			"selectedMicrophoneId":     uint64(99), // Not in Discover's six-field allowlist.
		}),
	}})

	got := Discover(context.Background(), locator)
	if got.State != voicetools.Ready || len(got.Actions) != 2 || !got.Actions[0].Candidates[0].Supported {
		t.Fatalf("shortcut discovery changed due to microphone-only type: %#v", got)
	}
	if strings.Contains(got.Actions[0].Candidates[0].Raw, "selectedMicrophoneId") {
		t.Fatalf("microphone field escaped into shortcut action: %#v", got.Actions[0].Candidates[0])
	}
}

func TestReadMicrophoneSelectionDoesNotExposeShortcutFields(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, fixtureShortVersion, fixtureBuildVersion)
	writeMMKVFixture(t, doubaoMMKVPath(locator.Home), []mmkvEntry{{
		key: []byte("d"),
		value: keyedArchiveFixture(t, map[string]interface{}{
			"asrShortcutKeyDisplay": "SECRET_SHORTCUT_SENTINEL",
			"selectedMicrophoneId":  "device.uid",
		}),
	}})

	got, err := ReadMicrophoneSelection(context.Background(), locator)
	if err != nil {
		t.Fatal(err)
	}
	if got.Automatic || got.DeviceUID != "device.uid" {
		t.Fatalf("selection = %#v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SECRET_SHORTCUT_SENTINEL") || strings.Contains(string(encoded), "asrShortcutKeyDisplay") {
		t.Fatalf("shortcut value escaped the narrow microphone API: %s", encoded)
	}
}

func TestReadMicrophoneSelectionTombstoneMeansAutomatic(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, fixtureShortVersion, fixtureBuildVersion)
	writeMMKVFixture(t, doubaoMMKVPath(locator.Home), []mmkvEntry{{key: []byte("d"), value: nil}})
	if got, err := ReadMicrophoneSelection(context.Background(), locator); err != nil || !got.Automatic || got.DeviceUID != "" || got.SelectedMicrophoneID != nil {
		t.Fatalf("tombstone selection = (%#v, %v), want automatic", got, err)
	}
}
