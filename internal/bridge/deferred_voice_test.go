package bridge

import (
	"reflect"
	"testing"
)

func TestDeferredVoicePromotesOnlyAfterReleasedSessionDrains(t *testing.T) {
	firstBinding := mustBinding(t, PolicyAfterDrain, "space")
	secondBinding := mustBinding(t, PolicyToggle, "option")
	core, device := setupCore(t, firstBinding)

	firstPress := mustPress(t, device, "voice", 1)
	firstEffects := mustStep(t, core, VoiceBegan(firstPress))
	firstToken := prepareToken(t, firstEffects)
	mustStep(t, core, SourcePrepared(firstToken, "first-source"))
	mustStep(t, core, VoiceEnded(firstPress))
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != PhaseDraining || len(snapshot.PressedButtons) != 0 {
		t.Fatalf("first physical release did not leave only a draining session: %+v", snapshot)
	}

	// This is a new down after the previous up, while that previous session is
	// still draining. Configure freezes the newly resolved action for this press.
	secondPress := mustPress(t, device, "voice", 2)
	mustStep(t, core, Configure(secondBinding))
	mustStep(t, core, DeferredVoiceBegan(secondPress, secondBinding))
	if snapshot := core.Snapshot(); !snapshot.HasSession || !snapshot.HasDeferredVoice || snapshot.DeferredVoice != secondPress ||
		snapshot.DeferredVoiceReady || !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{secondPress}) {
		t.Fatalf("second physical press was not retained as one deferred gesture: %+v", snapshot)
	}
	mustStep(t, core, VoiceRouteReady(secondPress))
	if core.CanPromoteDeferredVoice(secondPress) {
		t.Fatal("deferred press promoted before the previous session drained")
	}

	if got := keyTransitions(mustStep(t, core, SourceDrained(firstToken))); !reflect.DeepEqual(got, []string{"up:space"}) {
		t.Fatalf("old session drain effects = %v, want old shortcut release only", got)
	}
	if !core.CanPromoteDeferredVoice(secondPress) {
		t.Fatalf("held deferred press was not eligible after old drain: %+v", core.Snapshot())
	}
	started := mustStep(t, core, PromoteDeferredVoice(secondPress))
	secondToken := prepareToken(t, started)
	if got := tapKeys(t, started); !reflect.DeepEqual(got, [][]Key{{"option"}}) {
		t.Fatalf("deferred toggle start taps = %v, want one start tap", got)
	}
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Token != secondToken || snapshot.Session.Press != secondPress ||
		snapshot.Session.Binding.Policy() != PolicyToggle || snapshot.HasDeferredVoice {
		t.Fatalf("promoted session did not retain the second press contract: %+v", snapshot)
	}

	mustStep(t, core, SourcePrepared(secondToken, "second-source"))
	mustStep(t, core, VoiceEnded(secondPress))
	finished := mustStep(t, core, SourceDrained(secondToken))
	if got := tapKeys(t, finished); !reflect.DeepEqual(got, [][]Key{{"option"}}) {
		t.Fatalf("deferred toggle finish taps = %v, want one finish tap", got)
	}
	if snapshot := core.Snapshot(); snapshot.HasSession || snapshot.HasDeferredVoice || len(snapshot.HeldKeys) != 0 || len(snapshot.PressedButtons) != 0 {
		t.Fatalf("two released sessions left Core state: %+v", snapshot)
	}
}

func TestDeferredVoiceReleaseBeforeDrainNeverPromotes(t *testing.T) {
	firstBinding := mustBinding(t, PolicyAfterDrain, "space")
	secondBinding := mustBinding(t, PolicyToggle, "option")
	core, device := setupCore(t, firstBinding)

	firstPress := mustPress(t, device, "voice", 1)
	firstToken := prepareToken(t, mustStep(t, core, VoiceBegan(firstPress)))
	mustStep(t, core, SourcePrepared(firstToken, "first-source"))
	mustStep(t, core, VoiceEnded(firstPress))

	secondPress := mustPress(t, device, "voice", 2)
	mustStep(t, core, Configure(secondBinding))
	mustStep(t, core, DeferredVoiceBegan(secondPress, secondBinding))
	mustStep(t, core, VoiceRouteReady(secondPress))
	mustStep(t, core, OrdinaryEnded(secondPress))
	if snapshot := core.Snapshot(); snapshot.HasDeferredVoice || len(snapshot.PressedButtons) != 0 {
		t.Fatalf("released pending press remained promotable: %+v", snapshot)
	}

	oldDrain := mustStep(t, core, SourceDrained(firstToken))
	if core.Snapshot().HasSession || core.Snapshot().HasDeferredVoice || len(tapKeys(t, oldDrain)) != 0 {
		t.Fatalf("old drain replayed the released next press: effects=%#v snapshot=%+v", oldDrain, core.Snapshot())
	}

	thirdPress := mustPress(t, device, "voice", 3)
	third := mustStep(t, core, VoiceBegan(thirdPress))
	if !hasEffect[PrepareSourceEffect](third) || !core.Snapshot().HasSession || core.Snapshot().Session.Press != thirdPress {
		t.Fatalf("fresh press after cancelled pending gesture did not start: effects=%#v snapshot=%+v", third, core.Snapshot())
	}
}

func TestDeferredVoiceWaitsForRouteReadinessAndRejectsOldTokens(t *testing.T) {
	binding := mustBinding(t, PolicyAfterDrain, "space")
	core, device := setupCore(t, binding)
	mustStep(t, core, RouteAvailable(false))

	press := mustPress(t, device, "voice", 1)
	mustStep(t, core, DeferredVoiceBegan(press, binding))
	otherPress := mustPress(t, device, "voice", 2)
	mustStep(t, core, VoiceRouteReady(otherPress))
	if core.Snapshot().DeferredVoiceReady {
		t.Fatal("readiness for another press armed the deferred gesture")
	}
	mustStep(t, core, VoiceRouteReady(press))
	if core.CanPromoteDeferredVoice(press) {
		t.Fatal("route-unavailable press was promotable")
	}
	mustStep(t, core, RouteAvailable(true))
	if !core.CanPromoteDeferredVoice(press) {
		t.Fatalf("same held press did not become promotable after route readiness: %+v", core.Snapshot())
	}
	started := mustStep(t, core, PromoteDeferredVoice(press))
	if !hasEffect[PrepareSourceEffect](started) || !core.Snapshot().HasSession || core.Snapshot().Session.Press != press {
		t.Fatalf("ready pending press did not promote once: effects=%#v snapshot=%+v", started, core.Snapshot())
	}
	if effects := mustStep(t, core, PromoteDeferredVoice(press)); hasEffect[PrepareSourceEffect](effects) || len(effects) != 0 {
		t.Fatalf("duplicate promotion had effects: %#v", effects)
	}
}
