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

	t.Run("early release keeps receiver wait and drains captured PCM", func(t *testing.T) {
		f := newReceiverChainFixture(t)
		f.pressDown(t)
		waitReceiverChainSignal(t, f.sink.waiting)
		assertReceiverChainNoPCM(t, f.source, f.sink)
		assertReceiverChainCore(t, f, true, bridge.PhasePreparing)

		f.pressUp(t)
		waitReceiverChainSignal(t, f.source.stopped)
		if snapshot := f.host.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseDraining || len(snapshot.PressedButtons) != 0 || !reflect.DeepEqual(snapshot.HeldKeys, []bridge.Key{"RightCommand"}) {
			t.Fatalf("early physical release did not retain the draining voice lease: %+v", snapshot)
		}
		assertReceiverChainTargetStart(t, f.keySink, false)
		assertReceiverChainNoPCM(t, f.source, f.sink)

		f.sink.receiverDecision <- nil
		runHostTask(t, f.tasks) // Late SourcePrepared opens the already-draining session's gate.
		pcm := waitReceiverChainPCM(t, f.sink.writes)
		if !reflect.DeepEqual(pcm, []int16{17, -4, 3}) {
			t.Fatalf("copied PCM after early release = %v", pcm)
		}
		waitReceiverChainSignal(t, f.sink.drained)
		runHostTask(t, f.tasks) // SourceDrained releases the target key and disposes the source.
		waitReceiverChainSignal(t, f.source.closed)
		waitReceiverChainSignal(t, f.sink.closed)

		assertReceiverChainCore(t, f, false, 0)
		assertReceiverChainTargetStart(t, f.keySink, true)
		if f.resolveCalls.Load() != 1 || f.sink.waitCalls.Load() != 1 || f.sink.writeCalls.Load() != 1 || f.sink.drainCalls.Load() != 1 || f.source.stopCalls.Load() != 1 {
			t.Fatalf("early-release drain lifecycle: resolve=%d waits=%d writes=%d drains=%d stops=%d", f.resolveCalls.Load(), f.sink.waitCalls.Load(), f.sink.writeCalls.Load(), f.sink.drainCalls.Load(), f.source.stopCalls.Load())
		}
		assertReceiverChainNoMainTask(t, f.tasks)
		closeReceiverChainAudioPlugin(t, f)
	})

	t.Run("device disconnect still cancels receiver wait", func(t *testing.T) {
		f := newReceiverChainFixture(t)
		f.pressDown(t)
		waitReceiverChainSignal(t, f.sink.waiting)
		assertReceiverChainNoPCM(t, f.source, f.sink)
		assertReceiverChainCore(t, f, true, bridge.PhasePreparing)

		if err := f.host.coordinator.step(bridge.DeviceDisconnected(f.device)); err != nil {
			t.Fatal(err)
		}
		waitReceiverChainSignal(t, f.source.closed)
		waitReceiverChainSignal(t, f.sink.closed)
		assertReceiverChainNoPCM(t, f.source, f.sink)
		if snapshot := f.host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 {
			t.Fatalf("device disconnect left audio/Core state: %+v", snapshot)
		}
		assertReceiverChainTargetStart(t, f.keySink, true)
		if f.resolveCalls.Load() != 1 || f.sink.waitCalls.Load() != 1 {
			t.Fatalf("disconnect restarted preparation: resolve=%d waits=%d", f.resolveCalls.Load(), f.sink.waitCalls.Load())
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

	t.Run("early release receiver failure blocks next press until route is ready", func(t *testing.T) {
		f := newReceiverChainFixture(t)
		f.sink.closeStarted = make(chan struct{})
		f.sink.closeRelease = make(chan struct{})
		t.Cleanup(func() {
			select {
			case <-f.sink.closeRelease:
			default:
				close(f.sink.closeRelease)
			}
		})

		f.pressDown(t)
		waitReceiverChainSignal(t, f.sink.waiting)
		f.pressUp(t)
		waitReceiverChainSignal(t, f.source.stopped)
		if snapshot := f.host.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseDraining || len(snapshot.PressedButtons) != 0 || !reflect.DeepEqual(snapshot.HeldKeys, []bridge.Key{"RightCommand"}) {
			t.Fatalf("early release did not retain the draining voice lease: %+v", snapshot)
		}
		assertReceiverChainNoPCM(t, f.source, f.sink)

		f.sink.receiverDecision <- io.ErrUnexpectedEOF
		runHostTask(t, f.tasks) // SourceFailed must retire the already-draining Core session.
		waitReceiverChainSignal(t, f.source.closed)
		waitReceiverChainSignal(t, f.sink.closeStarted)
		if snapshot := f.host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 {
			t.Fatalf("receiver failure did not clear early-release ownership: %+v", snapshot)
		}
		if f.host.report.AudioError == "" || f.sink.drainCalls.Load() != 0 {
			t.Fatalf("receiver failure looked like a successful drain: report=%+v drains=%d", f.host.report, f.sink.drainCalls.Load())
		}
		assertReceiverChainNoPCM(t, f.source, f.sink)
		assertReceiverChainTargetStart(t, f.keySink, true)
		assertReceiverChainNoMainTask(t, f.tasks)
		if f.host.audioResourcesReady() {
			t.Fatal("leased route became ready while its old Dispose was blocked")
		}

		secondPress := voiceAudioTestPress(t, f.device, 2)
		acceptVoiceAudioEdge(t, f.host.coordinator, f.device, secondPress, devices.EdgeDown)
		if snapshot := f.host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.PressedButtons) != 1 || len(snapshot.HeldKeys) != 0 {
			t.Fatalf("press during old Dispose bypassed route readiness: %+v", snapshot)
		}
		if f.resolveCalls.Load() != 2 || f.sink.waitCalls.Load() != 1 {
			t.Fatalf("unready press started audio work: resolves=%d old waits=%d", f.resolveCalls.Load(), f.sink.waitCalls.Load())
		}
		select {
		case source := <-f.sources:
			t.Fatalf("replacement source opened before old Dispose completed: %p", source)
		default:
		}
		select {
		case sink := <-f.sinks:
			t.Fatalf("replacement sink opened before old Dispose completed: %p", sink)
		default:
		}
		acceptVoiceAudioEdge(t, f.host.coordinator, f.device, secondPress, devices.EdgeUp)
		if snapshot := f.host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 {
			t.Fatalf("unready press did not retire cleanly: %+v", snapshot)
		}
		assertReceiverChainTargetStart(t, f.keySink, true)

		close(f.sink.closeRelease)
		waitReceiverChainSignal(t, f.sink.closed)
		waitXiaomiRoute(t, f.host.audioResourcesReady)
		var secondSource *receiverChainSource
		select {
		case secondSource = <-f.sources:
		case <-time.After(2 * time.Second):
			t.Fatal("replacement source did not prepare after old Dispose completed")
		}
		var secondSink *receiverChainSink
		select {
		case secondSink = <-f.sinks:
		case <-time.After(2 * time.Second):
			t.Fatal("replacement receiver sink did not prepare after old Dispose")
		}
		if secondSink.prepareCalls.Load() != 1 {
			t.Fatalf("replacement sink Prepare calls = %d, want 1", secondSink.prepareCalls.Load())
		}
		select {
		case <-secondSink.waiting:
			t.Fatal("replacement route waited for the target receiver before a new press")
		default:
		}

		thirdPress := voiceAudioTestPress(t, f.device, 3)
		acceptVoiceAudioEdge(t, f.host.coordinator, f.device, thirdPress, devices.EdgeDown)
		assertReceiverChainCore(t, f, true, bridge.PhasePreparing)
		if f.resolveCalls.Load() != 3 || f.sink.waitCalls.Load() != 1 {
			t.Fatalf("ready press lifecycle: resolves=%d old waits=%d", f.resolveCalls.Load(), f.sink.waitCalls.Load())
		}
		waitReceiverChainSignal(t, secondSink.waiting)
		secondSink.receiverDecision <- nil
		runHostTask(t, f.tasks)
		if pcm := waitReceiverChainPCM(t, secondSink.writes); !reflect.DeepEqual(pcm, []int16{17, -4, 3}) {
			t.Fatalf("next session PCM = %v", pcm)
		}

		acceptVoiceAudioEdge(t, f.host.coordinator, f.device, thirdPress, devices.EdgeUp)
		waitReceiverChainSignal(t, secondSource.stopped)
		waitReceiverChainSignal(t, secondSink.drained)
		runHostTask(t, f.tasks)
		waitReceiverChainSignal(t, secondSource.closed)
		waitReceiverChainSignal(t, secondSink.closed)
		assertReceiverChainCore(t, f, false, 0)
		assertReceiverChainTargetKeyEffects(t, f.keySink, 2)
		if secondSink.waitCalls.Load() != 1 || secondSink.writeCalls.Load() != 1 || secondSink.drainCalls.Load() != 1 {
			t.Fatalf("next session lifecycle: waits=%d writes=%d drains=%d", secondSink.waitCalls.Load(), secondSink.writeCalls.Load(), secondSink.drainCalls.Load())
		}
		assertReceiverChainNoMainTask(t, f.tasks)
		closeReceiverChainAudioPlugin(t, f)
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
	sources      chan *receiverChainSource
	sinks        chan *receiverChainSink
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
		source: source, sink: sink, sources: sources, sinks: sinks,
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

func assertReceiverChainTargetKeyEffects(t *testing.T, sink *routedEffectsKeySink, sessions int) {
	t.Helper()
	want := make([]bridge.Key, sessions)
	for index := range want {
		want[index] = "RightCommand"
	}
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
	if !reflect.DeepEqual(downs, want) || !reflect.DeepEqual(ups, want) || taps != 0 {
		t.Fatalf("tool shortcut effects: down=%v up=%v taps=%d, want %d balanced hold sessions", downs, ups, taps, sessions)
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
	closeStarted     chan struct{}
	closeRelease     chan struct{}
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
	s.closeOnce.Do(func() {
		if s.closeStarted != nil {
			close(s.closeStarted)
		}
		if s.closeRelease != nil {
			<-s.closeRelease
		}
		close(s.closed)
	})
	return nil
}

var _ audiosession.Source = (*receiverChainSource)(nil)
var _ xiaomiPreparedSink = (*receiverChainSink)(nil)
