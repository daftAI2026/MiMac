package audiosession

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"mimac.local/mimac/internal/bridge"
)

type transcriptSinkStub struct {
	Sink
	started             atomic.Bool
	resultCalls         atomic.Int32
	startErr, resultErr error
}

func (s *transcriptSinkStub) StartSession(context.Context) error {
	s.started.Store(true)
	return s.startErr
}

func (s *transcriptSinkStub) Transcript() (string, bool, error) {
	s.resultCalls.Add(1)
	return "合成测试", true, s.resultErr
}

func TestTranscriptOnlyAccompaniesSuccessfulDrain(t *testing.T) {
	for _, resultErr := range []error{nil, errors.New("recognizer final failed")} {
		t.Run("result", func(t *testing.T) {
			source := newSourceStub()
			sink := &transcriptSinkStub{Sink: newSinkStub(), resultErr: resultErr}
			r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
				return Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
			})
			core, device := newCoreForSessionTest(t, "transcript")
			press, token, prepare := beginVoiceSession(t, core, device, 1)
			if err := r.Submit(prepare); err != nil {
				t.Fatal(err)
			}
			ready := waitCompletion(t, r)
			if !sink.started.Load() || ready.Transcript != nil {
				t.Fatal("result before prepared session")
			}
			applySourceEffects(t, r, applyCompletion(t, core, ready))
			ended, _ := core.Step(bridge.VoiceEnded(press))
			applySourceEffects(t, r, ended)
			finished := waitCompletion(t, r)
			if resultErr == nil {
				if finished.Transcript == nil || finished.Transcript.Token != token || finished.Transcript.Text != "合成测试" {
					t.Fatalf("missing scoped transcript: %#v", finished)
				}
			} else if !errors.Is(finished.Err, resultErr) || finished.Transcript != nil {
				t.Fatalf("failed final delivered: %#v", finished)
			}
			effects, err := core.Step(finished.Event)
			if err != nil {
				t.Fatal(err)
			}
			applySourceEffects(t, r, effects)
		})
	}
}

func TestSinkPreparationFailureNeverReadsOrEmitsTranscript(t *testing.T) {
	want := errors.New("model missing")
	source := newSourceStub()
	sink := &transcriptSinkStub{Sink: newSinkStub(), startErr: want}
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	core, device := newCoreForSessionTest(t, "prepare-failure")
	_, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	completion := waitCompletion(t, r)
	if !errors.Is(completion.Err, want) || completion.Transcript != nil || sink.resultCalls.Load() != 0 {
		t.Fatalf("bad preparation result: %#v", completion)
	}
	effects, err := core.Step(completion.Event)
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, r, effects)
	select {
	case <-source.readStarted:
		t.Fatal("read after preparation failed")
	default:
	}
}
