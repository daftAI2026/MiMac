package bridge

import (
	"errors"
	"reflect"
	"testing"
)

func mustBinding(t *testing.T, policy EndPolicy, keys ...Key) Binding {
	t.Helper()
	b, err := NewBinding(policy, keys...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustDevice(t *testing.T, id string, generation uint64) Device {
	t.Helper()
	d, err := NewDevice(id, generation)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustPress(t *testing.T, device Device, button string, id uint64) PressToken {
	t.Helper()
	p, err := NewPressToken(device, button, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustStep(t *testing.T, core *Core, event Event) []Effect {
	t.Helper()
	effects, err := core.Step(event)
	if err != nil {
		t.Fatal(err)
	}
	return effects
}

func setupCore(t *testing.T, binding Binding) (*Core, Device) {
	t.Helper()
	core, err := NewCore(binding)
	if err != nil {
		t.Fatal(err)
	}
	device := mustDevice(t, "remote-1", 1)
	mustStep(t, core, DeviceConnected(device))
	mustStep(t, core, RouteAvailable(true))
	mustStep(t, core, ScopeAvailable(true))
	return core, device
}

func keyTransitions(effects []Effect) []string {
	var got []string
	for _, effect := range effects {
		switch e := effect.(type) {
		case KeyDownEffect:
			got = append(got, "down:"+string(e.Key))
		case KeyUpEffect:
			got = append(got, "up:"+string(e.Key))
		}
	}
	return got
}

func tapKeys(t *testing.T, effects []Effect) [][]Key {
	t.Helper()
	var got [][]Key
	for _, effect := range effects {
		if tap, ok := effect.(TargetTapEffect); ok {
			got = append(got, tap.Keys())
		}
	}
	return got
}

func hasEffect[T any](effects []Effect) bool {
	var zero T
	wanted := reflect.TypeOf(zero)
	for _, effect := range effects {
		if reflect.TypeOf(effect) == wanted {
			return true
		}
	}
	return false
}

func blockedReason(effects []Effect) (BlockReason, bool) {
	for _, effect := range effects {
		if blocked, ok := effect.(BlockedEffect); ok {
			return blocked.Reason, true
		}
	}
	return 0, false
}

func TestCoreRequiresExplicitDeviceRouteAndScope(t *testing.T) {
	core, err := NewCore(mustBinding(t, PolicyExactEdge, "space"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := core.Snapshot(); snapshot.HasDevice || snapshot.RouteKnown || snapshot.ScopeKnown {
		t.Fatalf("new core should preserve unknown readiness explicitly: %#v", snapshot)
	}
	device := mustDevice(t, "remote-1", 1)
	press := mustPress(t, device, "voice", 1)
	if got := mustStep(t, core, VoiceBegan(press)); len(got) != 0 {
		t.Fatalf("disconnected press produced effects: %#v", got)
	}
	mustStep(t, core, DeviceConnected(device))
	if snapshot := core.Snapshot(); !snapshot.HasDevice || !snapshot.Connected || snapshot.RouteKnown || snapshot.ScopeKnown {
		t.Fatalf("device connect should not invent route/scope readiness: %#v", snapshot)
	}
	noRoutePress := mustPress(t, device, "voice", 2)
	if got := mustStep(t, core, VoiceBegan(noRoutePress)); len(got) != 0 {
		t.Fatalf("press without known route/scope produced effects: %#v", got)
	}
	mustStep(t, core, VoiceEnded(noRoutePress))
	mustStep(t, core, RouteAvailable(true))
	noScopePress := mustPress(t, device, "voice", 3)
	if got := mustStep(t, core, VoiceBegan(noScopePress)); len(got) != 0 {
		t.Fatalf("press without known scope produced effects: %#v", got)
	}
	mustStep(t, core, VoiceEnded(noScopePress))
	mustStep(t, core, ScopeAvailable(true))
	got := mustStep(t, core, VoiceBegan(mustPress(t, device, "voice", 4)))
	if !hasEffect[PrepareSourceEffect](got) || !reflect.DeepEqual(keyTransitions(got), []string{"down:space"}) {
		t.Fatalf("ready core did not begin explicitly: %#v", got)
	}
}

func TestUnconfiguredCoreWaitsForFreshVoicePressAfterConfiguration(t *testing.T) {
	if _, err := NewCore(Binding{}); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("NewCore zero binding error = %v", err)
	}
	core := NewUnconfiguredCore()
	if core == nil {
		t.Fatal("unconfigured constructor returned nil core")
	}
	if snapshot := core.Snapshot(); snapshot.Configured || snapshot.Adopted.Policy() != 0 || len(snapshot.Adopted.Keys()) != 0 {
		t.Fatalf("new core invented a binding: %#v", snapshot)
	}

	device := mustDevice(t, "remote-1", 1)
	mustStep(t, core, DeviceConnected(device))
	mustStep(t, core, RouteAvailable(true))
	mustStep(t, core, ScopeAvailable(true))

	held := mustPress(t, device, "voice", 1)
	if got := mustStep(t, core, VoiceBegan(held)); len(got) != 0 {
		t.Fatalf("unconfigured voice press produced action or audio effects: %#v", got)
	}
	if snapshot := core.Snapshot(); snapshot.Configured || snapshot.HasSession || len(snapshot.HeldKeys) != 0 || !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{held}) {
		t.Fatalf("unconfigured physical press was not safely observed: %#v", snapshot)
	}

	if _, err := core.Step(Configure(Binding{})); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("zero binding error = %v", err)
	}
	if snapshot := core.Snapshot(); snapshot.Configured || !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{held}) {
		t.Fatalf("invalid configuration changed the unconfigured press: %#v", snapshot)
	}
	if got := mustStep(t, core, Configure(mustBinding(t, PolicyExactEdge, "space"))); len(got) != 0 {
		t.Fatalf("configuration retroactively acted on a held press: %#v", got)
	}
	if snapshot := core.Snapshot(); !snapshot.Configured || snapshot.HasSession || !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{held}) {
		t.Fatalf("configuration changed held press state: %#v", snapshot)
	}
	if got := mustStep(t, core, VoiceEnded(held)); len(got) != 0 {
		t.Fatalf("release of a pre-configuration press produced effects: %#v", got)
	}
	if snapshot := core.Snapshot(); len(snapshot.PressedButtons) != 0 || snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("pre-configuration press did not release cleanly: %#v", snapshot)
	}

	fresh := mustPress(t, device, "voice", 2)
	effects := mustStep(t, core, VoiceBegan(fresh))
	if !hasEffect[PrepareSourceEffect](effects) || !reflect.DeepEqual(keyTransitions(effects), []string{"down:space"}) {
		t.Fatalf("fresh configured press did not start: %#v", effects)
	}
}

func TestObservedPressSharesReplayGateAndDoesNotOwnKeys(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
	observed := mustPress(t, device, "unmapped", 1)
	if got := mustStep(t, core, ObservedBegan(observed)); len(got) != 0 {
		t.Fatalf("observed begin produced effects: %#v", got)
	}
	if snapshot := core.Snapshot(); len(snapshot.HeldKeys) != 0 || !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{observed}) {
		t.Fatalf("observed begin acquired keys or was not tracked: %#v", snapshot)
	}
	if got := mustStep(t, core, ObservedBegan(observed)); len(got) != 0 {
		t.Fatalf("duplicate observed begin produced effects: %#v", got)
	}

	// Press IDs are shared by all event kinds within a connection generation.
	replayedAsVoice := mustPress(t, device, "voice", observed.PressID())
	if got := mustStep(t, core, VoiceBegan(replayedAsVoice)); len(got) != 0 {
		t.Fatalf("observed press ID replayed as voice: %#v", got)
	}

	endedBeforeBegin := mustPress(t, device, "late", 2)
	if got := mustStep(t, core, OrdinaryEnded(endedBeforeBegin)); len(got) != 0 {
		t.Fatalf("unmatched observed release produced effects: %#v", got)
	}
	if got := mustStep(t, core, ObservedBegan(endedBeforeBegin)); len(got) != 0 {
		t.Fatalf("late observed begin replayed after release: %#v", got)
	}

	voice := mustPress(t, device, "voice", 3)
	begin := mustStep(t, core, VoiceBegan(voice))
	if !hasEffect[PrepareSourceEffect](begin) || !reflect.DeepEqual(keyTransitions(begin), []string{"down:space"}) {
		t.Fatalf("voice press after observed presses failed: %#v", begin)
	}
	token := prepareToken(t, begin)
	mustStep(t, core, SourcePrepared(token, "source"))

	snapshot := core.Snapshot()
	if !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{observed, voice}) {
		t.Fatalf("pressed tokens are not complete and press-ID sorted: %v", snapshot.PressedButtons)
	}
	snapshot.PressedButtons[0] = PressToken{}
	if got := core.Snapshot().PressedButtons; !reflect.DeepEqual(got, []PressToken{observed, voice}) {
		t.Fatalf("mutating snapshot pressed tokens changed core state: %v", got)
	}

	if got := mustStep(t, core, OrdinaryEnded(observed)); len(got) != 0 {
		t.Fatalf("observed release produced key/session effects: %#v", got)
	}
	if snapshot := core.Snapshot(); !snapshot.HasSession || !reflect.DeepEqual(snapshot.HeldKeys, []Key{"space"}) || !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{voice}) {
		t.Fatalf("observed release affected voice ownership: %#v", snapshot)
	}
	mustStep(t, core, VoiceEnded(voice))
	if got := keyTransitions(mustStep(t, core, SourceDrained(token))); !reflect.DeepEqual(got, []string{"up:space"}) {
		t.Fatalf("voice release after observed press = %v", got)
	}
	if got := core.Snapshot().PressedButtons; len(got) != 0 {
		t.Fatalf("released buttons remain in snapshot: %v", got)
	}
}

func TestObservedPressDisconnectAndOldGenerationAreHarmless(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
	observed := mustPress(t, device, "unmapped", 1)
	voice := mustPress(t, device, "voice", 2)
	mustStep(t, core, ObservedBegan(observed))
	begin := mustStep(t, core, VoiceBegan(voice))
	token := prepareToken(t, begin)
	mustStep(t, core, SourcePrepared(token, "source"))

	effects := mustStep(t, core, DeviceDisconnected(device))
	if !hasEffect[CloseGateEffect](effects) || !hasEffect[StopSourceEffect](effects) || !reflect.DeepEqual(keyTransitions(effects), []string{"up:space"}) {
		t.Fatalf("disconnect failed to clean active voice ownership: %#v", effects)
	}
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("disconnect retained pressed or leased state: %#v", snapshot)
	}

	nextDevice := mustDevice(t, "remote-1", 2)
	mustStep(t, core, DeviceConnected(nextDevice))
	stale := mustPress(t, device, "unmapped", 3)
	if got := mustStep(t, core, ObservedBegan(stale)); len(got) != 0 {
		t.Fatalf("old-generation observed begin produced effects: %#v", got)
	}
	fresh := mustPress(t, nextDevice, "unmapped", 1)
	if got := mustStep(t, core, ObservedBegan(fresh)); len(got) != 0 {
		t.Fatalf("fresh-generation observed begin produced effects: %#v", got)
	}
	if snapshot := core.Snapshot(); !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{fresh}) {
		t.Fatalf("fresh-generation press was not recorded: %#v", snapshot.PressedButtons)
	}
	if got := mustStep(t, core, OrdinaryEnded(observed)); len(got) != 0 {
		t.Fatalf("old-generation release produced effects: %#v", got)
	}
	if snapshot := core.Snapshot(); !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{fresh}) {
		t.Fatalf("old-generation release affected fresh press: %#v", snapshot.PressedButtons)
	}
	mustStep(t, core, OrdinaryEnded(fresh))
	if got := core.Snapshot().PressedButtons; len(got) != 0 {
		t.Fatalf("fresh release left a pressed button: %v", got)
	}
}

func TestExactEdgeFreezesBindingAndReleasesInReverseChordOrder(t *testing.T) {
	inputKeys := []Key{"ctrl", "shift", "space"}
	initial, err := NewBinding(PolicyExactEdge, inputKeys...)
	if err != nil {
		t.Fatal(err)
	}
	inputKeys[0] = "mutated"
	core, device := setupCore(t, initial)
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	if got, want := keyTransitions(begin), []string{"down:ctrl", "down:shift", "down:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("key-down order = %v, want %v", got, want)
	}
	prep := prepareToken(t, begin)
	ready := mustStep(t, core, SourcePrepared(prep, "source-a"))
	if !hasEffect[OpenGateEffect](ready) {
		t.Fatalf("prepared source did not open gate: %#v", ready)
	}
	newKeys := []Key{"option", "enter"}
	newBinding, err := NewBinding(PolicyAfterDrain, newKeys...)
	if err != nil {
		t.Fatal(err)
	}
	newKeys[0] = "mutated-again"
	mustStep(t, core, Configure(newBinding))
	if got := core.Snapshot().Session.Binding.Keys(); !reflect.DeepEqual(got, []Key{"ctrl", "shift", "space"}) {
		t.Fatalf("session binding changed under configuration: %v", got)
	}
	release := mustStep(t, core, VoiceEnded(press))
	if got, want := keyTransitions(release), []string{"up:space", "up:shift", "up:ctrl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exact-edge release order = %v, want %v", got, want)
	}
	if !hasEffect[StopSourceEffect](release) || !hasEffect[KeyUpEffect](release) {
		t.Fatalf("release did not stop capture and release held keys: %#v", release)
	}
	drained := mustStep(t, core, SourceDrained(prep))
	if !hasEffect[CloseGateEffect](drained) || !hasEffect[DisposeResourceEffect](drained) {
		t.Fatalf("completion omitted cleanup: %#v", drained)
	}
	snapshot := core.Snapshot()
	if snapshot.HasSession || snapshot.Adopted.Policy() != PolicyAfterDrain || !reflect.DeepEqual(snapshot.Adopted.Keys(), []Key{"option", "enter"}) {
		t.Fatalf("pending config was not adopted after completion: %#v", snapshot)
	}
}

func TestAfterDrainKeepsLeaseUntilLocalDrain(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "ctrl", "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	mustStep(t, core, SourcePrepared(token, "source-a"))
	newBinding := mustBinding(t, PolicyExactEdge, "enter")
	mustStep(t, core, Configure(newBinding))
	release := mustStep(t, core, VoiceEnded(press))
	if got := keyTransitions(release); len(got) != 0 {
		t.Fatalf("after-drain key released at physical edge: %v", got)
	}
	if got := core.Snapshot().HeldKeys; !reflect.DeepEqual(got, []Key{"ctrl", "space"}) {
		t.Fatalf("key lease was not held while draining: %v", got)
	}
	if got := mustStep(t, core, SourceDrained(token)); !reflect.DeepEqual(keyTransitions(got), []string{"up:space", "up:ctrl"}) {
		t.Fatalf("after-drain key-up order = %v", keyTransitions(got))
	}
	if got := core.Snapshot().Adopted.Keys(); !reflect.DeepEqual(got, []Key{"enter"}) {
		t.Fatalf("configuration not adopted at drain boundary: %v", got)
	}
}

func TestEarlyVoiceEndedDrainsPreparationAndOpensLateResource(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	ended := mustStep(t, core, VoiceEnded(press))
	if hasEffect[CloseGateEffect](ended) || hasEffect[KeyUpEffect](ended) || !hasEffect[StopSourceEffect](ended) {
		t.Fatalf("early voice end did not retain the draining lease: %#v", ended)
	}
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != PhaseDraining || !reflect.DeepEqual(snapshot.HeldKeys, []Key{"space"}) {
		t.Fatalf("early voice end did not enter draining with its key lease: %+v", snapshot)
	}
	prepared := mustStep(t, core, SourcePrepared(token, "late-source"))
	if !reflect.DeepEqual(prepared, []Effect{OpenGateEffect{Token: token}}) {
		t.Fatalf("matching resource prepared after voice end should open the gate: %#v", prepared)
	}
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != PhaseDraining {
		t.Fatalf("late preparation changed the draining phase: %+v", snapshot)
	}
	drained := mustStep(t, core, SourceDrained(token))
	if !hasEffect[CloseGateEffect](drained) || !containsDisposed(drained, "late-source") || !reflect.DeepEqual(keyTransitions(drained), []string{"up:space"}) {
		t.Fatalf("early voice end did not complete normal drain cleanup: %#v", drained)
	}
	if core.Snapshot().HasSession {
		t.Fatal("drained early voice end left an active session")
	}
}

func TestToggleEarlyVoiceEndedDrainsPreparationWithoutGuessingCancellation(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyToggle, "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	ended := mustStep(t, core, VoiceEnded(press))
	if hasEffect[CloseGateEffect](ended) || hasEffect[TargetTapEffect](ended) || !hasEffect[StopSourceEffect](ended) {
		t.Fatalf("toggle voice end during preparation did not wait for drain: %#v", ended)
	}
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != PhaseDraining {
		t.Fatalf("toggle voice end did not enter draining: %+v", snapshot)
	}
	prepared := mustStep(t, core, SourcePrepared(token, "toggle-source"))
	if !reflect.DeepEqual(prepared, []Effect{OpenGateEffect{Token: token}}) {
		t.Fatalf("matching toggle resource prepared after voice end should open the gate: %#v", prepared)
	}
	drained := mustStep(t, core, SourceDrained(token))
	if !reflect.DeepEqual(tapKeys(t, drained), [][]Key{{"space"}}) || !hasEffect[CloseGateEffect](drained) || !containsDisposed(drained, "toggle-source") {
		t.Fatalf("toggle completion did not tap only at drain boundary: %#v", drained)
	}
	if core.Snapshot().HasSession || len(core.Snapshot().RecoveryKeys) != 0 {
		t.Fatalf("normal toggle drain incorrectly entered recovery: %+v", core.Snapshot())
	}
}

func TestExactEdgeEarlyVoiceEndedStillCancelsPreparation(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyExactEdge, "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	ended := mustStep(t, core, VoiceEnded(press))
	if !hasEffect[CloseGateEffect](ended) || !hasEffect[StopSourceEffect](ended) || !hasEffect[KeyUpEffect](ended) {
		t.Fatalf("exact-edge early release no longer cancels preparation: %#v", ended)
	}
	if core.Snapshot().HasSession {
		t.Fatal("exact-edge early release left an active session")
	}
	late := mustStep(t, core, SourcePrepared(token, "late-source"))
	if hasEffect[OpenGateEffect](late) || !containsDisposed(late, "late-source") {
		t.Fatalf("exact-edge late resource was not disposed: %#v", late)
	}
}

func TestFailureAfterEarlyVoiceEndedStillCancelsAndReleasesOwnership(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	ended := mustStep(t, core, VoiceEnded(press))
	if hasEffect[CloseGateEffect](ended) || !hasEffect[StopSourceEffect](ended) {
		t.Fatalf("VoiceEnded was treated as cancellation: %#v", ended)
	}

	failed := mustStep(t, core, SourceFailed(token))
	if !hasEffect[CloseGateEffect](failed) || !hasEffect[StopSourceEffect](failed) || !reflect.DeepEqual(keyTransitions(failed), []string{"up:space"}) {
		t.Fatalf("source failure during early drain did not cancel and release ownership: %#v", failed)
	}
	if core.Snapshot().HasSession || len(core.Snapshot().HeldKeys) != 0 {
		t.Fatalf("source failure left an early-drain lease: %+v", core.Snapshot())
	}
	late := mustStep(t, core, SourcePrepared(token, "failed-late-source"))
	if hasEffect[OpenGateEffect](late) || !containsDisposed(late, "failed-late-source") {
		t.Fatalf("late resource after drain failure was not disposed: %#v", late)
	}
}

func TestPreparationFailureCleansUpAndAppliesPendingConfig(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyExactEdge, "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	mustStep(t, core, Configure(mustBinding(t, PolicyAfterDrain, "enter")))
	failed := mustStep(t, core, SourceFailed(token))
	if got, want := keyTransitions(failed), []string{"up:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed preparation did not release held keys: got %v want %v", got, want)
	}
	if !hasEffect[CloseGateEffect](failed) || !hasEffect[StopSourceEffect](failed) {
		t.Fatalf("failed preparation omitted cleanup requests: %#v", failed)
	}
	snapshot := core.Snapshot()
	if snapshot.HasSession || snapshot.Adopted.Policy() != PolicyAfterDrain || !reflect.DeepEqual(snapshot.Adopted.Keys(), []Key{"enter"}) {
		t.Fatalf("failure did not finish session and adopt pending config: %#v", snapshot)
	}
}

func TestToggleCancellationLocksKeysUntilExplicitConfirmation(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyToggle, "ctrl", "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	if got := tapKeys(t, begin); !reflect.DeepEqual(got, [][]Key{{"ctrl", "space"}}) {
		t.Fatalf("toggle start did not preserve chord order: %v", got)
	}
	mustStep(t, core, SourcePrepared(token, "source-a"))
	ended := mustStep(t, core, VoiceEnded(press))
	if hasEffect[TargetTapEffect](ended) {
		t.Fatalf("physical release should wait for local drain before the stop tap: %#v", ended)
	}
	cancelled := mustStep(t, core, RouteAvailable(false))
	if hasEffect[TargetTapEffect](cancelled) {
		t.Fatalf("cancellation guessed target toggle state: %#v", cancelled)
	}
	if got := core.Snapshot().RecoveryKeys; !reflect.DeepEqual(got, []Key{"ctrl", "space"}) {
		t.Fatalf("toggle cancellation recovery set = %v", got)
	}
	mustStep(t, core, RouteAvailable(true))
	mustStep(t, core, Configure(mustBinding(t, PolicyToggle, "option")))
	ordinary := mustPress(t, device, "ordinary", 2)
	if reason, ok := blockedReason(mustStep(t, core, OrdinaryBegan(ordinary, "space"))); !ok || reason != BlockToggleRecoveryRequired {
		t.Fatalf("ordinary mapping bypassed recovery lock: reason=%v ok=%v", reason, ok)
	}
	ordinarySpace := mustPress(t, device, "ordinary", 2)
	mustStep(t, core, OrdinaryEnded(ordinarySpace))
	if core.Snapshot().HasSession {
		t.Fatal("blocked ordinary press started a voice session")
	}
	// Reconfiguring back to an uncertain toggle key also remains blocked.
	mustStep(t, core, Configure(mustBinding(t, PolicyToggle, "space")))
	if reason, ok := blockedReason(mustStep(t, core, VoiceBegan(mustPress(t, device, "voice", 3)))); !ok || reason != BlockToggleRecoveryRequired {
		t.Fatalf("configuration change cleared toggle recovery: reason=%v ok=%v", reason, ok)
	}
	mustStep(t, core, VoiceEnded(mustPress(t, device, "voice", 3)))
	mustStep(t, core, ConfirmToggleIdle("space"))
	allowed := mustStep(t, core, VoiceBegan(mustPress(t, device, "voice", 4)))
	if !hasEffect[TargetTapEffect](allowed) {
		t.Fatalf("explicit idle confirmation did not clear recovery: %#v", allowed)
	}
}

func TestRecoveryBlocksReconfiguredVoiceChord(t *testing.T) {
	for _, policy := range []EndPolicy{PolicyExactEdge, PolicyAfterDrain} {
		name := map[EndPolicy]string{
			PolicyExactEdge:  "exact-edge",
			PolicyAfterDrain: "after-drain",
		}[policy]
		t.Run(name, func(t *testing.T) {
			core, device := setupCore(t, mustBinding(t, PolicyToggle, "ctrl", "space"))
			oldPress := mustPress(t, device, "voice", 1)
			begin := mustStep(t, core, VoiceBegan(oldPress))
			prepareToken(t, begin)

			mustStep(t, core, RouteAvailable(false))
			mustStep(t, core, VoiceEnded(oldPress))
			mustStep(t, core, RouteAvailable(true))
			mustStep(t, core, Configure(mustBinding(t, policy, "ctrl", "space")))

			blockedPress := mustPress(t, device, "voice", 2)
			blocked := mustStep(t, core, VoiceBegan(blockedPress))
			if reason, ok := blockedReason(blocked); !ok || reason != BlockToggleRecoveryRequired {
				t.Fatalf("reconfigured voice chord bypassed recovery lock: reason=%v ok=%v effects=%#v", reason, ok, blocked)
			}
			if hasEffect[KeyDownEffect](blocked) || hasEffect[PrepareSourceEffect](blocked) {
				t.Fatalf("recovery-blocked voice press acquired keys or prepared a source: %#v", blocked)
			}
			mustStep(t, core, VoiceEnded(blockedPress))

			// A disjoint chord remains usable while recovery is pending.
			mustStep(t, core, Configure(mustBinding(t, policy, "enter")))
			disjointPress := mustPress(t, device, "voice", 3)
			disjoint := mustStep(t, core, VoiceBegan(disjointPress))
			if reason, ok := blockedReason(disjoint); ok {
				t.Fatalf("disjoint voice chord was blocked during recovery: reason=%v effects=%#v", reason, disjoint)
			}
			if !hasEffect[PrepareSourceEffect](disjoint) || !reflect.DeepEqual(keyTransitions(disjoint), []string{"down:enter"}) {
				t.Fatalf("disjoint voice chord did not start normally: %#v", disjoint)
			}
			disjointToken := prepareToken(t, disjoint)
			mustStep(t, core, SourcePrepared(disjointToken, "disjoint-source"))
			mustStep(t, core, VoiceEnded(disjointPress))
			mustStep(t, core, SourceDrained(disjointToken))
			if got := core.Snapshot().RecoveryKeys; !reflect.DeepEqual(got, []Key{"ctrl", "space"}) {
				t.Fatalf("using a disjoint chord cleared recovery state: %v", got)
			}

			// Explicit confirmation clears only the caller-confirmed uncertainty.
			mustStep(t, core, Configure(mustBinding(t, policy, "ctrl", "space")))
			mustStep(t, core, ConfirmToggleIdle("ctrl", "space"))
			allowedPress := mustPress(t, device, "voice", 4)
			allowed := mustStep(t, core, VoiceBegan(allowedPress))
			if reason, ok := blockedReason(allowed); ok {
				t.Fatalf("confirmed voice chord remained blocked: reason=%v effects=%#v", reason, allowed)
			}
			if !hasEffect[PrepareSourceEffect](allowed) || !reflect.DeepEqual(keyTransitions(allowed), []string{"down:ctrl", "down:space"}) {
				t.Fatalf("explicit confirmation did not permit voice chord: %#v", allowed)
			}
			mustStep(t, core, VoiceEnded(allowedPress))
		})
	}
}

func TestToggleCompletesWithSecondTapOnlyAfterDrain(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyToggle, "ctrl", "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	mustStep(t, core, SourcePrepared(token, "source-a"))
	ordinary := mustPress(t, device, "ordinary", 2)
	if reason, ok := blockedReason(mustStep(t, core, OrdinaryBegan(ordinary, "space"))); !ok || reason != BlockOrdinaryConflictsWithToggle {
		t.Fatalf("ordinary mapping overlapped a live toggle session: reason=%v ok=%v", reason, ok)
	}
	mustStep(t, core, OrdinaryEnded(ordinary))
	mustStep(t, core, VoiceEnded(press))
	if got := tapKeys(t, mustStep(t, core, SourceDrained(token))); !reflect.DeepEqual(got, [][]Key{{"ctrl", "space"}}) {
		t.Fatalf("normal toggle completion did not request stop chord: %v", got)
	}
	if got := core.Snapshot().RecoveryKeys; len(got) != 0 {
		t.Fatalf("successful toggle acquired a recovery lock: %v", got)
	}
}

func TestOrdinaryKeyLeasesAcrossButtonsAndChordOrdering(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "voice"))
	one := mustPress(t, device, "button-1", 1)
	two := mustPress(t, device, "button-2", 2)
	if got, want := keyTransitions(mustStep(t, core, OrdinaryBegan(one, "ctrl", "alt"))), []string{"down:ctrl", "down:alt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first chord down = %v, want %v", got, want)
	}
	if got := mustStep(t, core, OrdinaryBegan(two, "alt")); len(got) != 0 {
		t.Fatalf("shared key generated duplicate down: %#v", got)
	}
	if got, want := keyTransitions(mustStep(t, core, OrdinaryEnded(one))), []string{"up:ctrl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reverse chord release with shared lease = %v, want %v", got, want)
	}
	if got := core.Snapshot().HeldKeys; !reflect.DeepEqual(got, []Key{"alt"}) {
		t.Fatalf("shared key released before last owner: %v", got)
	}
	if got, want := keyTransitions(mustStep(t, core, OrdinaryEnded(two))), []string{"up:alt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("last owner did not release shared key: %v, want %v", got, want)
	}
}

func TestDuplicateBeginMismatchedUpAndOldGenerationAreHarmless(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "voice"))
	first := mustPress(t, device, "button", 1)
	begin := mustStep(t, core, OrdinaryBegan(first, "space"))
	if !reflect.DeepEqual(keyTransitions(begin), []string{"down:space"}) {
		t.Fatalf("ordinary key did not start: %#v", begin)
	}
	if got := mustStep(t, core, OrdinaryBegan(first, "space")); len(got) != 0 {
		t.Fatalf("duplicate begin produced effects: %#v", got)
	}
	wrongUp := mustPress(t, device, "button", 2)
	if got := mustStep(t, core, OrdinaryEnded(wrongUp)); len(got) != 0 || !reflect.DeepEqual(core.Snapshot().HeldKeys, []Key{"space"}) {
		t.Fatalf("mismatched release changed ownership: effects=%#v keys=%v", got, core.Snapshot().HeldKeys)
	}
	nextDevice := mustDevice(t, "remote-1", 2)
	connectEffects := mustStep(t, core, DeviceConnected(nextDevice))
	if !reflect.DeepEqual(keyTransitions(connectEffects), []string{"up:space"}) {
		t.Fatalf("connection generation change failed to release old key: %#v", connectEffects)
	}
	newPress := mustPress(t, nextDevice, "button", 3)
	mustStep(t, core, OrdinaryBegan(newPress, "space"))
	if got := mustStep(t, core, OrdinaryEnded(first)); len(got) != 0 || !reflect.DeepEqual(core.Snapshot().HeldKeys, []Key{"space"}) {
		t.Fatalf("old-generation up released new lease: effects=%#v keys=%v", got, core.Snapshot().HeldKeys)
	}
	if got := keyTransitions(mustStep(t, core, OrdinaryEnded(newPress))); !reflect.DeepEqual(got, []string{"up:space"}) {
		t.Fatalf("new owner did not release key: %v", got)
	}
}

func TestReleaseObservedBeforeBeginPreventsLateStart(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyExactEdge, "space"))
	press := mustPress(t, device, "voice", 1)
	if got := mustStep(t, core, VoiceEnded(press)); len(got) != 0 {
		t.Fatalf("unmatched release produced effects: %#v", got)
	}
	if got := mustStep(t, core, VoiceBegan(press)); len(got) != 0 {
		t.Fatalf("late began opened a session after its release: %#v", got)
	}
	snapshot := core.Snapshot()
	if snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("release-before-begin left active state: %#v", snapshot)
	}
}

func TestVoiceAndOrdinaryOwnersShareOnePhysicalKeyTransition(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
	ordinary := mustPress(t, device, "ordinary", 1)
	voice := mustPress(t, device, "voice", 2)
	if got := keyTransitions(mustStep(t, core, OrdinaryBegan(ordinary, "space"))); !reflect.DeepEqual(got, []string{"down:space"}) {
		t.Fatalf("ordinary start = %v", got)
	}
	voiceBegin := mustStep(t, core, VoiceBegan(voice))
	if got := keyTransitions(voiceBegin); len(got) != 0 {
		t.Fatalf("shared key generated duplicate down: %v", got)
	}
	token := prepareToken(t, voiceBegin)
	mustStep(t, core, SourcePrepared(token, "source"))
	if got := keyTransitions(mustStep(t, core, OrdinaryEnded(ordinary))); len(got) != 0 {
		t.Fatalf("ordinary release lifted voice's key: %v", got)
	}
	mustStep(t, core, VoiceEnded(voice))
	if got := keyTransitions(mustStep(t, core, SourceDrained(token))); !reflect.DeepEqual(got, []string{"up:space"}) {
		t.Fatalf("last voice owner did not release key: %v", got)
	}
}

func TestDisconnectCancelsSessionAndDisposesLateCallbacks(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
	voice := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(voice))
	token := prepareToken(t, begin)
	mustStep(t, core, SourcePrepared(token, "accepted"))
	ordinary := mustPress(t, device, "ordinary", 2)
	mustStep(t, core, OrdinaryBegan(ordinary, "ctrl"))
	newDevice := mustDevice(t, "remote-1", 2)
	effects := mustStep(t, core, DeviceConnected(newDevice))
	if !hasEffect[CloseGateEffect](effects) || !hasEffect[StopSourceEffect](effects) {
		t.Fatalf("device replacement did not cancel active session: %#v", effects)
	}
	if got, want := keyTransitions(effects), []string{"up:space", "up:ctrl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement key releases = %v, want %v", got, want)
	}
	if !containsDisposed(effects, "accepted") {
		t.Fatalf("accepted resource not disposed on replacement: %#v", effects)
	}
	if got := mustStep(t, core, SourcePrepared(token, "late")); len(got) != 1 || !containsDisposed(got, "late") || hasEffect[OpenGateEffect](got) {
		t.Fatalf("stale callback was not disposed without opening gate: %#v", got)
	}
	if core.Snapshot().HasSession || len(core.Snapshot().HeldKeys) != 0 {
		t.Fatal("device replacement left old session or key ownership")
	}
}

func TestScopeLossReleasesAllHeldKeysAndSuppressesHeldOrdinaryPress(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
	voice := mustPress(t, device, "voice", 1)
	voiceBegin := mustStep(t, core, VoiceBegan(voice))
	token := prepareToken(t, voiceBegin)
	mustStep(t, core, SourcePrepared(token, "source"))
	ordinary := mustPress(t, device, "ordinary", 2)
	mustStep(t, core, OrdinaryBegan(ordinary, "ctrl"))
	if got, want := keyTransitions(mustStep(t, core, ScopeAvailable(false))), []string{"up:space", "up:ctrl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scope loss did not release every lease: got %v want %v", got, want)
	}
	if got := core.Snapshot().HeldKeys; len(got) != 0 {
		t.Fatalf("scope loss left held keys: %v", got)
	}
	mustStep(t, core, ScopeAvailable(true))
	if got := mustStep(t, core, OrdinaryBegan(mustPress(t, device, "ordinary", 3), "ctrl")); len(got) != 0 {
		t.Fatalf("new begin was accepted while the physical button is still held: %#v", got)
	}
	if got := mustStep(t, core, OrdinaryEnded(ordinary)); len(got) != 0 {
		t.Fatalf("release after scope loss repeated key-up: %#v", got)
	}
	fresh := mustPress(t, device, "ordinary", 4)
	if got := keyTransitions(mustStep(t, core, OrdinaryBegan(fresh, "ctrl"))); !reflect.DeepEqual(got, []string{"down:ctrl"}) {
		t.Fatalf("fresh press after release did not acquire key: %v", got)
	}
	if got := keyTransitions(mustStep(t, core, OrdinaryEnded(fresh))); !reflect.DeepEqual(got, []string{"up:ctrl"}) {
		t.Fatalf("fresh press cleanup = %v", got)
	}
	mustStep(t, core, VoiceEnded(voice))
}

func TestOutputFailureCancelsAndWaitsForCleanupAndAllPressReleases(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
	voice := mustPress(t, device, "voice", 1)
	voiceBegin := mustStep(t, core, VoiceBegan(voice))
	voiceToken := prepareToken(t, voiceBegin)
	mustStep(t, core, SourcePrepared(voiceToken, "voice-source"))

	ordinary := mustPress(t, device, "ordinary", 2)
	if got := keyTransitions(mustStep(t, core, OrdinaryBegan(ordinary, "ctrl"))); !reflect.DeepEqual(got, []string{"down:ctrl"}) {
		t.Fatalf("ordinary begin = %v", got)
	}

	failure := mustStep(t, core, OutputFailed(1))
	if got, want := keyTransitions(failure), []string{"up:space", "up:ctrl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output failure key releases = %v, want %v", got, want)
	}
	if !hasEffect[CloseGateEffect](failure) || !hasEffect[StopSourceEffect](failure) || !containsDisposed(failure, "voice-source") {
		t.Fatalf("output failure omitted session cleanup: %#v", failure)
	}
	if hasEffect[TargetTapEffect](failure) {
		t.Fatalf("output failure attempted a compensating tap: %#v", failure)
	}
	if snapshot := core.Snapshot(); snapshot.HasSession || snapshot.OutputFailureID != 1 || len(snapshot.HeldKeys) != 0 || !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{voice, ordinary}) {
		t.Fatalf("output failure lost fault or physical press state: %#v", snapshot)
	}

	// New physical begins are still recorded behind the output fault barrier.
	suppressed := mustPress(t, device, "suppressed", 3)
	if got := mustStep(t, core, OrdinaryBegan(suppressed, "option")); len(got) != 0 {
		t.Fatalf("failed output admitted a new key effect: %#v", got)
	}
	if got := core.Snapshot().PressedButtons; !reflect.DeepEqual(got, []PressToken{voice, ordinary, suppressed}) {
		t.Fatalf("fault-period physical presses were not recorded: %v", got)
	}

	if got := mustStep(t, core, OutputRecovered(1)); len(got) != 0 {
		t.Fatalf("recovery should only update readiness: %#v", got)
	}
	if got := core.Snapshot().OutputFailureID; got != 1 {
		t.Fatalf("output unlocked while presses were held: failure ID %d", got)
	}
	recoveredSuppressed := mustPress(t, device, "after-recovery", 4)
	if got := mustStep(t, core, OrdinaryBegan(recoveredSuppressed, "shift")); len(got) != 0 {
		t.Fatalf("press during the old-press barrier produced effects: %#v", got)
	}
	for _, release := range []Event{VoiceEnded(voice), OrdinaryEnded(ordinary), OrdinaryEnded(suppressed), OrdinaryEnded(recoveredSuppressed)} {
		if got := mustStep(t, core, release); len(got) != 0 {
			t.Fatalf("suppressed old press generated output while ending: %#v", got)
		}
	}
	if snapshot := core.Snapshot(); snapshot.OutputFailureID != 0 || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("output did not recover after cleanup and all releases: %#v", snapshot)
	}

	fresh := mustPress(t, device, "ordinary", 5)
	if got := keyTransitions(mustStep(t, core, OrdinaryBegan(fresh, "enter"))); !reflect.DeepEqual(got, []string{"down:enter"}) {
		t.Fatalf("fresh press did not output after recovery: %v", got)
	}
	if got := keyTransitions(mustStep(t, core, OrdinaryEnded(fresh))); !reflect.DeepEqual(got, []string{"up:enter"}) {
		t.Fatalf("fresh press cleanup = %v", got)
	}
}

func TestOutputRecoveryRequiresMatchingFailureAndDoesNotUseDisconnectAsRecovery(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "voice"))
	press := mustPress(t, device, "button", 1)
	if got := keyTransitions(mustStep(t, core, OrdinaryBegan(press, "space"))); !reflect.DeepEqual(got, []string{"down:space"}) {
		t.Fatalf("ordinary begin = %v", got)
	}
	mustStep(t, core, OutputFailed(10))
	mustStep(t, core, OutputFailed(11))
	if got := mustStep(t, core, OutputRecovered(10)); len(got) != 0 || core.Snapshot().OutputFailureID != 11 {
		t.Fatalf("stale recovery cleared current output failure: effects=%#v snapshot=%#v", got, core.Snapshot())
	}

	if got := mustStep(t, core, OrdinaryEnded(press)); len(got) != 0 {
		t.Fatalf("faulted press release produced another output: %#v", got)
	}
	if got := core.Snapshot().OutputFailureID; got != 11 {
		t.Fatalf("physical release recovered sink without cleanup: failure ID %d", got)
	}
	if got := mustStep(t, core, DeviceDisconnected(device)); len(got) != 0 {
		t.Fatalf("disconnect after logical cleanup produced effects: %#v", got)
	}
	if got := core.Snapshot().OutputFailureID; got != 11 {
		t.Fatalf("device disconnect recovered failed sink: failure ID %d", got)
	}

	if got := mustStep(t, core, OutputRecovered(11)); len(got) != 0 || core.Snapshot().OutputFailureID != 0 {
		t.Fatalf("matching cleanup did not recover idle output: effects=%#v snapshot=%#v", got, core.Snapshot())
	}
	nextDevice := mustDevice(t, "remote-1", 2)
	mustStep(t, core, DeviceConnected(nextDevice))
	fresh := mustPress(t, nextDevice, "button", 1)
	if got := keyTransitions(mustStep(t, core, OrdinaryBegan(fresh, "space"))); !reflect.DeepEqual(got, []string{"down:space"}) {
		t.Fatalf("fresh generation did not use recovered output: %v", got)
	}
}

func TestOutputFailureReleasesSharedKeyOnlyAfterAllLogicalOwners(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
	voice := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(voice))
	token := prepareToken(t, begin)
	mustStep(t, core, SourcePrepared(token, "voice-source"))
	ordinary := mustPress(t, device, "ordinary", 2)
	if got := keyTransitions(mustStep(t, core, OrdinaryBegan(ordinary, "space"))); len(got) != 0 {
		t.Fatalf("shared ordinary owner emitted duplicate down: %v", got)
	}

	failure := mustStep(t, core, OutputFailed(1))
	if got := keyTransitions(failure); !reflect.DeepEqual(got, []string{"up:space"}) {
		t.Fatalf("shared lease failure cleanup = %v", got)
	}
	if got := core.Snapshot().HeldKeys; len(got) != 0 {
		t.Fatalf("output failure retained logical shared lease: %v", got)
	}
	mustStep(t, core, OutputRecovered(1))
	for _, release := range []Event{VoiceEnded(voice), OrdinaryEnded(ordinary)} {
		if got := mustStep(t, core, release); len(got) != 0 {
			t.Fatalf("old shared owner released a second time: %#v", got)
		}
	}
	if snapshot := core.Snapshot(); snapshot.OutputFailureID != 0 || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("shared failure did not recover cleanly: %#v", snapshot)
	}
}

func TestOutputFailurePreservesToggleUncertaintyAfterRecovery(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyToggle, "ctrl", "space"))
	voice := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(voice))
	voiceToken := prepareToken(t, begin)
	mustStep(t, core, SourcePrepared(voiceToken, "voice-source"))

	failure := mustStep(t, core, OutputFailed(1))
	if hasEffect[TargetTapEffect](failure) || len(keyTransitions(failure)) != 0 {
		t.Fatalf("toggle output failure guessed target state: %#v", failure)
	}
	if got := core.Snapshot().RecoveryKeys; !reflect.DeepEqual(got, []Key{"ctrl", "space"}) {
		t.Fatalf("toggle uncertainty was not preserved: %v", got)
	}
	mustStep(t, core, OutputRecovered(1))
	if got := mustStep(t, core, VoiceEnded(voice)); len(got) != 0 {
		t.Fatalf("ending cancelled toggle press produced effects: %#v", got)
	}
	if snapshot := core.Snapshot(); snapshot.OutputFailureID != 0 || !reflect.DeepEqual(snapshot.RecoveryKeys, []Key{"ctrl", "space"}) {
		t.Fatalf("output recovery cleared toggle recovery or stayed blocked: %#v", snapshot)
	}

	blocked := mustPress(t, device, "voice", 2)
	effects := mustStep(t, core, VoiceBegan(blocked))
	if reason, ok := blockedReason(effects); !ok || reason != BlockToggleRecoveryRequired || hasEffect[TargetTapEffect](effects) {
		t.Fatalf("toggle uncertainty was bypassed after output recovery: %#v", effects)
	}
	mustStep(t, core, VoiceEnded(blocked))
	mustStep(t, core, ConfirmToggleIdle("ctrl", "space"))
	fresh := mustPress(t, device, "voice", 3)
	if got := tapKeys(t, mustStep(t, core, VoiceBegan(fresh))); !reflect.DeepEqual(got, [][]Key{{"ctrl", "space"}}) {
		t.Fatalf("explicit confirmation did not restore toggle: %v", got)
	}
}

func TestOutputFailureAfterToggleSessionFinishesPreservesUncertainty(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyToggle, "ctrl", "space"))
	voice := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(voice))
	token := prepareToken(t, begin)
	mustStep(t, core, SourcePrepared(token, "voice-source"))
	mustStep(t, core, VoiceEnded(voice))
	drained := mustStep(t, core, SourceDrained(token))
	if got := tapKeys(t, drained); !reflect.DeepEqual(got, [][]Key{{"ctrl", "space"}}) {
		t.Fatalf("completed toggle did not emit its stop tap: %v", got)
	}
	if core.Snapshot().HasSession {
		t.Fatal("source drain left the toggle session active")
	}

	// A stop-tap output failure arrives after SourceDrained has committed Idle.
	failure := mustStep(t, core, OutputFailed(1, "ctrl", "space"))
	if hasEffect[TargetTapEffect](failure) || len(keyTransitions(failure)) != 0 {
		t.Fatalf("failed stop tap was blindly repeated: %#v", failure)
	}
	if got := core.Snapshot().RecoveryKeys; !reflect.DeepEqual(got, []Key{"ctrl", "space"}) {
		t.Fatalf("failed completed tap did not preserve recovery lock: %v", got)
	}
	mustStep(t, core, OutputRecovered(1))
	if got := core.Snapshot().RecoveryKeys; !reflect.DeepEqual(got, []Key{"ctrl", "space"}) {
		t.Fatalf("sink recovery cleared target toggle uncertainty: %v", got)
	}
	blocked := mustPress(t, device, "voice", 2)
	if reason, ok := blockedReason(mustStep(t, core, VoiceBegan(blocked))); !ok || reason != BlockToggleRecoveryRequired {
		t.Fatalf("failed completed toggle was not blocked after output recovery: reason=%v ok=%v", reason, ok)
	}
	mustStep(t, core, VoiceEnded(blocked))
	mustStep(t, core, ConfirmToggleIdle("ctrl", "space"))
}

func TestMatchedTimeoutCancelsAndCleansPreparedSource(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyExactEdge, "ctrl", "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	mustStep(t, core, SourcePrepared(token, "source"))
	timeout := mustStep(t, core, SessionTimeout(token))
	if got, want := keyTransitions(timeout), []string{"up:space", "up:ctrl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("timeout did not release in reverse order: got %v want %v", got, want)
	}
	if !hasEffect[CloseGateEffect](timeout) || !hasEffect[StopSourceEffect](timeout) || !containsDisposed(timeout, "source") {
		t.Fatalf("timeout omitted source cleanup: %#v", timeout)
	}
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("timeout left live ownership: %#v", snapshot)
	}
}

func TestToggleTapConflictsWithAnyLeasedChordMember(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyToggle, "ctrl", "space"))
	ordinary := mustPress(t, device, "ordinary", 1)
	mustStep(t, core, OrdinaryBegan(ordinary, "space"))
	voice := mustPress(t, device, "voice", 2)
	if reason, ok := blockedReason(mustStep(t, core, VoiceBegan(voice))); !ok || reason != BlockTapConflictsWithHeldKey {
		t.Fatalf("toggle chord overlapped a leased member: reason=%v ok=%v", reason, ok)
	}
	if core.Snapshot().HasSession || hasEffect[TargetTapEffect](mustStep(t, core, VoiceBegan(mustPress(t, device, "voice-2", 3)))) {
		t.Fatal("blocked toggle press started unexpectedly")
	}
	mustStep(t, core, VoiceEnded(voice))
	mustStep(t, core, VoiceEnded(mustPress(t, device, "voice-2", 3)))
	mustStep(t, core, OrdinaryEnded(ordinary))
}

func TestSnapshotAndBindingAccessorsDoNotExposeMutableState(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "ctrl", "space"))
	press := mustPress(t, device, "voice", 1)
	begin := mustStep(t, core, VoiceBegan(press))
	token := prepareToken(t, begin)
	snap := core.Snapshot()
	keys := snap.Session.Binding.Keys()
	keys[0] = "changed"
	adopted := snap.Adopted.Keys()
	adopted[1] = "changed"
	mustStep(t, core, SourcePrepared(token, "source"))
	if got := tapKeys(t, begin); len(got) != 0 {
		t.Fatalf("non-toggle unexpectedly produced tap: %v", got)
	}
	if got := core.Snapshot().Session.Binding.Keys(); !reflect.DeepEqual(got, []Key{"ctrl", "space"}) {
		t.Fatalf("snapshot mutation affected session: %v", got)
	}
	if got := core.Snapshot().Adopted.Keys(); !reflect.DeepEqual(got, []Key{"ctrl", "space"}) {
		t.Fatalf("snapshot mutation affected adopted config: %v", got)
	}
}

type unknownEvent struct{}

func (unknownEvent) bridgeEvent() {}

func TestInvalidAndUnknownEventsFailWithoutMutatingState(t *testing.T) {
	core, _ := setupCore(t, mustBinding(t, PolicyExactEdge, "space"))
	before := core.Snapshot()
	if _, err := core.Step(Configure(Binding{})); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("zero binding error = %v", err)
	}
	if _, err := core.Step(OrdinaryBegan(mustPress(t, mustDevice(t, "remote-1", 1), "button", 1), "space", "space")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate chord error = %v", err)
	}
	if _, err := core.Step(ObservedBegan(PressToken{})); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid observed press error = %v", err)
	}
	if _, err := core.Step(OutputFailed(0)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero output failure ID error = %v", err)
	}
	if _, err := core.Step(OutputRecovered(0)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero output recovery ID error = %v", err)
	}
	if _, err := core.Step(OutputFailed(1, "space", "space")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate uncertain toggle key error = %v", err)
	}
	if _, err := core.Step(unknownEvent{}); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("unknown event error = %v", err)
	}
	if after := core.Snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid event mutated state: before=%#v after=%#v", before, after)
	}
	var zero Core
	if _, err := zero.Step(DeviceConnected(mustDevice(t, "remote-1", 1))); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero-value core error = %v", err)
	}
}

func TestStaleDrainedAndTimeoutDoNotTouchNewSession(t *testing.T) {
	core, device := setupCore(t, mustBinding(t, PolicyExactEdge, "space"))
	firstPress := mustPress(t, device, "voice", 1)
	firstBegin := mustStep(t, core, VoiceBegan(firstPress))
	firstToken := prepareToken(t, firstBegin)
	mustStep(t, core, SourcePrepared(firstToken, "first"))
	mustStep(t, core, VoiceEnded(firstPress))
	mustStep(t, core, SourceDrained(firstToken))
	secondPress := mustPress(t, device, "voice", 2)
	mustStep(t, core, VoiceBegan(secondPress))
	before := core.Snapshot()
	if got := mustStep(t, core, VoiceEnded(firstPress)); len(got) != 0 {
		t.Fatalf("old physical release affected the new press: %#v", got)
	}
	if got := mustStep(t, core, SourceDrained(firstToken)); len(got) != 0 {
		t.Fatalf("stale drain produced effects: %#v", got)
	}
	if got := mustStep(t, core, SessionTimeout(firstToken)); len(got) != 0 {
		t.Fatalf("stale timeout produced effects: %#v", got)
	}
	if after := core.Snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("stale callbacks touched new session: before=%#v after=%#v", before, after)
	}
}

func prepareToken(t *testing.T, effects []Effect) SessionToken {
	t.Helper()
	for _, effect := range effects {
		if prepared, ok := effect.(PrepareSourceEffect); ok {
			return prepared.Token
		}
	}
	t.Fatalf("prepare-source effect not found in %#v", effects)
	return SessionToken{}
}

func containsDisposed(effects []Effect, resourceID string) bool {
	for _, effect := range effects {
		if disposed, ok := effect.(DisposeResourceEffect); ok && disposed.ResourceID == resourceID {
			return true
		}
	}
	return false
}
