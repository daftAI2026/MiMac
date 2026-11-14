package main

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
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
				heading, found := v.Find(onboardingStepHeading(onboardingStepWelcome))
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

func TestOnboardingWelcomeUsesVectorBrand(t *testing.T) {
	s := &shell{settingsState: settingsState{navigationSymbols: map[symbolKey]*ui.Bitmap{}}}
	s.onboarding.step = onboardingStepWelcome
	primeOnboardingTestSymbols(s, nil)
	ui.NewTester(s.onboardingView, windowMinWidth, windowMinHeight)
	if s.brandMark == nil || s.appIcon != nil {
		t.Fatal("welcome must draw the original vector without decoding the application icon")
	}
}

func TestOnboardingWelcomeUsesApplicationName(t *testing.T) {
	original := mygo.App.Name()
	t.Cleanup(func() { mygo.App.SetName(original) })
	for _, name := range []string{"MiMac Dev", "Example"} {
		mygo.App.SetName(name)
		if got := onboardingStepHeading(onboardingStepWelcome); got != "欢迎使用 "+name {
			t.Fatalf("welcome kept a hardcoded name: %q", got)
		}
	}
}

func TestOnboardingWelcomeDescriptionUsesNaturalWrapping(t *testing.T) {
	s := &shell{settingsState: settingsState{navigationSymbols: map[symbolKey]*ui.Bitmap{}}}
	s.onboarding.step = onboardingStepWelcome
	primeOnboardingTestSymbols(s, nil)
	v := ui.NewTester(s.onboardingView, windowMinWidth, windowMinHeight)
	if _, found := v.Find("花一点时间，连接遥控器、选择语音工具，并设置所需权限。"); !found {
		t.Fatal("welcome description must wrap from its available width, without a forced line break")
	}
}

func TestOnboardingBalancedTextPreservesHeight(t *testing.T) {
	for _, sample := range []struct {
		text  string
		width float32
	}{
		{"简短说明。", 300},
		{"窗口改变时，文字应自然换行，同时保持原来的行数。", 160},
		{"Window geometry controls wrapping, while the text keeps its original number of lines.", 260},
	} {
		t.Run(sample.text, func(t *testing.T) {
			ui.NewTester(func(c *ui.Context) {
				span := ui.Span{Text: sample.text, Size: 15, Weight: 400}
				_, initialHeight := c.MeasureText(sample.width, span)
				width := onboardingBalancedTextWidth(c, sample.text, 15, 400, sample.width)
				_, height := c.MeasureText(width, span)
				if width > sample.width+1 || height > initialHeight+.5 {
					t.Fatalf("balanced paragraph changed height or overflowed: width=%g height=%g original=%g", width, height, initialHeight)
				}
				if width > 2 {
					_, narrowerHeight := c.MeasureText(width-2, span)
					if narrowerHeight <= initialHeight+.5 {
						t.Fatalf("paragraph retained unnecessary width: %g", width)
					}
				}
				ui.Text(c, sample.text).Width(width).FontSize(15).FontWeight(400)
			}, 400, 300)
		})
	}
}

func TestOnboardingWelcomeLongApplicationNameFits(t *testing.T) {
	original := mygo.App.Name()
	t.Cleanup(func() { mygo.App.SetName(original) })
	mygo.App.SetName("An Application With A Descriptive Name That Changes Before Release")
	for _, width := range []int{900, 1304} {
		s := &shell{settingsState: settingsState{navigationSymbols: map[symbolKey]*ui.Bitmap{}}}
		s.onboarding.step = onboardingStepWelcome
		primeOnboardingTestSymbols(s, nil)
		v := ui.NewTester(s.onboardingView, width, 660)
		heading, found := v.Find(onboardingStepHeading(onboardingStepWelcome))
		if !found || heading.X < onboardingInsetX || heading.X+heading.W > float32(width)-onboardingInsetX {
			t.Fatalf("application name overflowed at width %d: %v", width, heading)
		}
		if heading.W > onboardingContentWidth+1 {
			t.Fatalf("application name exceeded the content width at window width %d: %v", width, heading)
		}
		primary, found := v.Find("继续")
		if !found || primary.Y < heading.Y+heading.H || primary.Y+primary.H > 660-24 {
			t.Fatalf("wrapped name displaced the primary action at width %d: %v", width, primary)
		}
	}
}
