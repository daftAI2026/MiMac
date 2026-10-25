package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestShandianPluginDiscoveryDoesNotAuthorizeOutput(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("bundle metadata requires macOS")
	}
	home := t.TempDir()
	apps := filepath.Join(home, "Apps")
	contents := filepath.Join(apps, "闪电说.app", "Contents")
	configDir := filepath.Join(home, "Library", "Application Support", "Shandianshuo")
	for _, p := range []string{contents, configDir} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>cn.shandianshuo.desktop</string><key>CFBundleShortVersionString</key><string>0.8.0</string><key>CFBundleVersion</key><string>0.8.0</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	config := `{"config_version":3,"unified_hotkey":{"keys":["RCmd"],"block_keys":true},"short_press_action":"VoiceInput","long_press_action":"VoiceAssistant"}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	s := &shell{voiceToolsState: voiceToolsState{tools: builtInVoiceTools(), currentVoiceTool: "voice-shandian"}}
	var target *voiceTool
	for _, tool := range s.tools {
		if tool.id == "voice-shandian" {
			target = tool
		}
	}
	if target == nil || target.kind != voiceInputMethod {
		t.Fatal("missing voice input plugin")
	}
	target.active = true
	target.sequence = 1
	target.snapshot = target.discover(context.Background(), voicetools.Locator{Home: home, ApplicationDirs: []string{apps}})
	if target.snapshot.State != voicetools.Ready || len(target.snapshot.Actions) == 0 {
		t.Fatalf("discovery = %+v", target.snapshot)
	}
	if voiceToolStatus(target) != "已读取配置" {
		t.Fatalf("status = %s", voiceToolStatus(target))
	}
	candidates := 0
	for _, action := range target.snapshot.Actions {
		for _, candidate := range action.Candidates {
			candidates++
			ref := voicetools.ActionRef{ToolID: target.id, ActionID: action.ID, CandidateRaw: candidate.Raw}
			if _, err := s.resolveToolAction(ref); err == nil {
				t.Fatalf("read-only discovery authorized output: %+v", action)
			}
		}
	}
	if candidates == 0 {
		t.Fatal("test requires an observed shortcut candidate")
	}
	if s.output != nil || s.coordinator != nil {
		t.Fatal("discovery created output resources")
	}
}
