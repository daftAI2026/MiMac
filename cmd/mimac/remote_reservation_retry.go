package main

import (
	"errors"
	"time"

	"mimac.local/mimac/internal/adapters/xiaomi"
)

const remoteReservationRetryBudget = 2 * time.Second

var remoteReservationRetryBackoff = [...]time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
	800 * time.Millisecond,
}

type remoteReservationRetryTimer interface {
	Stop() bool
}

type remoteReservationRetryAfterFunc func(time.Duration, func()) remoteReservationRetryTimer

// remoteReservationRetry owns one bounded attempt sequence for one device
// generation. Its epoch is independent of the device generation so an old
// timer callback cannot revive a later enable intent for the same generation.
type remoteReservationRetry struct {
	generation uint64
	epoch      uint64
	timerToken uint64
	timerCount uint64
	startedAt  time.Time
	lastErr    error
	nextDelay  int
	timer      remoteReservationRetryTimer
	pending    bool
	exhausted  bool
	terminal   bool
}

func (r *remoteReservationRetry) begin(generation uint64, now time.Time, err error) uint64 {
	r.cancel()
	r.generation = generation
	r.startedAt = now
	r.lastErr = err
	r.pending = generation != 0
	return r.epoch
}

func (r *remoteReservationRetry) cancel() {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = nil
	r.timerToken = 0
	r.timerCount++
	r.generation = 0
	r.startedAt = time.Time{}
	r.lastErr = nil
	r.nextDelay = 0
	r.pending = false
	r.exhausted = false
	r.terminal = false
	r.epoch++
}

func (r *remoteReservationRetry) current(generation, epoch uint64) bool {
	return r.pending && r.generation == generation && r.epoch == epoch
}

func (r *remoteReservationRetry) blocks(generation uint64) bool {
	return r.generation == generation && (r.pending || r.exhausted || r.terminal)
}

func (r *remoteReservationRetry) fail(generation uint64, err error) {
	r.cancel()
	r.generation = generation
	r.lastErr = err
	r.terminal = generation != 0
}

func (r *remoteReservationRetry) nextTimerToken() uint64 {
	r.timerCount++
	r.timerToken = r.timerCount
	return r.timerToken
}

func (r *remoteReservationRetry) timerFired(generation, epoch, timerToken uint64) bool {
	if !r.current(generation, epoch) || r.timerToken != timerToken || r.timer == nil {
		return false
	}
	r.timer = nil
	r.timerToken = 0
	return true
}

func (r *remoteReservationRetry) nextDelayAt(now time.Time) (time.Duration, bool) {
	if !r.pending || r.timer != nil {
		return 0, false
	}
	deadline := r.startedAt.Add(remoteReservationRetryBudget)
	if !now.Before(deadline) || r.nextDelay >= len(remoteReservationRetryBackoff) {
		r.exhaust()
		return 0, false
	}
	delay := remoteReservationRetryBackoff[r.nextDelay]
	if now.Add(delay).After(deadline) {
		r.exhaust()
		return 0, false
	}
	r.nextDelay++
	return delay, true
}

func (r *remoteReservationRetry) exhaust() {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = nil
	r.timerToken = 0
	r.timerCount++
	r.pending = false
	r.exhausted = true
	r.terminal = false
	r.epoch++
}

var errRemoteReservationRetryDispatcherUnavailable = errors.New("remote control: retry dispatcher is unavailable")

func (s *shell) remoteReservationRetryNow() time.Time {
	if s.remoteState.retryNow != nil {
		return s.remoteState.retryNow()
	}
	return time.Now()
}

func (s *shell) beginRemoteReservationRetry(generation uint64, err error, deferArm bool) bool {
	if generation == 0 || err == nil || !errors.Is(err, xiaomi.ErrVoiceMappingServiceNotReady) || s.remoteState.reservation != nil {
		return false
	}
	retry := &s.remoteState.reservationRetry
	if retry.blocks(generation) {
		if retry.pending {
			s.remoteState.err = nil
			if !deferArm && retry.timer == nil {
				s.scheduleRemoteReservationRetry(generation, retry.epoch)
			}
		} else {
			s.remoteState.err = retry.lastErr
		}
		s.syncVoiceBinding()
		s.invalidateDeviceView()
		return true
	}
	epoch := retry.begin(generation, s.remoteReservationRetryNow(), err)
	s.remoteState.err = nil
	s.syncVoiceBinding()
	if !deferArm {
		s.scheduleRemoteReservationRetry(generation, epoch)
	}
	s.invalidateDeviceView()
	return true
}

func (s *shell) armRemoteReservationRetry() {
	retry := &s.remoteState.reservationRetry
	if retry.pending && retry.timer == nil {
		s.scheduleRemoteReservationRetry(retry.generation, retry.epoch)
	}
}

func (s *shell) scheduleRemoteReservationRetry(generation, epoch uint64) bool {
	retry := &s.remoteState.reservationRetry
	if !retry.current(generation, epoch) || retry.timer != nil {
		return false
	}
	delay, ok := retry.nextDelayAt(s.remoteReservationRetryNow())
	if !ok {
		s.remoteState.err = retry.lastErr
		s.syncVoiceBinding()
		s.invalidateDeviceView()
		return false
	}
	after := s.remoteState.retryAfter
	if after == nil {
		after = func(delay time.Duration, fn func()) remoteReservationRetryTimer {
			return time.AfterFunc(delay, fn)
		}
	}
	post := s.remoteState.postMain
	if post == nil {
		retry.exhaust()
		s.remoteState.err = errors.Join(retry.lastErr, errRemoteReservationRetryDispatcherUnavailable)
		s.syncVoiceBinding()
		s.invalidateDeviceView()
		return false
	}
	timerToken := retry.nextTimerToken()
	retry.timer = after(delay, func() {
		go post(func() { s.fireRemoteReservationRetry(generation, epoch, timerToken) })
	})
	return true
}

func (s *shell) fireRemoteReservationRetry(generation, epoch, timerToken uint64) {
	retry := &s.remoteState.reservationRetry
	if !retry.timerFired(generation, epoch, timerToken) {
		return
	}
	if !s.remoteReservationRetryEligible(generation) {
		s.cancelRemoteReservationRetry()
		s.syncVoiceBinding()
		s.invalidateDeviceView()
		return
	}
	if !s.remoteReservationRetryNow().Before(retry.startedAt.Add(remoteReservationRetryBudget)) {
		lastErr := retry.lastErr
		retry.exhaust()
		s.remoteState.err = lastErr
		s.syncVoiceBinding()
		s.invalidateDeviceView()
		return
	}

	err := s.acquireRemoteReservation(generation, false)
	if err == nil && s.remoteState.reservation != nil && s.remoteState.reservationGeneration == generation {
		s.cancelRemoteReservationRetry()
		s.remoteState.err = nil
		s.syncVoiceBinding()
		s.refreshRemoteOutput(false)
		s.invalidateDeviceView()
		return
	}
	if err != nil && s.remoteState.reservation == nil && errors.Is(err, xiaomi.ErrVoiceMappingServiceNotReady) {
		retry.lastErr = err
		if s.scheduleRemoteReservationRetry(generation, epoch) {
			s.remoteState.err = nil
		} else {
			s.remoteState.err = err
		}
		s.syncVoiceBinding()
		s.invalidateDeviceView()
		return
	}

	s.cancelRemoteReservationRetry()
	if err != nil {
		retry.fail(generation, err)
		s.remoteState.err = err
	} else if s.remoteState.reservation == nil {
		retry.fail(generation, errRemoteNoReservation)
		s.remoteState.err = errRemoteNoReservation
	}
	s.syncVoiceBinding()
	s.invalidateDeviceView()
}

func (s *shell) remoteReservationRetryEligible(generation uint64) bool {
	if s.closing || s.powerInputBlocked() || s.remoteState.cleanupInFlight || s.remoteState.stopPending ||
		!s.remoteEnabled() || s.coordinator == nil || s.remoteState.reserveInFlight || s.remoteState.reservation != nil {
		return false
	}
	snapshot := s.coordinator.core.Snapshot()
	return snapshot.Connected && snapshot.Generation == generation
}

func (s *shell) cancelRemoteReservationRetry() {
	s.remoteState.reservationRetry.cancel()
	s.invalidateQueuedRemoteReserve()
	if errors.Is(s.remoteState.err, xiaomi.ErrVoiceMappingServiceNotReady) {
		s.remoteState.err = nil
	}
}

func (s *shell) invalidateQueuedRemoteReserve() {
	s.remoteState.reserveQueueSequence++
	s.remoteState.reserveQueued = 0
	s.remoteState.reserveQueuedSequence = 0
}
