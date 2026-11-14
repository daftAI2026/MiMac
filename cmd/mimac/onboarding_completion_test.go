package main

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/voicetools"
)

func TestOnboardingCompletionResumesWithoutMarkingSetupFinished(t *testing.T) {
	record := onboardingRecord{Version: onboardingRecordVersion, Started: true, Step: int(onboardingStepPermissions)}
	next, write := onboardingRecordForStep(record, onboardingAdjacentStep(onboardingStepPermissions, 1), true)
	if !write || next.Completed || next.Step != int(onboardingStepCompletion) {
		t.Fatalf("entering completion skipped its explicit finish action: %+v", next)
	}
	path := filepath.Join(t.TempDir(), onboardingRecordFile)
	if err := writeOnboardingRecord(path, next); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := readOnboardingRecord(path)
	if err != nil || !exists || onboardingInitialStep(loaded, true) != onboardingStepCompletion || !shouldPresentOnboarding(loaded, true, true, nil) {
		t.Fatalf("closing on completion lost the unfinished step: %+v %v", loaded, err)
	}
	if onboardingAdjacentStep(onboardingStepCompletion, -1) != onboardingStepPermissions {
		t.Fatal("completion return link no longer leads to permissions")
	}
}

func TestOnboardingCompletionUsesCurrentMappingsAndFits(t *testing.T) {
	profile, err := xiaomi.DeviceProfile()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{900, 660}, {1304, 840}} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d-dark%t", size[0], size[1], dark), func(t *testing.T) {
				s := &shell{settingsState: settingsState{navigationSymbols: map[symbolKey]*ui.Bitmap{}}}
				s.onboarding.step = onboardingStepCompletion
				s.deviceProfiles = []devicePlugin{{Profile: profile, VoiceButtonID: xiaomi.VoiceButtonID}}
				primeOnboardingTestSymbols(s, nil)
				v := ui.NewTester(s.onboardingView, size[0], size[1])
				v.SetDark(dark)
				start, found := v.Find("开始使用 " + mygo.App.Name())
				if !found || start.H != navigationHeight {
					t.Fatalf("missing completion handoff: %v", start)
				}
				back, found := v.Find("返回系统权限")
				if !found || back.Y <= start.Y+start.H || back.Y+back.H > float32(size[1])-24 {
					t.Fatalf("completion actions do not fit: %v", back)
				}
				if _, found := v.Find("未选择语音工具"); !found {
					t.Fatal("completion invented voice readiness")
				}
				s.tools = []*voiceTool{{id: "sample", name: "Sample Voice", active: true, snapshot: voicetools.Snapshot{State: voicetools.Ready}}}
				s.currentVoiceTool = "sample"
				s.profile.Profiles = map[string]map[string]string{profile.ID: {"hid:0007:0028": "Command+K"}}
				v.Frame()
				for _, value := range []string{"Sample Voice", "⌘K"} {
					if _, found := v.Find(value); !found {
						t.Fatalf("completion did not project the current configuration: %q", value)
					}
				}
				if _, found := v.Find("3 / 3"); found {
					t.Fatal("completion is presented as a numbered setup step")
				}
			})
		}
	}
}
