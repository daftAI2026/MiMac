package main

import (
	"errors"
	"fmt"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/platform/audioroute"
)

type audioSetupInspectorFake struct {
	capabilities audioroute.DeviceCapabilities
	err          error
	calls        int
	uid          string
}

func (i *audioSetupInspectorFake) DeviceByUID(uid string) (audioroute.DeviceCapabilities, error) {
	i.calls++
	i.uid = uid
	return i.capabilities, i.err
}

func TestRefreshAudioComponentUsesFixedUIDAndReportsMissing(t *testing.T) {
	s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
	inspector := &audioSetupInspectorFake{capabilities: audioroute.DeviceCapabilities{Exists: false}}
	s.liveAudio.componentInspector = func() (audioComponentInspector, error) { return inspector, nil }

	state := s.refreshAudioComponent()
	if inspector.calls != 1 || inspector.uid != compatAudioDeviceUID {
		t.Fatalf("component check = (%d calls, %q), want one read of the fixed UID", inspector.calls, inspector.uid)
	}
	if state.Component != audioComponentMissing || state.ComponentMessage != "没有找到遥控器音频组件" {
		t.Fatalf("component state = %q (%q), want missing", state.Component, state.ComponentMessage)
	}
}

func TestRefreshAudioComponentReportsInspectionError(t *testing.T) {
	s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
	checkErr := errors.New("Core Audio metadata query failed")
	s.liveAudio.componentInspector = func() (audioComponentInspector, error) {
		return &audioSetupInspectorFake{err: checkErr}, nil
	}

	state := s.refreshAudioComponent()
	if state.Component != audioComponentCheckFailed || state.ComponentMessage != "暂时无法检查遥控器音频组件" {
		t.Fatalf("component state = %q (%q), want check-failed", state.Component, state.ComponentMessage)
	}
}

func TestMissingCompatibilityComponentOnlyBlocksCompatibilityToolRoute(t *testing.T) {
	s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
	s.liveAudio.backend = compatibilityAudioBackend
	s.liveAudio.componentStatus = audioComponentMissing
	label, tone, applies := s.liveAudioStatus(s.coordinator.core.Snapshot())
	if label != "没有找到遥控器音频组件" || tone != toneError || !applies {
		t.Fatalf("compat route status = (%q, %v, %t), want missing component", label, tone, applies)
	}

	tool := s.tools[0]
	tool.id = appleSpeechToolID
	tool.audioRouteFactory = testAudioRouteFactory
	s.currentVoiceTool = appleSpeechToolID
	state := s.audioSetupSnapshot()
	if state.RouteLabel == "没有找到遥控器音频组件" || state.RouteTone == toneError {
		t.Fatalf("Apple speech route inherited compatibility component failure: %+v", state)
	}
	if state.ComponentNextStep != "Apple语音识别使用自己的语音输入，不需要遥控器音频组件" {
		t.Fatalf("Apple component next step = %q", state.ComponentNextStep)
	}
}

func TestLiveAudioStatusPreservesTypedBluetoothFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"permission", atvv.ErrBluetoothPermission, "请允许蓝牙连接"},
		{"powered off", atvv.ErrBluetoothPoweredOff, "请打开蓝牙"},
		{"remote disconnected", atvv.ErrRemoteNotConnected, "请连接遥控器"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
			s.liveAudio.backend = compatibilityAudioBackend
			s.liveAudio.audioFailure = fmt.Errorf("route setup: %w", test.err)
			label, tone, applies := s.liveAudioStatus(s.coordinator.core.Snapshot())
			if label != test.want || tone != toneError || !applies {
				t.Fatalf("status = (%q, %v, %t), want %q", label, tone, applies, test.want)
			}
		})
	}
}

func TestRetryAudioSetupIsBlockedDuringActiveVoice(t *testing.T) {
	s, _, _, _ := newSessionHostTest(t)
	beginHostVoice(t, s)

	message, _, blocked := s.audioRetryBlocked()
	if !blocked || message != "当前语音或按键尚未结束" {
		t.Fatalf("retry guard = (%q, %t), want active voice blocked", message, blocked)
	}
	err := s.retryAudioSetup()
	var setupErr *audioSetupFailure
	if !errors.As(err, &setupErr) || setupErr.message != message {
		t.Fatalf("retry error = %v, want guard failure %q", err, message)
	}
	if !s.coordinator.core.Snapshot().HasSession {
		t.Fatal("blocked retry altered the active Core session")
	}
}
