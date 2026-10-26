package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/audiosession"
)

const remotePlugin = "remote-control"

var (
	errRemoteCleanupPending = errors.New("remote control: audio cleanup is not confirmed")
	errRemoteNoReservation  = errors.New("remote control: monitor did not return a voice-key reservation")
)

type remoteVoiceReservation interface {
	Close() error
}

type remoteReserveFunc func(snapshotPath string) (remoteVoiceReservation, error)

type remoteState struct {
	reservation           remoteVoiceReservation
	reservationGeneration uint64
	reserve               remoteReserveFunc
	snapshotPath          string
	postMain              func(func())
	closeAudio            func(context.Context, *audioSessionHost) error
	cleanedAudio          *audiosession.Runtime
	reserveQueued         uint64
	cleanupSequence       uint64
	reserveInFlight       bool
	cleanupInFlight       bool
	stopPending           bool
	err                   error
}

func (s *shell) remoteStart() (func() error, error) {
	stop := func() error { return s.stopRemotePlugin() }
	if s.monitor == nil {
		return stop, errors.New("remote control: Xiaomi monitor is unavailable")
	}
	if err := s.configureRemoteReservation(); err != nil {
		return stop, err
	}
	if s.remoteState.snapshotPath == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return stop, fmt.Errorf("remote control: user config directory: %w", err)
		}
		s.remoteState.snapshotPath = filepath.Join(configDir, "MiMac", "voice-mapping-recovery.plist")
	}
	s.remoteState.err = nil
	if s.coordinator != nil {
		snapshot := s.coordinator.core.Snapshot()
		if snapshot.Connected {
			if err := s.acquireRemoteReservation(snapshot.Generation, true); err != nil {
				s.remoteState.err = err
				return stop, err
			}
		}
	}
	return stop, nil
}

func (s *shell) configureRemoteReservation() error {
	if s.remoteState.reserve != nil {
		return nil
	}
	s.remoteState.reserve = func(path string) (remoteVoiceReservation, error) {
		monitor, ok := s.monitor.(interface {
			ReserveVoiceKey(string) (*xiaomi.VoiceKeyReservation, error)
		})
		if !ok {
			return nil, errors.New("remote control: monitor does not support voice-key reservation")
		}
		reservation, err := monitor.ReserveVoiceKey(path)
		if reservation == nil {
			return nil, err
		}
		return reservation, err
	}
	return nil
}

func (s *shell) acquireRemoteReservation(generation uint64, starting bool) error {
	if s.closing || generation == 0 || s.coordinator == nil || s.remoteState.reserve == nil {
		return nil
	}
	if s.remoteState.cleanupInFlight || s.remoteState.stopPending {
		return nil
	}
	if !starting && !s.remoteEnabled() {
		return nil
	}
	snapshot := s.coordinator.core.Snapshot()
	if !snapshot.Connected || snapshot.Generation != generation {
		return nil
	}
	if s.remoteState.reservation != nil {
		if s.remoteState.reservationGeneration == generation {
			return nil
		}
		return errors.New("remote control: prior device reservation is still owned")
	}
	if s.audioSession != nil && snapshot.HasSession {
		return errors.New("remote control: finish the active voice session before takeover")
	}
	if s.remoteState.snapshotPath == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("remote control: user config directory: %w", err)
		}
		s.remoteState.snapshotPath = filepath.Join(configDir, "MiMac", "voice-mapping-recovery.plist")
	}
	s.remoteState.reserveInFlight = true
	reservation, err := s.remoteState.reserve(s.remoteState.snapshotPath)
	s.remoteState.reserveInFlight = false
	if reservation != nil {
		// A non-nil reservation returned with an error still owns native
		// recovery resources and must remain reachable by Stop.
		s.remoteState.reservation = reservation
		s.remoteState.reservationGeneration = generation
	}
	if err != nil {
		s.remoteState.err = err
		return err
	}
	if reservation == nil {
		s.remoteState.err = errRemoteNoReservation
		return errRemoteNoReservation
	}
	s.remoteState.err = nil
	s.syncVoiceBinding()
	return nil
}

func (s *shell) stopRemotePlugin() error {
	if s.coordinator != nil {
		if err := s.coordinator.disconnect(); err != nil {
			s.remoteState.err = err
			return err
		}
		if s.coordinator.sink != nil && len(s.coordinator.sink.Held()) != 0 {
			s.remoteState.err = errors.New("remote control: synthesized keys are still held")
			return s.remoteState.err
		}
	}
	reservation := s.remoteState.reservation
	if reservation == nil {
		return nil
	}
	if s.audioSession != nil && s.remoteState.cleanedAudio != s.audioSession.runtime {
		if s.audioSession.runtime == nil {
			s.remoteState.err = errRemoteCleanupPending
			return s.remoteState.err
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := s.audioSession.close(ctx)
		if err != nil {
			s.remoteState.err = errors.Join(errRemoteCleanupPending, err)
			return s.remoteState.err
		}
		s.remoteState.cleanedAudio = s.audioSession.runtime
	}
	if err := reservation.Close(); err != nil {
		s.remoteState.err = err
		return err
	}
	s.remoteState.reservation = nil
	s.remoteState.reservationGeneration = 0
	s.remoteState.err = nil
	s.syncVoiceBinding()
	return nil
}

func (s *shell) remoteHasReservation() bool {
	if s.remoteState.reservation == nil || s.remoteState.err != nil ||
		s.remoteState.cleanupInFlight || s.remoteState.stopPending ||
		!s.remoteEnabled() || s.plugins.Failure(remotePlugin) != nil || s.coordinator == nil {
		return false
	}
	snapshot := s.coordinator.core.Snapshot()
	return snapshot.Connected && snapshot.Generation == s.remoteState.reservationGeneration
}

func (s *shell) remoteEnabled() bool {
	return s.plugins != nil && s.plugins.Active(remotePlugin)
}

func (s *shell) remoteBusy() bool {
	if s.remoteState.reserveInFlight || s.remoteState.cleanupInFlight ||
		s.remoteState.stopPending || s.remoteState.reserveQueued != 0 {
		return true
	}
	return s.plugins != nil && s.plugins.Failure(remotePlugin) != nil
}

func (s *shell) remoteButtonLabel() string {
	if s.plugins != nil && s.plugins.Failure(remotePlugin) != nil {
		return "恢复失败"
	}
	if s.remoteBusy() {
		return "准备中"
	}
	if s.remoteEnabled() {
		return "停用遥控器"
	}
	return "启用遥控器"
}

// remoteStatus projects existing owners into one UI result. A connected HID
// lease alone does not mean the selected voice action is executable.
func (s *shell) remoteStatus() (string, statusTone) {
	if (s.plugins != nil && s.plugins.Failure(remotePlugin) != nil) || s.deviceNeedsRestart || s.outputNeedsRestart {
		return "恢复失败", toneError
	}
	if s.deviceNeedsPermission {
		return "需要输入监控权限", toneWarning
	}
	if s.outputNeedsPermission {
		return "需要辅助功能权限", toneWarning
	}
	if s.remoteState.err != nil {
		return "恢复失败", toneError
	}
	if s.remoteState.stopPending {
		return "正在停用", toneNeutral
	}
	if s.remoteBusy() {
		return "正在连接", toneNeutral
	}
	if !s.remoteEnabled() {
		return "未启用", toneNeutral
	}
	if !s.remoteHasReservation() {
		if s.coordinator != nil && !s.coordinator.core.Snapshot().Connected {
			return "等待遥控器", toneNeutral
		}
		return "正在连接", toneNeutral
	}
	if _, err := s.selectedVoiceAction(); err != nil {
		label := s.remoteSetupPrompt()
		return label, toneNeutral
	}
	if s.outputFaulted() {
		return "输出已暂停", toneWarning
	}
	if !s.plugins.Active(outputPlugin) || s.coordinator.sink == nil {
		return "输出未就绪", toneWarning
	}
	if s.voiceStartBlocked != "" {
		return s.voiceStartBlocked, toneNeutral
	}
	return "已启用", toneActive
}

func (s *shell) remoteSetupPrompt() string {
	active := false
	for _, tool := range s.tools {
		if !tool.active {
			continue
		}
		active = true
		if tool.id == s.currentVoiceTool {
			if tool.busy {
				return "正在读取配置"
			}
			return "请检查语音配置"
		}
	}
	if !active {
		return "请启用语音插件"
	}
	return "请选择语音工具"
}

func (s *shell) remoteLabel() string    { label, _ := s.remoteStatus(); return label }
func (s *shell) remoteTone() statusTone { _, tone := s.remoteStatus(); return tone }

func (s *shell) toggleRemote() {
	if s.closing || s.remoteBusy() || s.plugins == nil {
		return
	}
	if s.remoteEnabled() {
		s.beginRemoteStop()
		return
	}
	s.remoteState.err = nil
	s.deviceNeedsPermission = false
	if err := s.plugins.Enable(remotePlugin); err != nil {
		s.remoteState.err = err
		if errors.Is(err, xiaomi.ErrInputMonitoringRequired) {
			s.deviceNeedsPermission = true
			s.deviceStatus = "需要输入监控权限"
			s.deviceTone = toneWarning
		}
		if errors.Is(err, xiaomi.ErrMonitorRestartRequired) ||
			s.plugins.Failure(remotePlugin) != nil || s.plugins.Failure(xiaomiPlugin) != nil {
			s.deviceNeedsRestart = true
		}
		s.syncVoiceBinding()
		s.invalidateDeviceView()
		return
	}
	s.syncVoiceBinding()
	s.refreshRemoteOutput(true)
	s.invalidateDeviceView()
}

// refreshRemoteOutput is safe to call after mapping and tool configuration
// changes. It never opens a target or starts microphone capture.
func (s *shell) refreshRemoteOutput(requestPermission bool) {
	if s.plugins == nil || s.coordinator == nil || !s.remoteHasReservation() ||
		!s.coordinator.hasExecutableMapping() || s.plugins.Active(outputPlugin) {
		return
	}
	if !keyoutput.HasAccess() {
		s.outputNeedsPermission = true
		if requestPermission {
			keyoutput.RequestAccess()
		}
		s.invalidateDeviceView()
		return
	}
	if err := s.plugins.Enable(outputPlugin); err != nil {
		s.outputNeedsPermission = errors.Is(err, keyoutput.ErrPermissionRequired)
		s.outputStatus = "无法启用按键输出"
		return
	}
	s.outputNeedsPermission = false
	s.outputStatus = ""
}

func (s *shell) beginRemoteStop() {
	if s.closing || s.remoteState.stopPending || s.remoteState.cleanupInFlight {
		return
	}
	s.remoteState.stopPending = true
	s.remoteState.cleanupInFlight = true
	s.remoteState.cleanupSequence++
	sequence := s.remoteState.cleanupSequence
	var disconnectErr error
	if s.coordinator != nil {
		disconnectErr = s.coordinator.disconnect()
		s.report.Core = s.coordinator.report()
	}
	host := s.audioSession
	s.closeRemoteAudio(host, func(audioErr error) {
		s.finishRemoteStop(sequence, host, disconnectErr, audioErr)
	})
}

func (s *shell) remoteDeviceEvent(event xiaomi.MonitorEvent) {
	if s.closing {
		return
	}
	if event.State == xiaomi.MonitorStateConnected {
		if s.remoteEnabled() && event.Device.Generation() != 0 {
			s.queueRemoteReserve(event.Device.Generation())
		}
		return
	}
	if event.State != xiaomi.MonitorStateDisconnected ||
		s.remoteState.reservation == nil ||
		s.remoteState.reservationGeneration != event.Device.Generation() ||
		s.remoteState.cleanupInFlight || s.remoteState.stopPending {
		return
	}
	s.beginRemoteDisconnectCleanup(event.Device.Generation())
}

func (s *shell) queueRemoteReserve(generation uint64) {
	if generation == 0 || s.remoteState.reserveQueued == generation ||
		s.remoteState.reservation != nil || s.remoteState.cleanupInFlight ||
		s.remoteState.stopPending || !s.remoteEnabled() {
		return
	}
	s.remoteState.reserveQueued = generation
	s.postRemoteMain(func() {
		if s.remoteState.reserveQueued == generation {
			s.remoteState.reserveQueued = 0
		}
		if s.closing || !s.remoteEnabled() || s.remoteState.cleanupInFlight || s.remoteState.stopPending {
			return
		}
		if err := s.acquireRemoteReservation(generation, false); err != nil {
			s.remoteState.err = err
		}
		s.syncVoiceBinding()
		s.refreshRemoteOutput(false)
		s.invalidateDeviceView()
	})
}

func (s *shell) beginRemoteDisconnectCleanup(generation uint64) {
	if s.closing || s.remoteState.cleanupInFlight || s.remoteState.stopPending {
		return
	}
	s.remoteState.cleanupInFlight = true
	s.remoteState.cleanupSequence++
	sequence := s.remoteState.cleanupSequence
	host := s.audioSession
	if host == nil {
		s.postRemoteMain(func() {
			s.finishRemoteDisconnect(sequence, generation, nil, nil)
		})
		return
	}
	s.closeRemoteAudio(host, func(audioErr error) {
		s.finishRemoteDisconnect(sequence, generation, host, audioErr)
	})
}

func (s *shell) closeRemoteAudio(host *audioSessionHost, done func(error)) {
	if host == nil {
		s.postRemoteMain(func() { done(nil) })
		return
	}
	if host.cancel != nil {
		host.cancel()
	}
	closeAudio := s.remoteState.closeAudio
	post := s.remoteState.postMain
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var err error
		if closeAudio != nil {
			err = closeAudio(ctx, host)
		} else if host.runtime != nil {
			err = host.close(ctx)
		}
		cancel()
		postRemoteMainWith(post, func() { done(err) })
	}()
}

func (s *shell) finishRemoteStop(sequence uint64, host *audioSessionHost, disconnectErr, audioErr error) {
	if s.closing {
		return
	}
	if sequence != s.remoteState.cleanupSequence {
		return
	}
	s.remoteState.cleanupInFlight = false
	s.remoteState.stopPending = false
	if audioErr != nil {
		s.remoteState.err = errors.Join(disconnectErr, audioErr)
		s.report.AudioCleanupFailed = true
		s.report.AudioError = audioErr.Error()
		s.invalidateDeviceView()
		return
	}
	if host != nil && host.runtime != nil {
		s.remoteState.cleanedAudio = host.runtime
	}
	if s.plugins.Active(audioSessionPlugin) {
		if err := s.plugins.Disable(audioSessionPlugin); err != nil {
			s.remoteState.err = errors.Join(disconnectErr, err)
			s.report.AudioCleanupFailed = true
			s.report.AudioError = err.Error()
			s.invalidateDeviceView()
			return
		}
	} else if s.audioSession == host {
		s.detachAudioSession(host)
	}

	var stopErr error
	if s.plugins.Active(outputPlugin) {
		if err := s.plugins.Disable(outputPlugin); err != nil {
			s.outputNeedsRestart = true
			s.remoteState.err = errors.Join(disconnectErr, err)
			s.invalidateDeviceView()
			return
		}
	}
	if err := s.plugins.Disable(remotePlugin); err != nil {
		s.remoteState.err = errors.Join(disconnectErr, err)
		s.deviceNeedsRestart = true
		s.invalidateDeviceView()
		return
	}
	if s.plugins.Active(xiaomiPlugin) {
		if err := s.plugins.Disable(xiaomiPlugin); err != nil {
			s.deviceNeedsRestart = true
			stopErr = errors.Join(stopErr, err)
		}
	}
	s.remoteState.err = errors.Join(disconnectErr, stopErr)
	s.deviceNeedsPermission = false
	s.outputNeedsPermission = false
	s.outputStatus = ""
	s.syncVoiceBinding()
	s.invalidateDeviceView()
}

func (s *shell) finishRemoteDisconnect(sequence uint64, generation uint64, host *audioSessionHost, audioErr error) {
	if s.closing {
		return
	}
	if sequence != s.remoteState.cleanupSequence {
		return
	}
	s.remoteState.cleanupInFlight = false
	if audioErr != nil {
		s.remoteState.err = audioErr
		s.report.AudioCleanupFailed = true
		s.report.AudioError = audioErr.Error()
		s.invalidateDeviceView()
		return
	}
	if host != nil && host.runtime != nil {
		s.remoteState.cleanedAudio = host.runtime
	}
	if s.plugins.Active(audioSessionPlugin) {
		if err := s.plugins.Disable(audioSessionPlugin); err != nil {
			s.remoteState.err = err
			s.report.AudioCleanupFailed = true
			s.report.AudioError = err.Error()
			s.invalidateDeviceView()
			return
		}
	} else if s.audioSession == host {
		s.detachAudioSession(host)
	}
	// Do not restore the device mapping until the output adapter has confirmed
	// that its synthesized key ledger is empty and its target is closed.
	if s.plugins.Active(outputPlugin) {
		if err := s.plugins.Disable(outputPlugin); err != nil {
			s.outputNeedsRestart = true
			s.remoteState.err = err
			s.invalidateDeviceView()
			return
		}
	}
	if s.remoteState.reservation != nil && s.remoteState.reservationGeneration == generation {
		if err := s.remoteState.reservation.Close(); err != nil {
			s.remoteState.err = err
			s.invalidateDeviceView()
			return
		}
		s.remoteState.reservation = nil
		s.remoteState.reservationGeneration = 0
	}
	s.remoteState.err = nil
	s.syncVoiceBinding()
	if s.remoteEnabled() && s.plugins.Failure(remotePlugin) == nil && s.coordinator != nil {
		snapshot := s.coordinator.core.Snapshot()
		if snapshot.Connected {
			if err := s.acquireRemoteReservation(snapshot.Generation, false); err != nil {
				s.remoteState.err = err
			}
		}
	}
	s.refreshRemoteOutput(false)
	s.invalidateDeviceView()
}

func (s *shell) postRemoteMain(fn func()) {
	postRemoteMainWith(s.remoteState.postMain, fn)
}

func postRemoteMainWith(post func(func()), fn func()) {
	if post == nil {
		// setupPlugins supplies MyGo's dispatcher in production. The fallback is
		// for synchronous tests that don't install one.
		fn()
		return
	}
	go post(fn)
}
