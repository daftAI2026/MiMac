package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
)

// The host owns the monitor lifetime, independently of disposable settings
// windows. Monitor callbacks and all methods here run on the MyGo main thread.
func (s *shell) toggleDeviceMonitor() {
	if s.closing || s.deviceNeedsRestart || s.powerInputBlocked() {
		return
	}
	s.deviceReadSequence++ // A discovery result must not overwrite live monitor state.
	s.deviceReadBusy = false
	if s.monitor != nil {
		err := s.plugins.Disable(s.deviceModule())
		s.deviceStatus = "已停止监听"
		s.deviceDetail = ""
		s.deviceTone = toneNeutral
		if err != nil {
			s.deviceNeedsRestart = true
			s.deviceStatus = "请重启 MiMac"
			s.deviceTone = toneError
			s.report.Error = fmt.Sprintf("device stop: %v", err)
		}
		s.invalidateDeviceView()
		return
	}
	s.deviceNeedsPermission = false
	s.deviceLastPress = ""
	s.deviceTone = toneNeutral
	s.deviceStatus = "等待支持的遥控器"
	s.deviceDetail = ""
	err := s.plugins.Enable(s.deviceModule())
	if err != nil {
		if errors.Is(err, xiaomi.ErrInputMonitoringRequired) {
			s.deviceNeedsPermission = true
			s.deviceStatus = "需要输入监控权限"
			s.deviceDetail = ""
			s.deviceTone = toneWarning
		} else if errors.Is(err, xiaomi.ErrMonitorRestartRequired) {
			s.deviceNeedsRestart = true
			s.deviceStatus = "请重启 MiMac"
			s.deviceTone = toneError
		} else {
			s.deviceStatus = "无法开始监听"
			s.deviceTone = toneError
		}
	}
	s.invalidateDeviceView()
}

func (s *shell) handleDeviceEvent(event xiaomi.MonitorEvent) {
	if s.closing || s.powerInputBlocked() || s.remoteState.stopPending {
		return
	}
	var accepted bool
	var err error
	if s.editButton != "" {
		accepted, err = s.coordinator.acceptWhileShortcutRecording(event)
	} else {
		accepted, err = s.coordinator.accept(event)
	}
	s.report.Core = s.coordinator.report()
	if err != nil {
		if accepted && (event.State == xiaomi.MonitorStateConnected || event.State == xiaomi.MonitorStateDisconnected) {
			s.remoteDeviceEvent(event)
		}
		s.outputNeedsPermission = errors.Is(err, keyoutput.ErrPermissionRequired)
		s.report.Error = fmt.Sprintf("core event: %v", err)
		s.deviceStatus = "按键处理异常"
		s.deviceTone = toneError
		s.invalidateDeviceView()
		return
	}
	if !accepted {
		return
	}
	if len(event.Edges) == 0 {
		if s.report.MonitorStates == nil {
			s.report.MonitorStates = make(map[string]int)
		}
		s.report.MonitorStates[event.State]++
	}
	switch event.State {
	case "connected":
		s.deviceStatus = "正在监听"
		s.deviceDetail = ""
		s.deviceTone = toneActive
	case "waiting", "disconnected":
		s.deviceStatus = "等待支持的遥控器"
		s.deviceDetail = ""
		s.deviceTone = toneNeutral
	case "ambiguous":
		s.deviceStatus = "发现多个匹配项"
		s.deviceDetail = "暂时无法确定遥控器"
		s.deviceTone = toneWarning
	case "unsupported":
		s.deviceStatus = "按键格式暂不支持"
		s.deviceDetail = ""
		s.deviceTone = toneWarning
	case "report_error":
		s.deviceStatus = "按键信号异常"
		s.deviceDetail = ""
		s.deviceTone = toneWarning
	}
	for _, edge := range event.Edges {
		s.report.LastButtonAt = time.Now().UTC().Format(time.RFC3339Nano)
		if s.report.ButtonDowns == nil {
			s.report.ButtonDowns = make(map[string]int)
			s.report.ButtonUps = make(map[string]int)
		}
		if edge.Kind == xiaomi.EdgeDown {
			s.report.ButtonDowns[edge.Press.PhysicalButtonID()]++
			if _, known := s.activeProfile().Button(edge.Press.PhysicalButtonID()); known && s.editButton == "" {
				s.selectedButton = edge.Press.PhysicalButtonID()
				if s.page == pageMappings {
					s.revealButton = s.selectedButton
				}
			}
		} else {
			s.report.ButtonUps[edge.Press.PhysicalButtonID()]++
		}
		label := s.buttonLabel(edge.Press.PhysicalButtonID())
		action := "已按下"
		if edge.Kind == xiaomi.EdgeUp {
			action = "已松开"
			if event.State != "connected" {
				action = "已结束"
			}
		}
		s.deviceLastPress = label + " " + action
	}
	if event.State == xiaomi.MonitorStateConnected || event.State == xiaomi.MonitorStateDisconnected {
		s.remoteDeviceEvent(event)
	}
	s.invalidateDeviceView()
}

func (s *shell) invalidateDeviceView() {
	s.syncVoiceOverlay()
	s.updateTrayMenu()
	if s.window != nil && !s.window.IsDestroyed() {
		s.window.Invalidate()
	}
}

func (s *shell) openInputPermissionSettings() {
	_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent")
}
