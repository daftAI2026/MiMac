package main

import (
	"errors"
	"fmt"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/voicetools"
)

// deviceCoordinator is owned by MyGo's main thread. Discovery is not adoption:
// until a target action and its sinks are available, the core observes physical
// buttons with no guessed shortcut, key injection or audio preparation.
type deviceCoordinator struct {
	core               *bridge.Core
	sink               keySink
	audio              audioEffectSink
	mappings           map[string][]bridge.Key
	actions            map[string]voicetools.ActionRef
	resolveAction      func(voicetools.ActionRef) (bridge.ActionPlan, error)
	voiceButton        string
	resolveVoiceAction func() (bridge.ActionPlan, error)
	preflightAction    func(bridge.ActionPlan) error
	voiceAudioBinding  func(bridge.ActionPlan) (bridge.Binding, error)
	failureID          uint64
	submitted          uint64
	steps              uint64
	edges              uint64
	peakHeld           int
	unexpected         uint64
}

type coreReport struct {
	Steps             uint64 `json:"steps"`
	ReceivedEdges     uint64 `json:"received_edges"`
	PeakPressed       int    `json:"peak_pressed"`
	Pressed           int    `json:"pressed"`
	Connected         bool   `json:"connected"`
	Generation        uint64 `json:"generation"`
	Configured        bool   `json:"configured"`
	UnexpectedEffects uint64 `json:"unexpected_effects"`
	SubmittedEffects  uint64 `json:"submitted_effects"`
	OutputFailureID   uint64 `json:"output_failure_id"`
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
		// A disconnect retires local leases. Key-up may end a target action;
		// it does not prove that the target cancelled or discarded its result.
		return true, d.step(bridge.DeviceDisconnected(event.Device))
	}
	for _, edge := range event.Edges {
		var input bridge.Event
		if edge.Kind == devices.EdgeUp {
			input = bridge.OrdinaryEnded(edge.Press)
		} else if recording {
			input = bridge.ObservedBegan(edge.Press)
		} else if d.voiceButton != "" && edge.Press.PhysicalButtonID() == d.voiceButton {
			input = bridge.ObservedBegan(edge.Press)
			if d.voiceAudioBinding != nil {
				if d.sink != nil && d.audio != nil && d.resolveVoiceAction != nil {
					if plan, err := d.resolveVoiceAction(); err == nil && d.actionMayStart(plan) {
						if binding, err := d.voiceAudioBinding(plan); err == nil {
							if err := d.step(bridge.Configure(binding)); err == nil {
								input = bridge.VoiceBegan(edge.Press)
							} else {
								eventErr = errors.Join(eventErr, err)
							}
						}
					}
				}
			} else if d.sink != nil && d.resolveVoiceAction != nil {
				if plan, err := d.resolveVoiceAction(); err == nil && d.actionMayStart(plan) {
					input = bridge.TargetActionBegan(edge.Press, plan)
				}
			}
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
		eventErr = errors.Join(eventErr, d.step(input))
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
	return coreReport{
		Steps: d.steps, ReceivedEdges: d.edges, PeakPressed: d.peakHeld,
		Pressed: len(s.PressedButtons), Connected: s.Connected,
		Generation: s.Generation, Configured: s.Configured,
		UnexpectedEffects: d.unexpected,
		SubmittedEffects:  d.submitted, OutputFailureID: s.OutputFailureID,
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
