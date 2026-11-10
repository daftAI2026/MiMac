package main

import (
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestVoicePluginVisibilityIsBuildSpecificAndPresentationOnly(t *testing.T) {
	tools := builtInVoiceTools()
	current := "voice-typeless"
	var selected *voiceTool
	for _, tool := range tools {
		if tool.id == current {
			selected = tool
			break
		}
	}
	if selected == nil {
		t.Fatal("missing current voice tool fixture")
	}
	selected.active = true
	selected.snapshot = voicetools.Snapshot{State: voicetools.Ready}
	s := &shell{voiceToolsState: voiceToolsState{tools: tools, currentVoiceTool: current}}

	releaseCount := 0
	for _, tool := range s.tools {
		if s.voicePluginVisible(tool) {
			releaseCount++
		}
	}
	if releaseCount != 3 {
		t.Fatalf("release-visible plugin count = %d, want 3", releaseCount)
	}
	if s.currentVoiceTool != current {
		t.Fatalf("release visibility changed current tool: got %q, want %q", s.currentVoiceTool, current)
	}
	if !selected.active || selected.snapshot.State != voicetools.Ready {
		t.Fatal("release visibility changed plugin activation or discovered state")
	}

	s.development = true
	devCount := 0
	for _, tool := range s.tools {
		if s.voicePluginVisible(tool) {
			devCount++
		}
	}
	if devCount != 6 {
		t.Fatalf("development-visible plugin count = %d, want 6", devCount)
	}
	if s.currentVoiceTool != current {
		t.Fatalf("development visibility changed current tool: got %q, want %q", s.currentVoiceTool, current)
	}
	if !selected.active || selected.snapshot.State != voicetools.Ready {
		t.Fatal("development visibility changed plugin activation or discovered state")
	}
}

func TestReleaseReadyVoicePluginsStayVisibleWhenInactiveOrUninstalled(t *testing.T) {
	s := &shell{voiceToolsState: voiceToolsState{tools: builtInVoiceTools()}}
	states := map[string]voicetools.State{
		appleSpeechToolID: voicetools.ConfigMissing,
		"voice-doubao":    voicetools.NotFound,
		"voice-wetype":    voicetools.NotFound,
	}
	for id, state := range states {
		var target *voiceTool
		for _, tool := range s.tools {
			if tool.id == id {
				target = tool
				break
			}
		}
		if target == nil {
			t.Fatalf("missing built-in plugin %q", id)
		}
		// Installation/model availability and activation do not decide visibility.
		target.snapshot.State = state
		if target.active {
			t.Fatalf("fixture for %q is unexpectedly active", id)
		}
		if !s.voicePluginVisible(target) {
			t.Errorf("release-ready plugin %q was hidden while inactive or unavailable", id)
		}
	}
}
