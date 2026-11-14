package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/voicetools"
)

func TestOnboardingDiscoveryRecoveryControls(t *testing.T) {
	for _, step := range []onboardingStep{onboardingStepDevice, onboardingStepVoice} {
		s := &shell{settingsState: settingsState{
			navigationSymbols: make(map[symbolKey]*ui.Bitmap),
			voiceToolIcons:    map[string]*voiceToolIcon{"sample": {}},
		}}
		s.onboarding.step = step
		tool := &voiceTool{id: "sample", name: "Sample Tool", releaseReady: true}
		s.tools = []*voiceTool{tool}
		s.onboarding.toolLocations = map[string]voicetools.State{"sample": voicetools.Ready}
		s.onboarding.toolLocationErrors = make(map[string]bool)
		primeOnboardingTestSymbols(s, nil)
		v := ui.NewTester(s.onboardingView, windowMinWidth, windowMinHeight)
		check := func(wantRetry bool) {
			t.Helper()
			v.Frame()
			if _, found := v.Find("重新检测"); found {
				t.Fatal("routine discovery still requires a manual check")
			}
			if _, found := v.Find("重试"); found != wantRetry {
				t.Fatalf("step %d retry visible=%v, want %v", step, found, wantRetry)
			}
		}
		// Healthy, failed, in-flight, and recovered states share the same page.
		check(false)
		if step == onboardingStepDevice {
			s.deviceStatus = "检测失败"
			check(true)
			s.deviceReadBusy = true
			check(false)
			s.deviceReadBusy = false
			s.deviceStatus = "未连接"
			check(false)
		} else {
			s.onboarding.toolLocationErrors[tool.id] = true
			check(true)
			delete(s.onboarding.toolLocations, tool.id)
			delete(s.onboarding.toolLocationErrors, tool.id)
			check(false)
			s.onboarding.toolLocations[tool.id] = voicetools.Ready
			check(false)
			if _, found := v.Find("插件设置…"); found {
				t.Fatal("healthy setup offers an unrelated settings destination")
			}
			s.onboarding.toolLocations[tool.id] = voicetools.Ambiguous
			v.Frame()
			if _, found := v.Find("插件设置…"); !found {
				t.Fatal("ambiguous discovery lost its configuration recovery")
			}
			s.onboarding.toolLocations[tool.id] = voicetools.Ready
			tool.active = true
			tool.snapshot.State = voicetools.ReadError
			check(true)
			tool.busy = true
			check(false)
			tool.busy = false
			tool.snapshot.State = voicetools.Ready
			check(false)
		}
	}
}

func TestOnboardingVoiceInstallationAndSelectionStates(t *testing.T) {
	cases := []struct {
		name                   string
		active, selected, busy bool
		state                  voicetools.State
		status, action         string
		success                bool
	}{
		{name: "missing", state: voicetools.NotFound, status: "未安装", action: "下载安装…"},
		{name: "installed", state: voicetools.Ready, status: "已安装，未启用", action: "启用并使用"},
		{name: "selected disabled", selected: true, state: voicetools.Ready, status: "已安装，未启用", action: "启用并使用"},
		{name: "selected ready", active: true, selected: true, state: voicetools.Ready, status: "已读取配置", action: "已选用", success: true},
		{name: "selected reading", active: true, selected: true, busy: true, state: voicetools.Ready, status: "正在读取配置", action: "已选用"},
		{name: "selected missing", active: true, selected: true, state: voicetools.NotFound, status: "未安装", action: "下载安装…"},
	}
	for _, dark := range []bool{false, true} {
		for _, tt := range cases {
			t.Run(fmt.Sprintf("%s/dark%t", tt.name, dark), func(t *testing.T) {
				tool := &voiceTool{id: "sample", name: "语音输入法", installURL: "https://example.com/download", releaseReady: true, active: tt.active, busy: tt.busy}
				tool.snapshot.State = tt.state
				s := &shell{settingsState: settingsState{navigationSymbols: make(map[symbolKey]*ui.Bitmap), voiceToolIcons: map[string]*voiceToolIcon{"sample": {}}}, tools: []*voiceTool{tool}}
				s.onboarding.step = onboardingStepVoice
				s.onboarding.toolLocations = map[string]voicetools.State{"sample": tt.state}
				if tt.selected {
					s.currentVoiceTool = tool.id
				}
				primeOnboardingTestSymbols(s, nil)
				green := ui.Hex("#008a22")
				s.grantedStyles = [2]permissionGuideGrantedStyle{{color: green, fill: green.Alpha(.08)}, {color: green, fill: green.Alpha(.08)}}
				v := ui.NewTester(s.onboardingView, windowMinWidth, windowMinHeight)
				v.SetDark(dark)
				for _, label := range []string{tt.status, tt.action} {
					if _, ok := v.Find(label); !ok {
						t.Fatalf("%s missing %q", tt.name, label)
					}
				}
				// A completion mark must mean the selected adapter is actually ready.
				_, mark := v.Find("✓")
				if mark != tt.success {
					t.Fatalf("success mark=%v, want %v", mark, tt.success)
				}
				if _, old := v.Find("当前工具"); old {
					t.Fatal("ambiguous disabled-button label remains")
				}
				if out := os.Getenv("MIMAC_ONBOARDING_UI_EVIDENCE"); out != "" {
					if err := os.MkdirAll(out, 0700); err != nil {
						t.Fatal(err)
					}
					f, err := os.Create(filepath.Join(out, fmt.Sprintf("voice-state-%s-dark%t.png", tt.name, dark)))
					if err != nil {
						t.Fatal(err)
					}
					if err := png.Encode(f, v.Image()); err != nil {
						f.Close()
						t.Fatal(err)
					}
					if err := f.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
