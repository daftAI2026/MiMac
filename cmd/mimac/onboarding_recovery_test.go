package main

import (
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
