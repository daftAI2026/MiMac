package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
)

type remoteRetryFakeTimer struct {
	delay    time.Duration
	due      time.Time
	callback func()
	fired    bool
	stopped  bool
}

func (t *remoteRetryFakeTimer) Stop() bool {
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func (t *remoteRetryFakeTimer) fire() {
	if t.fired || t.stopped {
		return
	}
	t.fired = true
	t.callback()
}

type remoteRetryFakeClock struct {
	now    time.Time
	timers []*remoteRetryFakeTimer
}

func newRemoteRetryFakeClock() *remoteRetryFakeClock {
	return &remoteRetryFakeClock{now: time.Date(2026, time.November, 2, 0, 0, 0, 0, time.UTC)}
}

func (c *remoteRetryFakeClock) nowTime() time.Time { return c.now }

func (c *remoteRetryFakeClock) after(delay time.Duration, callback func()) remoteReservationRetryTimer {
	timer := &remoteRetryFakeTimer{delay: delay, due: c.now.Add(delay), callback: callback}
	c.timers = append(c.timers, timer)
	return timer
}

func (c *remoteRetryFakeClock) advance(elapsed time.Duration) {
	c.now = c.now.Add(elapsed)
	for {
		var next *remoteRetryFakeTimer
		for _, timer := range c.timers {
			if timer.fired || timer.stopped || timer.due.After(c.now) {
				continue
			}
			if next == nil || timer.due.Before(next.due) {
				next = timer
			}
		}
		if next == nil {
			return
		}
		next.fire()
	}
}

func (c *remoteRetryFakeClock) fireNext() *remoteRetryFakeTimer {
	var next *remoteRetryFakeTimer
	for _, timer := range c.timers {
		if timer.fired || timer.stopped {
			continue
		}
		if next == nil || timer.due.Before(next.due) {
			next = timer
		}
	}
	if next != nil {
		next.fire()
	}
	return next
}

func (c *remoteRetryFakeClock) pendingTimers() []*remoteRetryFakeTimer {
	var timers []*remoteRetryFakeTimer
	for _, timer := range c.timers {
		if !timer.fired && !timer.stopped {
			timers = append(timers, timer)
		}
	}
	return timers
}

func newRemoteReservationRetryFixture(t *testing.T, connectAt ...int) (
	*shell, *powerTestMonitorFactory, chan func(), *remoteRetryFakeClock, []string, *[]*remoteKeysRecoveryReservation,
) {
	t.Helper()
	s, factory, queue, wantSources, owners := newSavedMultiButtonRemoteRecovery(t, connectAt...)
	keys, err := s.profile.keys(s.activeProfile())
	if err != nil {
		t.Fatal(err)
	}
	s.coordinator.mappings = keys
	clock := newRemoteRetryFakeClock()
	s.remoteState.retryAfter = clock.after
	s.remoteState.retryNow = clock.nowTime
	return s, factory, queue, clock, wantSources, owners
}

func enableRemoteRetryTestPlugin(t *testing.T, s *shell) {
	t.Helper()
	s.enableRemote(false) // Avoid user-driven TIS selection and permission requests.
	if !s.remoteEnabled() {
		t.Fatalf("remote plugin did not remain enabled: err=%v", s.remoteState.err)
	}
}

func emitRemoteRetryKeyEdge(t *testing.T, factory *powerTestMonitorFactory, generation uint64, sequence uint64, kind xiaomi.EdgeKind) {
	t.Helper()
	device, err := bridge.NewDevice("power-test-remote", generation)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "hid:0007:0052", sequence)
	if err != nil {
		t.Fatal(err)
	}
	factory.monitors[0].emit(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateConnected,
		Device: device,
		Edges:  []xiaomi.Edge{{Kind: kind, Press: press}},
	})
}

func TestRemoteStartKeepsPluginActiveAndRetriesNotReadyWithoutOutput(t *testing.T) {
	s, factory, queue, clock, wantSources, owners := newRemoteReservationRetryFixture(t, 1)
	if err := s.plugins.Enable(outputPlugin); err != nil {
		t.Fatalf("enable fake output plugin: %v", err)
	}
	sink, ok := s.output.(*fakeKeySink)
	if !ok || sink == nil {
		t.Fatalf("output sink = %T, want fake sink", s.output)
	}
	reserveCalls := 0
	var owner *remoteKeysRecoveryReservation
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		if reserveCalls == 1 {
			return nil, xiaomi.ErrVoiceMappingServiceNotReady
		}
		owner = &remoteKeysRecoveryReservation{label: "ready-generation-1"}
		*owners = append(*owners, owner)
		return owner, nil
	}

	enableRemoteRetryTestPlugin(t, s)
	if reserveCalls != 1 || s.remoteState.reservation != nil || !s.remoteState.reservationRetry.pending ||
		s.remoteBusy() || s.remoteLabel() != "正在连接" || s.remoteButtonLabel() != "停用遥控器" {
		t.Fatalf("startup retry state: calls=%d owner=%v pending=%v busy=%v status=%q button=%q",
			reserveCalls, s.remoteState.reservation != nil, s.remoteState.reservationRetry.pending,
			s.remoteBusy(), s.remoteLabel(), s.remoteButtonLabel())
	}
	if timers := clock.pendingTimers(); len(timers) != 1 || timers[0].delay != 50*time.Millisecond {
		t.Fatalf("first retry timers = %#v, want one 50ms timer", timers)
	}

	emitRemoteRetryKeyEdge(t, factory, 1, 1, xiaomi.EdgeDown)
	emitRemoteRetryKeyEdge(t, factory, 1, 1, xiaomi.EdgeUp)
	if reserveCalls != 1 || len(clock.pendingTimers()) != 1 || len(sink.effects) != 0 ||
		s.coordinator.report().Pressed != 0 {
		t.Fatalf("pending retry allowed reserve/output churn: calls=%d timers=%d effects=%#v pressed=%d",
			reserveCalls, len(clock.pendingTimers()), sink.effects, s.coordinator.report().Pressed)
	}

	clock.advance(50 * time.Millisecond)
	remoteDispatch(t, queue)()
	if reserveCalls != 2 || owner == nil || s.remoteState.reservation != owner ||
		s.remoteState.reservationGeneration != 1 || !s.remoteHasReservation() ||
		!reflect.DeepEqual(owner.updates, [][]string{wantSources}) || len(clock.pendingTimers()) != 0 {
		t.Fatalf("ready retry did not acquire/reconcile one generation-1 owner: calls=%d owner=%v generation=%d updates=%#v timers=%d",
			reserveCalls, s.remoteState.reservation == owner, s.remoteState.reservationGeneration,
			owner.updates, len(clock.pendingTimers()))
	}
	if len(sink.effects) != 0 {
		t.Fatalf("retry unexpectedly injected mapped key effects: %#v", sink.effects)
	}
	emitRemoteRetryKeyEdge(t, factory, 1, 2, xiaomi.EdgeDown)
	emitRemoteRetryKeyEdge(t, factory, 1, 2, xiaomi.EdgeUp)
	wantEffects := []bridge.Effect{bridge.KeyDownEffect{Key: "A"}, bridge.KeyUpEffect{Key: "A"}}
	if !reflect.DeepEqual(sink.effects, wantEffects) {
		t.Fatalf("ready lease did not route the saved Up mapping to A: effects=%#v want=%#v", sink.effects, wantEffects)
	}
}

func TestRemoteReservationRetryIsBoundedAndEdgesDoNotRestartBudget(t *testing.T) {
	s, factory, queue, clock, _, _ := newRemoteReservationRetryFixture(t, 1)
	reserveCalls := 0
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		return nil, xiaomi.ErrVoiceMappingServiceNotReady
	}
	enableRemoteRetryTestPlugin(t, s)
	if reserveCalls != 1 {
		t.Fatalf("initial reserve calls = %d, want 1", reserveCalls)
	}

	for sequence := uint64(1); sequence <= 3; sequence++ {
		emitRemoteRetryKeyEdge(t, factory, 1, sequence, xiaomi.EdgeDown)
		emitRemoteRetryKeyEdge(t, factory, 1, sequence, xiaomi.EdgeUp)
	}
	if reserveCalls != 1 || len(clock.pendingTimers()) != 1 {
		t.Fatalf("connected edges bypassed the pending budget: calls=%d timers=%d", reserveCalls, len(clock.pendingTimers()))
	}

	backoff := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}
	for index, delay := range backoff {
		timers := clock.pendingTimers()
		if len(timers) != 1 || timers[0].delay != delay {
			t.Fatalf("retry %d timer = %#v, want one %s timer", index+1, timers, delay)
		}
		clock.advance(delay)
		remoteDispatch(t, queue)()
		if reserveCalls != index+2 {
			t.Fatalf("attempts after retry %d = %d, want %d", index+1, reserveCalls, index+2)
		}
	}
	if s.remoteState.reservationRetry.pending || !s.remoteState.reservationRetry.exhausted ||
		!errors.Is(s.remoteState.err, xiaomi.ErrVoiceMappingServiceNotReady) || len(clock.pendingTimers()) != 0 {
		t.Fatalf("budget did not end in a visible terminal state: retry=%+v err=%v timers=%d",
			s.remoteState.reservationRetry, s.remoteState.err, len(clock.pendingTimers()))
	}

	for sequence := uint64(4); sequence <= 6; sequence++ {
		emitRemoteRetryKeyEdge(t, factory, 1, sequence, xiaomi.EdgeDown)
		emitRemoteRetryKeyEdge(t, factory, 1, sequence, xiaomi.EdgeUp)
	}
	if reserveCalls != 6 || len(clock.pendingTimers()) != 0 {
		t.Fatalf("exhausted generation retried on later edges: calls=%d timers=%d", reserveCalls, len(clock.pendingTimers()))
	}

	factory.monitors[0].emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateConnected, 2))
	remoteDispatch(t, queue)()
	if reserveCalls != 7 || !s.remoteState.reservationRetry.pending ||
		s.remoteState.reservationRetry.generation != 2 || len(clock.pendingTimers()) != 1 ||
		clock.pendingTimers()[0].delay != 50*time.Millisecond {
		t.Fatalf("new generation did not receive one fresh bounded attempt: calls=%d retry=%+v timers=%#v",
			reserveCalls, s.remoteState.reservationRetry, clock.pendingTimers())
	}
}

func TestRemoteReservationRetryDoesNotReserveAfterPostedAttemptMissesBudget(t *testing.T) {
	s, _, queue, clock, _, _ := newRemoteReservationRetryFixture(t, 1)
	reserveCalls := 0
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		return nil, xiaomi.ErrVoiceMappingServiceNotReady
	}
	enableRemoteRetryTestPlugin(t, s)
	if reserveCalls != 1 {
		t.Fatalf("initial reserve calls = %d, want 1", reserveCalls)
	}

	clock.fireNext()
	queuedRetry := remoteDispatch(t, queue)
	clock.advance(remoteReservationRetryBudget + time.Millisecond)
	queuedRetry()
	if reserveCalls != 1 || s.remoteState.reservationRetry.pending || !s.remoteState.reservationRetry.exhausted ||
		!errors.Is(s.remoteState.err, xiaomi.ErrVoiceMappingServiceNotReady) || len(clock.pendingTimers()) != 0 {
		t.Fatalf("late main-thread retry exceeded its scheduling budget: calls=%d retry=%+v err=%v timers=%d",
			reserveCalls, s.remoteState.reservationRetry, s.remoteState.err, len(clock.pendingTimers()))
	}
}

func TestRemoteReservationRetryDoesNotRepeatTerminalFailuresForSameGeneration(t *testing.T) {
	cases := []struct {
		name    string
		failure error
		partial bool
	}{
		{name: "identity-error", failure: errors.New("event service identity changed")},
		{name: "partial-owner", failure: xiaomi.ErrVoiceMappingServiceNotReady, partial: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			s, factory, queue, clock, _, _ := newRemoteReservationRetryFixture(t)
			owner := &remoteKeysRecoveryReservation{label: testCase.name}
			reserveCalls := 0
			s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
				reserveCalls++
				if testCase.partial {
					return owner, testCase.failure
				}
				return nil, testCase.failure
			}
			enableRemoteRetryTestPlugin(t, s)
			factory.monitors[0].emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateConnected, 1))
			remoteDispatch(t, queue)()
			if reserveCalls != 1 || len(clock.pendingTimers()) != 0 ||
				s.remoteState.reservationRetry.pending || !s.remoteState.reservationRetry.terminal ||
				!errors.Is(s.remoteState.err, testCase.failure) {
				t.Fatalf("terminal reserve failure was retried or hidden: calls=%d retry=%+v err=%v timers=%d",
					reserveCalls, s.remoteState.reservationRetry, s.remoteState.err, len(clock.pendingTimers()))
			}
			if testCase.partial && s.remoteState.reservation != owner {
				t.Fatal("partial reservation owner was not retained")
			}

			factory.monitors[0].emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateConnected, 1))
			emitRemoteRetryKeyEdge(t, factory, 1, 1, xiaomi.EdgeDown)
			emitRemoteRetryKeyEdge(t, factory, 1, 1, xiaomi.EdgeUp)
			select {
			case fn := <-queue:
				fn()
			default:
			}
			if reserveCalls != 1 || len(clock.pendingTimers()) != 0 {
				t.Fatalf("same-generation edge repeated terminal failure: calls=%d timers=%d", reserveCalls, len(clock.pendingTimers()))
			}
		})
	}
}

func TestRemoteReservationRetryCanBeStoppedWhilePending(t *testing.T) {
	s, _, queue, clock, _, _ := newRemoteReservationRetryFixture(t, 1)
	reserveCalls := 0
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		return nil, xiaomi.ErrVoiceMappingServiceNotReady
	}
	enableRemoteRetryTestPlugin(t, s)
	clock.fireNext()
	queuedRetry := remoteDispatch(t, queue)

	s.toggleRemote()
	queuedRetry()
	remoteDispatch(t, queue)()
	if s.remoteEnabled() || reserveCalls != 1 || s.remoteState.reservationRetry.pending || len(clock.pendingTimers()) != 0 {
		t.Fatalf("stop did not cancel/fence a posted retry: enabled=%v calls=%d retry=%+v timers=%d",
			s.remoteEnabled(), reserveCalls, s.remoteState.reservationRetry, len(clock.pendingTimers()))
	}
}

func TestRemoteReservationRetryIsCanceledOnPowerPause(t *testing.T) {
	s, _, queue, clock, _, _ := newRemoteReservationRetryFixture(t, 1)
	reserveCalls := 0
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		return nil, xiaomi.ErrVoiceMappingServiceNotReady
	}
	enableRemoteRetryTestPlugin(t, s)
	clock.fireNext()
	queuedRetry := remoteDispatch(t, queue)

	s.setPowerSuspended(true)
	queuedRetry()
	remoteDispatch(t, queue)()
	if !s.powerInputBlocked() || s.remoteEnabled() || reserveCalls != 1 ||
		s.remoteState.reservationRetry.pending || len(clock.pendingTimers()) != 0 {
		t.Fatalf("power pause failed to cancel/fence retry: blocked=%v enabled=%v calls=%d retry=%+v timers=%d",
			s.powerInputBlocked(), s.remoteEnabled(), reserveCalls, s.remoteState.reservationRetry, len(clock.pendingTimers()))
	}
}

func TestRemoteReservationRetryIsCanceledOnDeferredQuit(t *testing.T) {
	s, _, queue, clock, _, _ := newRemoteReservationRetryFixture(t, 1)
	reserveCalls := 0
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		return nil, xiaomi.ErrVoiceMappingServiceNotReady
	}
	enableRemoteRetryTestPlugin(t, s)
	clock.fireNext()
	queuedRetry := remoteDispatch(t, queue)

	quitCalled := false
	s.audioSession = &audioSessionHost{cancel: func() {}}
	post := func(fn func()) { queue <- fn }
	if !s.deferAudioQuit(func() {}, post, func() { quitCalled = true }) {
		t.Fatal("deferred quit did not retain the main loop for audio cleanup")
	}
	queuedRetry()
	remoteDispatch(t, queue)()
	if !quitCalled || !s.closing || reserveCalls != 1 || s.remoteState.reservationRetry.pending || len(clock.pendingTimers()) != 0 {
		t.Fatalf("quit did not cancel/fence retry: quit=%v closing=%v calls=%d retry=%+v timers=%d",
			quitCalled, s.closing, reserveCalls, s.remoteState.reservationRetry, len(clock.pendingTimers()))
	}
}

func TestRemoteReservationRetryNewGenerationFencesPostedOldTimer(t *testing.T) {
	s, factory, queue, clock, _, _ := newRemoteReservationRetryFixture(t, 1)
	reserveCalls := 0
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		return nil, xiaomi.ErrVoiceMappingServiceNotReady
	}
	enableRemoteRetryTestPlugin(t, s)
	clock.fireNext()
	oldRetryCallback := remoteDispatch(t, queue)

	factory.monitors[0].emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateConnected, 2))
	remoteDispatch(t, queue)()
	newTimers := clock.pendingTimers()
	if reserveCalls != 2 || s.remoteState.reservationRetry.generation != 2 || len(newTimers) != 1 {
		t.Fatalf("new generation did not begin its own retry: calls=%d retry=%+v timers=%#v",
			reserveCalls, s.remoteState.reservationRetry, newTimers)
	}
	newTimer := newTimers[0]
	oldRetryCallback()
	if reserveCalls != 2 || s.remoteState.reservationRetry.generation != 2 ||
		s.remoteState.reservationRetry.timer != newTimer || newTimer.stopped || newTimer.fired {
		t.Fatalf("posted old-generation timer changed generation 2: calls=%d retry=%+v newTimer=%+v",
			reserveCalls, s.remoteState.reservationRetry, newTimer)
	}
}

func TestRemoteReservationRetryEpochFencesSameGenerationReenable(t *testing.T) {
	s, _, queue, clock, _, _ := newRemoteReservationRetryFixture(t)
	err := xiaomi.ErrVoiceMappingServiceNotReady
	oldEpoch := s.remoteState.reservationRetry.begin(7, clock.nowTime(), err)
	if !s.scheduleRemoteReservationRetry(7, oldEpoch) {
		t.Fatal("initial same-generation timer was not scheduled")
	}
	clock.fireNext()
	oldCallback := remoteDispatch(t, queue)

	s.cancelRemoteReservationRetry()
	newEpoch := s.remoteState.reservationRetry.begin(7, clock.nowTime(), err)
	if newEpoch == oldEpoch || !s.scheduleRemoteReservationRetry(7, newEpoch) {
		t.Fatalf("same-generation reenable did not receive a fresh retry epoch: old=%d new=%d", oldEpoch, newEpoch)
	}
	newTimer := clock.pendingTimers()[0]
	oldCallback()
	if !s.remoteState.reservationRetry.current(7, newEpoch) || s.remoteState.reservationRetry.timer != newTimer ||
		newTimer.stopped || newTimer.fired {
		t.Fatalf("stale same-generation callback changed the new intent: retry=%+v timer=%+v", s.remoteState.reservationRetry, newTimer)
	}
}
