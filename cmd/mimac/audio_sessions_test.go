package main

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

type hostPCMSource struct {
	samples chan []int16
	reading chan struct{}
	stopped chan struct{}
	once    sync.Once
	closed  atomic.Int32
}

func newHostPCMSource() *hostPCMSource {
	return &hostPCMSource{samples: make(chan []int16, 1), reading: make(chan struct{}, 2), stopped: make(chan struct{})}
}
func (s *hostPCMSource) Read(ctx context.Context, dst []int16) (int, error) {
	select {
	case s.reading <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case samples := <-s.samples:
		return copy(dst, samples), nil
	case <-s.stopped:
		return copy(dst, []int16{7, -7, 9}), io.EOF // A valid final short block.
	}
}
func (s *hostPCMSource) Stop(context.Context) error {
	s.once.Do(func() { close(s.stopped) })
	return nil
}
func (s *hostPCMSource) Close() error { s.closed.Add(1); return nil }

type hostPCMSink struct {
	writes    chan []int16
	closed    chan struct{}
	closeOnce sync.Once
	drains    atomic.Int32
}

func newHostPCMSink() *hostPCMSink {
	return &hostPCMSink{writes: make(chan []int16, 4), closed: make(chan struct{})}
}
func (s *hostPCMSink) TryWrite(p []int16) error    { s.writes <- append([]int16(nil), p...); return nil }
func (s *hostPCMSink) Drain(context.Context) error { s.drains.Add(1); return nil }
func (s *hostPCMSink) Close() error                { s.closeOnce.Do(func() { close(s.closed) }); return nil }

func newSessionHostTest(t *testing.T) (*shell, chan func(), *hostPCMSource, *hostPCMSink) {
	t.Helper()
	s := &shell{}
	if err := s.setupPluginsWith(func(func(xiaomi.MonitorEvent)) (hidMonitor, error) { t.Fatal("hardware opened"); return nil, nil }); err != nil {
		t.Fatal(err)
	}
	tasks := make(chan func(), 8)
	s.audioPost = func(fn func()) { tasks <- fn }
	source, sink := newHostPCMSource(), newHostPCMSink()
	s.audioSource = func(context.Context, bridge.SessionToken) (audiosession.Source, audiooutput.Config, error) {
		return source, audiooutput.Config{DeviceUID: "explicit-test-route", SampleRate: 16000}, nil
	}
	if err := s.plugins.Enable(audioSessionPlugin); err != nil {
		t.Fatal(err)
	}
	s.audioOutput.factory = func(cfg audiooutput.Config) (audioOutputSink, error) {
		if cfg.DeviceUID != "explicit-test-route" || cfg.SampleRate != 16000 {
			t.Errorf("lost explicit source format/route: %+v", cfg)
		}
		return sink, nil
	}
	t.Cleanup(func() {
		if s.audioSession != nil {
			s.audioSession.cancel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.audioSession.runtime.Close(ctx); err != nil {
				t.Error(err)
			}
		}
		if s.plugins != nil {
			if err := s.plugins.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return s, tasks, source, sink
}

func beginHostVoice(t *testing.T, s *shell) bridge.PressToken {
	t.Helper()
	b, _ := bridge.NewBinding(bridge.PolicyAfterDrain, "Control", "Space")
	d, _ := bridge.NewDevice("synthetic-source", 1)
	if err := s.coordinator.attachOutput(&routedEffectsKeySink{}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.Configure(b), bridge.DeviceConnected(d)} {
		if err := s.coordinator.step(event); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := bridge.NewPressToken(d, "voice", 1)
	if err := s.coordinator.step(bridge.VoiceBegan(p)); err != nil {
		t.Fatal(err)
	}
	return p
}
func runHostTask(t *testing.T, tasks <-chan func()) {
	t.Helper()
	select {
	case fn := <-tasks:
		fn()
	case <-time.After(3 * time.Second):
		t.Fatal("missing main-thread completion")
	}
}
func waitHostSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("missing lifecycle signal")
	}
}

func TestAudioSessionHostCombinesCorePluginsAndPCM(t *testing.T) {
	s, tasks, source, sink := newSessionHostTest(t)
	if s.audioOutput.sink != nil {
		t.Fatal("plugin enable opened audio")
	}
	press := beginHostVoice(t, s)
	runHostTask(t, tasks) // Prepared -> Core -> OpenGate.
	waitHostSignal(t, source.reading)
	source.samples <- []int16{1, -2, 3}
	select {
	case pcm := <-sink.writes:
		if !reflect.DeepEqual(pcm, []int16{1, -2, 3}) {
			t.Fatal(pcm)
		}
	case <-time.After(time.Second):
		t.Fatal("PCM not forwarded")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks) // Source EOF + local drain -> end keys + resource disposal.
	waitHostSignal(t, sink.closed)
	if pcm := <-sink.writes; !reflect.DeepEqual(pcm, []int16{7, -7, 9}) {
		t.Fatalf("lost EOF tail: %v", pcm)
	}
	snapshot := s.coordinator.core.Snapshot()
	if snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.PressedButtons) != 0 || sink.drains.Load() != 1 || source.closed.Load() != 1 || s.report.AudioError != "" {
		t.Fatalf("incomplete local chain: %+v report=%+v", snapshot, s.report)
	}
}

func TestAudioSessionPluginRejectsUnverifiedSourceAndRollsBackOutput(t *testing.T) {
	s := &shell{}
	if err := s.setupPluginsWith(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.plugins.Enable(audioSessionPlugin); err == nil {
		t.Fatal("unverified source enabled")
	}
	if s.plugins.Active(audioSessionPlugin) || s.plugins.Active(audioOutputPlugin) || s.audioOutput.enabled || s.audioSession != nil {
		t.Fatal("failed activation retained output module")
	}
	if err := s.plugins.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAudioQuitDoesNotWaitOnNativeOpenOrMainCompletion(t *testing.T) {
	s, tasks, source, sink := newSessionHostTest(t)
	opening, release := make(chan struct{}), make(chan struct{})
	s.audioOutput.factory = func(audiooutput.Config) (audioOutputSink, error) { close(opening); <-release; return sink, nil }
	beginHostVoice(t, s)
	waitHostSignal(t, opening)
	prevented, quitCalls := 0, 0
	post := func(fn func()) { tasks <- fn }
	if !s.deferAudioQuit(func() { prevented++ }, post, func() { quitCalls++ }) {
		t.Fatal("active audio did not defer quit")
	}
	if !s.deferAudioQuit(func() { prevented++ }, post, func() { quitCalls++ }) {
		t.Fatal("second quit escaped while closing")
	}
	if prevented != 2 || quitCalls != 0 || !s.closing || s.audioQuitReady {
		t.Fatal("quit waited or completed before native Open returned")
	}
	close(release)
	runHostTask(t, tasks)
	waitHostSignal(t, sink.closed)
	if quitCalls != 1 || !s.audioQuitReady || s.report.Error != "" || source.closed.Load() != 1 {
		t.Fatalf("late-open shutdown: quit=%d report=%+v source=%d", quitCalls, s.report, source.closed.Load())
	}
	if s.deferAudioQuit(func() { t.Fatal("completed quit prevented") }, post, func() { t.Fatal("extra quit") }) {
		t.Fatal("completed quit deferred again")
	}
	if s.coordinator.core.Snapshot().HasSession {
		t.Fatal("quit retained logical session")
	}
}

func TestAudioCompletionQueuedBeforeShutdownCannotReopenGate(t *testing.T) {
	s, tasks, source, sink := newSessionHostTest(t)
	beginHostVoice(t, s)
	var prepared func()
	select {
	case prepared = <-tasks:
	case <-time.After(time.Second):
		t.Fatal("prepared callback missing")
	}
	quitCalls := 0
	s.deferAudioQuit(func() {}, func(fn func()) { tasks <- fn }, func() { quitCalls++ })
	prepared() // Already queued when quit began; must be ignored.
	runHostTask(t, tasks)
	waitHostSignal(t, sink.closed)
	select {
	case <-source.reading:
		t.Fatal("late completion opened the source gate")
	default:
	}
	if quitCalls != 1 {
		t.Fatal(quitCalls)
	}
}

func TestSynchronousUnloadWithPendingOpenFailsWithoutTakingOutputLock(t *testing.T) {
	s, tasks, _, sink := newSessionHostTest(t)
	opening, release := make(chan struct{}), make(chan struct{})
	s.audioOutput.factory = func(audiooutput.Config) (audioOutputSink, error) { close(opening); <-release; return sink, nil }
	beginHostVoice(t, s)
	waitHostSignal(t, opening)
	runner := s.audioSession.runtime
	err := s.plugins.Disable(audioSessionPlugin)
	if !errors.Is(err, audiosession.ErrCleanupPending) {
		t.Fatalf("pending unload = %v", err)
	}
	if s.audioSession == nil || s.coordinator.audio != runner || s.coordinator.voiceAudioBinding == nil {
		t.Fatal("pending cleanup detached the audio routing hook")
	}
	if !s.plugins.Active(audioOutputPlugin) || !s.plugins.Active(corePlugin) {
		t.Fatal("pending source lost dependencies")
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	waitHostSignal(t, sink.closed)
	// This test deliberately faults pluginrt; its permanent error is expected.
	s.plugins = nil
	_ = tasks
}
