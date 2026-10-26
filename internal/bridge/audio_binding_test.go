package bridge

import (
	"errors"
	"reflect"
	"testing"
)

func TestAudioBindingIsTheOnlyKeylessBinding(t *testing.T) {
	audio := NewAudioBinding()
	if audio.Policy() != PolicyAudio || len(audio.Keys()) != 0 {
		t.Fatalf("audio binding = policy %v, keys %v", audio.Policy(), audio.Keys())
	}
	if generic, err := NewBinding(PolicyAudio); err != nil || generic.Policy() != PolicyAudio || len(generic.Keys()) != 0 {
		t.Fatalf("empty audio policy binding = (%#v, %v), want valid keyless binding", generic, err)
	}
	if _, err := NewBinding(PolicyAudio, "space"); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("audio binding with keys error = %v, want ErrInvalidBinding", err)
	}
	for _, policy := range []EndPolicy{PolicyExactEdge, PolicyAfterDrain, PolicyToggle} {
		if _, err := NewBinding(policy); !errors.Is(err, ErrInvalidBinding) {
			t.Errorf("key binding policy %v accepted an empty chord: %v", policy, err)
		}
	}
}

func TestAudioSessionCanBeginWithoutKeyboardScope(t *testing.T) {
	core, err := NewCore(NewAudioBinding())
	if err != nil {
		t.Fatal(err)
	}
	device := mustDevice(t, "remote-audio", 1)
	mustStep(t, core, DeviceConnected(device))
	mustStep(t, core, RouteAvailable(true))
	mustStep(t, core, ScopeAvailable(false))

	begin := mustStep(t, core, VoiceBegan(mustPress(t, device, "voice", 1)))
	if len(begin) != 1 || !hasEffect[PrepareSourceEffect](begin) {
		t.Fatalf("audio begin without keyboard scope = %#v, want only PrepareSource", begin)
	}
	assertNoKeyboardOutput(t, begin)
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Binding.Policy() != PolicyAudio || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("keyless audio was not admitted without keyboard scope: %#v", snapshot)
	}
}

func TestKeyboardScopeRevocationKeepsAudioAndReleasesOrdinaryKeys(t *testing.T) {
	core, device := setupCore(t, NewAudioBinding())
	voice := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(voice))
	token := prepareToken(t, begin)
	prepared := mustStep(t, core, SourcePrepared(token, "audio-source"))
	ordinary := mustPress(t, device, "ordinary", 2)
	down := mustStep(t, core, OrdinaryBegan(ordinary, "ctrl"))
	if got := keyTransitions(down); !reflect.DeepEqual(got, []string{"down:ctrl"}) {
		t.Fatalf("ordinary key-down before scope loss = %v", got)
	}

	scopeLost := mustStep(t, core, ScopeAvailable(false))
	if got := keyTransitions(scopeLost); !reflect.DeepEqual(got, []string{"up:ctrl"}) {
		t.Fatalf("scope loss key cleanup = %v, want ordinary key-up", got)
	}
	if hasEffect[CloseGateEffect](scopeLost) || hasEffect[StopSourceEffect](scopeLost) || hasEffect[DisposeResourceEffect](scopeLost) {
		t.Fatalf("keyboard scope loss canceled audio resources: %#v", scopeLost)
	}
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Token != token || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("keyboard scope loss did not preserve audio session and release keys: %#v", snapshot)
	}

	ordinaryUp := mustStep(t, core, OrdinaryEnded(ordinary))
	released := mustStep(t, core, VoiceEnded(voice))
	drained := mustStep(t, core, SourceDrained(token))
	if !hasEffect[StopSourceEffect](released) || !hasEffect[CloseGateEffect](drained) || !containsDisposed(drained, "audio-source") {
		t.Fatalf("audio did not finish after keyboard scope loss: release=%#v drain=%#v", released, drained)
	}
	assertNoKeyboardOutput(t, begin, prepared, released, drained)
	if got := keyTransitions(ordinaryUp); len(got) != 0 {
		t.Fatalf("ordinary release after scope cleanup repeated output: %v", got)
	}
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 0 {
		t.Fatalf("audio or key state remained after drain: %#v", snapshot)
	}
}

func TestAudioSessionNormalLifecycleHasNoKeyboardOutput(t *testing.T) {
	core, device := setupCore(t, NewAudioBinding())
	press := mustPress(t, device, "voice", 1)

	begin := mustStep(t, core, VoiceBegan(press))
	if len(begin) != 1 || !hasEffect[PrepareSourceEffect](begin) {
		t.Fatalf("audio begin effects = %#v, want only PrepareSource", begin)
	}
	token := prepareToken(t, begin)

	prepared := mustStep(t, core, SourcePrepared(token, "audio-source"))
	if len(prepared) != 1 || !hasEffect[OpenGateEffect](prepared) {
		t.Fatalf("audio prepared effects = %#v, want only OpenGate", prepared)
	}

	released := mustStep(t, core, VoiceEnded(press))
	if len(released) != 1 || !hasEffect[StopSourceEffect](released) {
		t.Fatalf("audio release effects = %#v, want only StopSource", released)
	}

	drained := mustStep(t, core, SourceDrained(token))
	if len(drained) != 2 || !hasEffect[CloseGateEffect](drained) || !containsDisposed(drained, "audio-source") {
		t.Fatalf("audio drain effects = %#v, want CloseGate and DisposeResource", drained)
	}
	assertNoKeyboardOutput(t, begin, prepared, released, drained)
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 0 {
		t.Fatalf("audio lifecycle retained key/session state: %#v", snapshot)
	}
}

func TestAudioReleaseBeforePreparationStillDrains(t *testing.T) {
	core, device := setupCore(t, NewAudioBinding())
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)

	released := mustStep(t, core, VoiceEnded(press))
	if len(released) != 1 || !hasEffect[StopSourceEffect](released) {
		t.Fatalf("early release effects = %#v, want only StopSource", released)
	}
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != PhaseDraining {
		t.Fatalf("early release did not retain draining session: %#v", snapshot)
	}

	prepared := mustStep(t, core, SourcePrepared(token, "late-ready-source"))
	if len(prepared) != 1 || !hasEffect[OpenGateEffect](prepared) {
		t.Fatalf("late preparation effects = %#v, want only OpenGate", prepared)
	}
	drained := mustStep(t, core, SourceDrained(token))
	if len(drained) != 2 || !hasEffect[CloseGateEffect](drained) || !containsDisposed(drained, "late-ready-source") {
		t.Fatalf("early-release drain effects = %#v, want CloseGate and DisposeResource", drained)
	}
	assertNoKeyboardOutput(t, begin, released, prepared, drained)
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 0 {
		t.Fatalf("early-release lifecycle retained state: %#v", snapshot)
	}
}

func TestAudioCancellationAndRouteFailureCleanResourcesWithoutKeyRecovery(t *testing.T) {
	t.Run("source failure", func(t *testing.T) {
		core, device := setupCore(t, NewAudioBinding())
		press := mustPress(t, device, "voice", 1)
		begin := mustStep(t, core, VoiceBegan(press))
		token := prepareToken(t, begin)
		prepared := mustStep(t, core, SourcePrepared(token, "source"))

		failed := mustStep(t, core, SourceFailed(token))
		if len(failed) != 3 || !hasEffect[CloseGateEffect](failed) || !hasEffect[StopSourceEffect](failed) || !containsDisposed(failed, "source") {
			t.Fatalf("source failure cleanup = %#v", failed)
		}
		assertNoKeyboardOutput(t, begin, prepared, failed)
		if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 0 {
			t.Fatalf("source failure retained audio or key state: %#v", snapshot)
		}
	})

	t.Run("route unavailable", func(t *testing.T) {
		core, device := setupCore(t, NewAudioBinding())
		press := mustPress(t, device, "voice", 1)
		begin := mustStep(t, core, VoiceBegan(press))
		token := prepareToken(t, begin)
		prepared := mustStep(t, core, SourcePrepared(token, "source"))

		lostRoute := mustStep(t, core, RouteAvailable(false))
		if len(lostRoute) != 3 || !hasEffect[CloseGateEffect](lostRoute) || !hasEffect[StopSourceEffect](lostRoute) || !containsDisposed(lostRoute, "source") {
			t.Fatalf("route failure cleanup = %#v", lostRoute)
		}
		assertNoKeyboardOutput(t, begin, prepared, lostRoute)
		if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 0 {
			t.Fatalf("route failure retained audio or key state: %#v", snapshot)
		}
	})
}

func TestAudioStaleCompletionCannotAffectNewSession(t *testing.T) {
	core, device := setupCore(t, NewAudioBinding())
	firstPress := mustPress(t, device, "voice", 1)
	firstBegin := mustStep(t, core, VoiceBegan(firstPress))
	firstToken := prepareToken(t, firstBegin)
	firstPrepared := mustStep(t, core, SourcePrepared(firstToken, "first-resource"))
	failed := mustStep(t, core, RouteAvailable(false))
	mustStep(t, core, VoiceEnded(firstPress))
	mustStep(t, core, RouteAvailable(true))

	secondPress := mustPress(t, device, "voice", 2)
	secondBegin := mustStep(t, core, VoiceBegan(secondPress))
	secondToken := prepareToken(t, secondBegin)
	before := core.Snapshot()
	if got := mustStep(t, core, SourceDrained(firstToken)); len(got) != 0 {
		t.Fatalf("stale drain effects = %#v, want none", got)
	}
	stalePrepared := mustStep(t, core, SourcePrepared(firstToken, "late-stale-resource"))
	if len(stalePrepared) != 1 || !containsDisposed(stalePrepared, "late-stale-resource") || hasEffect[OpenGateEffect](stalePrepared) {
		t.Fatalf("stale preparation effects = %#v, want only disposal", stalePrepared)
	}
	after := core.Snapshot()
	if !after.HasSession || after.Session.Token != secondToken || after.Session.Token == firstToken || !reflect.DeepEqual(after.Session, before.Session) {
		t.Fatalf("stale completion affected new session: before=%#v after=%#v", before.Session, after.Session)
	}
	assertNoKeyboardOutput(t, firstBegin, firstPrepared, failed, secondBegin, stalePrepared)
	if len(after.HeldKeys) != 0 || len(after.RecoveryKeys) != 0 {
		t.Fatalf("stale completion created key state: %#v", after)
	}
}

func TestAudioSessionCoexistsWithOrdinaryKeyMapping(t *testing.T) {
	core, device := setupCore(t, NewAudioBinding())
	voice := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(voice))
	token := prepareToken(t, begin)
	prepared := mustStep(t, core, SourcePrepared(token, "audio-source"))

	ordinary := mustPress(t, device, "ordinary", 2)
	down := mustStep(t, core, OrdinaryBegan(ordinary, "ctrl", "space"))
	if got, want := keyTransitions(down), []string{"down:ctrl", "down:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary mapping during audio = %v, want %v", got, want)
	}
	if snapshot := core.Snapshot(); !snapshot.HasSession || !reflect.DeepEqual(snapshot.HeldKeys, []Key{"ctrl", "space"}) {
		t.Fatalf("ordinary mapping interfered with audio session: %#v", snapshot)
	}

	up := mustStep(t, core, OrdinaryEnded(ordinary))
	if got, want := keyTransitions(up), []string{"up:space", "up:ctrl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary mapping release during audio = %v, want %v", got, want)
	}
	released := mustStep(t, core, VoiceEnded(voice))
	drained := mustStep(t, core, SourceDrained(token))
	assertNoKeyboardOutput(t, begin, prepared, released, drained)
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 0 {
		t.Fatalf("coexisting mappings did not clean up: %#v", snapshot)
	}
}

func assertNoKeyboardOutput(t *testing.T, batches ...[]Effect) {
	t.Helper()
	for _, effects := range batches {
		for _, effect := range effects {
			switch effect.(type) {
			case KeyDownEffect, KeyUpEffect, TargetTapEffect:
				t.Fatalf("audio path emitted keyboard effect %#v", effect)
			}
		}
	}
}
