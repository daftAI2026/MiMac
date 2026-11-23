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
	if err != nil || !exists || onboardingInitialStep(loaded, true, permissionGuideContinuation{}) != onboardingStepCompletion || !shouldPresentOnboarding(loaded, true, true, nil) {
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

func TestOnboardingHandoffStartsSelectedRemote(t *testing.T) {
	for _, test := range []struct {
		name       string
		step       onboardingStep
		button     string
		connected  bool
		denied     bool
		wantActive bool
	}{
		{"complete-connected", onboardingStepCompletion, "开始使用 " + mygo.App.Name(), true, false, true},
		{"complete-offline", onboardingStepCompletion, "开始使用 " + mygo.App.Name(), false, false, true},
		{"complete-without-input-access", onboardingStepCompletion, "开始使用 " + mygo.App.Name(), false, true, false},
		{"skip", onboardingStepWelcome, "稍后设置", true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, factory, _, _ := newPowerPermissionRecoveryShell(t)
			factory.connectAt[1] = test.connected
			if test.denied {
				factory.failAt[1] = xiaomi.ErrInputMonitoringRequired
			}
			s.onboarding.step = test.step
			s.navigationSymbols = make(map[symbolKey]*ui.Bitmap)
			primeOnboardingTestSymbols(s, nil)
			view := ui.NewTester(s.onboardingView, 900, 660)
			if factory.starts != 0 {
				t.Fatal("showing the page started the remote")
			}
			if err := view.Click(test.button); err != nil {
				t.Fatal(err)
			}
			if s.remoteEnabled() != test.wantActive || s.deviceNeedsPermission != test.denied {
				t.Fatalf("active=%t needs input access=%t error=%v", s.remoteEnabled(), s.deviceNeedsPermission, s.remoteState.err)
			}
			if test.wantActive {
				if err := view.Click(test.button); err != nil {
					t.Fatal(err)
				}
				if !s.remoteEnabled() || factory.starts != 1 || s.remoteHasReservation() != test.connected {
					t.Fatalf("repeated completion: active=%t starts=%d reserved=%t", s.remoteEnabled(), factory.starts, s.remoteHasReservation())
				}
				if !test.connected && s.remoteLabel() != "等待遥控器" {
					t.Fatalf("offline remote status = %q", s.remoteLabel())
				}
			} else if test.step == onboardingStepWelcome && factory.starts != 0 {
				t.Fatal("skipping setup started the remote")
			}
		})
	}
}

func TestOnboardingInputCheckKeepsExplicitPermissionRequestAvailable(t *testing.T) {
	s := &shell{}
	var silent []bool
	if err := s.setupPluginsWith(func(func(xiaomi.MonitorEvent)) (hidMonitor, error) {
		silent = append(silent, s.remoteState.suppressInputPrompt)
		return nil, xiaomi.ErrInputMonitoringRequired
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.plugins.Close() })
	s.startOnboardingRemote()
	s.toggleRemote()
	if len(silent) != 2 || !silent[0] || silent[1] || s.remoteState.suppressInputPrompt {
		t.Fatalf("input monitor prompt suppression on completion and explicit retry = %v", silent)
	}
}
