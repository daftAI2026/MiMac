package main

import "testing"

func TestInputMethodArtworkVisibleDuringInitialDiscovery(t *testing.T) {
	for _, tool := range builtInVoiceTools() {
		if tool.id != "voice-doubao" && tool.id != "voice-wetype" {
			continue
		}
		t.Run(tool.id, func(t *testing.T) {
			s := &shell{}
			tool.active, tool.busy = true, true
			icon := s.voiceToolIcon(tool)
			if icon == nil {
				t.Fatal("initial discovery rendered without the embedded application icon")
			}
			if s.voiceToolIcon(tool) != icon || s.voiceToolIcons[tool.id].cancel != nil {
				t.Fatal("pending artwork reloaded or started another discovery")
			}
		})
	}
}
