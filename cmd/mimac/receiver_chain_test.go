package main

import (
	"context"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

func TestReceiverChainPhysicalVoiceWaitsForReceiverBeforeCopy(t *testing.T) {
	t.Run("receiver confirms and PCM drains", func(t *testing.T) {
		f := newReceiverChainFixture(t)
		f.pressDown(t)
		waitReceiverChainSignal(t, f.sink.waiting)
		assertReceiverChainNoPCM(t, f.source, f.sink)
		if f.resolveCalls.Load() != 1 {
			t.Fatalf("physical voice start resolved tool action %d times, want 1", f.resolveCalls.Load())
		}
		assertReceiverChainCore(t, f, true, bridge.PhasePreparing)

		f.sink.receiverDecision <- nil
		runHostTask(t, f.tasks) // SourcePrepared on the Core owner opens Runtime's read gate.
		pcm := waitReceiverChainPCM(t, f.sink.writes)
		if !reflect.DeepEqual(pcm, []int16{17, -4, 3}) {
			t.Fatalf("copied PCM = %v", pcm)
		}
		if f.source.readCalls.Load() == 0 {
			t.Fatal("receiver became ready without reading the source")
		}

		f.pressUp(t)
		waitReceiverChainSignal(t, f.sink.drained)
		if snapshot := f.host.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseDraining {
			t.Fatalf("release before source drain did not keep the voice lease draining: %+v", snapshot)
		}
		runHostTask(t, f.tasks) // SourceDrained releases the tool shortcut and disposes the lease.
		waitReceiverChainSignal(t, f.source.closed)
		waitReceiverChainSignal(t, f.sink.closed)

		assertReceiverChainCore(t, f, false, 0)
		assertReceiverChainTargetStart(t, f.keySink, true)
		if f.resolveCalls.Load() != 1 || f.sink.writeCalls.Load() != 1 || f.sink.drainCalls.Load() != 1 || f.source.stopCalls.Load() != 1 {
			t.Fatalf("successful chain counts: resolve=%d writes=%d drains=%d stops=%d", f.resolveCalls.Load(), f.sink.writeCalls.Load(), f.sink.drainCalls.Load(), f.source.stopCalls.Load())
		}
		assertReceiverChainNoMainTask(t, f.tasks)
		closeReceiverChainAudioPlugin(t, f)
		if f.host.audioSession != nil || f.host.coordinator.audio != nil || f.host.coordinator.voiceAudioBinding != nil {
			t.Fatal("successful audio cleanup retained the host voice audio hook")
		}
	})

	t.Run("release cancels receiver wait", func(t *testing.T) {
		f := newReceiverChainFixture(t)
		f.pressDown(t)
		waitReceiverChainSignal(t, f.sink.waiting)
		assertReceiverChainNoPCM(t, f.source, f.sink)
		assertReceiverChainCore(t, f, true, bridge.PhasePreparing)

		f.pressUp(t)
		waitReceiverChainSignal(t, f.source.closed)
		waitReceiverChainSignal(t, f.sink.closed)

		assertReceiverChainNoPCM(t, f.source, f.sink)
		assertReceiverChainCore(t, f, false, 0)
		assertReceiverChainTargetStart(t, f.keySink, true)
		if f.resolveCalls.Load() != 1 || f.sink.waitCalls.Load() != 1 {
			t.Fatalf("cancelled wait restarted the tool or receiver: resolve=%d waits=%d", f.resolveCalls.Load(), f.sink.waitCalls.Load())
		}
		assertReceiverChainNoMainTask(t, f.tasks)
	})

	t.Run("receiver error retires one tool start", func(t *testing.T) {
		f := newReceiverChainFixture(t)
		f.pressDown(t)
		waitReceiverChainSignal(t, f.sink.waiting)
		assertReceiverChainNoPCM(t, f.source, f.sink)

		f.sink.receiverDecision <- io.ErrUnexpectedEOF
		runHostTask(t, f.tasks) // SourceFailed is applied on the same Core owner as physical input.
		waitReceiverChainSignal(t, f.source.closed)
		waitReceiverChainSignal(t, f.sink.closed)

		assertReceiverChainNoPCM(t, f.source, f.sink)
		if snapshot := f.host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.PressedButtons) != 1 {
			t.Fatalf("receiver failure left action/session state before physical release: %+v", snapshot)
		}
		f.pressUp(t)
		assertReceiverChainCore(t, f, false, 0)
		assertReceiverChainTargetStart(t, f.keySink, true)
		if f.resolveCalls.Load() != 1 || f.sink.waitCalls.Load() != 1 || f.host.report.AudioError == "" {
			t.Fatalf("receiver failure lifecycle: resolve=%d waits=%d report=%+v", f.resolveCalls.Load(), f.sink.waitCalls.Load(), f.host.report)
		}
		assertReceiverChainNoMainTask(t, f.tasks)
	})

	t.Run("resources not ready leave the press observed", func(t *testing.T) {
		f := newReceiverChainFixtureWithReadiness(t, func() bool { return false })
		f.pressDown(t)
		if f.resolveCalls.Load() != 1 {
			t.Fatalf("physical press resolved tool action %d times, want 1", f.resolveCalls.Load())
		}
		if snapshot := f.host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.PressedButtons) != 1 || len(snapshot.HeldKeys) != 0 {
			t.Fatalf("unready resources started a target action: %+v", snapshot)
		}
		if len(f.keySink.batches) != 0 || f.sink.waitCalls.Load() != 0 {
			t.Fatalf("unready resources emitted target/audio work: keys=%v receiver waits=%d", f.keySink.batches, f.sink.waitCalls.Load())
		}
		assertReceiverChainNoPCM(t, f.source, f.sink)

		f.pressUp(t)
		if len(f.host.coordinator.core.Snapshot().PressedButtons) != 0 || len(f.keySink.batches) != 0 {
			t.Fatalf("unready observed press did not retire cleanly: core=%+v keys=%v", f.host.coordinator.core.Snapshot(), f.keySink.batches)
		}
		assertReceiverChainNoMainTask(t, f.tasks)
	})
}

type receiverChainFixture struct {
	host         *shell
	tasks        chan func()
	keySink      *routedEffectsKeySink
	device       bridge.Device
	press        bridge.PressToken
	source       *receiverChainSource
	sink         *receiverChainSink
	resolveCalls atomic.Int32
}

func newReceiverChainFixture(t *testing.T) *receiverChainFixture {
	return newReceiverChainFixtureWithReadiness(t, nil)
}

func newReceiverChainFixtureWithReadiness(t *testing.T, ready func() bool) *receiverChainFixture {
	t.Helper()
	const generation = 87
	sources := make(chan *receiverChainSource, 4)
	sinks := make(chan *receiverChainSink, 4)
	config := audiooutput.Config{DeviceUID: "receiver-chain-test-output", SampleRate: 16000}
	route := newXiaomiAudioRouteForReceiverChain(t, generation, config, sources, sinks)
	waitXiaomiRoute(t, route.Ready)
	source, sink := <-sources, <-sinks
	if sink.prepareCalls.Load() != 1 {
		t.Fatalf("prewarmed sink Prepare calls = %d, want 1", sink.prepareCalls.Load())
	}
	select {
	case <-sink.waiting:
		t.Fatal("prewarm waited for the target receiver before a voice press")
	default:
	}

	host := &shell{}
	if err := host.setupPluginsWith(nil); err != nil {
		t.Fatal(err)
	}
	tasks := make(chan func(), 8)
	host.audioPost = func(fn func()) { tasks <- fn }
	host.audioResources = route.Prepare
	host.audioResourcesClose = route.Close
	if ready == nil {
		ready = route.Ready
	}
	host.audioResourcesReady = ready
	if err := host.plugins.Enable(audioSessionPlugin); err != nil {
		t.Fatal(err)
	}
	keySink := &routedEffectsKeySink{}
	if err := host.coordinator.attachOutput(keySink); err != nil {
		t.Fatal(err)
	}
	plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	host.coordinator.voiceButton = "voice"
	host.coordinator.preflightAction = func(bridge.ActionPlan) error { return nil }
	device, err := bridge.NewDevice("receiver-chain-device", generation)
	if err != nil {
		t.Fatal(err)
	}
	press := voiceAudioTestPress(t, device, 1)
	f := &receiverChainFixture{
		host: host, tasks: tasks, keySink: keySink, device: device, press: press,
		source: source, sink: sink,
	}
	host.coordinator.resolveVoiceAction = func() (bridge.ActionPlan, error) {
		f.resolveCalls.Add(1)
		return plan, nil
	}

	// Register after route startup so failure paths still release its prepared owner.
	t.Cleanup(func() {
		if host.audioSession != nil {
			host.audioSession.cancel()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := host.audioSession.close(ctx); err != nil {
				t.Errorf("close audio session host: %v", err)
			}
		}
		if host.plugins != nil {
			if err := host.plugins.Close(); err != nil {
				t.Errorf("close test plugins: %v", err)
			}
		}
	})
	return f
}

func TestAudioSessionBindingHookRequiresFactoryAndHostOwnership(t *testing.T) {
	t.Run("missing factory does not install hook", func(t *testing.T) {
		host := &shell{}
		if err := host.setupPluginsWith(nil); err != nil {
			t.Fatal(err)
		}
		keySink := &routedEffectsKeySink{}
		if err := host.coordinator.attachOutput(keySink); err != nil {
			t.Fatal(err)
		}
		plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
		host.coordinator.voiceButton = "voice"
		host.coordinator.resolveVoiceAction = func() (bridge.ActionPlan, error) { return plan, nil }
		device, err := bridge.NewDevice("no-audio-factory", 92)
		if err != nil {
			t.Fatal(err)
		}
		press := voiceAudioTestPress(t, device, 1)
		t.Cleanup(func() {
			if host.plugins != nil {
				if err := host.plugins.Close(); err != nil {
					t.Errorf("close plugins: %v", err)
				}
			}
		})

		if err := host.plugins.Enable(audioSessionPlugin); err == nil {
			t.Fatal("audio session enabled without a verified source factory")
		}
		if host.audioSession != nil || host.coordinator.audio != nil || host.coordinator.voiceAudioBinding != nil {
			t.Fatal("failed audio activation retained the voice audio hook")
		}
		acceptVoiceAudioEdge(t, host.coordinator, device, press, devices.EdgeDown)
		acceptVoiceAudioEdge(t, host.coordinator, device, press, devices.EdgeUp)
		assertReceiverChainTargetStart(t, keySink, true)
		if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.PressedButtons) != 0 {
			t.Fatalf("tool-only action retained audio/Core state: %+v", snapshot)
		}
	})

	t.Run("old host cannot detach replacement", func(t *testing.T) {
		host := &shell{coordinator: newDeviceCoordinator()}
		oldSession, newSession := &audioSessionHost{}, &audioSessionHost{}
		liveAudio := &routedEffectsAudioSink{}
		liveBinding := func(bridge.ActionPlan) (bridge.Binding, error) { return bridge.Binding{}, nil }
		host.audioSession = newSession
		host.coordinator.audio = liveAudio
		host.coordinator.voiceAudioBinding = liveBinding

		host.detachAudioSession(oldSession)
		if host.audioSession != newSession || host.coordinator.audio != liveAudio || host.coordinator.voiceAudioBinding == nil {
			t.Fatal("stale host cleanup detached the replacement audio session")
		}
	})
}

func newXiaomiAudioRouteForReceiverChain(t *testing.T, generation uint64, config audiooutput.Config,
	sources chan<- *receiverChainSource, sinks chan<- *receiverChainSink,
) *xiaomiAudioRoute {
	t.Helper()
	route := newXiaomiRouteTest(t, generation, config,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) {
			source := newReceiverChainSource()
			sources <- source
			return source, nil
		},
		func() error { return nil },
		func(got audiooutput.Config) (xiaomiPreparedSink, error) {
			if got != config {
				t.Errorf("receiver-chain sink config = %+v, want %+v", got, config)
			}
			sink := newReceiverChainSink()
			sinks <- sink
			return sink, nil
		})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := route.Close(ctx); err != nil {
			t.Errorf("close receiver-chain route: %v", err)
		}
	})
	return route
}

func (f *receiverChainFixture) pressDown(t *testing.T) {
	t.Helper()
	acceptVoiceAudioEdge(t, f.host.coordinator, f.device, f.press, devices.EdgeDown)
}

func (f *receiverChainFixture) pressUp(t *testing.T) {
	t.Helper()
	acceptVoiceAudioEdge(t, f.host.coordinator, f.device, f.press, devices.EdgeUp)
}

func closeReceiverChainAudioPlugin(t *testing.T, f *receiverChainFixture) {
	t.Helper()
	host := f.host.audioSession
	if host == nil {
		t.Fatal("audio session host disappeared before explicit cleanup")
	}
	host.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := host.close(ctx); err != nil {
		t.Fatalf("close audio session host: %v", err)
	}
	if err := f.host.plugins.Disable(audioSessionPlugin); err != nil {
		t.Fatalf("disable audio-session plugin after confirmed cleanup: %v", err)
	}
}

func assertReceiverChainCore(t *testing.T, f *receiverChainFixture, active bool, phase bridge.Phase) {
	t.Helper()
	snapshot := f.host.coordinator.core.Snapshot()
	if active {
		if !snapshot.HasSession || snapshot.Session.Phase != phase || len(snapshot.PressedButtons) != 1 || !reflect.DeepEqual(snapshot.HeldKeys, []bridge.Key{"RightCommand"}) {
			t.Fatalf("active receiver-chain state = %+v", snapshot)
		}
		return
	}
	if snapshot.HasSession || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 || f.host.coordinator.report().Pressed != 0 {
		t.Fatalf("receiver-chain cleanup left Core state = %+v report=%+v", snapshot, f.host.coordinator.report())
	}
}

func assertReceiverChainTargetStart(t *testing.T, sink *routedEffectsKeySink, released bool) {
	t.Helper()
	var downs, ups []bridge.Key
	taps := 0
	for _, batch := range sink.batches {
		for _, effect := range batch {
			switch e := effect.(type) {
			case bridge.KeyDownEffect:
				downs = append(downs, e.Key)
			case bridge.KeyUpEffect:
				ups = append(ups, e.Key)
			case bridge.TargetTapEffect:
				taps++
			}
		}
	}
	wantUps := []bridge.Key(nil)
	if released {
		wantUps = []bridge.Key{"RightCommand"}
	}
	if !reflect.DeepEqual(downs, []bridge.Key{"RightCommand"}) || !reflect.DeepEqual(ups, wantUps) || taps != 0 {
		t.Fatalf("tool shortcut effects: down=%v up=%v taps=%d", downs, ups, taps)
	}
}

func assertReceiverChainNoPCM(t *testing.T, source *receiverChainSource, sink *receiverChainSink) {
	t.Helper()
	if source.readCalls.Load() != 0 || sink.writeCalls.Load() != 0 {
		t.Fatalf("PCM crossed receiver gate early: reads=%d writes=%d", source.readCalls.Load(), sink.writeCalls.Load())
	}
	select {
	case pcm := <-sink.writes:
		t.Fatalf("PCM appeared before receiver confirmation: %v", pcm)
	default:
	}
}

func waitReceiverChainPCM(t *testing.T, writes <-chan []int16) []int16 {
	t.Helper()
	select {
	case pcm := <-writes:
		return pcm
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PCM after receiver confirmation")
		return nil
	}
}

func waitReceiverChainSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for receiver-chain lifecycle signal")
	}
}

func assertReceiverChainNoMainTask(t *testing.T, tasks <-chan func()) {
	t.Helper()
	select {
	case <-tasks:
		t.Fatal("unexpected audio completion remained queued after the chain retired")
	default:
	}
}

type receiverChainSource struct {
	readCalls atomic.Int32
	stopCalls atomic.Int32
	closeOnce sync.Once
	stopOnce  sync.Once
	stopped   chan struct{}
	closed    chan struct{}
}

func newReceiverChainSource() *receiverChainSource {
	return &receiverChainSource{stopped: make(chan struct{}), closed: make(chan struct{})}
}

func (s *receiverChainSource) Ready(ctx context.Context) (int, error) { return 16000, ctx.Err() }
func (*receiverChainSource) Err() error                               { return nil }
func (s *receiverChainSource) Read(ctx context.Context, dst []int16) (int, error) {
	if s.readCalls.Add(1) == 1 {
		return copy(dst, []int16{17, -4, 3}), nil
	}
	select {
	case <-s.stopped:
		return 0, io.EOF
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func (s *receiverChainSource) Stop(context.Context) error {
	s.stopCalls.Add(1)
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}
func (s *receiverChainSource) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

type receiverChainSink struct {
	receiverDecision chan error
	waiting          chan struct{}
	writes           chan []int16
	drained          chan struct{}
	closed           chan struct{}
	prepareCalls     atomic.Int32
	waitCalls        atomic.Int32
	writeCalls       atomic.Int32
	drainCalls       atomic.Int32
	closeOnce        sync.Once
	drainOnce        sync.Once
}

func newReceiverChainSink() *receiverChainSink {
	return &receiverChainSink{
		receiverDecision: make(chan error, 1), waiting: make(chan struct{}, 1),
		writes: make(chan []int16, 4), drained: make(chan struct{}), closed: make(chan struct{}),
	}
}

func (s *receiverChainSink) Prepare(context.Context) error {
	s.prepareCalls.Add(1)
	return nil
}
func (s *receiverChainSink) WaitReceiver(ctx context.Context) error {
	s.waitCalls.Add(1)
	select {
	case s.waiting <- struct{}{}:
	default:
	}
	select {
	case err := <-s.receiverDecision:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *receiverChainSink) TryWrite(pcm []int16) error {
	s.writeCalls.Add(1)
	s.writes <- append([]int16(nil), pcm...)
	return nil
}
func (s *receiverChainSink) Drain(context.Context) error {
	s.drainCalls.Add(1)
	s.drainOnce.Do(func() { close(s.drained) })
	return nil
}
func (s *receiverChainSink) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

var _ audiosession.Source = (*receiverChainSource)(nil)
var _ xiaomiPreparedSink = (*receiverChainSink)(nil)
