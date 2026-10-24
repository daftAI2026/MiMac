package doubao

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"howett.net/plist"
	"mimac.local/mimac/internal/voicetools"
)

type mmkvEntry struct {
	key   []byte
	value []byte
}

type invalidArchiveUID struct{}

func TestReadDoubaoMMKVRejectsBadCRC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.apple.xpc.activity")
	writeMMKVFixture(t, path, []mmkvEntry{{key: []byte("d"), value: keyedArchiveFixture(t, map[string]interface{}{
		"asrShortcutKeyDisplay": "Control+K",
	})}})

	metadataPath := path + ".crc"
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	metadata[0] ^= 0xff
	if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readDoubaoMMKV(context.Background(), path); !errorsIsUnsupported(err) {
		t.Fatalf("bad CRC error = %v, want unsupported file", err)
	}
}

func TestReadDoubaoMMKVRejectsTruncatedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.apple.xpc.activity")
	// The map body declares a one-byte value but the record declares five.
	writeMMKVRawFixture(t, path, []byte{4, 1, 'd', 5, 'x'})

	if _, err := readDoubaoMMKV(context.Background(), path); !errorsIsUnsupported(err) {
		t.Fatalf("truncated record error = %v, want unsupported file", err)
	}
}

func TestReadDoubaoMMKVLastDuplicateDValueWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.apple.xpc.activity")
	writeMMKVFixture(t, path, []mmkvEntry{
		{key: []byte("d"), value: keyedArchiveFixture(t, map[string]interface{}{"asrShortcutKeyDisplay": "first"})},
		{key: []byte("other"), value: []byte("unread")},
		{key: []byte("d"), value: keyedArchiveFixture(t, map[string]interface{}{"asrShortcutKeyDisplay": "last"})},
	})

	got, err := readDoubaoMMKV(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.valuePresent || got.strings["asrShortcutKeyDisplay"] != "last" {
		t.Fatalf("last d record did not win: %#v", got)
	}
}

func TestReadDoubaoMMKVReadsRecordsAppendedAfterOriginalLengthPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.apple.xpc.activity")
	data := encodeMMKVEntries([]mmkvEntry{{
		key:   []byte("d"),
		value: keyedArchiveFixture(t, map[string]interface{}{"asrShortcutKeyDisplay": "before"}),
	}})
	appended := encodeMMKVEntries([]mmkvEntry{{
		key:   []byte("d"),
		value: keyedArchiveFixture(t, map[string]interface{}{"asrShortcutKeyDisplay": "after"}),
	}})
	_, appendOffset, ok := readMMKVVarint(appended, 0)
	if !ok {
		t.Fatal("fixture's map prefix is malformed")
	}
	// MMKV append can leave the original map-size prefix intact.
	data = append(data, appended[appendOffset:]...)
	writeMMKVRawFixture(t, path, data)

	got, err := readDoubaoMMKV(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got.strings["asrShortcutKeyDisplay"] != "after" {
		t.Fatalf("appended last record was not read: %#v", got)
	}
}

func TestReadDoubaoMMKVTombstoneClearsEarlierDValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.apple.xpc.activity")
	writeMMKVFixture(t, path, []mmkvEntry{
		{key: []byte("d"), value: keyedArchiveFixture(t, map[string]interface{}{"asrShortcutKeyDisplay": "old"})},
		{key: []byte("d"), value: nil},
	})

	got, err := readDoubaoMMKV(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got.valuePresent || len(got.values) != 0 || len(got.strings) != 0 {
		t.Fatalf("empty last value must act as a tombstone: %#v", got)
	}
}

func TestParseDoubaoShortcutArchiveRejectsInvalidUID(t *testing.T) {
	archive := keyedArchiveFixture(t, map[string]interface{}{
		"asrShortcutKeyCode": invalidArchiveUID{},
	})
	if _, _, err := parseDoubaoShortcutArchive(archive); !errorsIsUnsupported(err) {
		t.Fatalf("invalid UID error = %v, want unsupported file", err)
	}
}

func TestParseDoubaoShortcutArchiveDoesNotReturnUnknownFields(t *testing.T) {
	archive := keyedArchiveFixture(t, map[string]interface{}{
		"asrShortcutKeyDisplay": "Control+K",
		"privateField":          "ARCHIVE_SECRET_SENTINEL",
	})
	values, displays, err := parseDoubaoShortcutArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || len(displays) != 1 || values["asrShortcutKeyDisplay"] == nil {
		t.Fatalf("parsed values = %#v, displays = %#v", values, displays)
	}
	encoded, err := json.Marshal(struct {
		Values   map[string]json.RawMessage
		Displays map[string]string
	}{Values: values, Displays: displays})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "ARCHIVE_SECRET_SENTINEL") || strings.Contains(string(encoded), "privateField") {
		t.Fatalf("unknown archive data escaped allowlist: %s", encoded)
	}
}

func TestVerifiedMMKVRejectsUnknownVersionFlagsAndEncryption(t *testing.T) {
	payload := encodeMMKVEntries([]mmkvEntry{{key: []byte("d"), value: []byte("opaque")}})
	base := makeMMKVFixture(payload)
	for _, test := range []struct {
		name   string
		mutate func([]byte)
	}{
		{name: "unknown version", mutate: func(meta []byte) { binary.LittleEndian.PutUint32(meta[4:8], 5) }},
		{name: "unknown flags", mutate: func(meta []byte) { binary.LittleEndian.PutUint64(meta[mmkvV4FlagsOffset:mmkvV4FlagsOffset+8], 1) }},
		{name: "encrypted IV", mutate: func(meta []byte) { meta[mmkvIVOffset] = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := append([]byte(nil), base.metadata...)
			test.mutate(metadata)
			if _, err := verifiedMMKVPayload(metadata, base.data); !errorsIsUnsupported(err) {
				t.Fatalf("invalid metadata error = %v, want unsupported file", err)
			}
		})
	}
}

func TestReadDoubaoMMKVRequiresDirectBinaryPlistValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.apple.xpc.activity")
	archive := keyedArchiveFixture(t, map[string]interface{}{"asrShortcutKeyDisplay": "direct"})
	writeMMKVFixture(t, path, []mmkvEntry{{key: []byte("d"), value: archive}})

	got, err := readDoubaoMMKV(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.valuePresent || got.strings["asrShortcutKeyDisplay"] != "direct" {
		t.Fatalf("direct bplist value was not parsed: %#v", got)
	}
}

func TestParseDoubaoShortcutArchiveSupportsUIDArrayContainers(t *testing.T) {
	archive := keyedArchiveUIDContainerFixture(t, map[string]interface{}{
		"asrShortcutKeyDisplay": "Control+K",
	})
	values, displays, err := parseDoubaoShortcutArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || displays["asrShortcutKeyDisplay"] != "Control+K" {
		t.Fatalf("UID array container was not parsed: values=%#v displays=%#v", values, displays)
	}
}

func writeMMKVFixture(t *testing.T, path string, entries []mmkvEntry) {
	t.Helper()
	writeMMKVRawFixture(t, path, encodeMMKVEntries(entries))
}

func writeMMKVRawFixture(t *testing.T, path string, payload []byte) {
	t.Helper()
	fixture := makeMMKVFixture(payload)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture.data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".crc", fixture.metadata, 0o600); err != nil {
		t.Fatal(err)
	}
}

type mmkvFixture struct {
	data     []byte
	metadata []byte
}

func makeMMKVFixture(payload []byte) mmkvFixture {
	data := make([]byte, 16<<10)
	binary.LittleEndian.PutUint32(data[:4], uint32(len(payload)))
	copy(data[4:], payload)
	metadata := make([]byte, 32<<10)
	binary.LittleEndian.PutUint32(metadata[0:4], crc32.ChecksumIEEE(payload))
	binary.LittleEndian.PutUint32(metadata[4:8], mmkvMetaVersionV4)
	binary.LittleEndian.PutUint32(metadata[8:12], 1)
	binary.LittleEndian.PutUint32(metadata[mmkvV4ActualSizeOffset:mmkvV4ActualSizeOffset+4], uint32(len(payload)))
	return mmkvFixture{data: data, metadata: metadata}
}

func encodeMMKVEntries(entries []mmkvEntry) []byte {
	var body bytes.Buffer
	for _, entry := range entries {
		writeMMKVVarint(&body, uint32(len(entry.key)))
		body.Write(entry.key)
		writeMMKVVarint(&body, uint32(len(entry.value)))
		body.Write(entry.value)
	}
	var payload bytes.Buffer
	writeMMKVVarint(&payload, uint32(body.Len()))
	payload.Write(body.Bytes())
	return payload.Bytes()
}

func writeMMKVVarint(buffer *bytes.Buffer, value uint32) {
	for value >= 0x80 {
		buffer.WriteByte(byte(value) | 0x80)
		value >>= 7
	}
	buffer.WriteByte(byte(value))
}

func keyedArchiveFixture(t *testing.T, fields map[string]interface{}) []byte {
	t.Helper()
	return makeKeyedArchiveFixture(t, fields, false)
}

func keyedArchiveUIDContainerFixture(t *testing.T, fields map[string]interface{}) []byte {
	t.Helper()
	return makeKeyedArchiveFixture(t, fields, true)
}

func makeKeyedArchiveFixture(t *testing.T, fields map[string]interface{}, uidArrayContainers bool) []byte {
	t.Helper()
	objects := []interface{}{
		"$null",
		map[string]interface{}{
			"$classes":   []interface{}{"NSDictionary", "NSObject"},
			"$classname": "NSDictionary",
		},
		nil,
	}
	keys := make([]interface{}, 0, len(fields))
	values := make([]interface{}, 0, len(fields))
	fieldNames := make([]string, 0, len(fields))
	for name := range fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)
	for _, name := range fieldNames {
		keyIndex := len(objects)
		objects = append(objects, name)
		keys = append(keys, plist.UID(keyIndex))
		value := fields[name]
		if _, invalid := value.(invalidArchiveUID); invalid {
			values = append(values, plist.UID(len(objects)+10))
			continue
		}
		valueIndex := len(objects)
		objects = append(objects, value)
		values = append(values, plist.UID(valueIndex))
	}
	var archiveKeys interface{} = keys
	var archiveValues interface{} = values
	if uidArrayContainers {
		keysIndex := len(objects)
		objects = append(objects, keys)
		valuesIndex := len(objects)
		objects = append(objects, values)
		archiveKeys = plist.UID(keysIndex)
		archiveValues = plist.UID(valuesIndex)
	}
	objects[2] = map[string]interface{}{
		"$class":     plist.UID(1),
		"NS.keys":    archiveKeys,
		"NS.objects": archiveValues,
	}
	archive := map[string]interface{}{
		"$archiver": "NSKeyedArchiver",
		"$version":  uint64(100000),
		"$top":      map[string]interface{}{"root": plist.UID(2)},
		"$objects":  objects,
	}
	encoded, err := plist.Marshal(archive, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func errorsIsUnsupported(err error) bool {
	return errors.Is(err, voicetools.ErrUnsupportedFile)
}
