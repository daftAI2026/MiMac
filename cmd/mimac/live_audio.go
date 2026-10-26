package main

import (
	"context"
	"errors"
	"log"
	"path/filepath"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/voicetools"
)

const compatibilityAudioBackend = "mimac-compat"

type liveAudioState struct {
	backend         string
	connection      *liveAudioConnection
	toolID, appPath string
	generation      uint64
	retiring        bool
	// Tests substitute native construction without opening hardware.
	factory func(context.Context, uint64, string) (liveAudioRoute, error)
}

func validateAudioBackend(backend string) error {
	if backend != "" && backend != compatibilityAudioBackend {
		return errors.New("audio-backend must be mimac-compat or empty")
	}
	return nil
}

func (s *shell) desiredLiveAudioTool() *voiceTool {
	if !s.audioRequested() || s.closing || s.plugins == nil || s.coordinator == nil ||
		!s.remoteHasReservation() || s.remoteState.stopPending || s.remoteState.cleanupInFlight {
		return nil
	}
	for _, tool := range s.tools {
		if tool.id == s.currentVoiceTool && (tool.id == "voice-doubao" || tool.id == appleSpeechToolID) && tool.active && !tool.busy && tool.snapshot.State == voicetools.Ready {
			if tool.id != appleSpeechToolID && !s.plugins.Active(outputPlugin) {
				return nil
			}
			if _, err := s.selectedVoiceAction(); err == nil || tool.id == appleSpeechToolID {
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
		if tool == nil || tool.id != s.liveAudio.toolID || tool.snapshot.AppPath != s.liveAudio.appPath || s.coordinator.core.Snapshot().Generation != s.liveAudio.generation {
			s.retireLiveAudio()
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
		resources, err := mygo.App.Path(mygo.PathResources)
		if err != nil {
			s.report.AudioError = err.Error()
			return
		}
		library := filepath.Join(resources, "libMiMacATVV.dylib")
		factory = func(ctx context.Context, generation uint64, appPath string) (liveAudioRoute, error) {
			if tool.id == appleSpeechToolID {
				return openAppleAudioRoute(generation, library, filepath.Join(resources, "libMiMacSpeech.dylib"))
			}
			return openLiveXiaomiRoute(ctx, generation, library, "com.bytedance.inputmethod.doubaoime", filepath.Join(appPath, "Contents/MacOS/DoubaoIme"), locator)
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
	s.report.AudioError = ""
	s.audioResources = connection.Prepare
	s.audioResourcesReady = func() bool {
		return !s.liveAudio.retiring && s.liveAudio.connection == connection && s.currentVoiceTool == tool.id && tool.active &&
			s.remoteHasReservation() && s.coordinator.core.Snapshot().Generation == generation && connection.Ready()
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
				s.report.AudioError = err.Error()
			}
			log.Printf("audio route: ready=%t error=%v", ready, err)
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
			s.invalidateDeviceView()
			return
		}
		s.refreshLiveAudio()
		s.stopForPowerWhenReady()
		s.invalidateDeviceView()
	})
}

func (s *shell) liveAudioStatus() (string, statusTone, bool) {
	if !s.audioRequested() {
		return "", toneNeutral, false
	}
	if s.liveAudio.retiring {
		return "正在切换语音工具", toneNeutral, true
	}
	if s.report.AudioError != "" {
		return "音频连接失败，请重新启用遥控器", toneError, true
	}
	if s.liveAudio.connection == nil {
		if s.currentVoiceTool != "voice-doubao" && s.currentVoiceTool != appleSpeechToolID {
			return "此工具尚不支持遥控器音频", toneNeutral, true
		}
		return "正在准备音频", toneNeutral, true
	}
	if err := s.liveAudio.connection.Err(); err != nil {
		return "音频连接失败，请重新启用遥控器", toneError, true
	}
	if !s.liveAudio.connection.Ready() && !s.coordinator.core.Snapshot().HasSession {
		return "正在准备音频", toneNeutral, true
	}
	return "", toneNeutral, false
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
}

func (s *shell) audioRequested() bool {
	return s.liveAudio.backend != "" || s.currentVoiceTool == appleSpeechToolID
}
