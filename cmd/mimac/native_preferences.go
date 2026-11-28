package main

import (
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	nativeprefs "mimac.local/mimac/internal/platform/preferences"
	"strings"
)

func (s *shell) nativePreferencesState() nativeprefs.State {
	p := &s.preferencesState
	if !p.loginLoaded {
		p.loginEnabled = mygo.App.OpenAtLogin()
		p.loginLoaded = true
	}
	errorText := p.errorText
	if p.loginError != "" {
		errorText = p.loginError
	}
	a := appAppearance(mygo.Theme.IsDark(), p.value.TextSize)
	presentationIndex, _ := voicePresentationIndex(p.value.VoicePresentation)
	permissions := s.snapshot
	if s.guideWindow == nil || !s.guideWindow.IsVisible() {
		permissions = s.readPermissionGuideSnapshot()
	}
	liveTextSupported, liveTextStatus := voiceToolLiveTextSetting(s.selectedVoiceTool())
	return nativeprefs.State{
		Login: p.loginEnabled, Dock: p.value.ShowInDock, VoicePresentation: presentationIndex,
		ShowLiveText: p.value.ShowLiveText, LiveTextSupported: liveTextSupported, LiveTextStatus: liveTextStatus,
		OnlyShowMiMacVoicePrompt: p.value.OnlyShowMiMacVoicePrompt,
		VoicePromptTools:         s.voicePromptToolNames(),
		DotAtBottom:              p.value.DotAtBottom, Appearance: string(p.value.Appearance), Dark: mygo.Theme.IsDark(),
		Disabled: p.loadFailed, Error: errorText, TextSize: p.value.TextSize,
		BodySize: float64(a.fonts.body), CaptionSize: float64(a.fonts.caption), Permissions: generalPermissionRows(permissions),
	}
}

func (s *shell) voicePromptToolNames() string {
	var names []string
	for _, tool := range s.tools {
		if tool != nil && s.voicePluginVisible(tool) && tool.voicePromptSuppressor != nil && tool.voicePromptBundleID != "" {
			names = append(names, tool.name)
		}
	}
	return strings.Join(names, "、")
}

func generalPermissionRows(snapshot permissionGuideSnapshot) []nativeprefs.PermissionRow {
	rows := make([]nativeprefs.PermissionRow, 0, len(snapshot.rows))
	for _, row := range snapshot.rows {
		rows = append(rows, nativeprefs.PermissionRow{
			ID: int32(row.id), Title: row.title, Status: permissionGuideAccessLabel(row.access), Granted: row.access == permissionGuideGranted,
		})
	}
	return rows
}

func (s *shell) syncGeneralControls() {
	if s.closing || s.window == nil || s.page != pageGeneral {
		s.closeGeneralControls()
		return
	}
	state := s.nativePreferencesState()
	if s.generalControls != nil {
		if err := s.generalControls.Update(state); err != nil {
			s.report.Error = fmt.Sprintf("preferences: %v", err)
			s.closeGeneralControls()
			s.preferencesState.errorText = "通用设置暂不可用"
			s.window.Invalidate()
		}
		return
	}
	callbacks := nativeprefs.Callbacks{
		TextSize: func(size int) { _ = s.applyTextSizePreference(size); s.syncGeneralControls() },
		Login: func(enabled bool) {
			_ = s.applyLoginAtStartup(enabled)
			s.preferencesState.loginEnabled = mygo.App.OpenAtLogin()
			s.syncGeneralControls()
		},
		Dock: func(enabled bool) { _ = s.applyDockPreference(enabled); s.syncGeneralControls() },
		VoicePresentation: func(index int) {
			if mode, ok := voicePresentationModeForIndex(index); ok {
				_ = s.applyVoicePresentationPreference(mode)
			}
			s.syncGeneralControls()
		},
		ShowLiveText: func(enabled bool) {
			_ = s.applyLiveTextPreference(enabled)
			s.syncGeneralControls()
		},
		OnlyShowMiMacVoicePrompt: func(enabled bool) {
			_ = s.applyOnlyShowMiMacVoicePrompt(enabled)
			s.syncGeneralControls()
		},
		DotAtBottom: func(bottom bool) {
			_ = s.applyDotPlacement(bottom)
			s.syncGeneralControls()
		},
		Appearance: func(source string) {
			_ = s.applyAppearancePreference(mygo.ThemeSource(source))
			s.syncGeneralControls()
		},
		ManagePermissions: func(value int32) {
			id := permissionGuideID(value)
			if int32(id) != value {
				return
			}
			snapshot := s.readPermissionGuideSnapshot()
			if _, exists := permissionGuideAccessForID(snapshot, id); !exists {
				s.syncGeneralControls()
				return
			}
			s.onboarding.permissionGuideOpened = false
			s.showPermissionGuideFor(id)
			if permissionGuideCanRestore(id, s.snapshot) {
				s.resumeID = id
			}
		},
	}
	var err error
	s.generalControls, err = nativeprefs.New(s.window.NativeHandle(), nativeprefs.Layout{Left: float64(sidebarWidth + lineWidth), Top: float64(contentHeaderHeight)}, state, callbacks)
	if err != nil {
		s.report.Error = fmt.Sprintf("preferences: %v", err)
		s.preferencesState.errorText = "通用设置暂不可用"
	}
}

func (s *shell) closeGeneralControls() {
	if s.generalControls != nil {
		s.generalControls.Close()
		s.generalControls = nil
	}
}

func (s *shell) generalSettings(c *ui.Context, a appearance) {
	if s.generalControls == nil && s.preferencesState.errorText != "" {
		ui.Text(c, s.preferencesState.errorText).FontSize(a.fonts.caption).TextColor(a.theme.Danger).Role(ui.RoleStatus)
	}
}
