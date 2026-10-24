package main

import (
	"errors"

	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/bridge"
)

type keySink interface {
	Submit([]bridge.Effect) error
	ReleaseAll() error
	Close() error
	Held() []bridge.Key
}

func (s *shell) outputFaulted() bool {
	return s.coordinator != nil && s.coordinator.core.Snapshot().OutputFailureID != 0
}

func (s *shell) outputLabel() string {
	if s.outputNeedsRestart {
		return "输出未能清理，请重启 MiMac"
	}
	if s.outputNeedsPermission {
		return "需要辅助功能权限"
	}
	if s.outputFaulted() {
		return "输出已暂停，请松开按键后恢复"
	}
	if s.outputStatus != "" {
		return s.outputStatus
	}
	if s.output == nil {
		return "未启用"
	}
	return "已启用，额外发送快捷键"
}

func (s *shell) toggleOutput() {
	if s.closing || s.outputNeedsRestart || s.mappingLoadFailed {
		return
	}
	s.outputStatus = ""
	defer s.invalidateDeviceView()
	if s.outputFaulted() && s.output != nil {
		if !keyoutput.HasAccess() {
			s.outputNeedsPermission = true
			keyoutput.RequestAccess()
			return
		}
		if err := s.output.ReleaseAll(); err != nil {
			s.outputNeedsPermission = errors.Is(err, keyoutput.ErrPermissionRequired)
			return
		}
		failure := s.coordinator.core.Snapshot().OutputFailureID
		if err := s.coordinator.step(bridge.OutputRecovered(failure)); err != nil {
			s.outputStatus = "恢复失败"
			return
		}
		s.outputNeedsPermission = false
		return
	}
	if s.output != nil {
		if err := s.plugins.Disable(outputPlugin); err != nil {
			s.outputNeedsRestart = true
		}
		return
	}
	if !s.coordinator.hasExecutableMapping() {
		s.outputStatus = "请先设置按键映射"
		s.showPage(pageMappings)
		return
	}
	if !keyoutput.HasAccess() {
		s.outputNeedsPermission = true
		keyoutput.RequestAccess()
		return
	}
	if err := s.plugins.Enable(outputPlugin); err != nil {
		s.outputNeedsPermission = errors.Is(err, keyoutput.ErrPermissionRequired)
		s.outputStatus = "无法启用按键输出"
		return
	}
	s.outputNeedsPermission = false
}

func (d *deviceCoordinator) attachOutput(sink keySink) error {
	d.sink = sink
	return d.step(bridge.ScopeAvailable(true))
}

func (d *deviceCoordinator) detachOutput() error {
	if d.sink == nil {
		return nil
	}
	// Disabling the selected output makes its action scope unavailable.
	// Permission failures use OutputFailed instead of changing this scope.
	stepErr := d.step(bridge.ScopeAvailable(false))
	if closeErr := d.sink.Close(); closeErr != nil {
		return errors.Join(stepErr, closeErr)
	}
	// Close proves the native ledger was released even if the last ordinary
	// submission failed. Do not retain an already closed sink as a live module.
	d.sink = nil
	if failure := d.core.Snapshot().OutputFailureID; failure != 0 {
		return d.step(bridge.OutputRecovered(failure))
	}
	return nil
}

func (d *deviceCoordinator) hasExecutableMapping() bool {
	for button, keys := range d.mappings {
		if button != d.voiceButton && len(keys) != 0 {
			return true
		}
	}
	if d.voiceButton != "" && d.resolveVoiceAction != nil {
		if _, err := d.resolveVoiceAction(); err == nil {
			return true
		}
	}
	if d.resolveAction != nil {
		for button, ref := range d.actions {
			if button == d.voiceButton {
				continue
			}
			if _, err := d.resolveAction(ref); err == nil {
				return true
			}
		}
	}
	return false
}
