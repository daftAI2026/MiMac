package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiobackendprefs"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/voicetools"
)

const compatibilityAudioBackend = audiobackendprefs.CompatibilityBackend

// audioRouteConfig carries host-verified identity and resources into one
// voice tool's route adapter. It deliberately contains no device or Core state.
type audioRouteConfig struct {
	Generation      uint64
	AppPath         string
	Locator         voicetools.Locator
	ResourcesPath   string
	OnPartial       func(bridge.SessionToken, string, string) bool
	PartialsEnabled func() bool
}

type audioRouteFactory func(context.Context, audioRouteConfig) (liveAudioRoute, error)

type liveAudioState struct {
	backend                string
	selectInstalledBackend bool
	connection             *liveAudioConnection
	toolID, appPath        string
	generation             uint64
	retiring               bool
	recoveryAttempted      bool
	audioFailure           error
	componentStatus        audioComponentAvailability
	componentInspector     func() (audioComponentInspector, error)
	// Tests substitute native construction without opening hardware.
	factory func(context.Context, uint64, string) (liveAudioRoute, error)
}

// A packaged host with device access can adopt the installed component.
// Explicit shortcut-only preferences and isolated UI development stay silent.
func (s *shell) configureAudioBackend(startup audiobackendprefs.Startup, packaged bool) {
	s.liveAudio.backend = startup.Backend
	s.liveAudio.selectInstalledBackend = packaged && !s.isolatedDevelopment && !startup.Explicit
}

func validateAudioBackend(backend string) error {
	return audiobackendprefs.Validate(backend)
}

func (s *shell) desiredLiveAudioTool() *voiceTool {
	if s.requireDeviceAccess() != nil || !s.audioRequested() || s.closing || s.plugins == nil || s.coordinator == nil ||
		!s.remoteHasReservation() || s.remoteState.stopPending || s.remoteState.cleanupInFlight {
		return nil
	}
	for _, tool := range s.tools {
		if tool.id == s.currentVoiceTool && tool.audioRouteFactory != nil && tool.active && !tool.busy && tool.snapshot.State == voicetools.Ready {
			if !voiceToolUsesDirectText(tool) && !s.plugins.Active(outputPlugin) {
				return nil
			}
			if _, err := s.selectedVoiceAction(); err == nil || (voiceToolUsesDirectText(tool) && s.directTextVoiceSelected()) {
				return tool
			}
		}
	}
	return nil
}

// This is host composition only: the core still knows sessions, keys and PCM,
// while device, target selection and output stay in their respective adapters.
func (s *shell) refreshLiveAudio() {
	s.blockUnpreparedLiveAudio()
	if s.closing || s.powerInputBlocked() || s.liveAudio.retiring || s.remoteState.stopPending || s.remoteState.cleanupInFlight {
		return
	}
	tool := s.desiredLiveAudioTool()
	if connection := s.liveAudio.connection; connection != nil {
		if tool == nil || tool.id != s.liveAudio.toolID || tool.snapshot.AppPath != s.liveAudio.appPath ||
			s.coordinator.core.Snapshot().Generation != s.liveAudio.generation {
			s.retireLiveAudio()
		} else {
			s.recoverFailedLiveAudio(connection)
		}
		return
	}
	if tool == nil || s.audioSession != nil {
		return
	}
	generation := s.coordinator.core.Snapshot().Generation
	appPath, locator := tool.snapshot.AppPath, s.toolLocator
	factory := s.liveAudio.factory
	if factory == nil {
		resourcesPath, err := mygo.App.Path(mygo.PathResources)
		if err != nil {
			s.report.AudioError = err.Error()
			s.liveAudio.audioFailure = err
			return
		}
		factory = bindAudioRouteFactory(tool.audioRouteFactory, resourcesPath, locator, s.voiceOverlay.interim.Publish, s.voiceOverlay.interim.PartialsEnabled)
		if factory == nil {
			err := errors.New("selected voice tool has no audio route factory")
			s.report.AudioError = err.Error()
			s.liveAudio.audioFailure = err
			return
		}
	}
	// Do not borrow a target preference or open a device until the session host
	// has registered ownership of Close, including while preparation is pending.
	start := make(chan struct{})
	connection := newLiveAudioConnection(func(ctx context.Context) (liveAudioRoute, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-start:
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return factory(ctx, generation, appPath)
	})
	s.liveAudio.connection, s.liveAudio.toolID, s.liveAudio.appPath, s.liveAudio.generation = connection, tool.id, appPath, generation
	s.liveAudio.audioFailure = nil
	s.report.AudioError = ""
	s.audioResources = connection.Prepare
	s.audioResourcesReady = func() bool {
		return !s.liveAudio.retiring && s.liveAudio.connection == connection && s.currentVoiceTool == tool.id && tool.active &&
			s.remoteHasReservation() && s.coordinator.core.Snapshot().Generation == generation && connection.Ready() && connection.Err() == nil
	}
	s.audioResourcesClose = connection.Close
	s.audioResourcesBeforeStart = connection.BeforeStart
	s.audioResourcesDeliver = connection.DeliverTranscript
	if err := s.plugins.Enable(audioSessionPlugin); err != nil {
		connection.cancel() // The closed startup gate proves no native owner exists.
		s.liveAudio.connection = nil
		s.audioResources, s.audioResourcesReady, s.audioResourcesClose = nil, nil, nil
		s.audioResourcesBeforeStart = nil
		s.audioResourcesDeliver = nil
		s.report.AudioError = err.Error()
		s.liveAudio.audioFailure = err
		s.blockUnpreparedLiveAudio()
		go func() { _ = connection.Close(context.Background()) }()
		return
	}
	close(start)
	go s.watchLiveAudio(connection)
}

func (s *shell) watchLiveAudio(connection *liveAudioConnection) {
	<-connection.Done()
	update := func() {
		s.postRemoteMain(func() {
			if s.closing || s.liveAudio.connection != connection {
				return
			}
			ready, err := connection.Ready(), connection.Err()
			if err != nil {
				traceAudioSession(s.liveAudio.generation, 0, 0, "route_unavailable", err)
				s.report.AudioError = err.Error()
				s.liveAudio.audioFailure = err
				if errors.Is(err, errCompatRouteDeviceUID) {
					s.liveAudio.componentStatus = audioComponentMissing
				}
			} else {
				s.liveAudio.audioFailure = nil
				s.report.AudioError = ""
			}
			log.Printf("audio route: ready=%t error=%v", ready, err)
			s.recoverFailedLiveAudio(connection)
			s.invalidateDeviceView()
		})
	}
	update()
	changes := connection.Changes()
	for changes != nil {
		select {
		case <-connection.Closed():
			return
		case _, ok := <-changes:
			if !ok {
				return
			}
			update()
		}
	}
}

// recoverFailedLiveAudio rebuilds only an explicitly faulted Source, through
// the existing audio-session owner. One attempt is allowed between successful
// audio sessions; the same persistent failure cannot create a retry loop.
func (s *shell) recoverFailedLiveAudio(connection *liveAudioConnection) bool {
	if s == nil || connection == nil || s.liveAudio.connection != connection || s.liveAudio.recoveryAttempted ||
		s.liveAudio.retiring || s.audioSession == nil || s.plugins == nil || !s.plugins.Active(audioSessionPlugin) ||
		s.plugins.Failure(audioSessionPlugin) != nil || s.report.AudioCleanupFailed ||
		s.closing || !s.audioRequested() || s.coordinator == nil || s.coordinator.core == nil {
		return false
	}
	if !recoverableLiveAudioSourceFailure(connection.sourceFailure()) {
		return false
	}
	if _, _, blocked := s.audioRetryBlocked(); blocked || !s.remoteHasReservation() {
		return false
	}
	snapshot := s.coordinator.core.Snapshot()
	if snapshot.Generation == 0 || snapshot.Generation != s.liveAudio.generation ||
		s.remoteState.reservationGeneration != snapshot.Generation || len(snapshot.RecoveryKeys) != 0 {
		return false
	}
	tool := s.desiredLiveAudioTool()
	if tool == nil || tool.id != s.liveAudio.toolID || tool.snapshot.AppPath != s.liveAudio.appPath {
		return false
	}
	s.liveAudio.recoveryAttempted = true
	traceAudioSession(snapshot.Generation, 0, 0, "source_rebuild", connection.sourceFailure())
	s.retireLiveAudio()
	return s.liveAudio.retiring
}

func recoverableLiveAudioSourceFailure(err error) bool {
	if err == nil {
		return false
	}
	// These errors describe user-controlled availability or a revoked runtime
	// request. Rebuilding immediately cannot fix them and could prompt/retry in
	// a loop. A later explicit user action or normal route refresh can retry.
	switch {
	case errors.Is(err, atvv.ErrBluetoothPermission),
		errors.Is(err, atvv.ErrBluetoothPoweredOff),
		errors.Is(err, atvv.ErrRemoteNotConnected),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return false
	default:
		return true
	}
}

// Tool changes retire the previous session before preparing the next target.
// HID ownership survives this swap; failed cleanup retains its original host.
func (s *shell) retireLiveAudio() {
	if s.liveAudio.retiring || s.audioSession == nil {
		return
	}
	s.liveAudio.retiring = true
	host := s.audioSession
	stepErr := s.coordinator.step(bridge.RouteAvailable(false))
	s.closeRemoteAudio(host, func(err error) {
		if s.closing || s.audioSession != host {
			return
		}
		s.liveAudio.retiring = false
		if err = errors.Join(stepErr, err); err != nil {
			s.report.AudioError = err.Error()
			s.report.AudioCleanupFailed = true
			s.liveAudio.audioFailure = err
			s.invalidateDeviceView()
			return
		}
		if s.plugins.Active(audioSessionPlugin) {
			err = s.plugins.Disable(audioSessionPlugin)
		} else {
			s.detachAudioSession(host)
		}
		if err != nil {
			s.report.AudioError = err.Error()
			s.liveAudio.audioFailure = err
			s.invalidateDeviceView()
			return
		}
		s.refreshLiveAudio()
		s.stopForPowerWhenReady()
		s.invalidateDeviceView()
	})
}

func (s *shell) liveAudioStatus(snapshot bridge.Snapshot) (string, statusTone, bool) {
	if !s.audioRequested() {
		return "", toneNeutral, false
	}
	if s.liveAudio.retiring {
		return "正在切换语音工具", toneNeutral, true
	}
	if !s.selectedVoiceUsesDirectText() && s.effectiveAudioBackend() == compatibilityAudioBackend && s.liveAudio.componentStatus == audioComponentMissing {
		return "没有找到遥控器音频组件", toneError, true
	}
	if s.currentVoiceTool == hexStreamToolID && s.voiceStartBlocked == hexModelUnavailableText {
		return hexModelUnavailableText, toneNeutral, true
	}
	if label, ok := liveAudioFailureStatus(s.liveAudio.audioFailure); ok {
		return label, toneError, true
	}
	if s.liveAudio.audioFailure != nil {
		return "音频连接失败，请重新连接音频", toneError, true
	}
	if s.report.AudioError != "" {
		return "音频连接失败，请重新连接音频", toneError, true
	}
	if s.liveAudio.connection == nil {
		if s.selectedAudioRouteTool() == nil {
			return "此工具尚不支持遥控器音频", toneNeutral, true
		}
		return "正在准备音频", toneNeutral, true
	}
	if err := s.liveAudio.connection.Err(); err != nil {
		if label, ok := liveAudioFailureStatus(err); ok {
			return label, toneError, true
		}
		return "音频连接失败，请重新连接音频", toneError, true
	}
	if !s.liveAudio.connection.Ready() && !snapshot.HasSession {
		return "正在准备音频", toneNeutral, true
	}
	return "", toneNeutral, false
}

func liveAudioFailureStatus(err error) (string, bool) {
	switch {
	case errors.Is(err, atvv.ErrBluetoothPermission):
		return "请允许蓝牙连接", true
	case errors.Is(err, atvv.ErrBluetoothPoweredOff):
		return "请打开蓝牙", true
	case errors.Is(err, atvv.ErrRemoteNotConnected):
		return "请连接遥控器", true
	case errors.Is(err, errCompatRouteDeviceUID):
		return "没有找到遥控器音频组件", true
	default:
		return "", false
	}
}

func (s *shell) blockUnpreparedLiveAudio() {
	if s.audioRequested() && s.coordinator != nil && s.audioSession == nil {
		s.coordinator.voiceAudioBinding = func(bridge.ActionPlan) (bridge.Binding, error) {
			return bridge.Binding{}, errors.New("remote audio backend is not prepared")
		}
	}
}

func (s *shell) logAudioCompletion(event bridge.Event, failed bool, err error) {
	if s.audioRequested() {
		log.Printf("audio completion: event=%T cleanup_failed=%t error=%v", event, failed, err)
	}
	if s.voiceOverlay.meter.trace != nil {
		var overflow *atvv.QueueOverflowError
		if errors.As(err, &overflow) {
			if data, encodeErr := json.Marshal(overflow.Snapshot()); encodeErr == nil {
				log.Printf("audio source queue: %s", data)
			}
		}
	}
}

func (s *shell) audioRequested() bool {
	return s.effectiveAudioBackend() != "" || s.selectedVoiceUsesDirectText()
}

// Ordinary packaged launches can reuse an existing verified endpoint for a
// supported current tool. A stored or diagnostic opt-out stays shortcut-only.
func (s *shell) effectiveAudioBackend() string {
	if s.liveAudio.selectInstalledBackend && s.liveAudio.componentStatus == audioComponentAvailable &&
		!s.selectedVoiceUsesDirectText() && s.selectedAudioRouteTool() != nil {
		return compatibilityAudioBackend
	}
	return s.liveAudio.backend
}

func (s *shell) selectedAudioRouteTool() *voiceTool {
	if s == nil {
		return nil
	}
	for _, tool := range s.tools {
		if tool.id == s.currentVoiceTool && tool.audioRouteFactory != nil {
			return tool
		}
	}
	return nil
}

func bindAudioRouteFactory(routeFactory audioRouteFactory, resourcesPath string, locator voicetools.Locator,
	onPartial func(bridge.SessionToken, string, string) bool, partialsEnabled ...func() bool) func(context.Context, uint64, string) (liveAudioRoute, error) {
	if routeFactory == nil {
		return nil
	}
	return func(ctx context.Context, generation uint64, appPath string) (liveAudioRoute, error) {
		config := audioRouteConfig{
			Generation: generation, AppPath: appPath, Locator: locator, ResourcesPath: resourcesPath, OnPartial: onPartial,
		}
		if len(partialsEnabled) > 0 {
			config.PartialsEnabled = partialsEnabled[0]
		}
		return routeFactory(ctx, config)
	}
}
