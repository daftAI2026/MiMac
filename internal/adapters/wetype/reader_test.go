package wetype

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"howett.net/plist"
	"mimac.local/mimac/internal/voicetools"
)

const (
	fixtureBundleShortVersion = "99.0.0"
	fixtureBundleBuildVersion = "99999"
)

type testEntry struct {
	key   string
	value []byte
}

func TestDecodeStoreReturnsOnlyAllowlistedRawCandidates(t *testing.T) {
	metadata, data := makeStoreFixture([]testEntry{
		{key: "voicePTTShortcut_keyCodes", value: mmkvStringValue("[63, 49]")},
		{key: "voicePTTShortcut_modifiers", value: mmkvInt32Value(7)},
		{key: "voiceToggleShortcut_keyCodes", value: mmkvStringValue("[70]")},
		{key: "voiceToggleShortcut_tapCount", value: mmkvInt32Value(-1)},
		{key: voiceShortcutGlobalEnabledField, value: encodeVarint(1)},
		{key: "unrelated.private.setting", value: []byte("PRIVATE_SENTINEL")},
	})

	candidate, err := decodeStore(metadata, data)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.MMKVVersion != 3 || len(candidate.Fields) != len(allowlistedFields) {
		t.Fatalf("candidate metadata = %#v", candidate)
	}
	assertFieldJSON(t, candidate, "voicePTTShortcut_keyCodes", "[63,49]")
	assertFieldJSON(t, candidate, "voicePTTShortcut_modifiers", "7")
	assertFieldJSON(t, candidate, "voiceToggleShortcut_keyCodes", "[70]")
	assertFieldJSON(t, candidate, "voiceToggleShortcut_tapCount", "-1")
	assertFieldJSON(t, candidate, voiceShortcutGlobalEnabledField, "true")
	assertFieldMissing(t, candidate, "voicePTTShortcut_tapCount")
	assertFieldMissing(t, candidate, "voiceToggleShortcut_modifiers")

	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE_SENTINEL") || strings.Contains(string(encoded), "unrelated.private.setting") {
		t.Fatalf("non-allowlisted data escaped: %s", encoded)
	}
}

func TestDecodeStoreUsesLastValueAndHonorsTombstones(t *testing.T) {
	metadata, data := makeStoreFixture([]testEntry{
		{key: "voicePTTShortcut_keyCodes", value: mmkvStringValue("[11]")},
		{key: "voicePTTShortcut_keyCodes", value: mmkvStringValue("[22]")},
		{key: "voiceToggleShortcut_tapCount", value: mmkvInt32Value(2)},
		{key: "voiceToggleShortcut_tapCount", value: nil},
	})

	candidate, err := decodeStore(metadata, data)
	if err != nil {
		t.Fatal(err)
	}
	assertFieldJSON(t, candidate, "voicePTTShortcut_keyCodes", "[22]")
	assertFieldMissing(t, candidate, "voiceToggleShortcut_tapCount")
	assertFieldMissing(t, candidate, voiceShortcutGlobalEnabledField)
}

func TestDecodeStoreRejectsUnsupportedVersionEncryptionCRCAndSizes(t *testing.T) {
	metadata, data := makeStoreFixture([]testEntry{{key: "voicePTTShortcut_keyCodes", value: mmkvStringValue("[1]")}})
	tests := []struct {
		name      string
		mutate    func([]byte, []byte)
		wantError error
	}{
		{
			name:      "unknown version",
			mutate:    func(meta, _ []byte) { binary.LittleEndian.PutUint32(meta[mmkvVersionOffset:], 4) },
			wantError: ErrUnsupportedFormat,
		},
		{
			name:      "encrypted IV",
			mutate:    func(meta, _ []byte) { meta[mmkvIVOffset] = 1 },
			wantError: ErrEncryptedStore,
		},
		{
			name:      "CRC mismatch",
			mutate:    func(meta, _ []byte) { meta[0] ^= 0xff },
			wantError: ErrIntegrity,
		},
		{
			name:      "actual size exceeds data",
			mutate:    func(meta, data []byte) { binary.LittleEndian.PutUint32(meta[mmkvActualSizeOffset:], uint32(len(data))) },
			wantError: ErrUnsupportedFormat,
		},
		{
			name:      "data prefix mismatch",
			mutate:    func(_, data []byte) { binary.LittleEndian.PutUint32(data[:mmkvDataPrefixSize], 0) },
			wantError: ErrUnsupportedFormat,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := append([]byte(nil), metadata...)
			contents := append([]byte(nil), data...)
			test.mutate(meta, contents)
			_, err := decodeStore(meta, contents)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
		})
	}
	if _, err := decodeStore(metadata[:mmkvMetaV3Size-1], data); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("truncated metadata error = %v", err)
	}
}

func TestDecodeStoreRejectsMalformedRecordsAndAllowlistedValues(t *testing.T) {
	metadata, data := makeRawStoreFixture([]byte{0x80})
	if _, err := decodeStore(metadata, data); !errors.Is(err, ErrMalformedRecord) {
		t.Fatalf("truncated record error = %v", err)
	}

	metadata, data = makeStoreFixture([]testEntry{{key: "voicePTTShortcut_keyCodes", value: mmkvStringValue("not-json")}})
	if _, err := decodeStore(metadata, data); !errors.Is(err, ErrMalformedValue) {
		t.Fatalf("malformed key-code value error = %v", err)
	}

	metadata, data = makeStoreFixture([]testEntry{{key: "voicePTTShortcut_modifiers", value: []byte{0x01, 0x02}}})
	if _, err := decodeStore(metadata, data); !errors.Is(err, ErrMalformedValue) {
		t.Fatalf("trailing Int32 bytes error = %v", err)
	}

	metadata, data = makeStoreFixture([]testEntry{{key: voiceShortcutGlobalEnabledField, value: encodeVarint(2)}})
	if _, err := decodeStore(metadata, data); !errors.Is(err, ErrMalformedValue) {
		t.Fatalf("non-boolean global flag error = %v", err)
	}
}

func TestDecodeBoolAcceptsOnlyMMKVBooleanValues(t *testing.T) {
	for _, test := range []struct {
		value int32
		want  string
	}{
		{value: 0, want: "false"},
		{value: 1, want: "true"},
	} {
		got, err := decodeBool(mmkvInt32Value(test.value))
		if err != nil || string(got) != test.want {
			t.Fatalf("decodeBool(%d) = %s, %v; want %s", test.value, got, err, test.want)
		}
	}
	for _, encoded := range [][]byte{{}, {0x02}, {0x01, 0x00}} {
		if _, err := decodeBool(encoded); !errors.Is(err, ErrMalformedValue) {
			t.Errorf("decodeBool(%v) error = %v, want ErrMalformedValue", encoded, err)
		}
	}
}

func TestDecodeKeyCodeArrayRejectsNonIntegerElements(t *testing.T) {
	for _, input := range []string{"[null]", "[true]", "[1.0]", "[1e0]", "[\"1\"]"} {
		t.Run(input, func(t *testing.T) {
			if _, err := decodeKeyCodeArray(mmkvStringValue(input)); !errors.Is(err, ErrMalformedValue) {
				t.Fatalf("%s error = %v, want malformed value", input, err)
			}
		})
	}
}

func TestReadStoreReadsStableSyntheticSnapshot(t *testing.T) {
	metadata, data := makeStoreFixture([]testEntry{{key: "voicePTTShortcut_keyCodes", value: mmkvStringValue("[5]")}})
	directory := t.TempDir()
	dataPath := filepath.Join(directory, storeName)
	if err := os.WriteFile(dataPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath+".crc", metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate, err := readStore(context.Background(), dataPath)
	if err != nil {
		t.Fatal(err)
	}
	assertFieldJSON(t, candidate, "voicePTTShortcut_keyCodes", "[5]")
}

func TestBundleIdentityMatchesAcrossReleases(t *testing.T) {
	bundlePath := filepath.Join(t.TempDir(), bundleName)
	infoPath := filepath.Join(bundlePath, "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeBundleInfo(t, infoPath, bundleIdentifier, fixtureBundleShortVersion, fixtureBundleBuildVersion)

	bundle, state, err := voicetools.FindBundle(context.Background(), []string{bundlePath}, bundleIdentifier)
	if err != nil {
		t.Fatal(err)
	}
	if state != voicetools.Ready || !matchesBundleIdentity(bundle) {
		t.Fatalf("future release with matching identity rejected: state=%q bundle=%#v", state, bundle)
	}
	bundle.Identifier = "wrong.identifier"
	if matchesBundleIdentity(bundle) {
		t.Fatalf("wrong bundle identity accepted: %#v", bundle)
	}
}

func TestDecodeInt32RejectsOutOfRangeAndAcceptsMMKVNegativeEncoding(t *testing.T) {
	if got, err := decodeInt32(mmkvInt32Value(-123)); err != nil || string(got) != "-123" {
		t.Fatalf("negative Int32 = %s, %v", got, err)
	}
	if _, err := decodeInt32(encodeVarint(uint64(1) << 31)); !errors.Is(err, ErrMalformedValue) {
		t.Fatalf("out-of-range Int32 error = %v", err)
	}
}

func assertFieldJSON(t *testing.T, candidate Candidate, key, want string) {
	t.Helper()
	field, ok := candidate.Fields[key]
	if !ok || field.Status != "present" || string(field.Value) != want {
		t.Fatalf("field %q = %#v, want present %s", key, field, want)
	}
}

func assertFieldMissing(t *testing.T, candidate Candidate, key string) {
	t.Helper()
	field, ok := candidate.Fields[key]
	if !ok || field.Status != "missing" || len(field.Value) != 0 {
		t.Fatalf("field %q = %#v, want missing without fallback value", key, field)
	}
}

func makeStoreFixture(entries []testEntry) ([]byte, []byte) {
	var body bytes.Buffer
	for _, entry := range entries {
		writeVarint(&body, uint64(len(entry.key)))
		body.WriteString(entry.key)
		writeVarint(&body, uint64(len(entry.value)))
		body.Write(entry.value)
	}
	var payload bytes.Buffer
	writeVarint(&payload, uint64(body.Len()))
	payload.Write(body.Bytes())
	return makeRawStoreFixture(payload.Bytes())
}

func makeRawStoreFixture(payload []byte) ([]byte, []byte) {
	metadata := make([]byte, 16<<10)
	data := make([]byte, 16<<10)
	binary.LittleEndian.PutUint32(metadata[:4], crc32.ChecksumIEEE(payload))
	binary.LittleEndian.PutUint32(metadata[mmkvVersionOffset:], 3)
	binary.LittleEndian.PutUint32(metadata[mmkvActualSizeOffset:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(data[:mmkvDataPrefixSize], uint32(len(payload)))
	copy(data[mmkvDataPrefixSize:], payload)
	return metadata, data
}

func mmkvStringValue(value string) []byte {
	var encoded bytes.Buffer
	writeVarint(&encoded, uint64(len(value)))
	encoded.WriteString(value)
	return encoded.Bytes()
}

func mmkvInt32Value(value int32) []byte {
	if value < 0 {
		return encodeVarint(uint64(int64(value)))
	}
	return encodeVarint(uint64(value))
}

func encodeVarint(value uint64) []byte {
	var encoded bytes.Buffer
	writeVarint(&encoded, value)
	return encoded.Bytes()
}

func writeVarint(destination *bytes.Buffer, value uint64) {
	for value >= 0x80 {
		destination.WriteByte(byte(value) | 0x80)
		value >>= 7
	}
	destination.WriteByte(byte(value))
}

func writeBundleInfo(t *testing.T, path, identifier, version, build string) {
	t.Helper()
	encoded, err := plist.Marshal(map[string]string{
		"CFBundleIdentifier":         identifier,
		"CFBundleShortVersionString": version,
		"CFBundleVersion":            build,
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
