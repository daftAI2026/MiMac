package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/pluginrt"
)

const audioSessionPlugin = "audio-session"

// A verified device plugin supplies the source and its negotiated PCM format
// and selected route. No production factory is installed until that device's
// real start/stop/tail contract is verified.
type audioSourceFactory func(context.Context, bridge.SessionToken) (audiosession.Source, audiooutput.Config, error)

type audioSessionHost struct {
	runtime *audiosession.Runtime
	cancel  context.CancelFunc
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
		if prepareSource == nil {
			return func() error { return nil }, errors.New("audio source has not been verified")
		}
		owner := s.audioOutput
		runner, err := audiosession.New(func(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
			source, config, err := prepareSource(ctx, token)
			resources := audiosession.Resources{Source: source, SampleRate: config.SampleRate}
			if err != nil || ctx.Err() != nil {
				return resources, errors.Join(err, ctx.Err())
			}
			err = owner.open(config)
			resources.Sink = sessionOutput{owner}
			return resources, err
		})
		if err != nil {
			return func() error { return nil }, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		host := &audioSessionHost{runtime: runner, cancel: cancel}
		s.audioSession = host
		s.coordinator.audio = runner
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
			closeErr := runner.Close(stoppedCtx)
			if closeErr == nil {
				s.coordinator.audio = nil
				s.audioSession = nil
			}
			return errors.Join(routeErr, closeErr)
		}, err
	}}
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
				err := s.coordinator.step(completion.Event)
				if completion.CleanupFailed {
					err = errors.Join(err, s.coordinator.step(bridge.RouteAvailable(false)))
					s.report.AudioCleanupFailed = true
				}
				if err = errors.Join(completion.Err, err); err != nil {
					s.report.AudioError = err.Error()
				}
				s.report.Core = s.coordinator.report()
			})
		}
	}
}

// deferAudioQuit leaves the MyGo loop alive while a worker releases native
// resources. Repeated quit requests reuse the same task. A deadline records
// unconfirmed cleanup; the plugin dependency graph retains the live handles.
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
		s.report.Error = fmt.Sprintf("audio shutdown: %v", err)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := host.runtime.Close(ctx)
		cancel()
		post(func() {
			s.audioQuitReady = true
			if err != nil {
				s.report.AudioCleanupFailed = true
				s.report.Error = fmt.Sprintf("audio shutdown: %v", err)
			}
			quit()
		})
	}()
	return true
}
