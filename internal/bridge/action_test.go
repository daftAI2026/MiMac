package bridge

import (
	"reflect"
	"testing"
)

func actionTestPlan(t *testing.T, scope ActionScope, trigger ActionTrigger, request, release TargetRequest, keys ...Key) ActionPlan {
	t.Helper()
	plan, err := NewActionPlan(ActionSpec{
		ToolID:         "tool-a",
		ActionID:       "dictation",
		CandidateRaw:   "Ctrl+Shift+V",
		Revision:       7,
		Scope:          scope,
		Trigger:        trigger,
		Request:        request,
		ReleaseRequest: release,
	}, keys...)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func actionTestReadyCore(t *testing.T, initial Binding) (*Core, Device) {
	t.Helper()
	var core *Core
	if initial.policy == 0 && len(initial.keys) == 0 {
		core = NewUnconfiguredCore()
	} else {
		var err error
		core, err = NewCore(initial)
		if err != nil {
			t.Fatal(err)
		}
	}
	device, err := NewDevice("action-remote", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(DeviceConnected(device)); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(RouteAvailable(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(ScopeAvailable(true)); err != nil {
		t.Fatal(err)
	}
	return core, device
}

func actionTestPress(t *testing.T, device Device, button string, id uint64) PressToken {
	t.Helper()
	press, err := NewPressToken(device, button, id)
	if err != nil {
		t.Fatal(err)
	}
	return press
}

func actionTestStep(t *testing.T, core *Core, event Event) []Effect {
	t.Helper()
	effects, err := core.Step(event)
	if err != nil {
		t.Fatal(err)
	}
	return effects
}

func actionTestKeyTransitions(effects []Effect) []string {
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

func actionTestTaps(effects []Effect) [][]Key {
	var got [][]Key
	for _, effect := range effects {
		if tap, ok := effect.(TargetTapEffect); ok {
			got = append(got, tap.Keys())
		}
	}
	return got
}

func actionTestHasSourceEffect(effects []Effect) bool {
	for _, effect := range effects {
		switch effect.(type) {
		case PrepareSourceEffect, StopSourceEffect, OpenGateEffect, CloseGateEffect, DisposeResourceEffect:
			return true
		}
	}
	return false
}

func actionTestBlockedReason(effects []Effect) (BlockReason, bool) {
	for _, effect := range effects {
		if blocked, ok := effect.(BlockedEffect); ok {
			return blocked.Reason, true
		}
	}
	return 0, false
}

func TestNewActionPlanFreezesSpecAndKeys(t *testing.T) {
	keys := []Key{"ctrl", "shift", "v"}
	spec := ActionSpec{
		ToolID: "tool-a", ActionID: "dictation", CandidateRaw: "Ctrl+Shift+V", Revision: 7,
		Scope: ActionGlobal, Trigger: ActionTap, Request: RequestToggle, ReleaseRequest: RequestNone,
	}
	plan, err := NewActionPlan(spec, keys...)
	if err != nil {
		t.Fatal(err)
	}
	keys[0] = "changed-input"
	spec.ToolID = "changed-spec"
	if got, want := plan.Spec(), (ActionSpec{
		ToolID: "tool-a", ActionID: "dictation", CandidateRaw: "Ctrl+Shift+V", Revision: 7,
		Scope: ActionGlobal, Trigger: ActionTap, Request: RequestToggle, ReleaseRequest: RequestNone,
	}); got != want {
		t.Fatalf("Spec() = %#v, want %#v", got, want)
	}
	returnedKeys := plan.Keys()
	returnedKeys[0] = "changed-return"
	if got, want := plan.Keys(), []Key{"ctrl", "shift", "v"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
}

func TestNewActionPlanRejectsInvalidContract(t *testing.T) {
	base := ActionSpec{
		ToolID: "tool-a", ActionID: "dictation", CandidateRaw: "Ctrl+Shift+V", Revision: 7,
		Scope: ActionGlobal, Trigger: ActionTap, Request: RequestToggle, ReleaseRequest: RequestNone,
	}
	cases := []struct {
		name string
		spec ActionSpec
	}{
		{name: "missing tool", spec: func() ActionSpec { s := base; s.ToolID = ""; return s }()},
		{name: "missing revision", spec: func() ActionSpec { s := base; s.Revision = 0; return s }()},
		{name: "unknown scope", spec: func() ActionSpec { s := base; s.Scope = "application-ish"; return s }()},
		{name: "tap without request", spec: func() ActionSpec { s := base; s.Request = RequestNone; return s }()},
		{name: "tap release request", spec: func() ActionSpec { s := base; s.ReleaseRequest = RequestFinish; return s }()},
		{name: "hold without start", spec: func() ActionSpec { s := base; s.Trigger = ActionHold; return s }()},
		{name: "hold cancel release", spec: func() ActionSpec {
			s := base
			s.Trigger = ActionHold
			s.Request = RequestStart
			s.ReleaseRequest = RequestCancel
			return s
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewActionPlan(tc.spec, "space"); err == nil {
				t.Fatal("NewActionPlan succeeded, want validation error")
			}
		})
	}
}

func TestTargetActionTapRunsOnceAndReleaseOnlyClosesLocalPress(t *testing.T) {
	core, device := actionTestReadyCore(t, Binding{})
	plan := actionTestPlan(t, ActionGlobal, ActionTap, RequestToggle, RequestNone, "ctrl", "space")
	press := actionTestPress(t, device, "voice-tool", 1)
	event := TargetActionBegan(press, plan)
	plan.keys[0] = "mutated-after-event"

	begin := actionTestStep(t, core, event)
	if got, want := actionTestTaps(begin), [][]Key{{"ctrl", "space"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tap begin = %v, want %v", got, want)
	}
	if len(actionTestKeyTransitions(begin)) != 0 || actionTestHasSourceEffect(begin) {
		t.Fatalf("tap action created a key lease or source effect: %#v", begin)
	}
	if snapshot := core.Snapshot(); snapshot.HasSession || snapshot.LastAction == nil || snapshot.LastAction.Status != actionStatusActive || snapshot.LastAction.Request != RequestToggle || snapshot.LastAction.TargetOutcome != actionOutcomeUnknown {
		t.Fatalf("tap action snapshot = %#v", snapshot)
	}

	duplicate := actionTestStep(t, core, TargetActionBegan(press, actionTestPlan(t, ActionGlobal, ActionTap, RequestCancel, RequestNone, "escape")))
	if len(duplicate) != 0 {
		t.Fatalf("duplicate token ran another action: %#v", duplicate)
	}
	release := actionTestStep(t, core, OrdinaryEnded(press))
	if len(release) != 0 || actionTestHasSourceEffect(release) {
		t.Fatalf("tap release emitted target or source effect: %#v", release)
	}
	snapshot := core.Snapshot()
	if snapshot.LastAction == nil || snapshot.LastAction.Status != actionStatusReleased || snapshot.LastAction.Request != RequestToggle {
		t.Fatalf("tap release snapshot = %#v", snapshot.LastAction)
	}
	if len(actionTestTaps(release)) != 0 {
		t.Fatalf("tap release repeated target tap: %#v", release)
	}

	snapshot.LastAction.Status = "mutated-copy"
	snapshot.LastAction.Plan.keys[0] = "mutated-copy"
	copyCheck := core.Snapshot().LastAction
	if copyCheck.Status != actionStatusReleased || !reflect.DeepEqual(copyCheck.Plan.Keys(), []Key{"ctrl", "space"}) {
		t.Fatalf("mutating LastAction snapshot changed core state: %#v", copyCheck)
	}
}

func TestActionAppScopeIsBlockedWithoutOutput(t *testing.T) {
	core, device := actionTestReadyCore(t, Binding{})
	press := actionTestPress(t, device, "app-action", 1)
	plan := actionTestPlan(t, ActionApp, ActionTap, RequestStart, RequestNone, "ctrl", "shift", "v")
	effects := actionTestStep(t, core, TargetActionBegan(press, plan))
	if len(effects) != 1 {
		t.Fatalf("app-scoped action effects = %#v, want one block", effects)
	}
	reason, ok := actionTestBlockedReason(effects)
	if !ok || reason != BlockActionScopeUnsupported {
		t.Fatalf("block reason = %v, ok=%v; want unsupported app scope", reason, ok)
	}
	if len(actionTestKeyTransitions(effects)) != 0 || len(actionTestTaps(effects)) != 0 || actionTestHasSourceEffect(effects) || len(core.Snapshot().HeldKeys) != 0 {
		t.Fatalf("app-scoped action produced output or source effects: %#v", effects)
	}
	if got := actionTestStep(t, core, OrdinaryEnded(press)); len(got) != 0 {
		t.Fatalf("app-scoped physical release produced effects: %#v", got)
	}
}

func TestHoldActionFinishesOnlyOnNormalRelease(t *testing.T) {
	core, device := actionTestReadyCore(t, Binding{})
	press := actionTestPress(t, device, "hold-action", 1)
	plan := actionTestPlan(t, ActionGlobal, ActionHold, RequestStart, RequestFinish, "ctrl", "space")
	begin := actionTestStep(t, core, TargetActionBegan(press, plan))
	if got, want := actionTestKeyTransitions(begin), []string{"down:ctrl", "down:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hold begin = %v, want %v", got, want)
	}
	if len(actionTestTaps(begin)) != 0 || actionTestHasSourceEffect(begin) {
		t.Fatalf("hold begin used tap or source effect: %#v", begin)
	}
	if snapshot := core.Snapshot(); snapshot.LastAction == nil || snapshot.LastAction.Status != actionStatusActive || snapshot.LastAction.Request != RequestStart {
		t.Fatalf("hold begin snapshot = %#v", snapshot.LastAction)
	}

	end := actionTestStep(t, core, OrdinaryEnded(press))
	if got, want := actionTestKeyTransitions(end), []string{"up:space", "up:ctrl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hold release = %v, want %v", got, want)
	}
	if len(actionTestTaps(end)) != 0 || actionTestHasSourceEffect(end) {
		t.Fatalf("hold release emitted target tap or source effect: %#v", end)
	}
	if snapshot := core.Snapshot(); snapshot.LastAction == nil || snapshot.LastAction.Status != actionStatusReleased || snapshot.LastAction.Request != RequestFinish {
		t.Fatalf("normal hold completion snapshot = %#v", snapshot.LastAction)
	}
}

func TestActionTapConflictsWithLeasedAndUncertainKeys(t *testing.T) {
	t.Run("leased", func(t *testing.T) {
		core, device := actionTestReadyCore(t, Binding{})
		ordinary := actionTestPress(t, device, "ordinary", 1)
		if got, want := actionTestKeyTransitions(actionTestStep(t, core, OrdinaryBegan(ordinary, "command"))), []string{"down:command"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ordinary key begin = %v, want %v", got, want)
		}
		action := actionTestPress(t, device, "tap", 2)
		effects := actionTestStep(t, core, TargetActionBegan(action, actionTestPlan(t, ActionGlobal, ActionTap, RequestToggle, RequestNone, "ctrl", "k")))
		if reason, ok := actionTestBlockedReason(effects); !ok || reason != BlockActionConflictsWithHeldKey {
			t.Fatalf("tap block reason = %v, ok=%v", reason, ok)
		}
		if len(actionTestTaps(effects)) != 0 || len(actionTestKeyTransitions(effects)) != 0 {
			t.Fatalf("tap inherited an unrelated held modifier: %#v", effects)
		}
		actionTestStep(t, core, OrdinaryEnded(action))
		if got, want := actionTestKeyTransitions(actionTestStep(t, core, OrdinaryEnded(ordinary))), []string{"up:command"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ordinary key release = %v, want %v", got, want)
		}
	})

	t.Run("uncertain", func(t *testing.T) {
		core, device := actionTestReadyCore(t, Binding{})
		actionTestStep(t, core, OutputFailed(1, "space"))
		actionTestStep(t, core, OutputRecovered(1))
		action := actionTestPress(t, device, "tap", 1)
		effects := actionTestStep(t, core, TargetActionBegan(action, actionTestPlan(t, ActionGlobal, ActionTap, RequestToggle, RequestNone, "space")))
		if reason, ok := actionTestBlockedReason(effects); !ok || reason != BlockToggleRecoveryRequired {
			t.Fatalf("uncertain tap block reason = %v, ok=%v", reason, ok)
		}
		if len(actionTestTaps(effects)) != 0 || len(actionTestKeyTransitions(effects)) != 0 {
			t.Fatalf("tap overlapping uncertain key produced output: %#v", effects)
		}
	})
}

func TestActionHoldIsBlockedByAnyExistingKeyLease(t *testing.T) {
	core, device := actionTestReadyCore(t, Binding{})
	ordinary := actionTestPress(t, device, "ordinary", 1)
	action := actionTestPress(t, device, "hold", 2)
	if got, want := actionTestKeyTransitions(actionTestStep(t, core, OrdinaryBegan(ordinary, "command"))), []string{"down:command"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary begin = %v, want %v", got, want)
	}
	holdBegin := actionTestStep(t, core, TargetActionBegan(action, actionTestPlan(t, ActionGlobal, ActionHold, RequestStart, RequestFinish, "ctrl", "k")))
	if reason, ok := actionTestBlockedReason(holdBegin); !ok || reason != BlockActionConflictsWithHeldKey {
		t.Fatalf("hold block reason = %v, ok=%v", reason, ok)
	}
	if len(actionTestTaps(holdBegin)) != 0 || len(actionTestKeyTransitions(holdBegin)) != 0 {
		t.Fatalf("hold inherited an unrelated held modifier: %#v", holdBegin)
	}
	if got, want := actionTestKeyTransitions(actionTestStep(t, core, OrdinaryEnded(ordinary))), []string{"up:command"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary lease release = %v, want %v", got, want)
	}
	if got := actionTestStep(t, core, OrdinaryEnded(action)); len(got) != 0 {
		t.Fatalf("blocked hold release had effects: %#v", got)
	}
}

func TestActionLeaseBlocksFollowingOrdinaryAndVoiceBegins(t *testing.T) {
	binding, err := NewBinding(PolicyExactEdge, "voice-key")
	if err != nil {
		t.Fatal(err)
	}
	core, device := actionTestReadyCore(t, binding)
	action := actionTestPress(t, device, "hold", 1)
	begin := actionTestStep(t, core, TargetActionBegan(action, actionTestPlan(t, ActionGlobal, ActionHold, RequestStart, RequestFinish, "command")))
	if got, want := actionTestKeyTransitions(begin), []string{"down:command"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("action lease begin = %v, want %v", got, want)
	}

	ordinary := actionTestPress(t, device, "ordinary", 2)
	ordinaryEffects := actionTestStep(t, core, OrdinaryBegan(ordinary, "control", "k"))
	if reason, ok := actionTestBlockedReason(ordinaryEffects); !ok || reason != BlockActionConflictsWithHeldKey {
		t.Fatalf("ordinary block reason = %v, ok=%v", reason, ok)
	}
	if len(actionTestKeyTransitions(ordinaryEffects)) != 0 || len(actionTestTaps(ordinaryEffects)) != 0 {
		t.Fatalf("ordinary begin inherited action-held modifier: %#v", ordinaryEffects)
	}

	voice := actionTestPress(t, device, "voice", 3)
	voiceEffects := actionTestStep(t, core, VoiceBegan(voice))
	if reason, ok := actionTestBlockedReason(voiceEffects); !ok || reason != BlockActionConflictsWithHeldKey {
		t.Fatalf("voice block reason = %v, ok=%v", reason, ok)
	}
	if len(actionTestKeyTransitions(voiceEffects)) != 0 || len(actionTestTaps(voiceEffects)) != 0 || actionTestHasSourceEffect(voiceEffects) {
		t.Fatalf("voice begin inherited action-held modifier or prepared audio: %#v", voiceEffects)
	}
	if got := actionTestKeyTransitions(actionTestStep(t, core, OrdinaryEnded(action))); !reflect.DeepEqual(got, []string{"up:command"}) {
		t.Fatalf("action physical release = %v, want key-up", got)
	}
	if got := actionTestStep(t, core, OrdinaryEnded(ordinary)); len(got) != 0 {
		t.Fatalf("blocked ordinary release had effects: %#v", got)
	}
	if got := actionTestStep(t, core, VoiceEnded(voice)); len(got) != 0 {
		t.Fatalf("blocked voice release had effects: %#v", got)
	}
}

func TestCancelToolActionsInterruptsWithoutTargetCancel(t *testing.T) {
	core, device := actionTestReadyCore(t, Binding{})
	press := actionTestPress(t, device, "hold", 1)
	plan := actionTestPlan(t, ActionGlobal, ActionHold, RequestStart, RequestFinish, "ctrl", "space")
	begin := actionTestStep(t, core, TargetActionBegan(press, plan))
	if len(actionTestKeyTransitions(begin)) != 2 {
		t.Fatalf("hold begin lacked local key leases: %#v", begin)
	}
	cancel := actionTestStep(t, core, CancelToolActions("tool-a"))
	if got, want := actionTestKeyTransitions(cancel), []string{"up:space", "up:ctrl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cancel local releases = %v, want %v", got, want)
	}
	if len(actionTestTaps(cancel)) != 0 || actionTestHasSourceEffect(cancel) {
		t.Fatalf("cancel guessed target/source effect: %#v", cancel)
	}
	if snapshot := core.Snapshot(); snapshot.LastAction == nil || snapshot.LastAction.Status != actionStatusInterrupted || snapshot.LastAction.Request != RequestNone || snapshot.LastAction.TargetOutcome != actionOutcomeUnknown || !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{press}) {
		t.Fatalf("cancel snapshot or physical press ledger = %#v", snapshot)
	}
	if got := actionTestStep(t, core, OrdinaryEnded(press)); len(got) != 0 {
		t.Fatalf("physical up repeated cancellation key-up: %#v", got)
	}
	if snapshot := core.Snapshot(); snapshot.LastAction.Status != actionStatusInterrupted || snapshot.LastAction.Request != RequestNone {
		t.Fatalf("interrupted action reported a finish on physical up: %#v", snapshot.LastAction)
	}
}

func TestActionTapIsInterruptedByOutputFailureAndScopeLoss(t *testing.T) {
	t.Run("output failure", func(t *testing.T) {
		core, device := actionTestReadyCore(t, Binding{})
		press := actionTestPress(t, device, "tap", 1)
		begin := actionTestStep(t, core, TargetActionBegan(press, actionTestPlan(t, ActionGlobal, ActionTap, RequestToggle, RequestNone, "ctrl", "space")))
		if len(actionTestTaps(begin)) != 1 {
			t.Fatalf("tap begin = %#v", begin)
		}
		failure := actionTestStep(t, core, OutputFailed(1, "ctrl", "space"))
		if len(actionTestKeyTransitions(failure)) != 0 || len(actionTestTaps(failure)) != 0 || actionTestHasSourceEffect(failure) {
			t.Fatalf("output failure sent compensating action: %#v", failure)
		}
		if snapshot := core.Snapshot(); snapshot.LastAction.Status != actionStatusInterrupted || snapshot.LastAction.Request != RequestNone || !reflect.DeepEqual(snapshot.RecoveryKeys, []Key{"ctrl", "space"}) {
			t.Fatalf("tap failure snapshot = %#v", snapshot)
		}
		if got := actionTestStep(t, core, OrdinaryEnded(press)); len(got) != 0 {
			t.Fatalf("tap physical up repeated target request: %#v", got)
		}
		actionTestStep(t, core, OutputRecovered(1))
	})

	t.Run("scope loss", func(t *testing.T) {
		core, device := actionTestReadyCore(t, Binding{})
		press := actionTestPress(t, device, "tap", 1)
		begin := actionTestStep(t, core, TargetActionBegan(press, actionTestPlan(t, ActionGlobal, ActionTap, RequestToggle, RequestNone, "space")))
		if len(actionTestTaps(begin)) != 1 {
			t.Fatalf("tap begin = %#v", begin)
		}
		if got := actionTestStep(t, core, ScopeAvailable(false)); len(got) != 0 {
			t.Fatalf("scope loss emitted compensating target action: %#v", got)
		}
		if snapshot := core.Snapshot(); snapshot.LastAction.Status != actionStatusInterrupted || snapshot.LastAction.Request != RequestNone {
			t.Fatalf("scope-loss tap snapshot = %#v", snapshot.LastAction)
		}
		if got := actionTestStep(t, core, OrdinaryEnded(press)); len(got) != 0 {
			t.Fatalf("release after scope loss repeated request: %#v", got)
		}
	})
}

func TestActionHoldOutputFailureReleasesOnlyLocalKeys(t *testing.T) {
	core, device := actionTestReadyCore(t, Binding{})
	press := actionTestPress(t, device, "hold", 1)
	begin := actionTestStep(t, core, TargetActionBegan(press, actionTestPlan(t, ActionGlobal, ActionHold, RequestStart, RequestFinish, "command", "space")))
	if got, want := actionTestKeyTransitions(begin), []string{"down:command", "down:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hold begin = %v, want %v", got, want)
	}

	failure := actionTestStep(t, core, OutputFailed(1))
	if got, want := actionTestKeyTransitions(failure), []string{"up:space", "up:command"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed hold cleanup = %v, want %v", got, want)
	}
	if len(actionTestTaps(failure)) != 0 || actionTestHasSourceEffect(failure) {
		t.Fatalf("failed hold guessed a target/source action: %#v", failure)
	}
	if snapshot := core.Snapshot(); snapshot.LastAction == nil || snapshot.LastAction.Status != actionStatusInterrupted || snapshot.LastAction.Request != RequestNone || snapshot.OutputFailureID != 1 || !reflect.DeepEqual(snapshot.PressedButtons, []PressToken{press}) {
		t.Fatalf("failed hold snapshot = %#v", snapshot)
	}
	if got := actionTestStep(t, core, OrdinaryEnded(press)); len(got) != 0 {
		t.Fatalf("physical release after sink cleanup repeated effects: %#v", got)
	}
	actionTestStep(t, core, OutputRecovered(1))
}

func TestActionDisconnectReleasesHeldKeysWithoutFinishingTarget(t *testing.T) {
	core, device := actionTestReadyCore(t, Binding{})
	press := actionTestPress(t, device, "hold", 1)
	begin := actionTestStep(t, core, TargetActionBegan(press, actionTestPlan(t, ActionGlobal, ActionHold, RequestStart, RequestFinish, "space")))
	if !reflect.DeepEqual(actionTestKeyTransitions(begin), []string{"down:space"}) {
		t.Fatalf("hold begin = %#v", begin)
	}
	disconnect := actionTestStep(t, core, DeviceDisconnected(device))
	if got, want := actionTestKeyTransitions(disconnect), []string{"up:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("disconnect cleanup = %v, want %v", got, want)
	}
	if len(actionTestTaps(disconnect)) != 0 || actionTestHasSourceEffect(disconnect) {
		t.Fatalf("disconnect guessed target or source action: %#v", disconnect)
	}
	if snapshot := core.Snapshot(); snapshot.LastAction.Status != actionStatusInterrupted || snapshot.LastAction.Request != RequestNone || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("disconnect action snapshot = %#v", snapshot)
	}
}

func TestActionRespectsActiveVoiceToggleChord(t *testing.T) {
	binding, err := NewBinding(PolicyToggle, "ctrl", "space")
	if err != nil {
		t.Fatal(err)
	}
	core, device := actionTestReadyCore(t, binding)
	voice := actionTestPress(t, device, "voice", 1)
	voiceBegin := actionTestStep(t, core, VoiceBegan(voice))
	if len(actionTestTaps(voiceBegin)) != 1 || !core.Snapshot().HasSession {
		t.Fatalf("toggle voice session did not start: %#v", voiceBegin)
	}
	action := actionTestPress(t, device, "hold", 2)
	blocked := actionTestStep(t, core, TargetActionBegan(action, actionTestPlan(t, ActionGlobal, ActionHold, RequestStart, RequestFinish, "space")))
	if reason, ok := actionTestBlockedReason(blocked); !ok || reason != BlockActionConflictsWithToggle {
		t.Fatalf("action/voice toggle conflict = %v, ok=%v", reason, ok)
	}
	if len(actionTestKeyTransitions(blocked)) != 0 || len(actionTestTaps(blocked)) != 0 || actionTestHasSourceEffect(blocked) {
		t.Fatalf("overlapping action bypassed toggle conflict: %#v", blocked)
	}
	if snapshot := core.Snapshot(); !snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("blocked action changed voice state: %#v", snapshot)
	}
}
