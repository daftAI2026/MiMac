package main

import (
	"errors"
	"fmt"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/voicetools"
)

// deviceCoordinator is owned by MyGo's main thread. Discovery is not adoption:
// until a target action and its sinks are available, the core observes physical
// buttons with no guessed shortcut, key injection or audio preparation.
type deviceCoordinator struct {
	core                  *bridge.Core
	sink                  keySink
	audio                 audioEffectSink
	mappings              map[string][]bridge.Key
	actions               map[string]voicetools.ActionRef
	resolveAction         func(voicetools.ActionRef) (bridge.ActionPlan, error)
	voiceButton           string
	resolveVoiceAction    func() (bridge.ActionPlan, error)
	preflightAction       func(bridge.ActionPlan) error
	voiceAudioBinding     func(bridge.ActionPlan) (bridge.Binding, error)
	voiceDeferralIdentity func() (deferredAudioRouteIdentity, bool)
	discardDeferredVoice  func(bridge.ActionPlan)
	resolveLocalVoice     func() (bridge.Binding, bool, error)
	beforeAudioStart      func() error
	deferredVoice         *deferredVoicePress
	failureID             uint64
	submitted             uint64
	steps                 uint64
	edges                 uint64
	peakHeld              int
	unexpected            uint64
}

// A single physical voice press may wait for the previous sentence to finish
// draining or for its replacement route to become ready. It remains a pending
// gesture only while its original press token is still held by the device.
type deferredVoicePress struct {
	press    bridge.PressToken
	plan     bridge.ActionPlan
	identity deferredAudioRouteIdentity
}

// Only route identity belongs here. The frozen action plan is compared
// separately with sameActionPlan when the pending gesture is revalidated.
type deferredAudioRouteIdentity struct {
	generation uint64
	toolID     string
	appPath    string
}

type coreReport struct {
	Steps             uint64                   `json:"steps"`
	ReceivedEdges     uint64                   `json:"received_edges"`
	PeakPressed       int                      `json:"peak_pressed"`
	Pressed           int                      `json:"pressed"`
	Connected         bool                     `json:"connected"`
	Generation        uint64                   `json:"generation"`
	Configured        bool                     `json:"configured"`
	UnexpectedEffects uint64                   `json:"unexpected_effects"`
	SubmittedEffects  uint64                   `json:"submitted_effects"`
	OutputFailureID   uint64                   `json:"output_failure_id"`
	RecoveryKeys      []bridge.Key             `json:"recovery_keys,omitempty"`
	AudioSessionID    uint64                   `json:"audio_session_id,omitempty"`
	AudioPhase        string                   `json:"audio_phase,omitempty"`
	AudioInput        *audiosession.InputLevel `json:"audio_input,omitempty"`
}

func newDeviceCoordinator() *deviceCoordinator {
	return &deviceCoordinator{core: bridge.NewUnconfiguredCore()}
}

func (d *deviceCoordinator) step(event bridge.Event) error {
	effects, err := d.core.Step(event)
	if err != nil {
		return err
	}
	d.steps++
	d.peakHeld = max(d.peakHeld, len(d.core.Snapshot().PressedButtons))
	return d.dispatchEffects(effects)
}

// accept returns false for events from a retired connection, so stale events
// cannot overwrite the current UI either. Native generations increase across
// monitor restarts, not only across connections inside one monitor.
func (d *deviceCoordinator) accept(event devices.InputEvent) (bool, error) {
	return d.acceptWithRecorderIsolation(event, false)
}

// acceptWhileShortcutRecording keeps physical press ownership in Core while
// preventing an existing mapping or tool-action binding from producing input.
func (d *deviceCoordinator) acceptWhileShortcutRecording(event devices.InputEvent) (bool, error) {
	return d.acceptWithRecorderIsolation(event, true)
}

func (d *deviceCoordinator) acceptWithRecorderIsolation(event devices.InputEvent, recording bool) (bool, error) {
	for _, edge := range event.Edges {
		if edge.Press.Device() != event.Device || (edge.Kind != devices.EdgeDown && edge.Kind != devices.EdgeUp) {
			return false, fmt.Errorf("invalid device edge envelope")
		}
	}
	if d.deferredVoice != nil && d.deferredVoice.press.Device() != event.Device && event.State == devices.InputStateConnected {
		d.clearDeferredVoice(true)
	}
	var eventErr error
	if event.State == devices.InputStateConnected {
		eventErr = d.step(bridge.DeviceConnected(event.Device))
	}
	snapshot := d.core.Snapshot()
	if event.Device.Generation() != 0 &&
		(!snapshot.Connected || snapshot.DeviceID != event.Device.ID() || snapshot.Generation != event.Device.Generation()) {
		return false, nil
	}
	d.edges += uint64(len(event.Edges))
	if event.State == devices.InputStateDisconnected {
		d.clearDeferredVoice(true)
		// A disconnect retires local leases. Key-up may end a target action;
		// it does not prove that the target cancelled or discarded its result.
		return true, d.step(bridge.DeviceDisconnected(event.Device))
	}
	if recording {
		d.clearDeferredVoice(true)
	}
	for _, edge := range event.Edges {
		var input bridge.Event
		deferred := false
		deferredRelease := false
		if pending := d.deferredVoice; pending != nil && edge.Press == pending.press && edge.Kind == devices.EdgeUp {
			// Core owns the held deferred press and clears it on this release.
			d.clearDeferredVoice(false)
			deferredRelease = true
			input = bridge.OrdinaryEnded(edge.Press)
		} else if pending := d.deferredVoice; pending != nil && edge.Press != pending.press &&
			edge.Press.Device() == pending.press.Device() && edge.Press.PressID() > pending.press.PressID() {
			// A newer physical edge makes the old token stale under Core's shared
			// monotonic press gate. Retire it as observed before processing the edge.
			d.clearDeferredVoice(true)
		}
		if input != nil {
			// Matched release of a deferred voice press.
		} else if edge.Kind == devices.EdgeUp {
			input = bridge.OrdinaryEnded(edge.Press)
		} else if recording {
			input = bridge.ObservedBegan(edge.Press)
		} else if d.voiceButton != "" && edge.Press.PhysicalButtonID() == d.voiceButton {
			var err error
			input, deferred, err = d.voiceInput(edge.Press)
			eventErr = errors.Join(eventErr, err)
		} else if ref, bound := d.actions[edge.Press.PhysicalButtonID()]; bound {
			input = bridge.ObservedBegan(edge.Press)
			if d.sink != nil && d.resolveAction != nil {
				if plan, err := d.resolveAction(ref); err == nil && d.actionMayStart(plan) {
					input = bridge.TargetActionBegan(edge.Press, plan)
				}
			}
		} else if keys := d.mappings[edge.Press.PhysicalButtonID()]; len(keys) != 0 && d.sink != nil {
			// A voice-labelled physical button may have an ordinary shortcut.
			// It does not imply an audio source or a configured voice session.
			input = bridge.OrdinaryBegan(edge.Press, keys...)
		} else {
			// Physical input IDs identify buttons, not actions. The host observes
			// any button that has no explicit ordinary-key mapping.
			input = bridge.ObservedBegan(edge.Press)
		}
		if deferred {
			traceVoiceEdge(edge.Press, d.core.Snapshot(), "press_deferred", nil)
			continue
		}
		var stepErr error
		if input != nil {
			stepErr = d.step(input)
		}
		if !recording && edge.Press.PhysicalButtonID() == d.voiceButton {
			state := d.core.Snapshot()
			eventName := "press"
			switch {
			case edge.Kind == devices.EdgeUp:
				if deferredRelease {
					eventName = "press_deferred_released"
				} else {
					eventName = "release"
				}
			case !state.HasSession:
				eventName = "press_without_session"
			case state.Session.Press != edge.Press:
				eventName = "press_during_session"
			}
			traceVoiceEdge(edge.Press, state, eventName, stepErr)
		}
		eventErr = errors.Join(eventErr, stepErr)
	}
	return true, eventErr
}

func (d *deviceCoordinator) disconnect() error {
	snapshot := d.core.Snapshot()
	if !snapshot.Connected {
		return nil
	}
	device, err := bridge.NewDevice(snapshot.DeviceID, snapshot.Generation)
	if err != nil {
		return err
	}
	return d.step(bridge.DeviceDisconnected(device))
}

func (d *deviceCoordinator) report() coreReport {
	s := d.core.Snapshot()
	voice := presentVoiceSession(s)
	return coreReport{
		Steps: d.steps, ReceivedEdges: d.edges, PeakPressed: d.peakHeld,
		Pressed: len(s.PressedButtons), Connected: s.Connected,
		Generation: s.Generation, Configured: s.Configured,
		UnexpectedEffects: d.unexpected,
		SubmittedEffects:  d.submitted, OutputFailureID: s.OutputFailureID,
		RecoveryKeys:   s.RecoveryKeys,
		AudioSessionID: voice.id, AudioPhase: voice.phase,
		AudioInput: presentVoiceInput(s, d.audio),
	}
}

// Preconditions govern new target requests only. Releases always use Core's
// frozen plan, even when the external input source changes during a press.
func (d *deviceCoordinator) actionMayStart(plan bridge.ActionPlan) bool {
	return d.preflightAction == nil || d.preflightAction(plan) == nil
}

// voiceAudioBindingForAction maps a verified target action onto Core's audio
// session lifetime. Physical press/release remains owned by the device event;
// this does not classify or delay the user's gesture.
func voiceAudioBindingForAction(plan bridge.ActionPlan) (bridge.Binding, error) {
	spec := plan.Spec()
	if spec.Scope != bridge.ActionGlobal {
		return bridge.Binding{}, errors.New("voice action is not global")
	}
	var policy bridge.EndPolicy
	switch {
	case spec.Trigger == bridge.ActionHold && spec.Request == bridge.RequestStart && spec.ReleaseRequest == bridge.RequestFinish:
		policy = bridge.PolicyAfterDrain
	case spec.Trigger == bridge.ActionTap && spec.Request == bridge.RequestToggle && spec.ReleaseRequest == bridge.RequestNone:
		policy = bridge.PolicyToggle
	default:
		return bridge.Binding{}, errors.New("voice action has no verified audio binding policy")
	}
	return bridge.NewBinding(policy, plan.Keys()...)
}

// Local PCM consumers enter the same voice session without manufacturing a
// keyboard ActionPlan. External tools keep their verified shortcut contract.
func (d *deviceCoordinator) voiceInput(press bridge.PressToken) (bridge.Event, bool, error) {
	observed := bridge.ObservedBegan(press)
	report := func(event string, err error) {
		traceAudioSession(press.Device().Generation(), press.PressID(), 0, event, err)
	}
	if d.resolveLocalVoice != nil {
		binding, selected, err := d.resolveLocalVoice()
		if selected {
			if err != nil {
				report("local_binding_rejected", err)
				return observed, false, err
			}
			if d.audio == nil {
				report("local_audio_unavailable", nil)
				return observed, false, nil
			}
			input, err := d.audioInput(press, binding)
			return input, false, err
		}
	}
	if d.voiceAudioBinding != nil {
		if d.sink == nil || d.resolveVoiceAction == nil {
			report("audio_unavailable", nil)
			return observed, false, nil
		}
		if d.audio == nil && d.voiceDeferralIdentity == nil {
			report("audio_unavailable", nil)
			return observed, false, nil
		}
		plan, err := d.resolveVoiceAction()
		if err != nil {
			report("action_unavailable", err)
			return observed, false, nil
		}
		if d.preflightAction != nil {
			if err := d.preflightAction(plan); err != nil {
				report("action_preflight_rejected", err)
				return observed, false, nil
			}
		}
		binding, bindingErr := d.voiceAudioBinding(plan)
		if bindingErr != nil {
			// A transient route rebuild may outlive a new held press. Preserve
			// only that exact physical gesture, and only for the same selected
			// action/device identity. Unsupported plans still fail closed.
			if errors.Is(bindingErr, errAudioSessionResourcesNotReady) && d.deferredVoiceEligible() {
				if staticBinding, staticErr := voiceAudioBindingForAction(plan); staticErr == nil && d.deferVoicePress(press, plan, staticBinding, false) {
					return nil, true, nil
				}
			}
			report("audio_binding_unready", bindingErr)
			return observed, false, nil
		}
		if snapshot := d.core.Snapshot(); snapshot.HasSession && snapshot.Session.Phase == bridge.PhaseDraining && snapshot.Session.Press != press {
			if d.deferVoicePress(press, plan, binding, true) {
				return nil, true, nil
			}
		}
		if d.audio == nil {
			report("audio_unavailable", nil)
			return observed, false, nil
		}
		input, err := d.audioInput(press, binding)
		if errors.Is(err, errAudioSessionResourcesNotReady) && d.deferredVoiceEligible() {
			if d.deferVoicePress(press, plan, binding, false) {
				return nil, true, nil
			}
		}
		return input, false, err
	} else if d.sink != nil && d.resolveVoiceAction != nil {
		if plan, err := d.resolveVoiceAction(); err == nil && d.actionMayStart(plan) {
			return bridge.TargetActionBegan(press, plan), false, nil
		}
	}
	return observed, false, nil
}

func (d *deviceCoordinator) deferredVoiceEligible() bool {
	if d == nil || d.core == nil {
		return false
	}
	snapshot := d.core.Snapshot()
	return !snapshot.HasSession || snapshot.Session.Phase == bridge.PhaseDraining
}

func (d *deviceCoordinator) deferVoicePress(press bridge.PressToken, plan bridge.ActionPlan, binding bridge.Binding, ready bool) bool {
	if d == nil || d.voiceDeferralIdentity == nil {
		return false
	}
	identity, ok := d.voiceDeferralIdentity()
	if !ok || identity.generation == 0 || identity.toolID == "" || identity.appPath == "" {
		return false
	}
	if existing := d.deferredVoice; existing != nil {
		if existing.press == press {
			return true
		}
		d.clearDeferredVoice(true)
	}
	if err := d.step(bridge.Configure(binding)); err != nil {
		return false
	}
	if err := d.step(bridge.DeferredVoiceBegan(press, binding)); err != nil {
		return false
	}
	snapshot := d.core.Snapshot()
	if !snapshot.HasDeferredVoice || snapshot.DeferredVoice != press {
		return false
	}
	d.deferredVoice = &deferredVoicePress{press: press, plan: plan, identity: identity}
	if ready {
		_ = d.step(bridge.VoiceRouteReady(press))
	}
	return true
}

// clearDeferredVoice retires a pending press while keeping its physical slot
// until the matching release arrives.
func (d *deviceCoordinator) clearDeferredVoice(observeHeld bool) {
	if d == nil || d.deferredVoice == nil {
		return
	}
	pending := d.deferredVoice
	d.deferredVoice = nil
	if d.discardDeferredVoice != nil {
		d.discardDeferredVoice(pending.plan)
	}
	if observeHeld && d.core != nil {
		_ = d.step(bridge.CancelDeferredVoice(pending.press))
		traceVoiceEdge(pending.press, d.core.Snapshot(), "press_deferred_cancelled", nil)
	}
}

func (d *deviceCoordinator) validateDeferredVoice() {
	if d == nil || d.deferredVoice == nil {
		return
	}
	pending := d.deferredVoice
	identity, ok := d.voiceDeferralIdentity()
	if !ok || identity != pending.identity || d.resolveVoiceAction == nil {
		d.clearDeferredVoice(true)
		return
	}
	current, err := d.resolveVoiceAction()
	if err != nil || !sameActionPlan(current, pending.plan) {
		d.clearDeferredVoice(true)
	}
}

// resumeDeferredVoice is called by the existing session completion and route
// readiness notifications. It promotes at most one still-held physical press.
func (d *deviceCoordinator) resumeDeferredVoice() bool {
	if d == nil || d.deferredVoice == nil || d.core == nil {
		return false
	}
	pending := d.deferredVoice
	snapshot := d.core.Snapshot()
	if !snapshot.HasDeferredVoice || snapshot.DeferredVoice != pending.press {
		d.clearDeferredVoice(false)
		return false
	}
	identity, ok := d.voiceDeferralIdentity()
	if !ok || identity != pending.identity || d.resolveVoiceAction == nil {
		d.clearDeferredVoice(true)
		return false
	}
	current, err := d.resolveVoiceAction()
	if err != nil || !sameActionPlan(current, pending.plan) {
		d.clearDeferredVoice(true)
		return false
	}
	if d.voiceAudioBinding == nil {
		return false
	}
	if _, err := d.voiceAudioBinding(pending.plan); err != nil {
		return false // Wait for the connection's next existing Changes signal.
	}
	_ = d.step(bridge.VoiceRouteReady(pending.press))
	snapshot = d.core.Snapshot()
	if snapshot.HasSession {
		return false
	}
	if !d.core.CanPromoteDeferredVoice(pending.press) {
		if snapshot.RouteAvailable {
			d.clearDeferredVoice(true)
		}
		return false
	}
	if d.preflightAction != nil {
		if err := d.preflightAction(pending.plan); err != nil {
			d.clearDeferredVoice(true)
			traceVoiceEdge(pending.press, snapshot, "press_deferred_cancelled", err)
			return false
		}
	}
	if d.beforeAudioStart != nil {
		if err := d.beforeAudioStart(); err != nil {
			if errors.Is(err, errAudioSessionResourcesNotReady) {
				return false
			}
			d.clearDeferredVoice(true)
			return false
		}
	}
	d.deferredVoice = nil
	stepErr := d.step(bridge.PromoteDeferredVoice(pending.press))
	started := d.core.Snapshot()
	if started.HasSession && started.Session.Press == pending.press {
		traceVoiceEdge(pending.press, started, "press_promoted", stepErr)
	} else {
		traceVoiceEdge(pending.press, started, "press_deferred_cancelled", stepErr)
	}
	return started.HasSession && started.Session.Press == pending.press
}

func sameActionPlan(left, right bridge.ActionPlan) bool {
	if left.Spec() != right.Spec() {
		return false
	}
	leftKeys, rightKeys := left.Keys(), right.Keys()
	if len(leftKeys) != len(rightKeys) {
		return false
	}
	for i := range leftKeys {
		if leftKeys[i] != rightKeys[i] {
			return false
		}
	}
	return true
}

func (d *deviceCoordinator) audioInput(press bridge.PressToken, binding bridge.Binding) (bridge.Event, error) {
	if err := d.step(bridge.Configure(binding)); err != nil {
		return bridge.ObservedBegan(press), err
	}
	if d.beforeAudioStart != nil && d.core.CanBeginVoice(press) {
		if err := d.beforeAudioStart(); err != nil {
			traceAudioSession(press.Device().Generation(), press.PressID(), 0, "route_preflight_rejected", err)
			return bridge.ObservedBegan(press), err
		}
	}
	return bridge.VoiceBegan(press), nil
}
