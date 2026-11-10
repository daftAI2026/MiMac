package main

// voicePluginVisible is a presentation gate for the plugin settings page.
// It does not affect plugin registration, activation, or saved configuration.
func (s *shell) voicePluginVisible(tool *voiceTool) bool {
	return s.development || (tool != nil && tool.releaseReady)
}
