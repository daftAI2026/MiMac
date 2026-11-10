package main

import (
	"errors"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
)

type setupAction uint8

const (
	setupNone setupAction = iota
	setupInputPermission
	setupOutputPermission
	setupBluetoothPermission
	setupBluetoothConnection
	setupRetryAudio
)

func (action setupAction) label() string {
	switch action {
	case setupInputPermission:
		return "允许输入监控"
	case setupOutputPermission:
		return "允许辅助功能"
	case setupBluetoothPermission:
		return "允许蓝牙连接"
	case setupBluetoothConnection:
		return "打开蓝牙设置"
	case setupRetryAudio:
		return "重新连接音频"
	}
	return ""
}

// Derive the next useful action from existing owners. This is presentation,
// not another device or voice lifecycle. Status labels stay read-only.
func (s *shell) nextSetupAction() setupAction {
	if s.deviceNeedsPermission {
		return setupInputPermission
	}
	if s.outputNeedsPermission {
		return setupOutputPermission
	}
	if s.audioRequested() {
		switch {
		case errors.Is(s.liveAudio.audioFailure, atvv.ErrBluetoothPermission):
			return setupBluetoothPermission
		case errors.Is(s.liveAudio.audioFailure, atvv.ErrBluetoothPoweredOff), errors.Is(s.liveAudio.audioFailure, atvv.ErrRemoteNotConnected):
			return setupBluetoothConnection
		}
	}
	if s.audioRequested() && s.liveAudio.audioFailure != nil && !errors.Is(s.liveAudio.audioFailure, errCompatRouteDeviceUID) {
		return setupRetryAudio
	}
	return setupNone
}

func (s *shell) performSetupAction(action setupAction) {
	if s.closing {
		return
	}
	switch action {
	case setupInputPermission:
		s.openInputPermissionSettings()
	case setupOutputPermission:
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility")
	case setupBluetoothPermission:
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_Bluetooth")
	case setupBluetoothConnection:
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.BluetoothSettings")
	case setupRetryAudio:
		if err := s.retryAudioSetup(); err != nil {
			s.toolError = err.Error()
		}
	}
}

// Activation may follow a visit to System Settings. Recheck a pending output
// grant through its existing owner; never enable a device or start capture.
func (s *shell) refreshSetupAfterActivation() {
	if s.closing {
		return
	}
	s.refreshPermissionGuide()
	if s.outputNeedsPermission && s.requireDeviceAccess() == nil {
		s.refreshRemoteOutput(false)
	}
	s.refreshAudioComponent()
	if s.page == pageDevice && s.monitor == nil && !s.deviceReadBusy {
		s.detectDevice()
	}
	s.invalidateDeviceView()
}
