package main

import (
	"errors"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

func TestVoiceAudioBindingMapsVerifiedActionContracts(t *testing.T) {
	tests := []struct {
		name    string
		trigger bridge.ActionTrigger
		request bridge.TargetRequest
		release bridge.TargetRequest
		policy  bridge.EndPolicy
	}{
		{name: "hold start finish", trigger: bridge.ActionHold, request: bridge.RequestStart, release: bridge.RequestFinish, policy: bridge.PolicyAfterDrain},
		{name: "tap toggle", trigger: bridge.ActionTap, request: bridge.RequestToggle, release: bridge.RequestNone, policy: bridge.PolicyToggle},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := voiceAudioTestPlan(t, test.trigger, test.request, test.release, "RightCommand")
			binding, err := voiceAudioBindingForAction(plan)
			if err != nil || binding.Policy() != test.policy || !reflect.DeepEqual(binding.Keys(), []bridge.Key{"RightCommand"}) {
				t.Fatalf("binding=%+v err=%v", binding, err)
			}
		})
	}
	unsupported := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestNone, "F5")
	if _, err := voiceAudioBindingForAction(unsupported); err == nil {
		t.Fatal("unsupported control contract was mapped to an audio binding")
	}
}

func TestVoiceAudioHoldRunsThroughCoreDrainBeforeTargetKeyUp(t *testing.T) {
	plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	d, device, order := newVoiceAudioTestCoordinator(t, plan)
	press := voiceAudioTestPress(t, device, 1)
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeDown)

	session := d.core.Snapshot().Session
	if !d.core.Snapshot().HasSession || session.Binding.Policy() != bridge.PolicyAfterDrain || !reflect.DeepEqual(session.Binding.Keys(), []bridge.Key{"RightCommand"}) {
		t.Fatalf("session did not freeze hold binding: %+v", d.core.Snapshot())
	}
	if err := d.step(bridge.SourcePrepared(session.Token, "fake-source")); err != nil {
		t.Fatal(err)
	}
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeUp)
	if !d.core.Snapshot().HasSession || d.core.Snapshot().Session.Phase != bridge.PhaseDraining {
		t.Fatalf("physical release did not wait for source drain: %+v", d.core.Snapshot())
	}
	if err := d.step(bridge.SourceDrained(session.Token)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"keys:[KeyDownEffect]",
		"audio:PrepareSourceEffect",
		"audio:OpenGateEffect",
		"audio:StopSourceEffect",
		"keys:[KeyUpEffect]",
		"audio:CloseGateEffect",
		"audio:DisposeResourceEffect",
	}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("full voice hold effect order = %v, want %v", *order, want)
	}
	snapshot := d.core.Snapshot()
	if snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.PressedButtons) != 0 {
		t.Fatalf("drained hold left core resources: %+v", snapshot)
	}
}

func TestVoiceAudioToggleStartsAndFinishesOnlyAtCoreBoundaries(t *testing.T) {
	plan := voiceAudioTestPlan(t, bridge.ActionTap, bridge.RequestToggle, bridge.RequestNone, "RightOption")
	d, device, order := newVoiceAudioTestCoordinator(t, plan)
	press := voiceAudioTestPress(t, device, 1)
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeDown)
	session := d.core.Snapshot().Session
	if !d.core.Snapshot().HasSession || session.Binding.Policy() != bridge.PolicyToggle {
		t.Fatalf("toggle session not configured: %+v", d.core.Snapshot())
	}
	if err := d.step(bridge.SourcePrepared(session.Token, "fake-source")); err != nil {
		t.Fatal(err)
	}
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeUp)
	if err := d.step(bridge.SourceDrained(session.Token)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"keys:[TargetTapEffect]",
		"audio:PrepareSourceEffect",
		"audio:OpenGateEffect",
		"audio:StopSourceEffect",
		"keys:[TargetTapEffect]",
		"audio:CloseGateEffect",
		"audio:DisposeResourceEffect",
	}
	if !reflect.DeepEqual(*order, want) {
		t.Fatalf("full voice toggle effect order = %v, want %v", *order, want)
	}
	taps := voiceAudioTargetTaps(d.sink.(*routedEffectsKeySink))
	if len(taps) != 2 || !reflect.DeepEqual(taps[0], []bridge.Key{"RightOption"}) || !reflect.DeepEqual(taps[1], []bridge.Key{"RightOption"}) {
		t.Fatalf("target toggle taps = %v", taps)
	}
}

func TestVoiceAudioToggleCancellationDoesNotSendASecondToggle(t *testing.T) {
	plan := voiceAudioTestPlan(t, bridge.ActionTap, bridge.RequestToggle, bridge.RequestNone, "RightOption")
	d, device, _ := newVoiceAudioTestCoordinator(t, plan)
	press := voiceAudioTestPress(t, device, 1)
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeDown)
	session := d.core.Snapshot().Session
	if err := d.step(bridge.SourcePrepared(session.Token, "fake-source")); err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.SourceFailed(session.Token)); err != nil {
		t.Fatal(err)
	}
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeUp)
	taps := voiceAudioTargetTaps(d.sink.(*routedEffectsKeySink))
	if len(taps) != 1 || !reflect.DeepEqual(taps[0], []bridge.Key{"RightOption"}) {
		t.Fatalf("cancellation blindly toggled target again: %v", taps)
	}
	if d.core.Snapshot().HasSession || !reflect.DeepEqual(d.core.Snapshot().RecoveryKeys, []bridge.Key{"RightOption"}) {
		t.Fatalf("toggle cancellation did not retire into recovery: %+v", d.core.Snapshot())
	}
}

func TestVoiceAudioUnavailableOrRejectedStaysObserved(t *testing.T) {
	plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	for _, test := range []struct {
		name        string
		audioAbsent bool
		reject      bool
	}{
		{name: "audio sink absent", audioAbsent: true},
		{name: "binding rejected", reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, device, _ := newVoiceAudioTestCoordinator(t, plan)
			if test.audioAbsent {
				d.audio = nil
			}
			bindingCalls := 0
			d.voiceAudioBinding = func(got bridge.ActionPlan) (bridge.Binding, error) {
				bindingCalls++
				if test.reject {
					return bridge.Binding{}, errors.New("audio binding unavailable")
				}
				return voiceAudioBindingForAction(got)
			}
			press := voiceAudioTestPress(t, device, 1)
			acceptVoiceAudioEdge(t, d, device, press, devices.EdgeDown)
			snapshot := d.core.Snapshot()
			if snapshot.Configured || snapshot.HasSession || len(snapshot.PressedButtons) != 1 || len(d.sink.(*routedEffectsKeySink).batches) != 0 {
				t.Fatalf("unavailable audio unexpectedly started target action: snapshot=%+v keys=%#v", snapshot, d.sink.(*routedEffectsKeySink).batches)
			}
			if d.audio != nil && len(d.audio.(*routedEffectsAudioSink).effects) != 0 {
				t.Fatalf("unavailable audio submitted effects: %#v", d.audio.(*routedEffectsAudioSink).effects)
			}
			if test.audioAbsent && bindingCalls != 0 || test.reject && bindingCalls != 1 {
				t.Fatalf("binding hook calls=%d", bindingCalls)
			}
			acceptVoiceAudioEdge(t, d, device, press, devices.EdgeUp)
			if d.report().Pressed != 0 || d.core.Snapshot().HasSession {
				t.Fatalf("observed unavailable press did not release: %+v", d.core.Snapshot())
			}
		})
	}
}

func TestVoiceAudioPreflightDenialHasNoTargetOrAudioEffects(t *testing.T) {
	plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	d, device, _ := newVoiceAudioTestCoordinator(t, plan)
	preflightCalls, bindingCalls := 0, 0
	d.preflightAction = func(bridge.ActionPlan) error {
		preflightCalls++
		return errors.New("input source mismatch")
	}
	d.voiceAudioBinding = func(bridge.ActionPlan) (bridge.Binding, error) {
		bindingCalls++
		return voiceAudioBindingForAction(plan)
	}
	press := voiceAudioTestPress(t, device, 1)
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeDown)
	snapshot := d.core.Snapshot()
	if preflightCalls != 1 || bindingCalls != 0 || snapshot.Configured || snapshot.HasSession || len(snapshot.PressedButtons) != 1 {
		t.Fatalf("preflight denial did not remain observed: calls=%d/%d snapshot=%+v", preflightCalls, bindingCalls, snapshot)
	}
	if len(d.sink.(*routedEffectsKeySink).batches) != 0 || len(d.audio.(*routedEffectsAudioSink).effects) != 0 {
		t.Fatalf("preflight denial emitted target/audio effects: key=%#v audio=%#v", d.sink.(*routedEffectsKeySink).batches, d.audio.(*routedEffectsAudioSink).effects)
	}
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeUp)
	if d.report().Pressed != 0 {
		t.Fatalf("denied physical press was not released: %+v", d.report())
	}
}

func TestVoiceAudioRecordingDoesNotResolveOrStartAction(t *testing.T) {
	plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	d, device, _ := newVoiceAudioTestCoordinator(t, plan)
	resolveCalls, preflightCalls, bindingCalls := 0, 0, 0
	d.resolveVoiceAction = func() (bridge.ActionPlan, error) { resolveCalls++; return plan, nil }
	d.preflightAction = func(bridge.ActionPlan) error { preflightCalls++; return nil }
	d.voiceAudioBinding = func(bridge.ActionPlan) (bridge.Binding, error) {
		bindingCalls++
		return voiceAudioBindingForAction(plan)
	}
	press := voiceAudioTestPress(t, device, 1)
	for _, kind := range []devices.EdgeKind{devices.EdgeDown, devices.EdgeUp} {
		accepted, err := d.acceptWhileShortcutRecording(devices.InputEvent{
			State: devices.InputStateConnected, Device: device,
			Edges: []devices.Edge{{Kind: kind, Press: press}},
		})
		if err != nil || !accepted {
			t.Fatalf("recorded edge %d: accepted=%v err=%v", kind, accepted, err)
		}
	}
	if resolveCalls != 0 || preflightCalls != 0 || bindingCalls != 0 || d.report().Pressed != 0 || d.core.Snapshot().HasSession || d.core.Snapshot().Configured {
		t.Fatalf("recording leaked into audio action: resolve=%d preflight=%d binding=%d core=%+v", resolveCalls, preflightCalls, bindingCalls, d.core.Snapshot())
	}
	if len(d.sink.(*routedEffectsKeySink).batches) != 0 || len(d.audio.(*routedEffectsAudioSink).effects) != 0 {
		t.Fatalf("recording sent target/audio effects: keys=%#v audio=%#v", d.sink.(*routedEffectsKeySink).batches, d.audio.(*routedEffectsAudioSink).effects)
	}
}

func TestVoiceAudioConfigureIsPendingUntilFrozenSessionDrains(t *testing.T) {
	first := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	second := voiceAudioTestPlan(t, bridge.ActionTap, bridge.RequestToggle, bridge.RequestNone, "RightOption")
	d, device, _ := newVoiceAudioTestCoordinator(t, first)
	current := first
	d.resolveVoiceAction = func() (bridge.ActionPlan, error) { return current, nil }
	firstPress := voiceAudioTestPress(t, device, 1)
	acceptVoiceAudioEdge(t, d, device, firstPress, devices.EdgeDown)
	firstSession := d.core.Snapshot().Session
	if err := d.step(bridge.SourcePrepared(firstSession.Token, "first-source")); err != nil {
		t.Fatal(err)
	}
	acceptVoiceAudioEdge(t, d, device, firstPress, devices.EdgeUp)

	current = second
	secondPress := voiceAudioTestPress(t, device, 2)
	acceptVoiceAudioEdge(t, d, device, secondPress, devices.EdgeDown)
	snapshot := d.core.Snapshot()
	if !snapshot.HasSession || !snapshot.HasPending || snapshot.Session.Token != firstSession.Token ||
		snapshot.Session.Binding.Policy() != bridge.PolicyAfterDrain || !reflect.DeepEqual(snapshot.Session.Binding.Keys(), []bridge.Key{"RightCommand"}) ||
		snapshot.Pending.Policy() != bridge.PolicyToggle || !reflect.DeepEqual(snapshot.Pending.Keys(), []bridge.Key{"RightOption"}) {
		t.Fatalf("new target contract overwrote active frozen session: %+v", snapshot)
	}
	acceptVoiceAudioEdge(t, d, device, secondPress, devices.EdgeUp)
	if got := countEffectName(d.audio.(*routedEffectsAudioSink).effects, "PrepareSourceEffect"); got != 1 {
		t.Fatalf("second press started another source: PrepareSource count=%d", got)
	}
	if err := d.step(bridge.SourceDrained(firstSession.Token)); err != nil {
		t.Fatal(err)
	}
	snapshot = d.core.Snapshot()
	if snapshot.HasSession || snapshot.HasPending || snapshot.Adopted.Policy() != bridge.PolicyToggle || !reflect.DeepEqual(snapshot.Adopted.Keys(), []bridge.Key{"RightOption"}) {
		t.Fatalf("pending binding was not adopted after drain: %+v", snapshot)
	}
	keys := d.sink.(*routedEffectsKeySink)
	if len(keys.batches) != 2 {
		t.Fatalf("only first frozen hold should write start and finish, got %#v", keys.batches)
	}
	if down, ok := keys.batches[0][0].(bridge.KeyDownEffect); !ok || down.Key != "RightCommand" {
		t.Fatalf("first session key-down changed: %#v", keys.batches[0])
	}
	if up, ok := keys.batches[1][0].(bridge.KeyUpEffect); !ok || up.Key != "RightCommand" {
		t.Fatalf("first session key-up changed: %#v", keys.batches[1])
	}
}

func TestVoiceAudioNilHookKeepsControlOnlyBehavior(t *testing.T) {
	plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	d, device, _ := newVoiceAudioTestCoordinator(t, plan)
	d.voiceAudioBinding = nil
	press := voiceAudioTestPress(t, device, 1)
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeDown)
	keys := d.sink.(*routedEffectsKeySink)
	if len(keys.batches) != 1 || len(keys.batches[0]) != 1 {
		t.Fatalf("control-only target action did not run: %#v", keys.batches)
	}
	if _, ok := keys.batches[0][0].(bridge.KeyDownEffect); !ok || len(d.audio.(*routedEffectsAudioSink).effects) != 0 {
		t.Fatalf("control-only path unexpectedly changed: keys=%#v audio=%#v", keys.batches, d.audio.(*routedEffectsAudioSink).effects)
	}
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeUp)
	if len(keys.batches) != 2 {
		t.Fatalf("control-only hold release was lost: %#v", keys.batches)
	}
}

func newVoiceAudioTestCoordinator(t *testing.T, plan bridge.ActionPlan) (*deviceCoordinator, bridge.Device, *[]string) {
	t.Helper()
	d := newDeviceCoordinator()
	order := []string{}
	d.sink = &routedEffectsKeySink{order: &order}
	d.audio = &routedEffectsAudioSink{order: &order}
	d.voiceButton = "voice"
	d.resolveVoiceAction = func() (bridge.ActionPlan, error) { return plan, nil }
	d.preflightAction = func(bridge.ActionPlan) error { return nil }
	d.voiceAudioBinding = voiceAudioBindingForAction
	device, err := bridge.NewDevice("voice-audio-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{
		bridge.DeviceConnected(device),
		bridge.ScopeAvailable(true),
		bridge.RouteAvailable(true),
	} {
		if err := d.step(event); err != nil {
			t.Fatal(err)
		}
	}
	return d, device, &order
}

func voiceAudioTestPlan(t *testing.T, trigger bridge.ActionTrigger, request, release bridge.TargetRequest, keys ...bridge.Key) bridge.ActionPlan {
	t.Helper()
	plan, err := bridge.NewActionPlan(bridge.ActionSpec{
		ToolID: "voice-doubao", ActionID: "asrShortcut", CandidateRaw: "verified", Revision: 1,
		Scope: bridge.ActionGlobal, Trigger: trigger, Request: request, ReleaseRequest: release,
	}, keys...)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func voiceAudioTestPress(t *testing.T, device bridge.Device, id uint64) bridge.PressToken {
	t.Helper()
	press, err := bridge.NewPressToken(device, "voice", id)
	if err != nil {
		t.Fatal(err)
	}
	return press
}

func acceptVoiceAudioEdge(t *testing.T, d *deviceCoordinator, device bridge.Device, press bridge.PressToken, kind devices.EdgeKind) {
	t.Helper()
	accepted, err := d.accept(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: kind, Press: press}},
	})
	if err != nil || !accepted {
		t.Fatalf("voice edge %d: accepted=%v err=%v", kind, accepted, err)
	}
}

func voiceAudioTargetTaps(sink *routedEffectsKeySink) [][]bridge.Key {
	var taps [][]bridge.Key
	for _, batch := range sink.batches {
		for _, effect := range batch {
			if tap, ok := effect.(bridge.TargetTapEffect); ok {
				taps = append(taps, tap.Keys())
			}
		}
	}
	return taps
}

func countEffectName(effects []bridge.Effect, name string) int {
	count := 0
	for _, effect := range effects {
		if effectName(effect) == name {
			count++
		}
	}
	return count
}
