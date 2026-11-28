package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mimac.local/mimac/internal/voicetools"
)

func TestVoiceToolLiveTextSettingUsesProviderCapabilities(t *testing.T) {
	tools := builtInVoiceTools()
	byID := make(map[string]*voiceTool, len(tools))
	for _, tool := range tools {
		byID[tool.id] = tool
	}

	for _, toolID := range []string{appleSpeechToolID, hexStreamToolID} {
		tool := byID[toolID]
		if tool == nil {
			t.Fatalf("missing built-in tool %q", toolID)
		}
		supported, status := voiceToolLiveTextSetting(tool)
		if !supported {
			t.Fatalf("%s live text support = false, status %q", toolID, status)
		}
	}
	if status := func() string { _, value := voiceToolLiveTextSetting(byID[appleSpeechToolID]); return value }(); status != "" {
		t.Fatalf("normally supported Apple tool needs no explanatory copy, got %q", status)
	}
	if supported, status := voiceToolLiveTextSetting(byID[hexStreamToolID]); !supported || !strings.Contains(status, "模型") {
		t.Fatalf("Hex model condition = supported %v, status %q", supported, status)
	}

	for _, toolID := range []string{"voice-doubao", "voice-wetype"} {
		tool := byID[toolID]
		if tool == nil || !voiceToolUsesNativeMarkedText(tool) {
			t.Fatalf("%s is not registered for native marked-range captions", toolID)
		}
		if supported, status := voiceToolLiveTextSetting(tool); !supported || !strings.Contains(status, "支持的输入框") || !strings.Contains(status, "输入法提交") {
			t.Fatalf("native marked-range status for %s = supported %v, %q", toolID, supported, status)
		}
	}
	if supported, status := voiceToolLiveTextSetting(nil); supported || !strings.Contains(status, "支持实时字幕") {
		t.Fatalf("empty selection status = supported %v, status %q", supported, status)
	}

	// Provider-specific presentation must not rewrite the independent preference.
	preferences := defaultHostPreferences()
	preferences.ShowLiveText = true
	for _, tool := range []*voiceTool{byID[appleSpeechToolID], byID[hexStreamToolID], byID["voice-doubao"], byID["voice-wetype"]} {
		voiceToolLiveTextSetting(tool)
		if !preferences.ShowLiveText {
			t.Fatalf("provider %q cleared the persisted subtitle preference", tool.id)
		}
	}
}

func TestHexDisplayGroupingDoesNotChangeVoiceRouteSemantics(t *testing.T) {
	var hex *voiceTool
	for _, candidate := range builtInVoiceTools() {
		if candidate.id == hexStreamToolID {
			hex = candidate
			break
		}
	}
	if hex == nil {
		t.Fatal("missing Hex registration")
	}
	if hex.name != "Hex" || voiceToolSettingsKind(hex) != voiceInputMethod {
		t.Fatalf("Hex visible name/group = %q/%q", hex.name, voiceToolSettingsKind(hex))
	}
	if hex.kind != voiceRecognition || hex.requiredInputSource != nil || hex.audioRouteFactory == nil {
		t.Fatalf("Hex display metadata changed the route registration: %+v", hex)
	}

	apple := &voiceTool{kind: voiceRecognition}
	if got := voiceToolSettingsKind(apple); got != voiceRecognition {
		t.Fatalf("default display group = %q, want %q", got, voiceRecognition)
	}
}

func TestHexModelMessageTracksServiceReadiness(t *testing.T) {
	hex := &voiceTool{name: "Hex", liveText: voiceToolLiveTextNeedsModel}
	if supported, status := voiceToolLiveTextSetting(hex); !supported || !strings.Contains(status, "启用本地服务") {
		t.Fatalf("inactive Hex status = %v, %q", supported, status)
	}
	hex.active = true
	hex.snapshot.State = voicetools.NotRunning
	if supported, status := voiceToolLiveTextSetting(hex); !supported || !strings.Contains(status, "尚未连接") {
		t.Fatalf("offline Hex status = %v, %q", supported, status)
	}
	hex.snapshot.State = voicetools.Ready
	if supported, status := voiceToolLiveTextSetting(hex); !supported || !strings.Contains(status, "模型") {
		t.Fatalf("ready Hex model status = %v, %q", supported, status)
	}
}

func TestLiveTextPreferenceSurvivesUnsupportedToolSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	tools := builtInVoiceTools()
	s := &shell{voiceToolsState: voiceToolsState{tools: tools}, preferencesState: preferencesState{
		value: defaultHostPreferences(), path: path,
	}}
	doubao := findVoiceToolForLiveTextTest(tools, "voice-doubao")
	if doubao == nil {
		t.Fatal("missing Doubao registration")
	}
	s.currentVoiceTool = doubao.id
	if err := s.applyLiveTextPreference(true); err != nil {
		t.Fatal(err)
	}
	if supported, status := voiceToolLiveTextSetting(s.selectedVoiceTool()); !supported || !strings.Contains(status, "支持的输入框") {
		t.Fatalf("Doubao marked-range presentation = %v, %q", supported, status)
	}

	for _, toolID := range []string{appleSpeechToolID, hexStreamToolID} {
		s.currentVoiceTool = toolID
		if !s.preferencesState.value.ShowLiveText {
			t.Fatalf("selecting %q cleared the live-text preference", toolID)
		}
		frame := s.voiceOverlayFrameAt(time.Now())
		if frame.ShowLiveText {
			t.Fatalf("preference-only test unexpectedly opened a live overlay for %q", toolID)
		}
	}
	saved, err := readHostPreferences(path)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.ShowLiveText {
		t.Fatal("unsupported provider selection did not preserve the saved subtitle preference")
	}
}

func findVoiceToolForLiveTextTest(tools []*voiceTool, id string) *voiceTool {
	for _, tool := range tools {
		if tool.id == id {
			return tool
		}
	}
	return nil
}
