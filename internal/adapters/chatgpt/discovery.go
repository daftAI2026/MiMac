package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/voicetools"
)

const (
	bundleIdentifier     = "com.openai.codex"
	verifiedShortVersion = "26.1007.71326"
	verifiedBuildVersion = "20398"
	keybindingsLimit     = 1 << 20
)

type commandDescriptor struct {
	id      string
	name    string
	scope   string
	gesture string
}

var allowedCommands = []commandDescriptor{
	{id: "composer.startVoiceMode", name: "语音对话", scope: "app", gesture: "toggle"},
	{id: "realtimeVoice.endCall", name: "结束语音对话", scope: "app", gesture: "end"},
}

const (
	defaultVoiceShortcut = "Ctrl+Shift+V"
	keybindingsSource    = "keybindings.json#allowlisted-command-rows; verified running application environment"
)

// Discover reads only allowlisted command rows from keybindings.json. It never
// invokes ChatGPT IPC or reads adjacent configuration fields.
func Discover(ctx context.Context, locator voicetools.Locator) voicetools.Snapshot {
	return discover(ctx, locator, resolveRunningConfigHome)
}

type configHomeResolver func(context.Context, string) (string, voicetools.State, error)

func discover(ctx context.Context, locator voicetools.Locator, resolveHome configHomeResolver) voicetools.Snapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	result := voicetools.Snapshot{State: voicetools.ReadError, Source: "discovery"}
	if err := ctx.Err(); err != nil {
		result.Source = "cancelled"
		return result
	}
	if runtime.GOOS != "darwin" {
		result.State = voicetools.Unsupported
		result.Source = "macOS bundle metadata is required"
		return result
	}
	if locator.Home == "" {
		result.Source = "home directory unavailable"
		return result
	}

	bundle, state, err := LocateApplication(ctx, locator)
	if err != nil {
		result.State = state
		result.AppPath = bundle.Path
		result.Source = "ChatGPT bundle metadata could not be read"
		return result
	}
	if state != voicetools.Ready {
		result.State = state
		result.AppPath = bundle.Path
		result.Source = "ChatGPT app bundle identity"
		return result
	}
	result.AppPath = bundle.Path
	result.Version = formatBundleVersion(bundle.Version, bundle.Build)
	if bundle.Version != verifiedShortVersion || bundle.Build != verifiedBuildVersion {
		result.State = voicetools.Unsupported
		result.Source = "unverified ChatGPT version"
		return result
	}

	codexHome, state, err := resolveHome(ctx, bundle.Path)
	if err != nil || state != voicetools.Ready {
		result.State = state
		if result.State == voicetools.Ready || result.State == "" {
			result.State = voicetools.ReadError
		}
		result.Source = "running ChatGPT configuration directory unavailable"
		if ctx.Err() != nil {
			result.Source = "cancelled"
		}
		return result
	}
	if !filepath.IsAbs(codexHome) {
		result.State = voicetools.Unsupported
		result.Source = "CODEX_HOME is not an absolute path"
		return result
	}
	settingsPath := filepath.Join(codexHome, "keybindings.json")
	result.SettingsPath = settingsPath

	data, err := voicetools.ReadRegularFile(ctx, settingsPath, keybindingsLimit)
	if err != nil {
		switch {
		case errors.Is(err, voicetools.ErrFileMissing):
			result.State = voicetools.Ready
			result.Actions = defaultActions()
			result.Source = "defaults:verified composer.startVoiceMode platform default; keybindings.json missing; verified running application environment"
		case errors.Is(err, voicetools.ErrUnsupportedFile), errors.Is(err, voicetools.ErrFileTooLarge):
			result.State = voicetools.Unsupported
			result.Source = "keybindings.json unsupported file"
		default:
			result.State = voicetools.ReadError
			if ctx.Err() != nil {
				result.Source = "cancelled"
			} else {
				result.Source = "keybindings.json read"
			}
		}
		return result
	}
	if err := ctx.Err(); err != nil {
		result.State = voicetools.ReadError
		result.Source = "cancelled"
		return result
	}

	actions, parseState, usedDefault := parseKeybindings(data)
	if parseState != voicetools.Ready {
		result.State = parseState
		result.Source = "keybindings.json unsupported format"
		return result
	}
	result.State = voicetools.Ready
	result.Actions = actions
	result.Source = keybindingsSource
	if usedDefault {
		result.Source = "defaults:verified composer.startVoiceMode platform default; " + keybindingsSource
	}
	return result
}

func appCandidatePaths(locator voicetools.Locator) []string {
	return voicetools.ApplicationCandidates(locator.ApplicationDirs, "ChatGPT.app", "Codex.app")
}

func parseKeybindings(data []byte) ([]voicetools.Action, voicetools.State, bool) {
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return nil, voicetools.Unsupported, false
	}

	descriptorByID := make(map[string]commandDescriptor, len(allowedCommands))
	for _, descriptor := range allowedCommands {
		descriptorByID[descriptor.id] = descriptor
	}
	seen := make(map[string]bool, len(allowedCommands))
	disabled := make(map[string]bool, len(allowedCommands))
	candidates := make(map[string][]voicetools.Candidate, len(allowedCommands))
	for _, rawRow := range rows {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(rawRow, &row); err != nil || row == nil {
			return nil, voicetools.Unsupported, false
		}
		var command string
		if raw, exists := row["command"]; !exists || json.Unmarshal(raw, &command) != nil {
			return nil, voicetools.Unsupported, false
		}
		rawKey, exists := row["key"]
		if !exists {
			return nil, voicetools.Unsupported, false
		}
		var key *string
		if err := json.Unmarshal(rawKey, &key); err != nil {
			return nil, voicetools.Unsupported, false
		}
		descriptor, allowed := descriptorByID[command]
		if !allowed {
			continue
		}
		seen[command] = true
		if key == nil {
			disabled[command] = true
			// The app treats any explicit null row as disabling the command,
			// including when other rows for that command also contain keys.
			// Preserve every row so consumers can distinguish this from absence.
			candidates[descriptor.id] = append(candidates[descriptor.id], voicetools.Candidate{Raw: "null"})
			continue
		}
		candidates[descriptor.id] = append(candidates[descriptor.id], normalizeCandidate(*key))
	}

	usedDefault := !seen["composer.startVoiceMode"]
	actions := make([]voicetools.Action, 0, len(allowedCommands))
	for _, descriptor := range allowedCommands {
		if descriptor.id == "composer.startVoiceMode" && usedDefault {
			actions = append(actions, defaultActions()[0])
			continue
		}
		actions = append(actions, voicetools.Action{
			ID:         descriptor.id,
			Name:       descriptor.name,
			Scope:      descriptor.scope,
			Gesture:    descriptor.gesture,
			Disabled:   disabled[descriptor.id],
			Candidates: candidates[descriptor.id],
		})
	}
	return actions, voicetools.Ready, usedDefault
}

func defaultActions() []voicetools.Action {
	return []voicetools.Action{
		{
			ID:      "composer.startVoiceMode",
			Name:    "语音对话",
			Scope:   "app",
			Gesture: "toggle",
			Candidates: []voicetools.Candidate{{
				Raw:       defaultVoiceShortcut,
				Shortcut:  "Control+Shift+V",
				Supported: false,
			}},
		},
		{ID: "realtimeVoice.endCall", Name: "结束语音对话", Scope: "app", Gesture: "end"},
	}
}

func normalizeCandidate(raw string) voicetools.Candidate {
	candidate := voicetools.Candidate{Raw: raw, Supported: false}
	parts := strings.Split(raw, "+")
	for i, part := range parts {
		switch part {
		case "Ctrl":
			parts[i] = "Control"
		case "Cmd":
			parts[i] = "Command"
		}
	}
	keys, err := keyoutput.ParseShortcut(strings.Join(parts, "+"))
	if err != nil {
		return candidate
	}
	shortcut, err := keyoutput.FormatShortcut(keys)
	if err != nil {
		return candidate
	}
	candidate.Shortcut = shortcut
	return candidate
}

func formatBundleVersion(version, build string) string {
	if build == "" || build == version {
		return version
	}
	return version + " (" + build + ")"
}
