package main

// releaseSettingsAssets drops decoded view-only assets after the settings
// window has detached. The shell keeps device, mapping, and status state.
func (s *shell) releaseSettingsAssets() {
	s.brandMark = nil
	s.modelBitmaps = nil
	s.navigationSymbols = nil
	s.appIcon = nil
	s.socialIcons = nil
	for _, icon := range s.voiceToolIcons {
		if icon.cancel != nil {
			icon.cancel()
		}
	}
	s.voiceToolIcons = nil
	clear(controlSymbols)
}
