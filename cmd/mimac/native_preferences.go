package main

import (
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	nativeprefs "mimac.local/mimac/internal/platform/preferences"
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
	permissions := s.snapshot
	if s.guideWindow == nil || !s.guideWindow.IsVisible() {
		permissions = s.readPermissionGuideSnapshot()
	}
	return nativeprefs.State{Login: p.loginEnabled, Dock: p.value.ShowInDock, VoiceOverlay: p.value.ShowVoiceOverlay, Appearance: string(p.value.Appearance), Dark: mygo.Theme.IsDark(), Disabled: p.loadFailed, Error: errorText, TextSize: p.value.TextSize, BodySize: float64(a.fonts.body), CaptionSize: float64(a.fonts.caption), Permissions: generalPermissionRows(permissions)}
}

func generalPermissionRows(snapshot permissionGuideSnapshot) []nativeprefs.PermissionRow {
	rows := make([]nativeprefs.PermissionRow, 0, 2)
	for _, row := range snapshot.rows {
		if permissionGuideAllowsAppDrag(row.id) {
			rows = append(rows, nativeprefs.PermissionRow{
				ID: int32(row.id), Title: row.title, Status: permissionGuideAccessLabel(row.access), Granted: row.access == permissionGuideGranted,
			})
		}
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
		VoiceOverlay: func(enabled bool) {
			_ = s.applyVoiceOverlayPreference(enabled)
			s.syncGeneralControls()
		},
		Appearance: func(source string) {
			_ = s.applyAppearancePreference(mygo.ThemeSource(source))
			s.syncGeneralControls()
		},
		ManagePermissions: func(value int32) {
			if value == int32(permissionGuideInput) || value == int32(permissionGuidePostEvent) {
				id := permissionGuideID(value)
				s.showPermissionGuideFor(id)
				if permissionGuideCanRestore(id, s.snapshot) {
					s.resumeID = id
				}
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
