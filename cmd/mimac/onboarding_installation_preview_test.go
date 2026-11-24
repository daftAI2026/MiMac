package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/voicetools"
)

// Exercise the actual first-use view with isolated installation snapshots.
// No application discovery, installation or configuration writes are performed.
func TestOnboardingMissingVoiceToolsRemainVisible(t *testing.T) {
	original := mygo.App.Name()
	mygo.App.SetName("MiMac")
	t.Cleanup(func() { mygo.App.SetName(original) })
	for _, sample := range []struct {
		name           string
		doubao, wetype voicetools.State
	}{
		{"both-missing", voicetools.NotFound, voicetools.NotFound},
		{"doubao-missing", voicetools.NotFound, voicetools.Ready},
		{"wetype-missing", voicetools.Ready, voicetools.NotFound},
	} {
		for _, dark := range []bool{false, true} {
			for _, size := range [][2]int{{900, 660}, {1304, 840}} {
				t.Run(fmt.Sprintf("%s/dark%t/%d", sample.name, dark, size[0]), func(t *testing.T) {
					s := &shell{settingsState: settingsState{navigationSymbols: make(map[symbolKey]*ui.Bitmap), voiceToolIcons: make(map[string]*voiceToolIcon)}}
					s.onboarding.step = onboardingStepVoice
					s.onboarding.toolLocations = map[string]voicetools.State{"voice-doubao": sample.doubao, "voice-wetype": sample.wetype}
					s.tools = builtInVoiceTools()
					for _, tool := range s.tools {
						cached := &voiceToolIcon{}
						if len(tool.fallbackIconPNG) > 0 {
							var err error
							cached.bitmap, err = ui.DecodeBitmap(tool.fallbackIconPNG)
							if err != nil {
								t.Fatal(err)
							}
						}
						s.voiceToolIcons[tool.id] = cached
						if state, found := s.onboarding.toolLocations[tool.id]; found {
							status, selectable, got := s.onboardingVoiceToolStatus(tool)
							if got != state || selectable != (state == voicetools.Ready) {
								t.Fatalf("%s installation projection = %s, selectable=%v", tool.id, got, selectable)
							}
							if state == voicetools.NotFound && status != "未安装" {
								t.Fatalf("missing tool lost its message: %s", status)
							}
						}
					}
					primeOnboardingTestSymbols(s, nil)
					if dir := os.Getenv("MIMAC_ONBOARDING_SYMBOLS"); dir != "" {
						for _, d := range []bool{false, true} {
							a := appAppearance(d, 0)
							suffix := "light"
							if d {
								suffix = "dark"
							}
							for _, name := range []string{"apple.logo", "app", "arrow.left", "arrow.right"} {
								raw, err := os.ReadFile(filepath.Join(dir, name+"-"+suffix+".png"))
								if err != nil {
									t.Fatal(err)
								}
								bitmap, err := ui.DecodeBitmap(raw)
								if err != nil {
									t.Fatal(err)
								}
								key := symbolKey{name: name, dark: d, size: settingsIconSize}
								if name == "arrow.left" {
									key.tint = a.theme.Text
								}
								if name == "arrow.right" {
									key.tint = a.theme.Background
								}
								s.navigationSymbols[key] = bitmap
							}
						}
					}
					v := ui.NewTester(s.onboardingView, size[0], size[1])
					v.SetScale(2)
					v.SetDark(dark)
					v.Frame()
					next, ok := v.Find("继续")
					if !ok {
						t.Fatal("continue action missing")
					}
					if _, ok := v.Find("Hex 本地识别"); ok {
						t.Fatal("inactive service-only tool offered before it can be selected")
					}
					for _, name := range []string{"Apple 语音识别", "豆包输入法", "微信输入法"} {
						r, ok := v.Find(name)
						if !ok || r.Y+r.H > next.Y {
							t.Fatalf("tool missing or clipped: %s %v", name, r)
						}
					}
					if _, ok := v.Find("下载安装…"); !ok {
						t.Fatal("missing installation lost its download action")
					}
					if _, ok := v.Find("已选用"); ok {
						t.Fatal("a missing or inactive tool was shown as selected")
					}
					if out := os.Getenv("MIMAC_ONBOARDING_UI_EVIDENCE"); out != "" {
						if err := os.MkdirAll(out, 0700); err != nil {
							t.Fatal(err)
						}
						theme := "light"
						if dark {
							theme = "dark"
						}
						file := filepath.Join(out, fmt.Sprintf("voice-tools-%s-%s-%d.png", sample.name, theme, size[0]))
						f, err := os.Create(file)
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
}
