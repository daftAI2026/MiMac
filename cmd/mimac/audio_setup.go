package main

import (
	"errors"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/audioroute"
)

type audioComponentAvailability string

const (
	audioComponentUnknown     audioComponentAvailability = "unknown"
	audioComponentAvailable   audioComponentAvailability = "available"
	audioComponentMissing     audioComponentAvailability = "missing"
	audioComponentCheckFailed audioComponentAvailability = "check-failed"
)

// audioSetupProjection is a read-only view for settings. A present endpoint
// only proves that the system can resolve the component UID; it does not prove
// that the selected tool received audio or inserted text.
type audioSetupProjection struct {
	Backend           string
	Component         audioComponentAvailability
	ComponentMessage  string
	ComponentNextStep string
	RouteLabel        string
	RouteTone         statusTone
	RouteApplies      bool
	Failure           error
}

type audioComponentInspector interface {
	DeviceByUID(string) (audioroute.DeviceCapabilities, error)
}

type audioSetupFailure struct {
	message  string
	nextStep string
	cause    error
}

func (e *audioSetupFailure) Error() string    { return e.message }
func (e *audioSetupFailure) Unwrap() error    { return e.cause }
func (e *audioSetupFailure) NextStep() string { return e.nextStep }

func audioSetupNextStep(err error) string {
	var setupErr *audioSetupFailure
	if errors.As(err, &setupErr) {
		return setupErr.nextStep
	}
	return "请稍后重试"
}

func newAudioComponentInspector() (audioComponentInspector, error) {
	return audioroute.New()
}

// audioSetupSnapshot reads only state already owned by the host. It does not
// query Core Audio or start/stop an audio route.
func (s *shell) audioSetupSnapshot() audioSetupProjection {
	if s == nil {
		return audioSetupProjection{
			Component:         audioComponentUnknown,
			ComponentMessage:  "尚未检查遥控器音频组件",
			ComponentNextStep: "重新检查音频组件",
		}
	}
	component := s.liveAudio.componentStatus
	projection := audioSetupProjection{
		Backend:   s.liveAudio.backend,
		Component: component,
		Failure:   s.liveAudio.audioFailure,
		RouteTone: toneNeutral,
	}
	switch component {
	case audioComponentAvailable:
		projection.ComponentMessage = "已找到遥控器音频组件"
		projection.ComponentNextStep = "组件已找到；实际音频连接状态会在启用遥控器后显示"
	case audioComponentMissing:
		projection.ComponentMessage = "没有找到遥控器音频组件"
		projection.ComponentNextStep = "安装音频组件后重新检查；Apple语音识别不需要此组件"
	case audioComponentCheckFailed:
		projection.ComponentMessage = "暂时无法检查遥控器音频组件"
		projection.ComponentNextStep = "重新检查音频组件"
	default:
		projection.Component = audioComponentUnknown
		projection.ComponentMessage = "尚未检查遥控器音频组件"
		projection.ComponentNextStep = "重新检查音频组件"
	}
	if s.currentVoiceTool == appleSpeechToolID {
		projection.ComponentNextStep = "Apple语音识别使用自己的语音输入，不需要遥控器音频组件"
	} else if s.liveAudio.backend == "" {
		projection.ComponentNextStep = "当前语音工具使用它自己的麦克风设置"
	}

	var snapshot bridge.Snapshot
	if s.coordinator != nil && s.coordinator.core != nil {
		snapshot = s.coordinator.core.Snapshot()
	}
	projection.RouteLabel, projection.RouteTone, projection.RouteApplies = s.liveAudioStatus(snapshot)
	if s.currentVoiceTool != appleSpeechToolID && s.liveAudio.backend == compatibilityAudioBackend &&
		(component == audioComponentMissing || errors.Is(s.liveAudio.audioFailure, errCompatRouteDeviceUID)) {
		projection.RouteLabel = "没有找到遥控器音频组件"
		projection.RouteTone = toneError
		projection.RouteApplies = true
	}
	return projection
}

// refreshAudioComponent checks the app's fixed compatibility endpoint by UID.
// The Core Audio inspector only reads device metadata; it never opens a stream.
func (s *shell) refreshAudioComponent() audioSetupProjection {
	if s == nil || s.closing {
		return s.audioSetupSnapshot()
	}
	makeInspector := s.liveAudio.componentInspector
	if makeInspector == nil {
		makeInspector = newAudioComponentInspector
	}
	inspector, err := makeInspector()
	if err != nil {
		s.liveAudio.componentStatus = audioComponentCheckFailed
		return s.audioSetupSnapshot()
	}
	if inspector == nil {
		s.liveAudio.componentStatus = audioComponentCheckFailed
		return s.audioSetupSnapshot()
	}
	capabilities, err := inspector.DeviceByUID(compatAudioDeviceUID)
	if err != nil {
		s.liveAudio.componentStatus = audioComponentCheckFailed
		return s.audioSetupSnapshot()
	}
	if !capabilities.Exists || capabilities.InputChannels == 0 || capabilities.OutputChannels == 0 {
		s.liveAudio.componentStatus = audioComponentMissing
	} else {
		s.liveAudio.componentStatus = audioComponentAvailable
	}
	return s.audioSetupSnapshot()
}

// retryAudioSetup retries an existing connection's cleanup or preparation
// through the host's current retirement path. It never sends a target key or
// starts microphone capture as a readiness probe.
func (s *shell) retryAudioSetup() error {
	if s == nil || s.closing {
		return &audioSetupFailure{message: "MiMac正在退出，无法重试音频", nextStep: "重新打开MiMac后重试"}
	}
	if message, next, blocked := s.audioRetryBlocked(); blocked {
		return &audioSetupFailure{message: message, nextStep: next}
	}
	if s.requireDeviceAccess() != nil {
		return &audioSetupFailure{
			message:  "开发版未接管设备，不能重试音频连接",
			nextStep: "仅保存的音频来源会在日用版使用",
		}
	}
	if s.liveAudio.connection == nil && s.audioSession == nil {
		if s.selectedAudioRouteTool() == nil {
			return &audioSetupFailure{message: "当前工具不支持遥控器音频", nextStep: "选择支持的语音工具或使用该工具的Mac麦克风"}
		}
		if s.currentVoiceTool != appleSpeechToolID && s.liveAudio.backend == compatibilityAudioBackend && s.liveAudio.componentStatus == audioComponentMissing {
			return &audioSetupFailure{message: "没有找到遥控器音频组件", nextStep: "安装音频组件后重新检查"}
		}
		s.refreshLiveAudio()
		return nil
	}
	if s.liveAudio.connection == nil || s.audioSession == nil {
		return &audioSetupFailure{message: "音频资源状态不完整，暂不能重试", nextStep: "停用并重新启用遥控器"}
	}
	s.retireLiveAudio()
	return nil
}

func (s *shell) audioRetryBlocked() (message, next string, blocked bool) {
	if s == nil || s.closing {
		return "MiMac正在退出，不能重连音频", "重新打开MiMac后重试", true
	}
	if s.liveAudio.retiring || s.remoteState.stopPending || s.remoteState.cleanupInFlight ||
		s.remoteState.reserveInFlight || s.remoteState.reserveQueued != 0 {
		return "音频或遥控器正在切换", "等待操作完成后再重新连接音频", true
	}
	if s.coordinator != nil && s.coordinator.core != nil {
		snapshot := s.coordinator.core.Snapshot()
		if snapshot.HasSession || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 {
			return "当前语音或按键尚未结束", "结束语音并松开遥控器按键后再重新连接", true
		}
	}
	return "", "", false
}
