package main

import (
	"slices"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/bridge"
)

const toggleRecoveryLabel = "恢复快捷键…"

// This receipt binds a manual acknowledgement to the Core state shown in the
// dialog. It is not a target-idle observation or another session state machine.
type toggleRecoveryReceipt struct {
	coordinator *deviceCoordinator
	core        *bridge.Core
	steps       uint64
	keys        []bridge.Key
}

func (s *shell) toggleRecoveryNeeded() bool {
	return s.coordinator != nil && s.coordinator.core != nil && len(s.coordinator.core.Snapshot().RecoveryKeys) != 0
}

func (s *shell) toggleRecoveryReady() bool {
	if !s.toggleRecoveryNeeded() || s.closing || s.remoteEnabled() || s.remoteBusy() ||
		s.powerPaused() || s.powerState.stopping || s.audioQuitPending ||
		s.report.AudioCleanupFailed || s.remoteState.err != nil || s.liveAudio.retiring ||
		s.audioSession != nil || s.liveAudio.connection != nil {
		return false
	}
	d := s.coordinator
	state := d.core.Snapshot()
	return !state.HasSession && len(state.PressedButtons) == 0 && len(state.HeldKeys) == 0 &&
		state.OutputFailureID == 0 && (d.sink == nil || len(d.sink.Held()) == 0) &&
		(s.output == nil || len(s.output.Held()) == 0)
}

func (s *shell) captureToggleRecovery() (toggleRecoveryReceipt, bool) {
	if !s.toggleRecoveryReady() {
		return toggleRecoveryReceipt{}, false
	}
	d := s.coordinator
	return toggleRecoveryReceipt{d, d.core, d.steps, slices.Clone(d.core.Snapshot().RecoveryKeys)}, true
}

// Only an explicit acknowledgement can reach this method. Stop/reconnect,
// passive RunningInput=false and output cleanup never confirm a target.
func (s *shell) confirmToggleRecovery(receipt toggleRecoveryReceipt, confirmed bool) bool {
	if !confirmed || !s.toggleRecoveryReady() || receipt.coordinator != s.coordinator ||
		receipt.core != s.coordinator.core || receipt.steps != s.coordinator.steps ||
		!slices.Equal(receipt.keys, s.coordinator.core.Snapshot().RecoveryKeys) {
		return false
	}
	return s.coordinator.step(bridge.ConfirmToggleIdle(receipt.keys...)) == nil
}

func toggleRecoveryMessage(parent *mygo.Window) mygo.MessageOptions {
	return mygo.MessageOptions{
		Parent: parent, Type: mygo.MessageWarning, Title: "恢复快捷键",
		Message: "请先结束所有相关语音工具中的录音，并等待结果处理完成。",
		Detail:  "确认后可再次使用快捷键。此操作不会停止语音工具或撤回文字。",
		Buttons: []string{"取消", "已结束，恢复"}, DefaultButton: 0, CancelButton: 0,
	}
}

func (s *shell) requestToggleRecovery() {
	receipt, ready := s.captureToggleRecovery()
	if !ready {
		return
	}
	opts := toggleRecoveryMessage(s.window)
	go func() {
		result, err := mygo.Dialog.Message(opts)
		mygo.RunOnMain(func() {
			if s.closing {
				return
			}
			s.confirmToggleRecovery(receipt, err == nil && result.Button == 1)
			s.invalidateDeviceView()
		})
	}()
}

func (s *shell) remoteButtonDisabled() bool {
	return s.remoteBusy() || (!s.remoteEnabled() && s.toggleRecoveryNeeded() && !s.toggleRecoveryReady())
}
