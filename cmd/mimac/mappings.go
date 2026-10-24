package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/voicetools"
)

const legacyXiaomiProfileID = "xiaomi-rc003"

type mappingProfile struct {
	Version     int                                        `json:"version"`
	Profiles    map[string]map[string]string               `json:"profiles"`
	ToolActions map[string]map[string]voicetools.ActionRef `json:"tool_actions,omitempty"`
}

func emptyProfile() mappingProfile {
	return mappingProfile{Version: 3, Profiles: make(map[string]map[string]string)}
}

// shortcuts returns the selected profile's stored actions without exposing the
// mapping store's mutable maps to UI code.
func (p mappingProfile) shortcuts(profileID string) map[string]string {
	stored := p.Profiles[profileID]
	result := make(map[string]string, len(stored))
	for buttonID, shortcut := range stored {
		result[buttonID] = shortcut
	}
	return result
}

// actionRefs returns selected actions only for buttons owned by profile.
// Unknown but valid stored IDs remain in the profile for a future plugin.
func (p mappingProfile) actionRefs(profile devices.Profile) map[string]voicetools.ActionRef {
	result := make(map[string]voicetools.ActionRef)
	if profile.ID == "" {
		return result
	}
	for buttonID, ref := range p.ToolActions[profile.ID] {
		if _, exists := profile.Button(buttonID); exists {
			result[buttonID] = ref
		}
	}
	return result
}

// keys resolves only buttons owned by the selected device profile. Valid
// mappings for other profiles and unknown buttons remain stored but never run.
func (p mappingProfile) keys(profile devices.Profile) (map[string][]bridge.Key, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	result := make(map[string][]bridge.Key)
	if profile.ID == "" {
		return result, nil
	}
	stored := p.Profiles[profile.ID]
	for _, button := range profile.Buttons {
		shortcut, exists := stored[button.ID]
		if !exists || shortcut == "" {
			continue
		}
		keys, err := keyoutput.ParseShortcut(shortcut)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", button.ID, err)
		}
		result[button.ID] = keys
	}
	return result, nil
}

func (p mappingProfile) validate() error {
	if p.Version != 3 || p.Profiles == nil {
		return errors.New("unsupported mapping profile")
	}
	for profileID, buttons := range p.Profiles {
		if !validMappingID(profileID) {
			return fmt.Errorf("invalid device profile ID %q", profileID)
		}
		if buttons == nil {
			return fmt.Errorf("invalid button map for profile %q", profileID)
		}
		for buttonID, shortcut := range buttons {
			if !validMappingID(buttonID) {
				return fmt.Errorf("invalid physical button ID %q", buttonID)
			}
			if shortcut == "" {
				continue
			}
			if _, err := keyoutput.ParseShortcut(shortcut); err != nil {
				return fmt.Errorf("%s/%s: %w", profileID, buttonID, err)
			}
		}
	}
	for profileID, actions := range p.ToolActions {
		if !validMappingID(profileID) {
			return fmt.Errorf("invalid device profile ID %q", profileID)
		}
		if actions == nil {
			return fmt.Errorf("invalid tool-action map for profile %q", profileID)
		}
		for buttonID, ref := range actions {
			if !validMappingID(buttonID) {
				return fmt.Errorf("invalid physical button ID %q", buttonID)
			}
			if _, conflict := p.Profiles[profileID][buttonID]; conflict {
				return fmt.Errorf("conflicting mapping for %s/%s", profileID, buttonID)
			}
			if err := ref.Validate(); err != nil {
				return fmt.Errorf("%s/%s: %w", profileID, buttonID, err)
			}
		}
	}
	return nil
}

func validMappingID(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, '\x00')
}

// withShortcut returns a copied store with exactly one action changed for the
// selected profile, plus the executable mapping for that profile.
func (p mappingProfile) withShortcut(profile devices.Profile, buttonID, shortcut string) (mappingProfile, map[string][]bridge.Key, error) {
	if err := p.validate(); err != nil {
		return mappingProfile{}, nil, err
	}
	if !validMappingID(profile.ID) {
		return mappingProfile{}, nil, errors.New("no active device profile")
	}
	if _, exists := profile.Button(buttonID); !exists {
		return mappingProfile{}, nil, fmt.Errorf("unknown physical button %q for profile %q", buttonID, profile.ID)
	}
	if shortcut != "" {
		keys, err := keyoutput.ParseShortcut(shortcut)
		if err != nil {
			return mappingProfile{}, nil, err
		}
		shortcut, err = keyoutput.FormatShortcut(keys)
		if err != nil {
			return mappingProfile{}, nil, err
		}
	}

	next := cloneMappingProfile(p)
	buttons := next.Profiles[profile.ID]
	if buttons == nil {
		buttons = make(map[string]string)
		next.Profiles[profile.ID] = buttons
	}
	buttons[buttonID] = shortcut
	delete(next.ToolActions[profile.ID], buttonID)
	keys, err := next.keys(profile)
	if err != nil {
		return mappingProfile{}, nil, err
	}
	return next, keys, nil
}

// withToolAction returns a copied store with a selected tool action at one
// known button. It removes any ordinary mapping at that position.
func (p mappingProfile) withToolAction(profile devices.Profile, buttonID string, ref voicetools.ActionRef) (mappingProfile, error) {
	if err := p.validate(); err != nil {
		return mappingProfile{}, err
	}
	if !validMappingID(profile.ID) {
		return mappingProfile{}, errors.New("no active device profile")
	}
	if _, exists := profile.Button(buttonID); !exists {
		return mappingProfile{}, fmt.Errorf("unknown physical button %q for profile %q", buttonID, profile.ID)
	}
	if err := ref.Validate(); err != nil {
		return mappingProfile{}, err
	}

	next := cloneMappingProfile(p)
	delete(next.Profiles[profile.ID], buttonID)
	if next.ToolActions == nil {
		next.ToolActions = make(map[string]map[string]voicetools.ActionRef)
	}
	actions := next.ToolActions[profile.ID]
	if actions == nil {
		actions = make(map[string]voicetools.ActionRef)
		next.ToolActions[profile.ID] = actions
	}
	actions[buttonID] = ref
	return next, nil
}

func cloneMappingProfile(p mappingProfile) mappingProfile {
	copy := mappingProfile{
		Version:  p.Version,
		Profiles: make(map[string]map[string]string, len(p.Profiles)),
	}
	for profileID, buttons := range p.Profiles {
		buttonCopy := make(map[string]string, len(buttons))
		for buttonID, shortcut := range buttons {
			buttonCopy[buttonID] = shortcut
		}
		copy.Profiles[profileID] = buttonCopy
	}
	if p.ToolActions != nil {
		copy.ToolActions = make(map[string]map[string]voicetools.ActionRef, len(p.ToolActions))
		for profileID, actions := range p.ToolActions {
			actionCopy := make(map[string]voicetools.ActionRef, len(actions))
			for buttonID, ref := range actions {
				actionCopy[buttonID] = ref
			}
			copy.ToolActions[profileID] = actionCopy
		}
	}
	return copy
}

type mappingFileHeader struct {
	Version     int             `json:"version"`
	Buttons     json.RawMessage `json:"buttons"`
	Profiles    json.RawMessage `json:"profiles"`
	ToolActions json.RawMessage `json:"tool_actions"`
}

func readProfile(path string) (mappingProfile, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyProfile(), nil
	}
	if err != nil {
		return mappingProfile{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return mappingProfile{}, errors.New("invalid mapping file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return mappingProfile{}, errors.New("mapping file too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var header mappingFileHeader
	if err = d.Decode(&header); err != nil {
		return mappingProfile{}, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return mappingProfile{}, errors.New("trailing mapping data")
	}

	var profile mappingProfile
	switch header.Version {
	case 1:
		if len(header.Buttons) == 0 || len(header.Profiles) != 0 || len(header.ToolActions) != 0 {
			return mappingProfile{}, errors.New("invalid version 1 mapping file")
		}
		var buttons map[string]string
		if err := json.Unmarshal(header.Buttons, &buttons); err != nil || buttons == nil {
			return mappingProfile{}, errors.New("invalid version 1 button map")
		}
		profile = emptyProfile()
		profile.Profiles[legacyXiaomiProfileID] = buttons
	case 2:
		if len(header.Profiles) == 0 || len(header.Buttons) != 0 || len(header.ToolActions) != 0 {
			return mappingProfile{}, errors.New("invalid version 2 mapping file")
		}
		var profiles map[string]map[string]string
		if err := json.Unmarshal(header.Profiles, &profiles); err != nil || profiles == nil {
			return mappingProfile{}, errors.New("invalid version 2 profiles map")
		}
		profile = mappingProfile{Version: 3, Profiles: profiles}
	case 3:
		if len(header.Profiles) == 0 || len(header.Buttons) != 0 {
			return mappingProfile{}, errors.New("invalid version 3 mapping file")
		}
		var profiles map[string]map[string]string
		if err := json.Unmarshal(header.Profiles, &profiles); err != nil || profiles == nil {
			return mappingProfile{}, errors.New("invalid version 3 profiles map")
		}
		var actions map[string]map[string]voicetools.ActionRef
		if len(header.ToolActions) != 0 {
			decoder := json.NewDecoder(bytes.NewReader(header.ToolActions))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&actions); err != nil || actions == nil {
				return mappingProfile{}, errors.New("invalid version 3 tool-actions map")
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return mappingProfile{}, errors.New("trailing version 3 tool-actions data")
			}
		}
		profile = mappingProfile{Version: 3, Profiles: profiles, ToolActions: actions}
	default:
		return mappingProfile{}, errors.New("unsupported mapping file version")
	}
	if err := profile.validate(); err != nil {
		return mappingProfile{}, err
	}
	return profile, nil
}

// saveProfile atomically writes a complete, validated version 3 store. Reading
// a version 1 or 2 file never rewrites it; the first successful save migrates it.
func saveProfile(path string, p mappingProfile) error {
	if err := p.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".mappings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s *shell) loadMappings() {
	defer s.syncVoiceBinding()
	dir, err := os.UserConfigDir()
	if err == nil {
		s.mappingPath = filepath.Join(dir, "MiMac", "mappings.json")
		s.profile, err = readProfile(s.mappingPath)
	}
	if err != nil {
		s.profile = emptyProfile()
		s.mappingLoadFailed = true
		s.mappingError = "无法读取按键配置，原文件已保留"
	}
	if s.coordinator != nil {
		profile := s.activeProfile()
		s.coordinator.mappings, _ = s.profile.keys(profile)
		s.coordinator.actions = s.profile.actionRefs(profile)
	}
}

func (s *shell) saveMapping(id, shortcut string) {
	if id != "" && id == s.voiceButtonID() {
		s.mappingError = "语音键由语音工具控制"
		return
	}
	if s.mappingLoadFailed {
		return
	}
	profile := s.activeProfile()
	next, keys, err := s.profile.withShortcut(profile, id, shortcut)
	if err != nil {
		s.mappingError = "快捷键或按键配置不受支持"
		return
	}
	// Freeze this operation at a physical idle boundary, rather than changing
	// what an already-held button means or losing an up edge.
	if s.coordinator != nil && s.coordinator.report().Pressed != 0 {
		s.mappingError = "请先松开遥控器按键，再录入快捷键"
		return
	}
	if err := saveProfile(s.mappingPath, next); err != nil {
		s.mappingError = "保存失败，仍使用原映射"
		return
	}
	s.profile = next
	if s.coordinator != nil {
		s.coordinator.mappings = keys
		s.coordinator.actions = next.actionRefs(profile)
	}
	s.mappingError = ""
	s.endShortcutRecording()
	s.refreshRemoteOutput(false)
	s.invalidateDeviceView()
}
