package main

import "github.com/egoist/mygo"

type trayState struct {
	status, remoteAction string
	remoteDisabled       bool
}

func (s *shell) showPage(page settingsPage) {
	s.navigate(page)
	s.showSettings()
	s.invalidateDeviceView()
}

// Keep a native menu attached to MyGo's tray. AppKit owns opening, placement,
// tracking and dismissal; refresh only when app state changes.
func (s *shell) updateTrayMenu() {
	if s.closing || s.tray == nil {
		return
	}
	state := trayState{
		status: s.remoteLabel(), remoteAction: s.remoteButtonLabel(), remoteDisabled: s.remoteButtonDisabled(),
	}
	if state == s.trayState {
		return
	}
	s.trayState = state
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: state.status, Disabled: true},
		{Label: state.remoteAction, Disabled: state.remoteDisabled, Click: func(*mygo.MenuItem, *mygo.Window) { s.toggleRemote() }},
		mygo.Separator(),
		{Label: "按键映射…", Click: func(*mygo.MenuItem, *mygo.Window) { s.showPage(pageMappings) }},
		{Label: "首次设置…", Click: func(*mygo.MenuItem, *mygo.Window) { s.showOnboarding() }},
		{Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) { s.showSettings() }},
		{Label: "系统权限…", Click: func(*mygo.MenuItem, *mygo.Window) { s.showPermissionGuide() }},
		mygo.Separator(),
		{Label: "退出 " + mygo.App.Name(), Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
	s.tray.SetMenu(menu)
}
