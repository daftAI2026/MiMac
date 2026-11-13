package main

import (
	"bytes"
	"fmt"
	"image/png"
	"math"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	brand "mimac.local/mimac/Assets/Brand"
)

func TestOnboardingWelcomePreservesExistingProgressNumbers(t *testing.T) {
	for index, step := range []onboardingStep{onboardingStepDevice, onboardingStepVoice, onboardingStepPermissions} {
		if int(step) != index {
			t.Fatalf("saved step %d changed to %d", index, step)
		}
		record := onboardingRecord{Version: onboardingRecordVersion, Started: true, Step: index}
		if onboardingInitialStep(record, true) != step {
			t.Fatalf("saved step %d did not resume", index)
		}
	}
	record, write := onboardingRecordForOpen(onboardingRecord{}, false, true)
	if !write || onboardingInitialStep(record, true) != onboardingStepWelcome {
		t.Fatalf("new setup skipped welcome: %+v", record)
	}
	path := filepath.Join(t.TempDir(), "onboarding.json")
	if err := writeOnboardingRecord(path, record); err != nil {
		t.Fatal(err)
	}
	restored, exists, err := readOnboardingRecord(path)
	if err != nil || !exists || onboardingInitialStep(restored, true) != onboardingStepWelcome {
		t.Fatalf("welcome record did not resume: %+v %t %v", restored, exists, err)
	}
	if onboardingInitialStep(record, false) != onboardingStepWelcome || onboardingAdjacentStep(onboardingStepWelcome, 1) != onboardingStepDevice || onboardingAdjacentStep(onboardingStepDevice, -1) != onboardingStepWelcome {
		t.Fatal("welcome navigation is disconnected")
	}
}

func TestOnboardingWelcomeLayout(t *testing.T) {
	for _, size := range [][2]float32{{900, 660}, {1304, 840}} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%gx%g-dark%t", size[0], size[1], dark), func(t *testing.T) {
				s := &shell{settingsState: settingsState{navigationSymbols: map[symbolKey]*ui.Bitmap{}}}
				s.onboarding.step = onboardingStepWelcome
				primeOnboardingTestSymbols(s, nil)
				v := ui.NewTester(s.onboardingView, int(size[0]), int(size[1]))
				v.SetDark(dark)
				heading, found := v.Find("欢迎使用 MiMac")
				if !found || math.Abs(float64(heading.X+heading.W/2-size[0]/2)) > 1 {
					t.Fatalf("welcome heading not centered: %v %t", heading, found)
				}
				primary, found := v.Find("继续")
				if !found || primary.H != navigationHeight || primary.Y <= heading.Y+heading.H {
					t.Fatalf("welcome primary action misplaced: %v %t", primary, found)
				}
				skip, found := v.Find("稍后设置")
				if !found || skip.Y <= primary.Y+primary.H || skip.Y+skip.H > size[1]-24 || math.Abs(float64(skip.X+skip.W/2-size[0]/2)) > 1 {
					t.Fatalf("welcome secondary action misplaced: %v %t", skip, found)
				}
				for _, absent := range []string{"蓝牙设置…", "重新检测", "1 / 3", "上一步"} {
					if _, found := v.Find(absent); found {
						t.Fatalf("welcome contains a setup-step control: %q", absent)
					}
				}
				t.Logf("heading=%v primary=%v skip=%v", heading, primary, skip)
			})
		}
	}
}

func TestOnboardingBrandSupportsRetinaWelcome(t *testing.T) {
	image, err := png.DecodeConfig(bytes.NewReader(brand.AboutIconPNG))
	if err != nil {
		t.Fatal(err)
	}
	if image.Width < int(onboardingWelcomeIconSize*2) || image.Height < int(onboardingWelcomeIconSize*2) {
		t.Fatalf("brand image %dx%d cannot cover the welcome at 2x", image.Width, image.Height)
	}
}
