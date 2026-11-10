package main

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/voicetools"
)

func TestOnboardingPagesFitTheMinimumWindow(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for step := onboardingStepDevice; step < onboardingStepCount; step++ {
			t.Run(fmt.Sprintf("step%d-dark%t", step, dark), func(t *testing.T) {
				s := &shell{development: true, settingsState: settingsState{navigationSymbols: map[symbolKey]*ui.Bitmap{}, voiceToolIcons: map[string]*voiceToolIcon{}}}
				profile, err := xiaomi.DeviceProfile()
				if err != nil {
					t.Fatal(err)
				}
				s.deviceProfiles = []devicePlugin{{ModuleID: xiaomiPlugin, Profile: profile}}
				s.onboarding.step = step
				s.onboarding.toolLocations = map[string]voicetools.State{}
				s.tools = builtInVoiceTools()
				for _, tool := range s.tools {
					s.voiceToolIcons[tool.id] = &voiceToolIcon{}
					s.onboarding.toolLocations[tool.id] = voicetools.Ready
				}
				// NewTester draws its initial frame before SetDark. Both palettes must
				// be native-free, regardless of the requested evidence appearance.
				for _, palette := range []bool{false, true} {
					s.navigationSymbols[symbolKey{name: "apple.logo", dark: palette, size: settingsIconSize}] = nil
					s.navigationSymbols[symbolKey{name: "app", dark: palette, size: settingsIconSize}] = nil
				}
				for _, id := range []permissionGuideID{permissionGuideInput, permissionGuidePostEvent, permissionGuideBluetooth} {
					row := permissionGuideRowFor(id)
					row.access = permissionGuideNotDetermined
					s.onboarding.permissionSnapshot.rows = append(s.onboarding.permissionSnapshot.rows, row)
					for _, palette := range []bool{false, true} {
						s.navigationSymbols[symbolKey{name: row.icon, dark: palette, size: 20}] = nil
					}
				}
				v := ui.NewTester(s.onboardingView, windowMinWidth, windowMinHeight)
				v.SetDark(dark)
				heading, ok := v.Find(onboardingStepHeading(step))
				if !ok || math.Abs(float64(heading.X+heading.W/2-450)) > 1 {
					t.Fatalf("heading is not centered: %v %t", heading, ok)
				}
				next := "继续"
				if step == onboardingStepPermissions {
					next = "进入按键映射"
				}
				rect, ok := v.Find(next)
				if !ok || rect.Y < 48 || rect.Y+rect.H > 620 {
					t.Fatalf("next action outside content: %v %t", rect, ok)
				}
				progress, ok := v.Find(onboardingStepPosition(step))
				if !ok || progress.Y+progress.H > 660 {
					t.Fatalf("progress outside window: %v %t", progress, ok)
				}
				if step == onboardingStepVoice {
					if _, found := v.Find("Typeless"); found {
						t.Fatal("unfinished adapters appeared in the first-use guide")
					}
					for _, name := range []string{"Apple 语音识别", "豆包输入法", "微信输入法"} {
						if r, found := v.Find(name); !found || r.X < 150 || r.Y < 48 || r.Y+r.H > rect.Y {
							t.Fatalf("tool clipped: %s %v %t", name, r, found)
						}
					}
				}
				if out := os.Getenv("MIMAC_ONBOARDING_UI_EVIDENCE"); out != "" {
					if err := os.MkdirAll(out, 0700); err != nil {
						t.Fatal(err)
					}
					f, err := os.Create(filepath.Join(out, fmt.Sprintf("step%d-dark%t.png", step, dark)))
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
