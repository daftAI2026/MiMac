// Package wetype reads only the statically allowlisted shortcut settings from
// the verified WeType MMKV store. It does not interpret key codes or actions.
package wetype

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"path/filepath"
	"runtime"
	"unicode/utf8"

	"mimac.local/mimac/internal/voicetools"
)

const (
	bundleIdentifier     = "com.tencent.inputmethod.wetype"
	verifiedShortVersion = "3.0.0"
	verifiedBuildVersion = "670"
	bundleName           = "WeType.app"
	storeDirectory       = "WeType/mmkv"
	storeName            = "wetype.settings"

	maxStoreDataBytes     int64 = 1 << 20
	maxStoreMetadataBytes int64 = 64 << 10
	mmkvMetaV3Size              = 104
	mmkvVersionOffset           = 4
	mmkvIVOffset                = 12
	mmkvIVSize                  = 16
	mmkvActualSizeOffset        = 28
	mmkvDataPrefixSize          = 4
	maxMMKVRecords              = 1 << 16
)

var (
	ErrUnsupportedPlatform = errors.New("wetype config: unsupported platform")
	ErrBundleUnavailable   = errors.New("wetype config: verified bundle unavailable")
	ErrUnverifiedBundle    = errors.New("wetype config: bundle version is unverified")
	ErrAmbiguousBundle     = errors.New("wetype config: multiple matching bundles")
	ErrUnsupportedFormat   = errors.New("wetype config: unsupported MMKV format")
	ErrEncryptedStore      = errors.New("wetype config: encrypted MMKV store is unsupported")
	ErrIntegrity           = errors.New("wetype config: MMKV integrity check failed")
	ErrMalformedValue      = errors.New("wetype config: allowlisted value is malformed")
	ErrMalformedRecord     = errors.New("wetype config: MMKV record is malformed")
)

var allowlistedFields = []fieldSpec{
	{name: "voicePTTShortcut_keyCodes", valueType: "MMKV string containing JSON [Int]", jsonInts: true},
	{name: "voicePTTShortcut_modifiers", valueType: "MMKV Int32"},
	{name: "voicePTTShortcut_tapCount", valueType: "MMKV Int32"},
	{name: "voiceToggleShortcut_keyCodes", valueType: "MMKV string containing JSON [Int]", jsonInts: true},
	{name: "voiceToggleShortcut_modifiers", valueType: "MMKV Int32"},
	{name: "voiceToggleShortcut_tapCount", valueType: "MMKV Int32"},
}

type fieldSpec struct {
	name      string
	valueType string
	jsonInts  bool
}

// FieldCandidate carries a typed raw value without assigning shortcut or
// action semantics. A missing MMKV key has status "missing" and no value.
type FieldCandidate struct {
	Status string          `json:"status"`
	Type   string          `json:"type"`
	Value  json.RawMessage `json:"value,omitempty"`
}

// Candidate includes the verified bundle identity and only the six
// allowlisted MMKV fields. Paths and unrelated MMKV records are not exposed.
type Candidate struct {
	BundleIdentifier string                    `json:"bundleIdentifier"`
	BundleVersion    string                    `json:"bundleVersion"`
	BundleBuild      string                    `json:"bundleBuild"`
	MMKVVersion      uint32                    `json:"mmkvVersion"`
	Fields           map[string]FieldCandidate `json:"fields"`
}

// ReadInstalled uses shared bundle discovery to verify WeType, then reads the
// corresponding user's fixed MMKV locator. With the default locator, shared
// discovery also checks registered application paths. It never launches the
// bundle or inspects other user files.
func ReadInstalled(ctx context.Context, locator voicetools.Locator) (Candidate, error) {
	candidate, _, _, err := readInstalled(ctx, locator)
	return candidate, err
}

func readInstalled(ctx context.Context, locator voicetools.Locator) (Candidate, voicetools.Bundle, voicetools.State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Candidate{}, voicetools.Bundle{}, voicetools.ReadError, err
	}
	if runtime.GOOS != "darwin" {
		return Candidate{}, voicetools.Bundle{}, voicetools.Unsupported, ErrUnsupportedPlatform
	}
	if locator.Home == "" {
		return Candidate{}, voicetools.Bundle{}, voicetools.ReadError, ErrBundleUnavailable
	}

	bundle, state, err := voicetools.FindBundleInLocator(
		ctx,
		locator,
		voicetools.ApplicationCandidates(locator.InputMethodDirs, bundleName),
		bundleIdentifier,
	)
	if err != nil {
		return Candidate{}, bundle, state, ErrBundleUnavailable
	}
	if !isVerifiedBundle(bundle) {
		if state == voicetools.Ambiguous {
			return Candidate{}, bundle, state, ErrAmbiguousBundle
		}
		if state != voicetools.Ready {
			return Candidate{}, bundle, state, ErrBundleUnavailable
		}
		return Candidate{}, bundle, state, ErrUnverifiedBundle
	}

	dataPath := settingsPath(locator)
	candidate, err := readStore(ctx, dataPath)
	if err != nil {
		return Candidate{}, bundle, state, err
	}
	candidate.BundleIdentifier = bundle.Identifier
	candidate.BundleVersion = bundle.Version
	candidate.BundleBuild = bundle.Build
	return candidate, bundle, state, nil
}

func settingsPath(locator voicetools.Locator) string {
	return filepath.Join(locator.Home, "Library", "Application Support", storeDirectory, storeName)
}

func isVerifiedBundle(bundle voicetools.Bundle) bool {
	return bundle.Identifier == bundleIdentifier && bundle.Version == verifiedShortVersion && bundle.Build == verifiedBuildVersion
}

func readStore(ctx context.Context, dataPath string) (Candidate, error) {
	metadataPath := dataPath + ".crc"
	metadataBefore, err := voicetools.ReadRegularFile(ctx, metadataPath, maxStoreMetadataBytes)
	if err != nil {
		return Candidate{}, err
	}
	data, err := voicetools.ReadRegularFile(ctx, dataPath, maxStoreDataBytes)
	if err != nil {
		return Candidate{}, err
	}
	metadataAfter, err := voicetools.ReadRegularFile(ctx, metadataPath, maxStoreMetadataBytes)
	if err != nil {
		return Candidate{}, err
	}
	if !bytes.Equal(metadataBefore, metadataAfter) {
		return Candidate{}, voicetools.ErrStaleFile
	}
	if err := ctx.Err(); err != nil {
		return Candidate{}, err
	}
	return decodeStore(metadataBefore, data)
}

func decodeStore(metadata, data []byte) (Candidate, error) {
	if int64(len(metadata)) > maxStoreMetadataBytes || len(metadata) < mmkvMetaV3Size ||
		int64(len(data)) > maxStoreDataBytes || len(data) < mmkvDataPrefixSize {
		return Candidate{}, ErrUnsupportedFormat
	}
	version := binary.LittleEndian.Uint32(metadata[mmkvVersionOffset : mmkvVersionOffset+4])
	if version != 3 {
		return Candidate{}, ErrUnsupportedFormat
	}
	for _, b := range metadata[mmkvIVOffset : mmkvIVOffset+mmkvIVSize] {
		if b != 0 {
			return Candidate{}, ErrEncryptedStore
		}
	}

	actualSize := uint64(binary.LittleEndian.Uint32(metadata[mmkvActualSizeOffset : mmkvActualSizeOffset+4]))
	if actualSize > uint64(len(data)-mmkvDataPrefixSize) {
		return Candidate{}, ErrUnsupportedFormat
	}
	if uint64(binary.LittleEndian.Uint32(data[:mmkvDataPrefixSize])) != actualSize {
		return Candidate{}, ErrUnsupportedFormat
	}
	payload := data[mmkvDataPrefixSize : mmkvDataPrefixSize+int(actualSize)]
	wantCRC := binary.LittleEndian.Uint32(metadata[:4])
	if crc32.ChecksumIEEE(payload) != wantCRC {
		return Candidate{}, ErrIntegrity
	}

	entries, err := allowlistedEntries(payload)
	if err != nil {
		return Candidate{}, err
	}
	fields := make(map[string]FieldCandidate, len(allowlistedFields))
	for _, spec := range allowlistedFields {
		field := FieldCandidate{Status: "missing", Type: spec.valueType}
		if value, present := entries[spec.name]; present {
			var encoded json.RawMessage
			if spec.jsonInts {
				encoded, err = decodeKeyCodeArray(value)
			} else {
				encoded, err = decodeInt32(value)
			}
			if err != nil {
				return Candidate{}, ErrMalformedValue
			}
			field.Status = "present"
			field.Value = encoded
		}
		fields[spec.name] = field
	}
	return Candidate{MMKVVersion: version, Fields: fields}, nil
}

func allowlistedEntries(payload []byte) (map[string][]byte, error) {
	selected := make(map[string][]byte, len(allowlistedFields))
	if len(payload) == 0 {
		return selected, nil
	}
	_, offset, ok := readMMKVVarint32(payload, 0)
	if !ok {
		return nil, ErrMalformedRecord
	}
	allowed := make(map[string]struct{}, len(allowlistedFields))
	for _, spec := range allowlistedFields {
		allowed[spec.name] = struct{}{}
	}
	for recordCount := 0; offset < len(payload); recordCount++ {
		if recordCount >= maxMMKVRecords {
			return nil, ErrUnsupportedFormat
		}
		keySize, next, ok := readMMKVVarint32(payload, offset)
		if !ok || uint64(keySize) > uint64(len(payload)-next) {
			return nil, ErrMalformedRecord
		}
		offset = next
		keyBytes := payload[offset : offset+int(keySize)]
		offset += int(keySize)
		valueSize, next, ok := readMMKVVarint32(payload, offset)
		if !ok || uint64(valueSize) > uint64(len(payload)-next) {
			return nil, ErrMalformedRecord
		}
		offset = next
		valueEnd := offset + int(valueSize)
		key := string(keyBytes)
		if _, isAllowed := allowed[key]; isAllowed {
			if valueSize == 0 {
				delete(selected, key)
			} else {
				selected[key] = payload[offset:valueEnd]
			}
		}
		offset = valueEnd
	}
	return selected, nil
}

func decodeKeyCodeArray(encoded []byte) (json.RawMessage, error) {
	length, offset, ok := readMMKVVarint32(encoded, 0)
	if !ok || uint64(length) != uint64(len(encoded)-offset) {
		return nil, ErrMalformedValue
	}
	text := encoded[offset:]
	if !utf8.Valid(text) {
		return nil, ErrMalformedValue
	}
	trimmed := bytes.TrimSpace(text)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, ErrMalformedValue
	}
	var rawCodes []json.RawMessage
	if err := json.Unmarshal(text, &rawCodes); err != nil || rawCodes == nil {
		return nil, ErrMalformedValue
	}
	codes := make([]int, len(rawCodes))
	for i, rawCode := range rawCodes {
		rawCode = bytes.TrimSpace(rawCode)
		if len(rawCode) == 0 || (rawCode[0] != '-' && (rawCode[0] < '0' || rawCode[0] > '9')) {
			return nil, ErrMalformedValue
		}
		if err := json.Unmarshal(rawCode, &codes[i]); err != nil {
			return nil, ErrMalformedValue
		}
	}
	return json.Marshal(codes)
}

func decodeInt32(encoded []byte) (json.RawMessage, error) {
	value, offset, ok := readMMKVVarint64(encoded, 0)
	if !ok || offset != len(encoded) {
		return nil, ErrMalformedValue
	}
	var decoded int32
	switch {
	case value <= uint64(1<<31-1):
		decoded = int32(value)
	case value >= 0xffffffff80000000:
		decoded = int32(uint32(value))
	default:
		return nil, ErrMalformedValue
	}
	return json.Marshal(decoded)
}

func readMMKVVarint32(data []byte, offset int) (uint32, int, bool) {
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

func readMMKVVarint64(data []byte, offset int) (uint64, int, bool) {
	if offset < 0 || offset > len(data) {
		return 0, offset, false
	}
	var value uint64
	for i := 0; i < 10; i++ {
		if offset >= len(data) {
			return 0, offset, false
		}
		b := data[offset]
		offset++
		if i == 9 && b > 1 {
			return 0, offset, false
		}
		value |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return value, offset, true
		}
	}
	return 0, offset, false
}
