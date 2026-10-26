package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/adapters/textoutput"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/pluginrt"
)

const audioSessionPlugin = "audio-session"
const audioShutdownErrorPrefix = "audio shutdown: "

// A verified device plugin supplies the source and its negotiated PCM format
// and selected route. No production factory is installed until that device's
// real start/stop/tail contract is verified.
type audioSourceFactory func(context.Context, bridge.SessionToken) (audiosession.Source, audiooutput.Config, error)

type audioSessionHost struct {
	runtime          *audiosession.Runtime
	deliver          func(bridge.SessionToken, string) error
	cancel           context.CancelFunc
	closeResources   func(context.Context) error
	resourcesClosed  atomic.Bool
	resourcesCloseMu sync.Mutex
}

// Session resources close before their connection-level owner. A cancelled
// main-thread unload only checks completed cleanup; native work stays off-main.
func (h *audioSessionHost) close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if h.runtime != nil {
		if err := h.runtime.Close(ctx); err != nil {
			return err
		}
	}
	if h.closeResources == nil || h.resourcesClosed.Load() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(audiosession.ErrCleanupPending, err)
	}
	if !h.resourcesCloseMu.TryLock() {
		return audiosession.ErrCleanupPending
	}
	defer h.resourcesCloseMu.Unlock()
	if h.resourcesClosed.Load() {
		return nil
	}
	if err := h.closeResources(ctx); err != nil {
		return err
	}
	h.resourcesClosed.Store(true)
	return nil
}

// Only the session runtime owns this facade, and it closes the previous
// resource group before allowing another Prepare. The owner preserves partial
// native Open resources and failed closes through that same lifecycle.
type sessionOutput struct{ owner *audioOutputOwner }

func (s sessionOutput) TryWrite(pcm []int16) error      { return s.owner.tryWrite(pcm) }
func (s sessionOutput) Drain(ctx context.Context) error { return s.owner.drain(ctx) }
func (s sessionOutput) Close() error                    { return s.owner.close() }

func (s *shell) audioSessionManifest() pluginrt.Manifest {
	return pluginrt.Manifest{ID: audioSessionPlugin, Requires: []string{corePlugin, audioOutputPlugin}, Start: func() (pluginrt.StopFunc, error) {
		prepareSource := s.audioSource
		prepare := s.audioResources
		if prepareSource == nil && prepare == nil {
			return func() error { return nil }, errors.New("audio source has not been verified")
		}
		owner := s.audioOutput
		// A device may transfer already-prepared resources. Runtime remains
		// their sole session owner, including partial preparation failures.
		// The source-only path retains lazy output for other device adapters.
		if prepare == nil {
			prepare = func(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
				source, config, err := prepareSource(ctx, token)
				resources := audiosession.Resources{Source: source, SampleRate: config.SampleRate}
				if err != nil || ctx.Err() != nil {
					return resources, errors.Join(err, ctx.Err())
				}
				err = owner.open(config)
				resources.Sink = sessionOutput{owner}
				return resources, err
			}
		}
		runner, err := audiosession.New(prepare)
		if err != nil {
			return func() error { return nil }, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		host := &audioSessionHost{runtime: runner, cancel: cancel, closeResources: s.audioResourcesClose, deliver: s.audioResourcesDeliver}
		s.audioSession = host
		s.coordinator.audio = runner
		// Activating a verified audio factory must also select Core's audio
		// path for physical voice edges. Otherwise they remain shortcut-only.
		ready := s.audioResourcesReady
		s.coordinator.voiceAudioBinding = func(plan bridge.ActionPlan) (bridge.Binding, error) {
			if s.closing || s.audioSession != host || (ready != nil && !ready()) {
				return bridge.Binding{}, errors.New("audio session resources are not ready")
			}
			return voiceAudioBindingForAction(plan)
		}
		beforeStart := s.audioResourcesBeforeStart
		s.coordinator.beforeAudioStart = func() error {
			if s.closing || s.audioSession != host || (ready != nil && !ready()) {
				return errors.New("audio session resources are not ready")
			}
			if beforeStart == nil {
				return nil
			}
			// This is a stale-admission budget, not cancellation of an in-flight
			// CoreAudio/CF call. No polling or device preparation runs here.
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			started := time.Now()
			err := errors.Join(beforeStart(ctx), ctx.Err())
			log.Printf("audio route: before_start elapsed=%s error=%v", time.Since(started), err)
			if s.appleVoiceSelected() {
				// A text target problem is local to this press, not a failed
				// audio connection that requires reconnecting the remote.
				s.report.AudioError = ""
				s.voiceStartBlocked = appleTextTargetPrompt(err)
				s.outputNeedsPermission = errors.Is(err, textoutput.ErrPermissionDenied)
				if s.outputNeedsPermission {
					textoutput.RequestAccess()
				}
				return err
			}
			if err != nil {
				s.report.AudioError = err.Error()
				s.voiceStartBlocked = "语音输入未就绪，请重试"
			} else {
				s.report.AudioError = ""
			}
			return err
		}
		post := s.audioPost
		if post == nil {
			post = mygo.RunOnMain
		}
		go s.receiveAudioCompletions(ctx, host, post)
		err = s.coordinator.step(bridge.RouteAvailable(true))
		return func() error {
			cancel()
			routeErr := s.coordinator.step(bridge.RouteAvailable(false))
			// Normal quit awaits cleanup off-main before stopping modules. An
			// unprepared direct unload must fail promptly and pin dependencies,
			// never wait on a worker's native Open/Close while holding the UI.
			stoppedCtx, stop := context.WithCancel(context.Background())
			stop()
			closeErr := host.close(stoppedCtx)
			if closeErr == nil {
				s.detachAudioSession(host)
			}
			return errors.Join(routeErr, closeErr)
		}, err
	}}
}

func appleTextTargetPrompt(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, textoutput.ErrPermissionDenied):
		return "请允许辅助功能权限"
	case errors.Is(err, textoutput.ErrSecureInput):
		return "请退出密码输入后重试"
	case errors.Is(err, textoutput.ErrTargetChanged):
		return "输入位置已变化，请重试"
	case errors.Is(err, context.DeadlineExceeded):
		return "输入框响应超时，请重试"
	default:
		return "请将光标放入可编辑输入框"
	}
}

func (s *shell) receiveAudioCompletions(ctx context.Context, host *audioSessionHost, post func(func())) {
	for {
		select {
		case <-ctx.Done():
			return
		case completion := <-host.runtime.Completions():
			post(func() {
				if s.closing || s.audioSession != host || ctx.Err() != nil {
					return
				}
				s.logAudioCompletion(completion.Event, completion.CleanupFailed, completion.Err)
				var deliveryErr error
				if result := completion.Transcript; result != nil {
					snapshot := s.coordinator.core.Snapshot()
					if snapshot.HasSession && snapshot.Session.Token == result.Token && snapshot.Session.Phase == bridge.PhaseDraining && snapshot.Session.Binding.Policy() == bridge.PolicyAudio && host.deliver != nil {
						deliveryErr = host.deliver(result.Token, result.Text)
						if deliveryErr != nil {
							s.voiceStartBlocked = "文字未写入，请保持原输入框"
						}
					}
				}
				err := errors.Join(deliveryErr, s.coordinator.step(completion.Event))
				if completion.CleanupFailed {
					err = errors.Join(err, s.coordinator.step(bridge.RouteAvailable(false)))
					s.report.AudioCleanupFailed = true
				}
				if err = errors.Join(completion.Err, err); err != nil {
					s.report.AudioError = err.Error()
				}
				s.report.Core = s.coordinator.report()
				s.invalidateDeviceView()
			})
		}
	}
}

// deferAudioQuit leaves the MyGo loop alive while a worker releases native
// resources. Repeated quit requests reuse the same task. A failed attempt keeps
// the owner and voice gate, but permits a later quit request to retry cleanup.
func (s *shell) deferAudioQuit(prevent func(), post func(func()), quit func()) bool {
	if s.audioSession == nil || s.audioQuitReady {
		return false
	}
	prevent()
	if s.audioQuitPending {
		return true
	}
	s.audioQuitPending = true
	s.closing = true
	host := s.audioSession
	host.cancel()
	if err := s.coordinator.step(bridge.RouteAvailable(false)); err != nil {
		if s.report.Error == "" || strings.HasPrefix(s.report.Error, audioShutdownErrorPrefix) {
			s.report.Error = fmt.Sprintf("%s%v", audioShutdownErrorPrefix, err)
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := host.close(ctx)
		cancel()
		post(func() {
			s.audioQuitPending = false
			if err != nil {
				s.audioQuitReady = false
				// Invalidate any older remote cleanup completion and keep route
				// preparation disabled until a later quit confirms this owner closed.
				s.remoteState.cleanupSequence++
				s.remoteState.stopPending = true
				s.remoteState.cleanupInFlight = true
				if s.coordinator != nil {
					s.coordinator.voiceAudioBinding = func(bridge.ActionPlan) (bridge.Binding, error) {
						return bridge.Binding{}, errors.New("audio shutdown cleanup is pending")
					}
				}
				s.report.AudioCleanupFailed = true
				if s.report.Error == "" || strings.HasPrefix(s.report.Error, audioShutdownErrorPrefix) {
					s.report.Error = fmt.Sprintf("%s%v", audioShutdownErrorPrefix, err)
				}
				s.closing = false
				s.invalidateDeviceView()
				return
			}
			s.audioQuitReady = true
			// Clear only the error this shutdown path wrote. Preserve independent
			// diagnostics that may have been recorded earlier.
			if strings.HasPrefix(s.report.Error, audioShutdownErrorPrefix) {
				s.report.Error = ""
			}
			s.report.AudioCleanupFailed = false
			quit()
		})
	}()
	return true
}

// Only a confirmed cleanup may remove the audio routing hook. A failed close
// retains the hook, so another press cannot silently fall back to tool-only
// control while this session still owns audio resources.
func (s *shell) detachAudioSession(host *audioSessionHost) {
	if s.audioSession != host {
		return
	}
	s.liveAudio.retiring = false
	s.audioSession = nil
	if s.liveAudio.connection != nil {
		s.liveAudio.connection = nil
		s.audioResources, s.audioResourcesReady, s.audioResourcesClose = nil, nil, nil
		s.audioResourcesBeforeStart = nil
		s.audioResourcesDeliver = nil
		s.report.AudioError = ""
	}
	if s.coordinator != nil {
		s.coordinator.audio = nil
		s.coordinator.voiceAudioBinding = nil
		s.coordinator.beforeAudioStart = nil
	}
	s.blockUnpreparedLiveAudio()
}
