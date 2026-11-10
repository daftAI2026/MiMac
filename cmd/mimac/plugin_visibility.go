package main

import "slices"

// voicePluginVisible is a presentation gate for the plugin settings page.
// The same gate controls release startup restoration. It does not affect plugin
// registration or mutate the saved configuration.
func (s *shell) voicePluginVisible(tool *voiceTool) bool {
	return s.development || (tool != nil && tool.releaseReady)
}

// restoreVoiceTools publishes the available Current before starting plugins,
// then activates saved enabled tools without rewriting the loaded preferences.
func (s *shell) restoreVoiceTools(preferences toolPreferences, enable func(string) error) (failed bool) {
	current := preferences.Current
	for _, tool := range s.tools {
		if tool.id == current && !s.voicePluginVisible(tool) {
			current = ""
			break
		}
	}
	s.currentVoiceTool = current
	for _, tool := range s.tools {
		if !s.voicePluginVisible(tool) || !slices.Contains(preferences.Enabled, tool.id) {
			continue
		}
		if enable(tool.id) != nil {
			failed = true
		}
	}
	return failed
}
