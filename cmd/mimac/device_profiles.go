package main

import (
	"errors"
	"image"
	"mimac.local/mimac/internal/devices"
)

// Each statically composed device plugin owns both its lifecycle module and
// the immutable presentation contract. The shell knows neither a brand's
// physical layout nor its list of buttons.
type devicePlugin struct {
	ModuleID      string
	VoiceButtonID string
	Profile       devices.Profile
	Discover      func() (deviceDiscovery, error)
	Artwork       func() (image.Image, error)
}

type deviceDiscovery struct {
	Count     int
	Supported bool
}

func (s *shell) activeDevicePlugin() devicePlugin {
	for _, plugin := range s.deviceProfiles {
		if plugin.Profile.ID == s.selectedProfile {
			return plugin
		}
	}
	if len(s.deviceProfiles) > 0 {
		return s.deviceProfiles[0]
	}
	return devicePlugin{}
}
func (s *shell) activeProfile() devices.Profile { return s.activeDevicePlugin().Profile }
func (s *shell) deviceModule() string           { return s.activeDevicePlugin().ModuleID }
func (s *shell) voiceButtonID() string          { return s.activeDevicePlugin().VoiceButtonID }
func (s *shell) buttonLabel(id string) string {
	if button, ok := s.activeProfile().Button(id); ok {
		return button.Label
	}
	return id
}

func (s *shell) selectDeviceProfile(id string) error {
	if id == s.activeProfile().ID {
		return nil
	}
	if s.remoteEnabled() {
		return errors.New("disable remote before switching device")
	}
	var next *devicePlugin
	for i := range s.deviceProfiles {
		if s.deviceProfiles[i].Profile.ID == id {
			next = &s.deviceProfiles[i]
			break
		}
	}
	if next == nil {
		return errors.New("unknown device plugin")
	}
	if s.deviceNeedsRestart || s.outputNeedsRestart {
		return errors.New("restart required before switching device")
	}
	if s.coordinator != nil && s.coordinator.report().Pressed != 0 {
		return errors.New("release buttons before switching device")
	}
	keys, err := s.profile.keys(next.Profile)
	if err != nil {
		return err
	}
	// Retire old output and input before adopting another plugin's identity.
	if s.plugins != nil && s.plugins.Active(outputPlugin) {
		if err = s.plugins.Disable(outputPlugin); err != nil {
			s.outputNeedsRestart = true
			return err
		}
	}
	if s.plugins != nil && s.plugins.Active(s.deviceModule()) {
		if err = s.plugins.Disable(s.deviceModule()); err != nil {
			s.deviceNeedsRestart = true
			return err
		}
	}
	s.endShortcutRecording()
	s.selectedProfile = id
	if s.coordinator != nil {
		s.coordinator.mappings = keys
		s.coordinator.actions = s.profile.actionRefs(next.Profile)
	}
	s.syncVoiceBinding()
	s.selectedButton, s.revealButton = "", ""
	s.deviceStatus, s.deviceLastPress, s.deviceDetail = "未启用", "", ""
	s.deviceNeedsPermission = false
	s.deviceReadSequence++ // Ignore an in-flight discovery belonging to the old profile.
	s.deviceReadBusy = false
	s.outputNeedsPermission, s.outputStatus = false, ""
	s.invalidateDeviceView()
	return nil
}
