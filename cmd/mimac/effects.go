package main

import (
	"errors"
	"fmt"

	"mimac.local/mimac/internal/bridge"
)

var (
	errKeyEffectSinkUnavailable   = errors.New("key effect sink is unavailable")
	errAudioEffectSinkUnavailable = errors.New("audio effect sink is unavailable")
)

// audioEffectSink accepts one non-key Core effect at a time. The coordinator
// preserves Core's ordering and groups adjacent keyboard effects separately.
type audioEffectSink interface {
	Submit(bridge.Effect) error
}

type effectDispatchState struct {
	errs               error
	submitted          uint64
	keyFailureHandled  bool
	failedSources      map[bridge.SessionToken]bool
	routeUnavailable   bool
	routeChangeApplied bool
}

func (d *deviceCoordinator) dispatchEffects(effects []bridge.Effect) error {
	state := &effectDispatchState{}
	filtered, err := filterKnownEffects(effects)
	if err != nil {
		d.unexpected++
		return err
	}
	d.dispatchSequence(filtered, state, false)
	if state.routeUnavailable && !state.routeChangeApplied {
		d.setRouteUnavailable(state)
	}
	d.submitted += state.submitted
	return state.errs
}

// filterKnownEffects validates the whole Core result before executing its
// first side effect. BlockedEffect is a Core decision, not a sink command.
func filterKnownEffects(effects []bridge.Effect) ([]bridge.Effect, error) {
	filtered := effects[:0]
	for _, effect := range effects {
		switch effect.(type) {
		case bridge.BlockedEffect:
			continue
		case bridge.KeyDownEffect, bridge.KeyUpEffect, bridge.TargetTapEffect,
			bridge.PrepareSourceEffect, bridge.StopSourceEffect,
			bridge.OpenGateEffect, bridge.CloseGateEffect, bridge.DisposeResourceEffect:
			filtered = append(filtered, effect)
		default:
			return nil, fmt.Errorf("unsupported Core effect %T", effect)
		}
	}
	return filtered, nil
}

func (d *deviceCoordinator) dispatchSequence(effects []bridge.Effect, state *effectDispatchState, cleanup bool) {
	for i := 0; i < len(effects); {
		if isKeyEffect(effects[i]) {
			end := i + 1
			for end < len(effects) && isKeyEffect(effects[end]) {
				end++
			}
			batch := effects[i:end]
			if state.keyFailureHandled {
				// ReleaseAll is the authoritative cleanup for a failed keyboard
				// sink. Re-submitting Core's KeyUp effects could recurse into it.
				i = end
				continue
			}
			var submitErr error
			if d.sink == nil {
				d.unexpected += uint64(len(batch))
				submitErr = errKeyEffectSinkUnavailable
			} else {
				submitErr = d.sink.Submit(batch)
			}
			if submitErr != nil {
				state.errs = errors.Join(state.errs, submitErr)
				d.handleKeyFailure(batch, effects[end:], state)
				return
			}
			state.submitted += uint64(len(batch))
			i = end
			continue
		}

		effect := effects[i]
		i++
		if err := d.submitAudioEffect(effect); err != nil {
			state.errs = errors.Join(state.errs, fmt.Errorf("submit %T: %w", effect, err))
			if cleanup {
				// A cleanup failure is reported once, closes the route after the
				// rest of this cleanup tail runs, and never re-enters SourceFailed.
				state.routeUnavailable = true
				d.dispatchSequence(cleanupEffects(effects[i:]), state, true)
				return
			}
			d.handleAudioFailure(effect, effects[i:], state)
			return
		}
		state.submitted++
	}
}

func isKeyEffect(effect bridge.Effect) bool {
	switch effect.(type) {
	case bridge.KeyDownEffect, bridge.KeyUpEffect, bridge.TargetTapEffect:
		return true
	default:
		return false
	}
}

func isAudioEffect(effect bridge.Effect) bool {
	switch effect.(type) {
	case bridge.PrepareSourceEffect, bridge.StopSourceEffect,
		bridge.OpenGateEffect, bridge.CloseGateEffect, bridge.DisposeResourceEffect:
		return true
	default:
		return false
	}
}

func (d *deviceCoordinator) submitAudioEffect(effect bridge.Effect) error {
	if !isAudioEffect(effect) {
		return fmt.Errorf("effect %T is not routed to audio", effect)
	}
	if d.audio == nil {
		d.unexpected++
		return errAudioEffectSinkUnavailable
	}
	return d.audio.Submit(effect)
}

func (d *deviceCoordinator) handleKeyFailure(batch, tail []bridge.Effect, state *effectDispatchState) {
	if !state.keyFailureHandled {
		state.keyFailureHandled = true
		d.failureID++
		uncertain := uncertainTapKeys(batch)
		failedEffects, coreErr := d.applyCoreEvent(bridge.OutputFailed(d.failureID, uncertain...))
		state.errs = errors.Join(state.errs, coreErr)
		if d.sink == nil {
			state.errs = errors.Join(state.errs, errKeyEffectSinkUnavailable)
			state.routeUnavailable = true
		} else {
			releaseErr := d.sink.ReleaseAll()
			state.errs = errors.Join(state.errs, releaseErr)
			if releaseErr != nil {
				state.routeUnavailable = true
			}
		}
		// Finish cleanup already emitted by the initiating Core event before
		// the cleanup returned by OutputFailed. Starts/taps in the old tail are
		// discarded because the event plan has failed.
		cleanup := append(cleanupEffects(tail), cleanupEffects(failedEffects)...)
		d.dispatchSequence(cleanup, state, true)
		return
	}
	state.errs = errors.Join(state.errs, errors.New("additional key effect failed during cleanup"))
	d.dispatchSequence(cleanupEffects(tail), state, true)
}

func (d *deviceCoordinator) handleAudioFailure(effect bridge.Effect, tail []bridge.Effect, state *effectDispatchState) {
	token, ok := effectSessionToken(effect)
	if !ok {
		state.errs = errors.Join(state.errs, fmt.Errorf("audio effect %T has no session token", effect))
		state.routeUnavailable = true
		d.dispatchSequence(cleanupEffects(tail), state, true)
		return
	}

	var failedEffects []bridge.Effect
	if state.failedSources == nil {
		state.failedSources = make(map[bridge.SessionToken]bool)
	}
	if !state.failedSources[token] {
		state.failedSources[token] = true
		var err error
		failedEffects, err = d.applyCoreEvent(bridge.SourceFailed(token))
		state.errs = errors.Join(state.errs, err)
	}
	cleanup := append(cleanupEffects(tail), cleanupEffects(failedEffects)...)
	// Gate-close and resource-dispose failures are themselves cleanup failures,
	// even if Core has already retired the logical session.
	if _, ok := effect.(bridge.CloseGateEffect); ok {
		state.routeUnavailable = true
	}
	if _, ok := effect.(bridge.DisposeResourceEffect); ok {
		state.routeUnavailable = true
	}
	d.dispatchSequence(cleanup, state, true)
}

func cleanupEffects(effects []bridge.Effect) []bridge.Effect {
	cleanup := make([]bridge.Effect, 0, len(effects))
	for _, effect := range effects {
		switch effect.(type) {
		case bridge.KeyUpEffect, bridge.StopSourceEffect, bridge.CloseGateEffect, bridge.DisposeResourceEffect:
			cleanup = append(cleanup, effect)
		}
	}
	return cleanup
}

func uncertainTapKeys(effects []bridge.Effect) []bridge.Key {
	keys := make([]bridge.Key, 0)
	seen := make(map[bridge.Key]struct{})
	for _, effect := range effects {
		tap, ok := effect.(bridge.TargetTapEffect)
		if !ok {
			continue
		}
		for _, key := range tap.Keys() {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}

func effectSessionToken(effect bridge.Effect) (bridge.SessionToken, bool) {
	switch e := effect.(type) {
	case bridge.PrepareSourceEffect:
		return e.Token, true
	case bridge.StopSourceEffect:
		return e.Token, true
	case bridge.OpenGateEffect:
		return e.Token, true
	case bridge.CloseGateEffect:
		return e.Token, true
	case bridge.DisposeResourceEffect:
		return e.Token, true
	default:
		return bridge.SessionToken{}, false
	}
}

func (d *deviceCoordinator) applyCoreEvent(event bridge.Event) ([]bridge.Effect, error) {
	effects, err := d.core.Step(event)
	if err != nil {
		return nil, err
	}
	d.steps++
	d.peakHeld = max(d.peakHeld, len(d.core.Snapshot().PressedButtons))
	return effects, nil
}

func (d *deviceCoordinator) setRouteUnavailable(state *effectDispatchState) {
	if state.routeChangeApplied {
		return
	}
	state.routeChangeApplied = true
	effects, err := d.applyCoreEvent(bridge.RouteAvailable(false))
	state.errs = errors.Join(state.errs, err)
	if err != nil {
		return
	}
	d.dispatchSequence(effects, state, true)
}
