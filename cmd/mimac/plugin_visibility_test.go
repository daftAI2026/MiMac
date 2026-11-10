package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

type fakeVoiceToolRuntime struct {
	shell           *shell
	tools           map[string]*voiceTool
	enabled         []string
	observedCurrent []string
}

func (r *fakeVoiceToolRuntime) Enable(id string) error {
	r.observedCurrent = append(r.observedCurrent, r.shell.currentVoiceTool)
	tool := r.tools[id]
	if tool == nil {
		return errors.New("unknown voice tool")
	}
	r.enabled = append(r.enabled, id)
	tool.active = true
	if tool.discover != nil {
		tool.snapshot = tool.discover(context.Background(), voicetools.Locator{})
	}
	return nil
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

func TestRestoreVoiceToolsUsesVisibilityGateWithoutRewritingPreferences(t *testing.T) {
	for _, tc := range []struct {
		name             string
		development      bool
		current          string
		wantEnabled      []string
		wantCurrent      string
		wantVisibleReads int
		wantHiddenReads  int
	}{
		{name: "release hidden current", current: "voice-typeless", wantEnabled: []string{"voice-doubao"}, wantVisibleReads: 1},
		{name: "release supported current", current: "voice-doubao", wantEnabled: []string{"voice-doubao"}, wantCurrent: "voice-doubao", wantVisibleReads: 1},
		{name: "development hidden current", development: true, current: "voice-typeless", wantEnabled: []string{"voice-doubao", "voice-typeless"}, wantCurrent: "voice-typeless", wantVisibleReads: 1, wantHiddenReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte("{\n  \"version\": 1, \"enabled\": [\"voice-doubao\", \"voice-typeless\"], \"current\": \"" + tc.current + "\"\n}\n")
			path := filepath.Join(t.TempDir(), "tools.json")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			preferences, err := readToolPreferences(path)
			if err != nil {
				t.Fatal(err)
			}

			visibleReads, hiddenReads := 0, 0
			visible := &voiceTool{id: "voice-doubao", releaseReady: true, discover: func(context.Context, voicetools.Locator) voicetools.Snapshot {
				visibleReads++
				return voicetools.Snapshot{State: voicetools.Ready}
			}}
			hidden := &voiceTool{id: "voice-typeless", discover: func(context.Context, voicetools.Locator) voicetools.Snapshot {
				hiddenReads++
				return voicetools.Snapshot{State: voicetools.Ready}
			}}
			s := &shell{development: tc.development, voiceToolsState: voiceToolsState{
				tools:           []*voiceTool{visible, hidden},
				toolPreferences: preferences,
			}}
			runtime := &fakeVoiceToolRuntime{shell: s, tools: map[string]*voiceTool{
				visible.id: visible,
				hidden.id:  hidden,
			}}

			failed := s.restoreVoiceTools(s.toolPreferences, runtime.Enable)
			if failed {
				t.Fatal("fake plugin runtime unexpectedly failed to enable a tool")
			}
			if s.currentVoiceTool != tc.wantCurrent {
				t.Fatalf("runtime Current = %q, want %q", s.currentVoiceTool, tc.wantCurrent)
			}
			if !slices.Equal(runtime.enabled, tc.wantEnabled) {
				t.Fatalf("enabled plugins = %v, want %v", runtime.enabled, tc.wantEnabled)
			}
			if visibleReads != tc.wantVisibleReads || hiddenReads != tc.wantHiddenReads {
				t.Fatalf("discover calls = visible %d, hidden %d; want %d, %d", visibleReads, hiddenReads, tc.wantVisibleReads, tc.wantHiddenReads)
			}
			for _, current := range runtime.observedCurrent {
				if current != tc.wantCurrent {
					t.Fatalf("Enable observed Current %q, want %q already published", current, tc.wantCurrent)
				}
			}
			if !slices.Equal(s.toolPreferences.Enabled, preferences.Enabled) || s.toolPreferences.Current != preferences.Current {
				t.Fatalf("loaded preferences were changed: got %+v, want %+v", s.toolPreferences, preferences)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, original) {
				t.Fatalf("tools.json bytes changed: got %q, want %q", got, original)
			}
		})
	}
}
