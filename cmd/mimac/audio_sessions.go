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
	hexapi "mimac.local/mimac/internal/adapters/hex"
	"mimac.local/mimac/internal/adapters/textoutput"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/pluginrt"
)

const audioSessionPlugin = "audio-session"
const audioShutdownErrorPrefix = "audio shutdown: "

var (
	errAudioSessionResourcesNotReady = errors.New("audio session resources are not ready")
	errAudioSessionUnavailable       = errors.New("audio session is unavailable")
)

// A verified device plugin supplies the source and its negotiated PCM format
// and selected route. No production factory is installed until that device's
// real start/stop/tail contract is verified.
type audioSourceFactory func(context.Context, bridge.SessionToken) (audiosession.Source, audiooutput.Config, error)

type audioSessionHost struct {
	runtime          *audiosession.Runtime
	deliver          func(bridge.SessionToken, string) error
	connection       *liveAudioConnection
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
		// Presentation observes this same source stream. It does not own a
		// capture device, alter PCM, or introduce another session state machine.
		prepareAudio := prepare
		prepare = func(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
			resources, err := prepareAudio(ctx, token)
			if err == nil && ctx.Err() == nil {
				if s.sourceCheck != nil {
					resources = s.sourceCheck.Wrap(token, resources)
				}
				if resources.Observer == nil {
					resources.Observer = s.voiceOverlay.meter.observer(ctx, token, resources.SampleRate)
				}
				resources = s.voiceOverlay.meter.trace.traceResources(token, resources)
			}
			return resources, err
		}
		runner, err := audiosession.New(prepare)
		if err != nil {
			return func() error { return nil }, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		host := &audioSessionHost{
			runtime: runner, cancel: cancel, closeResources: s.audioResourcesClose,
			deliver: s.audioResourcesDeliver, connection: s.liveAudio.connection,
		}
		s.audioSession = host
		s.coordinator.audio = runner
		// Activating a verified audio factory must also select Core's audio
		// path for physical voice edges. Otherwise they remain shortcut-only.
		ready := s.audioResourcesReady
		checkReady := func() error {
			if s.closing {
				return errAudioSessionUnavailable
			}
			if s.audioSession != host {
				return errAudioSessionResourcesNotReady
			}
			if ready != nil && !ready() {
				// A press can reach the main thread before the source's change
				// notification. Start eligible recovery before Core records it as
				// held, so the existing deferred press can follow the replacement.
				s.recoverFailedLiveAudio(host.connection)
				return errAudioSessionResourcesNotReady
			}
			return nil
		}
		s.coordinator.voiceAudioBinding = func(plan bridge.ActionPlan) (bridge.Binding, error) {
			if err := checkReady(); err != nil {
				return bridge.Binding{}, err
			}
			return voiceAudioBindingForAction(plan)
		}
		beforeStart := s.audioResourcesBeforeStart
		s.coordinator.beforeAudioStart = func() error {
			if err := checkReady(); err != nil {
				return err
			}
			if beforeStart == nil {
				return nil
			}
			if s.directTextVoiceSelected() {
				// The output lookup is optional and runs off-thread. A voice
				// press is also valid on a desktop, a button, or a slow editor.
				s.voiceStartBlocked = ""
				s.outputNeedsPermission = false
				err := beforeStart(ctx)
				if err == nil {
					s.prepareVoicePromptPresentation()
				}
				return err
			}
			// A compatibility route checks the external tool before signalling it.
			admission, stop := context.WithTimeout(ctx, 500*time.Millisecond)
			defer stop()
			started := time.Now()
			err := errors.Join(beforeStart(admission), admission.Err())
			log.Printf("audio route: before_start elapsed=%s error=%v", time.Since(started), err)
			if err != nil {
				s.report.AudioError = err.Error()
				s.voiceStartBlocked = "语音输入未就绪，请重试"
			} else {
				s.report.AudioError = ""
				s.prepareVoicePromptPresentation()
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

// A native error can occur after accepting text. An uncertain write must
// never be retried automatically or reported as definitely absent.
func directTextDeliveryPrompt(err error) string {
	switch {
	case errors.Is(err, textoutput.ErrPermissionDenied):
		return "请允许辅助功能权限"
	case errors.Is(err, textoutput.ErrSecureInput):
		return "请退出密码输入后重试"
	case errors.Is(err, textoutput.ErrUnsupportedText):
		return "此输入框不支持多行或控制字符"
	case errors.Is(err, textoutput.ErrTargetChanged):
		return "输入位置已变化，请重试"
	case errors.Is(err, textoutput.ErrEventPayload):
		return "文字未写入，此输入方式暂不可用"
	default:
		return "请检查输入结果"
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
				event, completionToken := bridge.AudioEventIdentity(completion.Event)
				if completion.CleanupFailed {
					event = "cleanup_failed"
				}
				if event == "drained" || event == "failed" {
					traceAudioCompletion(completionToken.Device().Generation(), completionToken.ID(), event, completion.Err, completion.PCMReceipt)
				} else {
					traceAudioSession(completionToken.Device().Generation(), 0, completionToken.ID(), event, completion.Err)
				}
				before := s.coordinator.core.Snapshot()
				successfulAudioDrain := completion.Err == nil && !completion.CleanupFailed && before.HasSession &&
					before.Session.Phase == bridge.PhaseDraining &&
					completion.Event == bridge.SourceDrained(before.Session.Token)
				var deliveryErr error
				var reportTextDelivery bool
				if result := completion.Transcript; result != nil {
					if before.HasSession && before.Session.Token == result.Token && before.Session.Phase == bridge.PhaseDraining && before.Session.Binding.Policy() == bridge.PolicyAudio && host.deliver != nil {
						deliveryErr = host.deliver(result.Token, result.Text)
						reportTextDelivery = result.Text != ""
						if reportTextDelivery && errors.Is(deliveryErr, textoutput.ErrNoInsertionTarget) {
							// The editor did not receive this result. Only an explicit
							// menu click copies it; retain one sentence in memory.
							s.uninsertedDictation = result.Text
							reportTextDelivery = false
							deliveryErr = nil
						} else if reportTextDelivery && deliveryErr == nil {
							s.uninsertedDictation = ""
						}
						if deliveryErr != nil {
							s.outputNeedsPermission = errors.Is(deliveryErr, textoutput.ErrPermissionDenied)
							s.voiceStartBlocked = directTextDeliveryPrompt(deliveryErr)
						}
					}
				}
				stepErr := s.coordinator.step(completion.Event)
				err := errors.Join(deliveryErr, stepErr)
				if completion.CleanupFailed {
					err = errors.Join(err, s.coordinator.step(bridge.RouteAvailable(false)))
					s.report.AudioCleanupFailed = true
				}
				if errors.Is(completion.Err, hexapi.ErrModelUnavailable) && s.currentVoiceTool == hexStreamToolID {
					s.voiceStartBlocked = hexModelUnavailableText
				}
				if err = errors.Join(completion.Err, err); err != nil {
					s.report.AudioError = err.Error()
				}
				after := s.coordinator.core.Snapshot()
				if before.HasSession && !after.HasSession {
					token := before.Session.Token
					switch {
					case completion.Event == bridge.SourceFailed(token):
						s.finishVoiceOverlay(token, true)
					case successfulAudioDrain && reportTextDelivery:
						s.finishVoiceOverlay(token, err != nil)
					}
				}
				if connection := s.liveAudio.connection; connection != nil {
					// A ready route does not rearm recovery. Only a matching,
					// source PCM block proves that the rebuilt source has worked.
					// This is not a sink or target acceptance acknowledgement. A
					// route that fails before producing PCM keeps its allowance spent.
					if s.liveAudio.recoveryAttempted && sourcePCMReceiptMatchesCurrentLiveSession(
						completion, before, after, host.connection, connection, s.liveAudio.generation,
					) {
						s.liveAudio.recoveryAttempted = false
						traceAudioSession(before.Generation, 0, before.Session.Token.ID(), "source_recovery_rearmed", completion.Err)
					}
					s.recoverFailedLiveAudio(connection)
				}
				s.coordinator.resumeDeferredVoice()
				s.report.Core = s.coordinator.report()
				s.invalidateDeviceView()
			})
		}
	}
}

func sourcePCMReceiptMatchesCurrentLiveSession(
	completion audiosession.Completion,
	before, after bridge.Snapshot,
	hostConnection, currentConnection *liveAudioConnection,
	liveGeneration uint64,
) bool {
	name, token := bridge.AudioEventIdentity(completion.Event)
	terminal := name == "failed" || name == "drained"
	return completion.SourcePCMRead && !completion.CleanupFailed && terminal &&
		before.HasSession && before.Session.Token == token && !after.HasSession &&
		before.Generation == after.Generation && liveGeneration == before.Generation &&
		hostConnection != nil && hostConnection == currentConnection
}

// deferAudioQuit leaves the MyGo loop alive while a worker releases native
// resources. Repeated quit requests reuse the same task. A failed attempt keeps
// the owner and voice gate, but permits a later quit request to retry cleanup.
func (s *shell) deferAudioQuit(prevent func(), post func(func()), quit func()) bool {
	if s.audioSession == nil || s.audioQuitReady {
		return false
	}
	s.cancelRemoteReservationRetry()
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
	s.report.AudioCleanupFailed = false
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
