package typeless

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeFixture(t *testing.T, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app-settings.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireReadError(t *testing.T, err error, code ErrorCode, status Status) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected read error %q", code)
	}
	var readErr *ReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
	if readErr.Code() != code || readErr.Status() != status {
		t.Fatalf("read error code/status = %q/%q, want %q/%q", readErr.Code(), readErr.Status(), code, status)
	}
}

func TestReadMissingAndNullDoNotSubstituteDefaults(t *testing.T) {
	missingPath := writeFixture(t, []byte(`{}`))
	missing, err := ReadFile(missingPath)
	if err != nil {
		t.Fatal(err)
	}
	if missing.FeatureShortcutBindings.Status != StatusMissing ||
		missing.FeatureShortcutBindings.DictationMode.Status != StatusMissing ||
		missing.FeatureShortcutBindings.AskAnythingMode.Status != StatusMissing ||
		missing.FeatureShortcutBindings.TranslationMode.Status != StatusMissing ||
		missing.FeatureShortcutBindings.PasteLastTranscript.Status != StatusMissing {
		t.Fatalf("missing shortcut settings were not preserved as missing: %#v", missing.FeatureShortcutBindings)
	}
	if missing.SelectedMicrophone.Status != StatusMissing || missing.SelectedMicrophone.DeviceID.Status != StatusUnknown {
		t.Fatalf("missing microphone setting was not preserved: %#v", missing.SelectedMicrophone)
	}

	nullPath := writeFixture(t, []byte(`{"featureShortcutBindings":null,"selectedMicrophoneDevice":null}`))
	null, err := ReadFile(nullPath)
	if err != nil {
		t.Fatal(err)
	}
	if null.FeatureShortcutBindings.Status != StatusNull || null.FeatureShortcutBindings.DictationMode.Status != StatusUnknown {
		t.Fatalf("null shortcut settings were conflated with missing/default: %#v", null.FeatureShortcutBindings)
	}
	if null.SelectedMicrophone.Status != StatusNull || null.SelectedMicrophone.DeviceID.Status != StatusUnknown {
		t.Fatalf("null microphone setting was not preserved: %#v", null.SelectedMicrophone)
	}
}

func TestReadPreservesMultipleShortcutStringsAndOnlyKnownFields(t *testing.T) {
	fixture := []byte(`{
		"featureShortcutBindings": {
			"dictationMode": ["Fn", "Ctrl+K", "KeypadAdd", "NumpadAdd"],
			"askAnythingMode": [],
			"translationMode": null,
			"pasteLastTranscript": ["Ctrl+Cmd+V"],
			"futurePrivateField": "UNKNOWN_NESTED_SECRET"
		},
		"selectedMicrophoneDevice": {
			"deviceId": "fixture-device-uid",
			"label": "UNKNOWN_DEVICE_LABEL_SECRET",
			"token": "UNKNOWN_DEVICE_TOKEN_SECRET"
		},
		"apiKey": "UNKNOWN_TOP_LEVEL_SECRET",
		"history": ["UNKNOWN_HISTORY_SECRET"]
	}`)
	path := writeFixture(t, fixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(before)

	snapshot, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	shortcuts := snapshot.FeatureShortcutBindings
	if shortcuts.Status != StatusPresent || shortcuts.DictationMode.Status != StatusPresent {
		t.Fatalf("dictation shortcut status = %#v", shortcuts)
	}
	if got, want := shortcuts.DictationMode.Values, []string{"Fn", "Ctrl+K", "KeypadAdd", "NumpadAdd"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shortcut strings = %#v, want %#v", got, want)
	}
	if shortcuts.DictationMode.Interpretation != StatusUnknown {
		t.Fatalf("shortcut interpretation = %q, want unknown", shortcuts.DictationMode.Interpretation)
	}
	if shortcuts.AskAnythingMode.Status != StatusPresent || len(shortcuts.AskAnythingMode.Values) != 0 {
		t.Fatalf("empty binding array was not kept distinct from missing: %#v", shortcuts.AskAnythingMode)
	}
	if shortcuts.TranslationMode.Status != StatusNull || !reflect.DeepEqual(shortcuts.PasteLastTranscript.Values, []string{"Ctrl+Cmd+V"}) {
		t.Fatalf("other known actions were not read independently: %#v", shortcuts)
	}
	if got := snapshot.SelectedMicrophone; got.Status != StatusPresent || got.DeviceID.Status != StatusPresent || got.DeviceID.Value != "fixture-device-uid" || got.IDMapping != StatusUnknown {
		t.Fatalf("microphone selection = %#v", got)
	}

	output, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"UNKNOWN_NESTED_SECRET",
		"UNKNOWN_DEVICE_LABEL_SECRET",
		"UNKNOWN_DEVICE_TOKEN_SECRET",
		"UNKNOWN_TOP_LEVEL_SECRET",
		"UNKNOWN_HISTORY_SECRET",
		"futurePrivateField",
		"apiKey",
		"history",
	} {
		if strings.Contains(string(output), secret) {
			t.Fatalf("reader output leaked ignored field %q: %s", secret, output)
		}
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterHash := sha256.Sum256(after); afterHash != beforeHash {
		t.Fatal("read changed the fixture file")
	}
}

func TestReadKnownFieldTypeErrorsAreContained(t *testing.T) {
	path := writeFixture(t, []byte(`{
		"featureShortcutBindings": {
			"dictationMode": "Ctrl+Space",
			"askAnythingMode": ["Fn", 7],
			"translationMode": [],
			"pasteLastTranscript": null
		},
		"selectedMicrophoneDevice": {"deviceId": 42, "private": "TYPE_ERROR_SECRET"}
	}`))
	snapshot, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	shortcuts := snapshot.FeatureShortcutBindings
	if shortcuts.Status != StatusPresent || shortcuts.DictationMode.Status != StatusMalformed || shortcuts.AskAnythingMode.Status != StatusMalformed {
		t.Fatalf("wrong shortcut types were not marked malformed: %#v", shortcuts)
	}
	if len(shortcuts.DictationMode.Values) != 0 || len(shortcuts.AskAnythingMode.Values) != 0 {
		t.Fatalf("malformed shortcuts leaked partially decoded values: %#v", shortcuts)
	}
	if shortcuts.TranslationMode.Status != StatusPresent || shortcuts.PasteLastTranscript.Status != StatusNull {
		t.Fatalf("valid/null action fields were affected by sibling errors: %#v", shortcuts)
	}
	if snapshot.SelectedMicrophone.Status != StatusPresent || snapshot.SelectedMicrophone.DeviceID.Status != StatusMalformed || snapshot.SelectedMicrophone.DeviceID.Value != "" {
		t.Fatalf("wrong microphone id type was not contained: %#v", snapshot.SelectedMicrophone)
	}
}

func TestNullShortcutItemIsMalformed(t *testing.T) {
	path := writeFixture(t, []byte(`{"featureShortcutBindings":{"dictationMode":["Fn",null]}}`))
	snapshot, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	action := snapshot.FeatureShortcutBindings.DictationMode
	if action.Status != StatusMalformed {
		t.Fatalf("shortcut array containing null status = %q, want malformed", action.Status)
	}
	if len(action.Values) != 0 {
		t.Fatalf("malformed shortcut array returned partial values: %#v", action.Values)
	}
}

func TestReadFIFOWithoutWriterReturnsUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app-settings.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := ReadFile(path)
		result <- err
	}()

	select {
	case err := <-result:
		requireReadError(t, err, ErrorUnsupportedFile, StatusUnsupported)
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO without a writer did not return promptly")
	}
}

func TestReadRejectsMalformedTrailingAndUnsupportedRootJSON(t *testing.T) {
	malformedPath := writeFixture(t, []byte(`{} {}`))
	_, err := ReadFile(malformedPath)
	requireReadError(t, err, ErrorMalformedJSON, StatusMalformed)
	if strings.Contains(err.Error(), malformedPath) || strings.Contains(err.Error(), "{}") {
		t.Fatalf("read error exposed input: %q", err)
	}

	unsupportedPath := writeFixture(t, []byte(`[]`))
	_, err = ReadFile(unsupportedPath)
	requireReadError(t, err, ErrorUnsupportedRoot, StatusUnsupported)
}

func TestReadRequiresExplicitPathAndEnforcesSizeLimit(t *testing.T) {
	if _, err := ReadFile(" \t"); err == nil {
		t.Fatal("blank path should not trigger path discovery")
	} else {
		requireReadError(t, err, ErrorInvalidPath, StatusError)
	}

	nonexistent := filepath.Join(t.TempDir(), "does-not-exist.json")
	_, err := ReadFile(nonexistent)
	requireReadError(t, err, ErrorRead, StatusError)
	if strings.Contains(err.Error(), nonexistent) {
		t.Fatalf("read error exposed path: %q", err)
	}

	tooLarge := make([]byte, MaxConfigBytes+1)
	path := writeFixture(t, tooLarge)
	_, err = ReadFile(path)
	requireReadError(t, err, ErrorTooLarge, StatusError)
}
