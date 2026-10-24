package doubao

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"

	"howett.net/plist"
	"mimac.local/mimac/internal/voicetools"
)

const (
	maxMMKVDataBytes       int64  = 1 << 20
	maxMMKVMetadataBytes   int64  = 64 << 10
	mmkvMetaInfoV4Size            = 112
	mmkvMetaVersionV4      uint32 = 4
	mmkvV4FlagsOffset             = 104
	mmkvV4ActualSizeOffset        = 28
	mmkvIVOffset                  = 12
	mmkvIVSize                    = 16
	mmkvDataPrefixSize            = 4
	maxArchiveObjects             = 1 << 16
)

// readDoubaoMMKV reads a stable metadata/data/metadata snapshot and extracts
// only the allowlisted fields from the final non-empty MMKV key "d" value.
func readDoubaoMMKV(ctx context.Context, dataPath string) (settingsCandidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	metadataPath := dataPath + ".crc"
	metadataBefore, err := voicetools.ReadRegularFile(ctx, metadataPath, maxMMKVMetadataBytes)
	if err != nil {
		return settingsCandidate{}, err
	}
	data, err := voicetools.ReadRegularFile(ctx, dataPath, maxMMKVDataBytes)
	if err != nil {
		return settingsCandidate{}, err
	}
	metadataAfter, err := voicetools.ReadRegularFile(ctx, metadataPath, maxMMKVMetadataBytes)
	if err != nil {
		return settingsCandidate{}, err
	}
	if !bytes.Equal(metadataBefore, metadataAfter) {
		return settingsCandidate{}, voicetools.ErrStaleFile
	}
	if err := ctx.Err(); err != nil {
		return settingsCandidate{}, err
	}

	payload, err := verifiedMMKVPayload(metadataBefore, data)
	if err != nil {
		return settingsCandidate{}, err
	}
	dValue, found, err := lastMMKVValue(payload, []byte("d"))
	if err != nil {
		return settingsCandidate{}, err
	}
	candidate := settingsCandidate{
		values:  make(map[string]json.RawMessage, len(shortcutFields)),
		strings: make(map[string]string, 2),
	}
	if !found || len(dValue) == 0 { // Empty MMKV values are tombstones.
		return candidate, nil
	}

	values, strings, err := parseDoubaoShortcutArchive(dValue)
	if err != nil {
		return settingsCandidate{}, err
	}
	candidate.valuePresent = true
	candidate.values = values
	candidate.strings = strings
	candidate.hasShortcutField = len(values) != 0
	return candidate, nil
}

func verifiedMMKVPayload(metadata, data []byte) ([]byte, error) {
	if int64(len(metadata)) > maxMMKVMetadataBytes || len(metadata) < mmkvMetaInfoV4Size ||
		int64(len(data)) > maxMMKVDataBytes || len(data) < mmkvDataPrefixSize {
		return nil, voicetools.ErrUnsupportedFile
	}
	version := binary.LittleEndian.Uint32(metadata[4:8])
	if version != mmkvMetaVersionV4 {
		return nil, voicetools.ErrUnsupportedFile
	}
	// MMKV records an IV when encryption is enabled. This adapter only accepts
	// the observed plaintext layout and does not attempt any decryption.
	for _, b := range metadata[mmkvIVOffset : mmkvIVOffset+mmkvIVSize] {
		if b != 0 {
			return nil, voicetools.ErrUnsupportedFile
		}
	}
	if binary.LittleEndian.Uint64(metadata[mmkvV4FlagsOffset:mmkvV4FlagsOffset+8]) != 0 {
		return nil, voicetools.ErrUnsupportedFile
	}

	actualSize := uint64(binary.LittleEndian.Uint32(metadata[mmkvV4ActualSizeOffset : mmkvV4ActualSizeOffset+4]))
	if actualSize > uint64(len(data)-mmkvDataPrefixSize) {
		return nil, voicetools.ErrUnsupportedFile
	}
	payload := data[mmkvDataPrefixSize : mmkvDataPrefixSize+int(actualSize)]
	wantCRC := binary.LittleEndian.Uint32(metadata[0:4])
	if crc32.ChecksumIEEE(payload) != wantCRC {
		return nil, voicetools.ErrUnsupportedFile
	}
	return payload, nil
}

func lastMMKVValue(payload, wantedKey []byte) ([]byte, bool, error) {
	// decodeOneMap consumes the initial encoded length, then iterates to the
	// end of the input. Appended records may leave that prefix unchanged.
	_, offset, ok := readMMKVVarint(payload, 0)
	if !ok {
		return nil, false, voicetools.ErrUnsupportedFile
	}
	end := len(payload)
	var selected []byte
	found := false
	for offset < end {
		keySize, next, ok := readMMKVVarint(payload, offset)
		if !ok || uint64(keySize) > uint64(end-next) {
			return nil, false, voicetools.ErrUnsupportedFile
		}
		offset = next
		key := payload[offset : offset+int(keySize)]
		offset += int(keySize)

		valueSize, next, ok := readMMKVVarint(payload, offset)
		if !ok || uint64(valueSize) > uint64(end-next) {
			return nil, false, voicetools.ErrUnsupportedFile
		}
		offset = next
		valueEnd := offset + int(valueSize)
		if bytes.Equal(key, wantedKey) {
			found = true
			if valueSize == 0 {
				selected = nil
			} else {
				selected = payload[offset:valueEnd]
			}
		}
		offset = valueEnd
	}
	return selected, found, nil
}

func readMMKVVarint(data []byte, offset int) (uint32, int, bool) {
	if offset < 0 || offset > len(data) {
		return 0, offset, false
	}
	var value uint32
	for i := 0; i < 5; i++ {
		if offset >= len(data) {
			return 0, offset, false
		}
		b := data[offset]
		offset++
		if i == 4 && (b&0xf0) != 0 {
			return 0, offset, false
		}
		value |= uint32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return value, offset, true
		}
	}
	return 0, offset, false
}

func parseDoubaoShortcutArchive(data []byte) (map[string]json.RawMessage, map[string]string, error) {
	if len(data) < len("bplist00") || !bytes.HasPrefix(data, []byte("bplist00")) {
		return nil, nil, voicetools.ErrUnsupportedFile
	}
	var decoded any
	format, err := plist.Unmarshal(data, &decoded)
	if err != nil || format != plist.BinaryFormat {
		return nil, nil, voicetools.ErrUnsupportedFile
	}
	archive, ok := decoded.(map[string]interface{})
	if !ok || archive["$archiver"] != "NSKeyedArchiver" {
		return nil, nil, voicetools.ErrUnsupportedFile
	}
	objects, ok := archive["$objects"].([]interface{})
	if !ok || len(objects) == 0 || len(objects) > maxArchiveObjects || objects[0] != "$null" {
		return nil, nil, voicetools.ErrUnsupportedFile
	}
	top, ok := archive["$top"].(map[string]interface{})
	if !ok {
		return nil, nil, voicetools.ErrUnsupportedFile
	}
	root, _, err := resolveArchiveUID(objects, top["root"])
	if err != nil {
		return nil, nil, err
	}
	dictionary, ok := root.(map[string]interface{})
	if !ok {
		return nil, nil, voicetools.ErrUnsupportedFile
	}
	if err := requireNSDictionary(objects, dictionary["$class"]); err != nil {
		return nil, nil, err
	}
	keys, err := archiveArray(objects, dictionary["NS.keys"])
	if err != nil {
		return nil, nil, err
	}
	values, err := archiveArray(objects, dictionary["NS.objects"])
	if err != nil {
		return nil, nil, err
	}
	if len(keys) != len(values) || len(keys) > maxArchiveObjects {
		return nil, nil, voicetools.ErrUnsupportedFile
	}

	allowed := make(map[string]fieldSpec, len(shortcutFields))
	for _, field := range shortcutFields {
		allowed[field.name] = field
	}
	result := make(map[string]json.RawMessage, len(shortcutFields))
	displays := make(map[string]string, 2)
	for i, rawKey := range keys {
		keyObject, _, err := resolveArchiveUID(objects, rawKey)
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyObject.(string)
		if !ok {
			return nil, nil, voicetools.ErrUnsupportedFile
		}
		field, isAllowed := allowed[key]
		if !isAllowed {
			// Do not resolve or copy values for unrelated archive keys.
			continue
		}
		if _, duplicate := result[key]; duplicate {
			return nil, nil, voicetools.ErrUnsupportedFile
		}
		value, uidIndex, err := resolveArchiveUID(objects, values[i])
		if err != nil {
			return nil, nil, err
		}
		if uidIndex == 0 {
			value = nil
		}
		var raw json.RawMessage
		if value == nil {
			raw = json.RawMessage("null")
		} else if field.plistType == "string" {
			display, ok := value.(string)
			if !ok {
				return nil, nil, errShortcutFieldType
			}
			raw, err = json.Marshal(display)
			if err != nil {
				return nil, nil, voicetools.ErrUnsupportedFile
			}
			displays[key] = display
		} else {
			if !isArchiveInteger(value) {
				return nil, nil, errShortcutFieldType
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, nil, voicetools.ErrUnsupportedFile
			}
			raw = encoded
		}
		result[key] = raw
	}
	return result, displays, nil
}

func requireNSDictionary(objects []interface{}, classRef interface{}) error {
	classValue, _, err := resolveArchiveUID(objects, classRef)
	if err != nil {
		return err
	}
	classInfo, ok := classValue.(map[string]interface{})
	if !ok {
		return voicetools.ErrUnsupportedFile
	}
	name, ok := classInfo["$classname"].(string)
	if !ok || (name != "NSDictionary" && name != "NSMutableDictionary") {
		return voicetools.ErrUnsupportedFile
	}
	return nil
}

func archiveArray(objects []interface{}, raw interface{}) ([]interface{}, error) {
	if array, ok := raw.([]interface{}); ok {
		return array, nil
	}
	object, _, err := resolveArchiveUID(objects, raw)
	if err != nil {
		return nil, err
	}
	array, ok := object.([]interface{})
	if !ok {
		return nil, voicetools.ErrUnsupportedFile
	}
	return array, nil
}

func resolveArchiveUID(objects []interface{}, raw interface{}) (interface{}, uint64, error) {
	uid, ok := raw.(plist.UID)
	if !ok || uint64(uid) >= uint64(len(objects)) {
		return nil, 0, voicetools.ErrUnsupportedFile
	}
	index := uint64(uid)
	return objects[index], index, nil
}

func isArchiveInteger(value interface{}) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}
