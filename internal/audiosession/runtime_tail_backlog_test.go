package audiosession

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/bridge"
)

type backlogMeasuredSink struct {
	Sink
	mu          sync.Mutex
	pcm         []int16
	drainErr    error
	drainCalled bool
}

func (s *backlogMeasuredSink) TryWrite(p []int16) error {
	err := s.Sink.TryWrite(p)
	if err == nil {
		s.mu.Lock()
		s.pcm = append(s.pcm, p...)
		s.mu.Unlock()
	}
	return err
}
func (s *backlogMeasuredSink) Drain(ctx context.Context) error {
	err := s.Sink.Drain(ctx)
	s.mu.Lock()
	s.drainCalled = true
	s.drainErr = err
	s.mu.Unlock()
	return err
}
func TestRuntimeDrainsAcceptedBacklogAfterSourceStops(t *testing.T) {
	for _, packetsN := range []int{100, 150} {
		t.Run(fmt.Sprint(packetsN), func(t *testing.T) {
			_, transport, source := newRuntimeATVVFixture(t)
			output := newPacedRuntimeATVVTestSink()
			sink := &backlogMeasuredSink{Sink: output}
			t.Cleanup(func() { _ = sink.Close() })
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
				return Resources{Source: source, Sink: sink, SampleRate: 16000, WaitReceiver: func(ctx context.Context) error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}}, nil
			})
			t.Cleanup(func() {
				if transport.audioStarted.Load() && !transport.audioStopSeen.Load() {
					_ = transport.emit(atvv.NativeEventControlValue, []byte{0})
				}
			})
			core, device := newCoreForSessionTest(t, "backlog-probe")
			press, _, prepare := beginVoiceSession(t, core, device, 1)
			if err := r.Submit(prepare); err != nil {
				t.Fatal(err)
			}
			waitSignal(t, entered)
			if err := transport.emit(atvv.NativeEventControlValue, []byte{4, 3, 2, 11}); err != nil {
				t.Fatal(err)
			}
			packets, want := runtimeATVVPCMFixture(packetsN)
			for i, p := range packets {
				if err := transport.emit(atvv.NativeEventAudioValue, p); err != nil {
					t.Fatal(err)
				}
				waitATVVReadSamples(t, source, min((i+1)*240, 32000))
			}
			staged := source.samples.Load()
			unblock()
			prepared := waitCompletion(t, r)
			if prepared.Err != nil {
				t.Fatal(prepared.Err)
			}
			applySourceEffects(t, r, applyCompletion(t, core, prepared))
			started := time.Now()
			ended, err := core.Step(bridge.VoiceEnded(press))
			if err != nil {
				t.Fatal(err)
			}
			applySourceEffects(t, r, ended)
			waitSignal(t, transport.stopRequested)
			if err := transport.emit(atvv.NativeEventControlValue, []byte{0}); err != nil {
				t.Fatal(err)
			}
			var result Completion
			select {
			case result = <-r.Completions():
			case <-time.After(6 * time.Second):
				t.Fatal("completion timeout")
			}
			sink.mu.Lock()
			got := append([]int16(nil), sink.pcm...)
			drainCalled, drainErr := sink.drainCalled, sink.drainErr
			sink.mu.Unlock()
			t.Logf("packets=%d staged=%d source_read=%d accepted=%d wanted=%d elapsed=%s completion=%v drain_called=%v drain_err=%v", packetsN, staged, source.samples.Load(), len(got), len(want), time.Since(started), result.Err, drainCalled, drainErr)
			if result.Err != nil {
				applySourceEffects(t, r, applyExpectedFailure(t, core, result))
			} else {
				applySourceEffects(t, r, applyCompletion(t, core, result))
			}
			if result.Err != nil || !reflect.DeepEqual(got, want) || !drainCalled || drainErr != nil {
				t.Fatalf("valid buffered speech not fully drained: %v", result.Err)
			}
		})
	}
}
