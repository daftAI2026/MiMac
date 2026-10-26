package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

func TestAudioHostDeliversFinalTranscriptOnceAfterTailDrain(t *testing.T) {
	host, tasks, source, sink, delivered := newTranscriptAudioHost(t, nil)
	press, token := beginTranscriptAudioSession(t, host)
	runHostTask(t, tasks) // SourcePrepared is applied on the Core owner.
	waitHostSignal(t, source.reading)

	if err := host.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	waitHostSignal(t, sink.drainStarted)
	completion := runAndReturnHostTask(t, tasks)
	completion()
	waitHostSignal(t, sink.closed)

	if got := <-sink.writes; !reflect.DeepEqual(got, []int16{7, -7, 9}) {
		t.Fatalf("final source tail = %v, want [7 -7 9] before drain", got)
	}
	if sink.drains.Load() != 1 || sink.transcriptCalls.Load() != 1 {
		t.Fatalf("sink completion calls: drains=%d transcript=%d", sink.drains.Load(), sink.transcriptCalls.Load())
	}
	if !sink.drainSawTail.Load() {
		t.Fatal("sink Drain began before the source's final PCM tail was written")
	}
	if got, want := delivered.entries(), []transcriptDelivery{{token: token, text: "最终识别文本"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered transcript = %#v, want %#v", got, want)
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("successful transcript delivery left Core state: %+v", snapshot)
	}
}

func TestAudioHostDropsQueuedTranscriptAfterCoreRetiresSession(t *testing.T) {
	host, tasks, source, sink, delivered := newTranscriptAudioHost(t, nil)
	press, token := beginTranscriptAudioSession(t, host)
	runHostTask(t, tasks) // SourcePrepared opens the read gate.
	waitHostSignal(t, source.reading)
	if err := host.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	waitHostSignal(t, sink.drainStarted)
	lateCompletion := runAndReturnHostTask(t, tasks) // Already queued, but not applied yet.
	if sink.transcriptCalls.Load() != 1 {
		t.Fatalf("runtime did not finish transcript before queueing: calls=%d", sink.transcriptCalls.Load())
	}

	if err := host.coordinator.step(bridge.RouteAvailable(false)); err != nil {
		t.Fatal(err)
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession {
		t.Fatalf("route cancellation retained session before queued callback: %+v", snapshot)
	}
	waitHostSignal(t, sink.closed)
	lateCompletion()

	if got := delivered.entries(); len(got) != 0 {
		t.Fatalf("late transcript for retired token %v was delivered: %#v", token, got)
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("late completion changed retired Core state: %+v", snapshot)
	}
}

func TestAudioHostDoesNotDeliverTranscriptWhenDrainFails(t *testing.T) {
	drainErr := errors.New("recognizer drain failed")
	host, tasks, source, sink, delivered := newTranscriptAudioHost(t, drainErr)
	press, _ := beginTranscriptAudioSession(t, host)
	runHostTask(t, tasks)
	waitHostSignal(t, source.reading)
	if err := host.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	waitHostSignal(t, sink.drainStarted)
	runHostTask(t, tasks) // SourceFailed is applied; no Transcript is attached.
	waitHostSignal(t, sink.closed)

	if got := delivered.entries(); len(got) != 0 {
		t.Fatalf("transcript delivered after failed drain: %#v", got)
	}
	if sink.drains.Load() != 1 || sink.transcriptCalls.Load() != 0 {
		t.Fatalf("failed drain called Transcript: drains=%d transcript=%d", sink.drains.Load(), sink.transcriptCalls.Load())
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession || host.report.AudioError == "" {
		t.Fatalf("failed drain cleanup/report: snapshot=%+v report=%+v", snapshot, host.report)
	}
}

func TestAppleRoutePrearmsSourceButOpensRecognizerAtSessionStart(t *testing.T) {
	const generation = 94
	var sourceCalls, openCalls atomic.Int32
	target := &transcriptTargetTestStub{}
	route, err := newAppleAudioRoute(generation,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) {
			sourceCalls.Add(1)
			return newHostPCMSource(), nil
		},
		func() error { return nil },
		func(context.Context) (localRecognizer, error) {
			openCalls.Add(1)
			return &appleRecognizerTestStub{hostPCMSink: newHostPCMSink()}, nil
		},
		func() (transcriptTarget, error) { return target, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	var resources audiosession.Resources
	t.Cleanup(func() {
		if resources.Source != nil {
			_ = resources.Source.Close()
		}
		if resources.Sink != nil {
			_ = resources.Sink.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := route.Close(ctx); err != nil {
			t.Errorf("close Apple route: %v", err)
		}
	})

	if err := route.Start(); err != nil {
		t.Fatal(err)
	}
	waitXiaomiRoute(t, route.Ready)
	if sourceCalls.Load() != 1 || openCalls.Load() != 0 {
		t.Fatalf("idle route preparation opened recognizer: sources=%d recognizers=%d", sourceCalls.Load(), openCalls.Load())
	}
	if err := route.BeforeStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	resources, err = route.Prepare(context.Background(), xiaomiRouteSessionToken(t, generation))
	if err != nil {
		t.Fatal(err)
	}
	if openCalls.Load() != 0 {
		t.Fatalf("resource transfer opened recognizer before session start: %d", openCalls.Load())
	}
	sessionSink, ok := resources.Sink.(audiosession.SessionSink)
	if !ok {
		t.Fatalf("prepared Apple sink %T does not defer consumer start", resources.Sink)
	}
	if err := sessionSink.StartSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if openCalls.Load() != 1 {
		t.Fatalf("StartSession opened recognizer %d times, want once", openCalls.Load())
	}
	if err := resources.Source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatal(err)
	}
	resources = audiosession.Resources{}
}

type hostTranscriptTestSink struct {
	*hostPCMSink
	text            string
	drainErr        error
	drainStarted    chan struct{}
	drainStartOnce  sync.Once
	transcriptCalls atomic.Int32
	tailWritten     atomic.Bool
	drainSawTail    atomic.Bool
}

func newHostTranscriptTestSink(drainErr error) *hostTranscriptTestSink {
	return &hostTranscriptTestSink{
		hostPCMSink: newHostPCMSink(), text: "最终识别文本", drainErr: drainErr,
		drainStarted: make(chan struct{}),
	}
}

func (s *hostTranscriptTestSink) TryWrite(pcm []int16) error {
	if reflect.DeepEqual(pcm, []int16{7, -7, 9}) {
		s.tailWritten.Store(true)
	}
	return s.hostPCMSink.TryWrite(pcm)
}

func (s *hostTranscriptTestSink) Drain(ctx context.Context) error {
	s.drainStartOnce.Do(func() { close(s.drainStarted) })
	s.drainSawTail.Store(s.tailWritten.Load())
	if err := ctx.Err(); err != nil {
		return err
	}
	s.drains.Add(1)
	return s.drainErr
}

func (s *hostTranscriptTestSink) Transcript() (string, bool, error) {
	s.transcriptCalls.Add(1)
	return s.text, true, nil
}

type transcriptDelivery struct {
	token bridge.SessionToken
	text  string
}

type transcriptDeliveryRecorder struct {
	mu          sync.Mutex
	entriesList []transcriptDelivery
}

func (r *transcriptDeliveryRecorder) deliver(token bridge.SessionToken, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entriesList = append(r.entriesList, transcriptDelivery{token: token, text: text})
	return nil
}

func (r *transcriptDeliveryRecorder) entries() []transcriptDelivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]transcriptDelivery(nil), r.entriesList...)
}

func newTranscriptAudioHost(t *testing.T, drainErr error) (*shell, chan func(), *hostPCMSource, *hostTranscriptTestSink, *transcriptDeliveryRecorder) {
	t.Helper()
	host := &shell{}
	if err := host.setupPluginsWith(func(func(xiaomi.MonitorEvent)) (hidMonitor, error) {
		t.Fatal("hardware opened")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	tasks := make(chan func(), 8)
	host.audioPost = func(fn func()) { tasks <- fn }
	source, sink := newHostPCMSource(), newHostTranscriptTestSink(drainErr)
	deliveries := &transcriptDeliveryRecorder{}
	host.audioResources = func(context.Context, bridge.SessionToken) (audiosession.Resources, error) {
		return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	}
	host.audioResourcesDeliver = deliveries.deliver
	if err := host.plugins.Enable(audioSessionPlugin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if host.audioSession != nil {
			host.audioSession.cancel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := host.audioSession.close(ctx); err != nil {
				t.Errorf("close transcript host: %v", err)
			}
		}
		if host.plugins != nil {
			if err := host.plugins.Close(); err != nil {
				t.Errorf("close transcript host plugins: %v", err)
			}
		}
	})
	return host, tasks, source, sink, deliveries
}

func beginTranscriptAudioSession(t *testing.T, host *shell) (bridge.PressToken, bridge.SessionToken) {
	t.Helper()
	device, err := bridge.NewDevice("transcript-host", 1)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.Configure(bridge.NewAudioBinding())} {
		if err := host.coordinator.step(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := host.coordinator.step(bridge.VoiceBegan(press)); err != nil {
		t.Fatal(err)
	}
	snapshot := host.coordinator.core.Snapshot()
	if !snapshot.HasSession || snapshot.Session.Binding.Policy() != bridge.PolicyAudio {
		t.Fatalf("host did not start the keyless session: %+v", snapshot)
	}
	return press, snapshot.Session.Token
}

func runAndReturnHostTask(t *testing.T, tasks <-chan func()) func() {
	t.Helper()
	select {
	case fn := <-tasks:
		return fn
	case <-time.After(3 * time.Second):
		t.Fatal("missing queued main-thread completion")
		return nil
	}
}

type appleRecognizerTestStub struct{ *hostPCMSink }

func (*appleRecognizerTestStub) FinalText() (string, error) { return "识别文本", nil }

type transcriptTargetTestStub struct{ closeCalls atomic.Int32 }

func (*transcriptTargetTestStub) Insert(string) error { return nil }

func (t *transcriptTargetTestStub) Close() error {
	t.closeCalls.Add(1)
	return nil
}

var _ audiosession.Source = (*hostPCMSource)(nil)
var _ audiosession.TranscriptSink = (*hostTranscriptTestSink)(nil)
var _ localRecognizer = (*appleRecognizerTestStub)(nil)
