package main

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/voicetools"
)

func TestOnboardingDownloadOnlyOpensTheOfficialPage(t *testing.T) {
	urls := map[string]string{"voice-doubao": "https://shurufa.doubao.com/pc", "voice-wetype": "https://z.weixin.qq.com/"}
	for _, tool := range builtInVoiceTools() {
		want, ok := urls[tool.id]
		if !ok {
			continue
		}
		s := &shell{}
		s.onboarding.step = onboardingStepVoice
		calls := 0
		s.openOnboardingVoiceDownload(tool, func(url string) error {
			calls++
			if url != want {
				t.Fatalf("unexpected download URL: %s", url)
			}
			return errors.New("browser unavailable")
		})
		if s.onboarding.errorText == "" {
			t.Fatal("open failure lost its retry feedback")
		}
		s.openOnboardingVoiceDownload(tool, func(url string) error {
			calls++
			if url != want {
				t.Fatal(url)
			}
			return nil
		})
		if calls != 2 || s.onboarding.errorText != "" {
			t.Fatal("successful retry retained a failed launch")
		}
		if tool.active || s.currentVoiceTool != "" || s.onboarding.step != onboardingStepVoice || s.onboarding.toolLocationCancel != nil {
			t.Fatal("opening the vendor page changed selection, progress or started a query")
		}
	}
}

func TestOnboardingInstallationUsesTheSameResultForStatusAndArtwork(t *testing.T) {
	tool := &voiceTool{id: "sample"}
	old := &voiceToolIcon{}
	cancelled := false
	old.cancel = func() { cancelled = true }
	s := &shell{settingsState: settingsState{voiceToolIcons: map[string]*voiceToolIcon{tool.id: old}}}
	s.onboarding.toolLocations = map[string]voicetools.State{tool.id: voicetools.NotFound}
	s.onboarding.toolLocationErrors = make(map[string]bool)
	calls := []string{}
	bitmap := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 2, 2)))
	load := func(path string, _ []byte) *ui.Bitmap { calls = append(calls, path); return bitmap }
	bundle := voicetools.Bundle{Path: "/fixture/Voice.app", Version: "1"}
	s.applyOnboardingVoiceLocation(tool, bundle, voicetools.Ready, nil, load)
	if !cancelled || s.voiceToolIcons[tool.id] == old || s.voiceToolIcons[tool.id].bitmap != bitmap || len(calls) != 1 || calls[0] != bundle.Path {
		t.Fatal("new installation retained old artwork or duplicated discovery")
	}
	status, canSelect, _ := s.onboardingVoiceToolStatus(tool)
	if status != "已安装，未启用" || !canSelect || tool.active || s.currentVoiceTool != "" {
		t.Fatal("installation enabled or selected the adapter implicitly")
	}
	s.applyOnboardingVoiceLocation(tool, bundle, voicetools.Ready, nil, load)
	if len(calls) != 1 {
		t.Fatal("unchanged identity reloaded artwork")
	}
	s.applyOnboardingVoiceLocation(tool, voicetools.Bundle{}, voicetools.ReadError, errors.New("query failed"), load)
	if len(calls) != 1 || s.voiceToolIcons[tool.id].path != bundle.Path || !s.onboarding.toolLocationErrors[tool.id] {
		t.Fatal("query error destroyed last verified artwork")
	}
	s.applyOnboardingVoiceLocation(tool, voicetools.Bundle{}, voicetools.NotFound, nil, load)
	if len(calls) != 2 || calls[1] != "" || s.voiceToolIcons[tool.id].path != "" {
		t.Fatal("removed application retained its installed artwork")
	}
}

func TestOnboardingDiscoveryCancellationRejectsLateResults(t *testing.T) {
	s := &shell{settingsState: settingsState{voiceToolIcons: map[string]*voiceToolIcon{
		"pending": {}, "finished": {path: "/fixture/Voice.app"},
	}}}
	s.tools = []*voiceTool{{id: "pending"}, {id: "finished"}}
	s.onboarding.toolLocations = map[string]voicetools.State{"finished": voicetools.Ready}
	s.onboarding.step = onboardingStepVoice
	ctx, cancel := context.WithCancel(context.Background())
	s.onboarding.toolLocationCancel = cancel
	generation := s.onboarding.toolLocationGeneration
	if !onboardingLocationResultCurrent(ctx, generation, generation, true) {
		t.Fatal("current result refused")
	}
	s.stopOnboardingVoiceLocations()
	if s.voiceToolIcons["pending"] != nil || s.voiceToolIcons["finished"] == nil {
		t.Fatal("handoff retained unfinished artwork or discarded a completed result")
	}
	if ctx.Err() == nil || s.onboarding.toolLocationCancel != nil || onboardingLocationResultCurrent(ctx, generation, s.onboarding.toolLocationGeneration, true) {
		t.Fatal("leaving the page retained an old query")
	}
	fresh := context.Background()
	if onboardingLocationResultCurrent(fresh, generation, s.onboarding.toolLocationGeneration, true) || onboardingLocationResultCurrent(fresh, 1, 1, false) {
		t.Fatal("stale generation or a different page accepted a result")
	}
	if onboardingAdjacentStep(onboardingStepVoice, 1) != onboardingStepPermissions {
		t.Fatal("continue gained an installation prerequisite")
	}
}

func TestOnboardingRecognizesInstalledBundlesAfterReturn(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native bundle metadata reader")
	}
	for _, tc := range []struct{ id, name, bundleID string }{
		{"voice-doubao", "DoubaoIme.app", "com.bytedance.inputmethod.doubaoime"},
		{"voice-wetype", "WeType.app", "com.tencent.inputmethod.wetype"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			dir := t.TempDir()
			locator := voicetools.Locator{Home: dir, InputMethodDirs: []string{dir}}
			var tool *voiceTool
			for _, candidate := range builtInVoiceTools() {
				if candidate.id == tc.id {
					tool = candidate
				}
			}
			if tool == nil {
				t.Fatal("adapter missing")
			}
			_, state, err := tool.locateApplication(context.Background(), locator)
			if err != nil || state != voicetools.NotFound {
				t.Fatalf("before installation: %s %v", state, err)
			}
			contents := filepath.Join(dir, tc.name, "Contents")
			if err := os.MkdirAll(contents, 0700); err != nil {
				t.Fatal(err)
			}
			plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + tc.bundleID + `</string><key>CFBundleShortVersionString</key><string>1.0</string><key>CFBundleVersion</key><string>1</string></dict></plist>`
			if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0600); err != nil {
				t.Fatal(err)
			}
			bundle, state, err := tool.locateApplication(context.Background(), locator)
			if err != nil || state != voicetools.Ready || bundle.Path != filepath.Dir(contents) {
				t.Fatalf("after return: %+v %s %v", bundle, state, err)
			}
		})
	}
}
