package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"mimac.local/mimac/internal/platform/audioroute"
	"mimac.local/mimac/internal/platform/processidentity"
)

type receiverProbeSink struct {
	*compatRouteTestSink
	writes, drains int
	onDrain        func()
}

func (s *receiverProbeSink) TryWrite([]int16) error { s.writes++; return nil }
func (s *receiverProbeSink) Drain(context.Context) error {
	s.drains++
	if s.onDrain != nil {
		s.onDrain()
	}
	return nil
}

type receiverProbeInspector struct {
	*compatRouteTestInspector
	inspectErr   error
	afterInspect func()
}

func (i *receiverProbeInspector) ProcessInput(pid int32) (audioroute.ProcessInput, error) {
	if i.inspectErr != nil {
		return audioroute.ProcessInput{}, i.inspectErr
	}
	v, err := i.compatRouteTestInspector.ProcessInput(pid)
	if i.afterInspect != nil {
		i.afterInspect()
	}
	return v, err
}

func readyReceiverProbe(t *testing.T) (*compatAudioReceiverSink, *receiverProbeInspector, *compatRouteTestIdentity, *receiverProbeSink) {
	t.Helper()
	i := &receiverProbeInspector{compatRouteTestInspector: newCompatRouteTestInspector()}
	i.input = audioroute.ProcessInput{Found: true, PID: 42, RunningInput: true, InputDeviceUIDs: []string{compatAudioDeviceUID}}
	identity := &compatRouteTestIdentity{}
	sink := &receiverProbeSink{compatRouteTestSink: &compatRouteTestSink{}}
	receiver := &compatAudioReceiverSink{xiaomiPreparedSink: sink, inspector: i, identity: identity, pid: 42, deviceUID: compatAudioDeviceUID}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := receiver.WaitReceiver(ctx); err != nil {
		t.Fatal(err)
	}
	return receiver, i, identity, sink
}

func TestCompatReceiverRejectsChangedTargetBeforeFirstPCM(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*receiverProbeInspector, *compatRouteTestIdentity)
	}{
		{"externally stopped", func(i *receiverProbeInspector, _ *compatRouteTestIdentity) { i.input.RunningInput = false }},
		{"missing process", func(i *receiverProbeInspector, _ *compatRouteTestIdentity) { i.input.Found = false }},
		{"wrong pid", func(i *receiverProbeInspector, _ *compatRouteTestIdentity) { i.input.PID = 43 }},
		{"wrong input", func(i *receiverProbeInspector, _ *compatRouteTestIdentity) {
			i.input.InputDeviceUIDs = []string{"another-microphone"}
		}},
		{"unknown input", func(i *receiverProbeInspector, _ *compatRouteTestIdentity) { i.input.InputDeviceUIDs = nil }},
		{"multiple inputs", func(i *receiverProbeInspector, _ *compatRouteTestIdentity) {
			i.input.InputDeviceUIDs = []string{compatAudioDeviceUID, "another-microphone"}
		}},
		{"read error", func(i *receiverProbeInspector, _ *compatRouteTestIdentity) {
			i.inspectErr = errors.New("native query failed")
		}},
		{"identity changed", func(_ *receiverProbeInspector, id *compatRouteTestIdentity) {
			id.setError(processidentity.ErrIdentityChanged)
		}},
		{"identity changes during snapshot", func(i *receiverProbeInspector, id *compatRouteTestIdentity) {
			i.afterInspect = func() { id.setError(processidentity.ErrIdentityChanged) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, i, id, s := readyReceiverProbe(t)
			tc.mutate(i, id)
			if err := r.TryWrite([]int16{11, -11}); err == nil {
				t.Fatal("target loss accepted PCM")
			}
			if s.writes != 0 {
				t.Fatalf("wrote %d blocks after target loss", s.writes)
			}
			i.inspectErr = nil
			i.afterInspect = nil
			i.input = audioroute.ProcessInput{Found: true, PID: 42, RunningInput: true, InputDeviceUIDs: []string{compatAudioDeviceUID}}
			id.setError(nil)
			if err := r.TryWrite([]int16{12}); err == nil {
				t.Fatal("failed receiver resumed after target restarted")
			}
			if err := r.Drain(context.Background()); err == nil {
				t.Fatal("failed receiver reported successful drain")
			}
			if s.writes != 0 || s.drains != 0 {
				t.Fatalf("failed receiver touched sink: writes=%d drains=%d", s.writes, s.drains)
			}
		})
	}
}

func TestCompatReceiverChecksBothDrainBoundaries(t *testing.T) {
	for _, when := range []string{"before", "during"} {
		t.Run(when, func(t *testing.T) {
			r, i, _, s := readyReceiverProbe(t)
			if err := r.TryWrite([]int16{11}); err != nil {
				t.Fatal(err)
			}
			stop := func() { i.input.RunningInput = false }
			if when == "before" {
				stop()
			} else {
				s.onDrain = stop
			}
			if err := r.Drain(context.Background()); err == nil {
				t.Fatal("target stopped but receiver reported successful drain")
			}
			want := 0
			if when == "during" {
				want = 1
			}
			if s.drains != want {
				t.Fatalf("drains=%d want=%d", s.drains, want)
			}
		})
	}
}

func TestCompatReceiverHealthyWritesReuseRecentObservation(t *testing.T) {
	r, i, _, s := readyReceiverProbe(t)
	now := time.Unix(100, 0)
	r.now = func() time.Time { return now }
	for range 20 {
		if err := r.TryWrite([]int16{1}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _, _, calls := i.snapshot()
	if calls != 1 || s.writes != 20 {
		t.Fatalf("queries=%d writes=%d", calls, s.writes)
	}
	now = now.Add(compatReceiverCheckInterval)
	if err := r.TryWrite([]int16{2}); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, calls = i.snapshot()
	if calls != 2 {
		t.Fatalf("expired observation not refreshed: %d", calls)
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, calls = i.snapshot()
	if calls != 4 || s.drains != 1 {
		t.Fatalf("final checks=%d drains=%d", calls, s.drains)
	}
}

func TestCompatReceiverWatchDetectsStopWithoutPCM(t *testing.T) {
	r, i, _, s := readyReceiverProbe(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.WatchReceiver(ctx) }()
	i.setRunning(false)
	select {
	case err := <-done:
		if !errors.Is(err, errCompatReceiverStopped) {
			t.Fatalf("watch error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watch waited for source PCM")
	}
	if s.writes != 0 || s.drains != 0 {
		t.Fatal("watch started output")
	}
}

func TestCompatReceiverWatchCancellationDoesNotPoisonHealthyReceiver(t *testing.T) {
	r, _, _, s := readyReceiverProbe(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.WatchReceiver(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("watch cancellation=%v", err)
	}
	if err := r.TryWrite([]int16{1}); err != nil {
		t.Fatal(err)
	}
	if s.writes != 1 {
		t.Fatal("cancelled observation changed sink")
	}
}
