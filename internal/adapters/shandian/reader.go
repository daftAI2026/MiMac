// Package shandian reads the verified, shortcut-related portion of Shandianshuo's
// per-user configuration. It never writes settings or exposes unrelated fields.
package shandian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"mimac.local/mimac/internal/voicetools"
)

const (
	bundleIdentifier           = "cn.shandianshuo.desktop"
	verifiedShortVersion       = "0.8.0"
	verifiedBuildVersion       = "0.8.0"
	bundleName                 = "闪电说.app"
	configRelativePath         = "Library/Application Support/Shandianshuo/config.json"
	maxConfigBytes       int64 = 1 << 20
	verifiedSchema             = 3
)

var (
	ErrUnsupportedPlatform = errors.New("shandian config: macOS bundle metadata is required")
	ErrHomeUnavailable     = errors.New("shandian config: home directory unavailable")
	ErrBundleUnavailable   = errors.New("shandian config: verified bundle unavailable")
	ErrAmbiguousBundle     = errors.New("shandian config: multiple matching bundles")
	ErrUnverifiedBundle    = errors.New("shandian config: bundle version is unverified")
	ErrConfigMissing       = errors.New("shandian config: configuration file missing")
	ErrConfigRead          = errors.New("shandian config: configuration file read failed")
	ErrConfigChanged       = errors.New("shandian config: configuration changed during read")
	ErrUnsupportedConfig   = errors.New("shandian config: unsupported file")
	ErrUnsupportedSchema   = errors.New("shandian config: configuration schema is unverified")
	ErrMalformedConfig     = errors.New("shandian config: malformed or incomplete allowlisted fields")
)

// UnifiedHotkey contains only the observed unified hotkey fields. Key names
// stay verbatim because this reader does not translate them into OS key codes.
type UnifiedHotkey struct {
	Keys      []string `json:"keys"`
	BlockKeys bool     `json:"block_keys"`
}

// Candidate is an all-or-nothing snapshot of the verified config schema. It
// intentionally omits legacy hotkey fields, unrelated settings, and controls.
type Candidate struct {
	BundleIdentifier string        `json:"bundle_id"`
	BundleVersion    string        `json:"bundle_version"`
	BundleBuild      string        `json:"bundle_build"`
	ConfigVersion    int           `json:"config_version"`
	UnifiedHotkey    UnifiedHotkey `json:"unified_hotkey"`
	ShortPressAction string        `json:"short_press_action"`
	LongPressAction  string        `json:"long_press_action"`
}

// ReadInstalled verifies the exact Shandianshuo bundle before reading one
// bounded, regular config file beneath locator.Home. It never launches the app.
func ReadInstalled(ctx context.Context, locator voicetools.Locator) (Candidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Candidate{}, err
	}
	if runtime.GOOS != "darwin" {
		return Candidate{}, ErrUnsupportedPlatform
	}
	if locator.Home == "" {
		return Candidate{}, ErrHomeUnavailable
	}

	bundle, state, err := voicetools.FindBundleInLocator(
		ctx,
		locator,
		voicetools.ApplicationCandidates(locator.ApplicationDirs, bundleName),
		bundleIdentifier,
	)
	if err := ctx.Err(); err != nil {
		return Candidate{}, err
	}
	if state == voicetools.Ambiguous {
		return Candidate{}, ErrAmbiguousBundle
	}
	if err != nil || state != voicetools.Ready {
		return Candidate{}, ErrBundleUnavailable
	}
	if bundle.Identifier != bundleIdentifier ||
		bundle.Version != verifiedShortVersion ||
		bundle.Build != verifiedBuildVersion {
		return Candidate{}, ErrUnverifiedBundle
	}

	configPath := filepath.Join(locator.Home, filepath.FromSlash(configRelativePath))
	data, err := voicetools.ReadRegularFile(ctx, configPath, maxConfigBytes)
	if err != nil {
		return Candidate{}, configReadError(ctx, err)
	}
	candidate, err := decodeConfig(data)
	if err != nil {
		return Candidate{}, err
	}
	candidate.BundleIdentifier = bundle.Identifier
	candidate.BundleVersion = bundle.Version
	candidate.BundleBuild = bundle.Build
	return candidate, nil
}

func configReadError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	switch {
	case errors.Is(err, voicetools.ErrFileMissing):
		return ErrConfigMissing
	case errors.Is(err, voicetools.ErrStaleFile):
		return ErrConfigChanged
	case errors.Is(err, voicetools.ErrUnsupportedFile), errors.Is(err, voicetools.ErrFileTooLarge):
		return ErrUnsupportedConfig
	default:
		return ErrConfigRead
	}
}

func decodeConfig(data []byte) (Candidate, error) {
	if !utf8.Valid(data) {
		return Candidate{}, ErrMalformedConfig
	}
	root, err := allowlistedObject(data, "config_version", "unified_hotkey", "short_press_action", "long_press_action")
	if err != nil {
		return Candidate{}, ErrMalformedConfig
	}

	versionRaw, exists := root["config_version"]
	if !exists || isJSONNull(versionRaw) {
		return Candidate{}, ErrUnsupportedSchema
	}
	var version int
	if json.Unmarshal(versionRaw, &version) != nil || version != verifiedSchema {
		return Candidate{}, ErrUnsupportedSchema
	}

	hotkeyRaw, exists := root["unified_hotkey"]
	if !exists || isJSONNull(hotkeyRaw) {
		return Candidate{}, ErrMalformedConfig
	}
	hotkeyFields, err := allowlistedObject(hotkeyRaw, "keys", "block_keys")
	if err != nil {
		return Candidate{}, ErrMalformedConfig
	}
	var rawKeys []json.RawMessage
	if err := unmarshalRequired(hotkeyFields, "keys", &rawKeys); err != nil || rawKeys == nil {
		return Candidate{}, ErrMalformedConfig
	}
	hotkey := UnifiedHotkey{Keys: make([]string, len(rawKeys))}
	for index, rawKey := range rawKeys {
		var key string
		if isJSONNull(rawKey) || json.Unmarshal(rawKey, &key) != nil || strings.TrimSpace(key) == "" {
			return Candidate{}, ErrMalformedConfig
		}
		hotkey.Keys[index] = key
	}
	if err := unmarshalRequired(hotkeyFields, "block_keys", &hotkey.BlockKeys); err != nil {
		return Candidate{}, ErrMalformedConfig
	}

	var shortAction, longAction string
	if err := unmarshalRequired(root, "short_press_action", &shortAction); err != nil || strings.TrimSpace(shortAction) == "" {
		return Candidate{}, ErrMalformedConfig
	}
	if err := unmarshalRequired(root, "long_press_action", &longAction); err != nil || strings.TrimSpace(longAction) == "" {
		return Candidate{}, ErrMalformedConfig
	}

	return Candidate{
		ConfigVersion:    version,
		UnifiedHotkey:    hotkey,
		ShortPressAction: shortAction,
		LongPressAction:  longAction,
	}, nil
}

// allowlistedObject parses a JSON object while retaining only the named keys.
// Duplicate allowlisted keys are rejected to avoid parser-dependent values.
func allowlistedObject(data []byte, allowlistedKeys ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrMalformedConfig
	}

	allowed := make(map[string]struct{}, len(allowlistedKeys))
	for _, key := range allowlistedKeys {
		allowed[key] = struct{}{}
	}
	fields := make(map[string]json.RawMessage, len(allowlistedKeys))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrMalformedConfig
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrMalformedConfig
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, ErrMalformedConfig
		}
		if _, retain := allowed[key]; !retain {
			continue
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, ErrMalformedConfig
		}
		fields[key] = raw
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrMalformedConfig
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrMalformedConfig
	}
	return fields, nil
}

func unmarshalRequired(fields map[string]json.RawMessage, name string, destination any) error {
	raw, exists := fields[name]
	if !exists || isJSONNull(raw) {
		return ErrMalformedConfig
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return ErrMalformedConfig
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
