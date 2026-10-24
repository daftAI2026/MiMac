package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

func TestAudioSessionAdoptsPreparedResourcesWithoutReopening(t *testing.T) {
	for _, prepareFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "partial failure"}[prepareFails], func(t *testing.T) {
			s := &shell{}
			if err := s.setupPluginsWith(nil); err != nil {
				t.Fatal(err)
			}
			tasks := make(chan func(), 8)
			s.audioPost = func(fn func()) { tasks <- fn }
			source, sink := newHostPCMSource(), newHostPCMSink()
			s.audioResources = func(_ context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
				if token.ID() == 0 {
					t.Error("missing Core token")
				}
				var err error
				if prepareFails {
					err = errors.New("source format rejected")
				}
				return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, err
			}
			if err := s.plugins.Enable(audioSessionPlugin); err != nil {
				t.Fatal(err)
			}
			s.audioOutput.factory = func(audiooutput.Config) (audioOutputSink, error) {
				t.Error("reopened prepared output")
				return nil, errors.New("unexpected Open")
			}
			t.Cleanup(func() {
				s.audioSession.cancel()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.audioSession.runtime.Close(ctx); err != nil {
					t.Error(err)
				}
				if err := s.plugins.Close(); err != nil {
					t.Error(err)
				}
			})
			p := beginHostVoice(t, s)
			runHostTask(t, tasks)
			if !prepareFails {
				waitHostSignal(t, source.reading)
				if err := s.coordinator.step(bridge.VoiceEnded(p)); err != nil {
					t.Fatal(err)
				}
				runHostTask(t, tasks)
			}
			waitHostSignal(t, sink.closed)
			if source.closed.Load() != 1 {
				t.Fatal("source ownership not released")
			}
			if prepareFails && s.report.AudioError == "" {
				t.Fatal("partial failure not reported")
			}
			if !prepareFails && sink.drains.Load() != 1 {
				t.Fatal("prepared output did not drain")
			}
		})
	}
}
