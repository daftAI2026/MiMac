package main

import (
	"context"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/voicetools"
)

// Opening the vendor page does not start an install watcher or change setup
// progress. Returning to the window uses the existing one-shot focus check.
func (s *shell) openOnboardingVoiceDownload(tool *voiceTool, open func(string) error) {
	s.onboarding.errorText = ""
	if err := open(tool.installURL); err != nil {
		s.onboarding.errorText = "无法打开下载页面，请重试"
	}
	s.invalidateOnboardingWindow()
}

func onboardingLocationResultCurrent(ctx context.Context, started, current uint64, onVoicePage bool) bool {
	return ctx.Err() == nil && started == current && onVoicePage
}

// Installation status and artwork consume the same verified Bundle result.
// Loading artwork stays on the host thread, outside the view's drawing pass.
func (s *shell) applyOnboardingVoiceLocation(tool *voiceTool, bundle voicetools.Bundle, state voicetools.State, err error, load func(string, []byte) *ui.Bitmap) {
	s.onboarding.toolLocations[tool.id] = state
	s.onboarding.toolLocationErrors[tool.id] = err != nil
	if err != nil || (state != voicetools.Ready && state != voicetools.NotFound) {
		return
	}
	icon := s.voiceToolIcons[tool.id]
	if icon != nil && icon.cancel != nil {
		icon.cancel()
		icon.cancel = nil
	}
	path, version := "", ""
	if state == voicetools.Ready {
		path, version = bundle.Path, bundle.Version
	}
	if icon != nil && icon.path == path && icon.version == version {
		return
	}
	if s.voiceToolIcons == nil {
		s.voiceToolIcons = make(map[string]*voiceToolIcon)
	}
	s.voiceToolIcons[tool.id] = &voiceToolIcon{path: path, version: version, bitmap: load(path, tool.fallbackIconPNG)}
}
