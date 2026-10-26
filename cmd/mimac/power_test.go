package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
)

// powerTestMonitorFactory supplies a new fake HID owner for each start. The
// generation is tied to the start number so a wake must adopt a fresh device
// generation rather than reviving the old one.
type powerTestMonitorFactory struct {
	starts       int
	connectAt    map[int]bool
	failAt       map[int]error
	monitors     []*remoteTestMonitor
	reservedFor  []uint64
	reservations []*remoteTestReservation
}

func (f *powerTestMonitorFactory) start(onEvent func(xiaomi.MonitorEvent)) (hidMonitor, error) {
	f.starts++
	if err := f.failAt[f.starts]; err != nil {
		return nil, err
	}
	monitor := &remoteTestMonitor{callback: onEvent}
	f.monitors = append(f.monitors, monitor)
	if f.connectAt[f.starts] {
		device, _ := bridge.NewDevice("power-test-remote", uint64(f.starts))
		onEvent(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device})
	}
	return monitor, nil
}

func newPowerTestShell(t *testing.T, connectAt ...int) (*shell, *powerTestMonitorFactory, chan func()) {
	t.Helper()
	factory := &powerTestMonitorFactory{
		connectAt: make(map[int]bool),
		failAt:    make(map[int]error),
	}
	for _, start := range connectAt {
		factory.connectAt[start] = true
	}
	s := &shell{}
	if err := s.setupPluginsWith(factory.start); err != nil {
		t.Fatal(err)
	}
	s.remoteState.snapshotPath = t.TempDir() + "/voice-mapping-recovery.plist"
	dispatches := make(chan func(), 32)
	s.remoteState.postMain = func(fn func()) { dispatches <- fn }
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		factory.reservedFor = append(factory.reservedFor, s.coordinator.core.Snapshot().Generation)
		reservation := &remoteTestReservation{}
		factory.reservations = append(factory.reservations, reservation)
		return reservation, nil
	}
	return s, factory, dispatches
}

func enablePowerTestRemote(t *testing.T, s *shell) {
	t.Helper()
	s.toggleRemote()
	if !s.remoteEnabled() {
		t.Fatalf("remote did not enable: err=%v report=%+v", s.remoteState.err, s.report)
	}
}

func pumpPowerDispatches(t *testing.T, queue <-chan func(), done func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for !done() {
		select {
		case fn := <-queue:
			fn()
		case <-deadline.C:
			t.Fatal("power lifecycle did not reach the expected state")
		}
	}
}

func TestPowerPauseReasonsAreIdempotentAndWakeWaitsForCleanup(t *testing.T) {
	s, factory, queue := newPowerTestShell(t, 1, 2)
	enablePowerTestRemote(t, s)

	entered, release := make(chan struct{}, 1), make(chan struct{})
	var closeCalls atomic.Int32
	cancelCalls := 0
	host := &audioSessionHost{cancel: func() { cancelCalls++ }}
	s.audioSession = host
	s.remoteState.closeAudio = func(context.Context, *audioSessionHost) error {
		closeCalls.Add(1)
		entered <- struct{}{}
		<-release
		return nil
	}

	s.setPowerSuspended(true)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sleep did not begin retiring the active audio owner")
	}
	s.setPowerLocked(true)
	s.setPowerSuspended(true) // duplicate sleep must not start another stop
	s.setPowerLocked(true)    // duplicate lock must be idempotent
	if !s.powerPaused() || !s.powerInputBlocked() || closeCalls.Load() != 1 || cancelCalls != 1 {
		t.Fatalf("overlapping pause duplicated cleanup or opened input: paused=%v blocked=%v closes=%d cancels=%d", s.powerPaused(), s.powerInputBlocked(), closeCalls.Load(), cancelCalls)
	}

	s.setPowerSuspended(false)
	if !s.powerPaused() || factory.starts != 1 {
		t.Fatal("wake cleared the still-active lock reason or reopened the remote")
	}
	s.setPowerLocked(false)
	if s.powerPaused() || !s.powerInputBlocked() || factory.starts != 1 {
		t.Fatal("wake reopened input before old resource cleanup completed")
	}

	close(release)
	pumpPowerDispatches(t, queue, func() bool { return !s.remoteBusy() && s.remoteEnabled() })
	if factory.starts != 2 || s.remoteState.reservationGeneration != 2 || len(factory.reservedFor) != 2 || factory.reservedFor[1] != 2 {
		t.Fatalf("successful wake did not restore the fresh remote generation: starts=%d reservation=%d reserved_for=%v", factory.starts, s.remoteState.reservationGeneration, factory.reservedFor)
	}
}

func TestPowerWakeFencesOldMonitorCallbackAndQueuedReservation(t *testing.T) {
	// The first monitor connects after remote enable and queues reservation work.
	// The second monitor connects at generation 2 during wake restoration.
	s, factory, queue := newPowerTestShell(t, 2)
	enablePowerTestRemote(t, s)
	oldMonitor := factory.monitors[0]
	oldCallback := oldMonitor.callback
	oldMonitor.emit(connectedEvent(t, 1))
	queuedReserve := remoteDispatch(t, queue)
	if s.remoteState.reserveQueued != 1 {
		t.Fatal("test did not capture a queued generation-1 reservation")
	}

	oldDevice, _ := bridge.NewDevice("remote-test", 1)
	oldPress, _ := bridge.NewPressToken(oldDevice, "hid:0007:003e", 1)
	s.setPowerSuspended(true)
	if !s.powerInputBlocked() {
		t.Fatal("sleep did not block input before stopping the old monitor")
	}
	oldCallback(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateConnected,
		Device: oldDevice,
		Edges:  []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: oldPress}},
	})
	remoteDispatch(t, queue)() // complete stop while leaving the retired reserve queued
	if s.remoteEnabled() {
		t.Fatal("sleep cleanup left the prior remote module enabled")
	}
	if s.coordinator.report().Pressed != 0 {
		t.Fatal("old monitor callback admitted a press during sleep")
	}

	s.setPowerSuspended(false)
	if !s.remoteEnabled() || factory.starts != 2 || s.remoteState.reservationGeneration != 2 {
		t.Fatalf("wake did not restore only the new HID generation: starts=%d enabled=%v reservation=%d", factory.starts, s.remoteEnabled(), s.remoteState.reservationGeneration)
	}
	reserveCount := len(factory.reservations)
	queuedReserve() // queued from the retired monitor; its generation must fail
	oldCallback(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateConnected,
		Device: oldDevice,
		Edges:  []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: oldPress}},
	})
	if len(factory.reservations) != reserveCount || s.remoteState.reservationGeneration != 2 || s.coordinator.report().Pressed != 0 {
		t.Fatalf("old work affected the resumed generation: reservations=%v current=%d pressed=%d", factory.reservedFor, s.remoteState.reservationGeneration, s.coordinator.report().Pressed)
	}
}

func TestPowerCleanupFailureRetainsOwnerAndDoesNotReopen(t *testing.T) {
	s, factory, queue := newPowerTestShell(t, 1)
	enablePowerTestRemote(t, s)
	reservation := s.remoteState.reservation
	host := &audioSessionHost{cancel: func() {}}
	s.audioSession = host
	closeErr := errors.New("audio close pending")
	var closeCalls atomic.Int32
	s.remoteState.closeAudio = func(context.Context, *audioSessionHost) error {
		closeCalls.Add(1)
		return closeErr
	}

	s.setPowerSuspended(true)
	s.setPowerSuspended(false) // wake before the failed cleanup completion
	pumpPowerDispatches(t, queue, func() bool { return !s.remoteBusy() })
	if !s.remoteEnabled() || s.remoteState.reservation != reservation || reservation.(*remoteTestReservation).closeCalls != 0 {
		t.Fatal("failed cleanup dropped the remote or its still-owned voice mapping")
	}
	if !errors.Is(s.remoteState.err, closeErr) || !s.powerInputBlocked() {
		t.Fatalf("cleanup failure was not retained behind the input gate: err=%v blocked=%v", s.remoteState.err, s.powerInputBlocked())
	}
	s.setPowerSuspended(false) // duplicate wake must not auto-retry or reopen
	if factory.starts != 1 || closeCalls.Load() != 1 {
		t.Fatalf("failed cleanup retried automatically: monitor starts=%d close calls=%d", factory.starts, closeCalls.Load())
	}
}

func TestPowerWakeRestoresOnlyPreviouslyEnabledMonitorIntent(t *testing.T) {
	t.Run("remote was disabled", func(t *testing.T) {
		s, factory, queue := newPowerTestShell(t)
		s.setPowerSuspended(true)
		s.setPowerSuspended(false)
		pumpPowerDispatches(t, queue, func() bool { return !s.remoteBusy() })
		if factory.starts != 0 || s.plugins.Active(remotePlugin) || s.plugins.Active(xiaomiPlugin) {
			t.Fatal("wake enabled a remote that was disabled before sleep")
		}
	})

	t.Run("monitor only", func(t *testing.T) {
		s, factory, queue := newPowerTestShell(t, 1, 2)
		if err := s.plugins.Enable(xiaomiPlugin); err != nil {
			t.Fatal(err)
		}
		if s.remoteEnabled() || s.remoteState.reservation != nil {
			t.Fatal("test setup unexpectedly enabled takeover")
		}
		s.setPowerSuspended(true)
		remoteDispatch(t, queue)()
		s.setPowerSuspended(false)
		if !s.plugins.Active(xiaomiPlugin) || s.remoteEnabled() || s.remoteState.reservation != nil || factory.starts != 2 {
			t.Fatalf("wake did not restore passive monitor only: starts=%d remote=%v reservation=%v", factory.starts, s.remoteEnabled(), s.remoteState.reservation != nil)
		}
		if s.plugins.Active(outputPlugin) || s.plugins.Active(audioSessionPlugin) || s.plugins.Active(audioOutputPlugin) {
			t.Fatal("monitor-only wake reopened keyboard output or audio")
		}
	})

	t.Run("permission denied on quiet reopen", func(t *testing.T) {
		s, factory, queue := newPowerTestShell(t, 1)
		enablePowerTestRemote(t, s)
		factory.failAt[2] = xiaomi.ErrInputMonitoringRequired
		s.setPowerSuspended(true)
		pumpPowerDispatches(t, queue, func() bool { return !s.remoteBusy() && !s.remoteEnabled() })
		s.setPowerSuspended(false)
		if factory.starts != 2 || s.remoteEnabled() || s.plugins.Active(xiaomiPlugin) || len(factory.reservations) != 1 {
			t.Fatalf("permission failure did not fail closed: starts=%d remote=%v monitor=%v reservations=%d", factory.starts, s.remoteEnabled(), s.plugins.Active(xiaomiPlugin), len(factory.reservations))
		}
		s.setPowerSuspended(false) // repeated resume must not prompt or retry
		if factory.starts != 2 || s.remoteEnabled() {
			t.Fatalf("permission failure triggered automatic retry: starts=%d remote=%v", factory.starts, s.remoteEnabled())
		}
	})
}

func TestPowerExplicitStopAndQuitRevokeResumeIntent(t *testing.T) {
	t.Run("explicit stop during pending sleep cleanup", func(t *testing.T) {
		s, factory, queue := newPowerTestShell(t, 1)
		enablePowerTestRemote(t, s)
		s.toggleRemote() // explicit user stop begins cleanup before sleep arrives
		s.setPowerSuspended(true)
		remoteDispatch(t, queue)()
		if s.remoteEnabled() {
			t.Fatal("explicit stop did not finish before wake")
		}
		if s.powerState.resumeRemote {
			t.Fatal("sleep retained restoration intent after the user explicitly stopped remote takeover")
		}
		s.setPowerSuspended(false)
		if factory.starts != 1 || s.remoteEnabled() {
			t.Fatal("wake reversed the user's explicit stop")
		}
	})

	t.Run("quit while paused", func(t *testing.T) {
		s, factory, queue := newPowerTestShell(t, 1)
		enablePowerTestRemote(t, s)
		s.setPowerSuspended(true)
		remoteDispatch(t, queue)()
		if s.remoteEnabled() {
			t.Fatal("sleep cleanup did not finish before simulated quit")
		}
		s.closing = true // same host flag set by the quit path
		s.setPowerSuspended(false)
		if factory.starts != 1 || s.remoteEnabled() {
			t.Fatal("quit left a pending wake path able to restore remote takeover")
		}
	})
}

func TestPowerWakeUsesNewGenerationAndCannotReviveOldVoiceToken(t *testing.T) {
	s, _, queue := newPowerTestShell(t, 1, 2)
	enablePowerTestRemote(t, s)
	d := s.coordinator
	keySink := &fakeKeySink{}
	if err := d.attachOutput(keySink); err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.RouteAvailable(true)); err != nil {
		t.Fatal(err)
	}
	d.audio = &routedEffectsAudioSink{}
	d.voiceButton = "voice-button"
	plan := voiceAudioTestPlan(t, bridge.ActionTap, bridge.RequestToggle, bridge.RequestNone, "Option")
	d.resolveLocalVoice = func() (bridge.Binding, bool, error) { return bridge.Binding{}, false, nil }
	d.resolveVoiceAction = func() (bridge.ActionPlan, error) { return plan, nil }
	d.voiceAudioBinding = voiceAudioBindingForAction
	deviceSnapshot := d.core.Snapshot()
	device, err := bridge.NewDevice(deviceSnapshot.DeviceID, deviceSnapshot.Generation)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, d.voiceButton, 1)
	if err != nil {
		t.Fatal(err)
	}
	s.audioSession = &audioSessionHost{cancel: func() {}}
	s.remoteState.closeAudio = func(context.Context, *audioSessionHost) error { return nil }
	s.handleDeviceEvent(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateConnected,
		Device: device,
		Edges:  []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: press}},
	})
	oldSession := d.core.Snapshot().Session
	if !d.core.Snapshot().HasSession || oldSession.Token.ID() == 0 {
		t.Fatal("test setup did not start the generation-1 voice session")
	}
	if err := d.step(bridge.SourcePrepared(oldSession.Token, "generation-1-source")); err != nil {
		t.Fatal(err)
	}
	if got := countPowerTargetTaps(keySink.effects); got != 1 {
		t.Fatalf("toggle start taps=%d, want exactly one", got)
	}

	s.setPowerSuspended(true)
	pumpPowerDispatches(t, queue, func() bool { return !s.remoteBusy() && !s.remoteEnabled() })
	if d.core.Snapshot().HasSession || len(d.core.Snapshot().HeldKeys) != 0 || countPowerTargetTaps(keySink.effects) != 1 {
		t.Fatal("sleep retained the old session or sent a compensating toggle")
	}
	s.setPowerSuspended(false)
	if !s.remoteEnabled() || d.core.Snapshot().Generation != 2 || s.remoteState.reservationGeneration != 2 {
		t.Fatalf("wake did not establish the new device generation: core=%d reservation=%d", d.core.Snapshot().Generation, s.remoteState.reservationGeneration)
	}

	late, err := d.core.Step(bridge.SourcePrepared(oldSession.Token, "late-generation-1-source"))
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range late {
		if _, ok := effect.(bridge.OpenGateEffect); ok {
			t.Fatal("late old token reopened audio after wake")
		}
		if _, ok := effect.(bridge.TargetTapEffect); ok {
			t.Fatal("late old token synthesized a target shortcut after wake")
		}
	}
	if !d.core.Snapshot().Connected || d.core.Snapshot().Generation != 2 || d.core.Snapshot().HasSession || countPowerTargetTaps(keySink.effects) != 1 {
		t.Fatalf("old token affected the resumed generation: snapshot=%+v taps=%d", d.core.Snapshot(), countPowerTargetTaps(keySink.effects))
	}
}

func countPowerTargetTaps(effects []bridge.Effect) int {
	count := 0
	for _, effect := range effects {
		if _, ok := effect.(bridge.TargetTapEffect); ok {
			count++
		}
	}
	return count
}

func TestPowerPauseJoinsExistingCleanup(t *testing.T) {
	for _, retiringTool := range []bool{false, true} {
		name := "disconnect"
		if retiringTool {
			name = "tool swap"
		}
		t.Run(name, func(t *testing.T) {
			s, factory, queue := newPowerTestShell(t, 1, 2)
			enablePowerTestRemote(t, s)
			host := &audioSessionHost{cancel: func() {}}
			s.audioSession = host
			entered, release := make(chan struct{}, 2), make(chan struct{})
			var calls atomic.Int32
			s.remoteState.closeAudio = func(context.Context, *audioSessionHost) error {
				calls.Add(1)
				entered <- struct{}{}
				<-release
				return nil
			}
			if retiringTool {
				s.retireLiveAudio()
			} else {
				device, _ := bridge.NewDevice("power-test-remote", 1)
				factory.monitors[0].emit(xiaomi.MonitorEvent{State: xiaomi.MonitorStateDisconnected, Device: device})
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("existing cleanup did not start")
			}
			s.setPowerSuspended(true)
			s.setPowerSuspended(false)
			if calls.Load() != 1 || !s.powerInputBlocked() || factory.starts != 1 {
				t.Fatal("pause raced the existing native Close")
			}
			close(release)
			pumpPowerDispatches(t, queue, func() bool { return !s.powerInputBlocked() && !s.remoteBusy() })
			if calls.Load() != 1 || factory.starts != 2 || !s.remoteEnabled() || s.coordinator.core.Snapshot().HasSession {
				t.Fatalf("cleanup did not hand off once: closes=%d starts=%d enabled=%v", calls.Load(), factory.starts, s.remoteEnabled())
			}
		})
	}
}

func TestPowerStopWhilePausedAndRetryNeverRestoreIntent(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "clean pause"
		if failed {
			name = "failed pause"
		}
		t.Run(name, func(t *testing.T) {
			s, factory, queue := newPowerTestShell(t, 1)
			enablePowerTestRemote(t, s)
			if failed {
				s.audioSession = &audioSessionHost{cancel: func() {}}
				s.remoteState.closeAudio = func(context.Context, *audioSessionHost) error { return errors.New("pending") }
			}
			s.setPowerLocked(true)
			pumpPowerDispatches(t, queue, func() bool { return !s.remoteBusy() })
			if s.remoteTone() == toneActive {
				t.Fatal("paused/failed remote showed active status")
			}
			if failed {
				s.setPowerLocked(false)
				if s.remoteButtonLabel() != "重试清理" {
					t.Fatal("missing explicit cleanup retry")
				}
				s.remoteState.closeAudio = func(context.Context, *audioSessionHost) error { return nil }
			}
			s.toggleRemote()
			pumpPowerDispatches(t, queue, func() bool { return !s.remoteBusy() })
			s.setPowerLocked(false)
			if factory.starts != 1 || s.remoteEnabled() || s.powerState.resumeRemote || s.powerState.stopping {
				t.Fatal("explicit stop/retry restored cancelled intent")
			}
		})
	}
}

func TestPowerPendingRetirementIsBusyAndNativeFaultCannotRetry(t *testing.T) {
	t.Run("retirement still running", func(t *testing.T) {
		s := &shell{powerState: powerState{stopping: true}, liveAudio: liveAudioState{retiring: true}}
		if !s.remoteBusy() || s.remoteButtonLabel() == "重试清理" {
			t.Fatal("pending retirement is presented as a retryable failure")
		}
	})
	t.Run("failed HID close requires restart", func(t *testing.T) {
		s, factory, queue := newPowerTestShell(t, 1)
		enablePowerTestRemote(t, s)
		factory.monitors[0].closeErr = errors.New("native close failed")
		s.setPowerSuspended(true)
		remoteDispatch(t, queue)()
		s.setPowerSuspended(false)
		if !s.powerInputBlocked() || !s.remoteBusy() || s.remoteButtonLabel() != "恢复失败" || s.remoteLabel() != "恢复失败" {
			t.Fatalf("faulted plugin incorrectly offered retry: blocked=%v busy=%v button=%q status=%q", s.powerInputBlocked(), s.remoteBusy(), s.remoteButtonLabel(), s.remoteLabel())
		}
	})
}
