package voicetools

import (
	"mimac.local/mimac/internal/bridge"
	"reflect"
	"testing"
)

func actionFixture() (ActionRef, Snapshot) {
	return ActionRef{ToolID: "tool", ActionID: "dictation", CandidateRaw: "Ctrl+K"}, Snapshot{State: Ready, Actions: []Action{{
		ID: "dictation", Scope: "global", Control: &Control{Trigger: bridge.ActionTap, Request: bridge.RequestToggle, ReleaseRequest: bridge.RequestNone}, Candidates: []Candidate{{Raw: "Ctrl+K", Shortcut: "Control+K", Supported: true}},
	}}}
}

func TestResolveActionRejectsUnavailableAndAmbiguousConfigurations(t *testing.T) {
	tests := map[string]func(*ActionRef, *Snapshot){
		"missing ref":           func(r *ActionRef, s *Snapshot) { r.ToolID = "" },
		"not ready":             func(r *ActionRef, s *Snapshot) { s.State = ConfigMissing },
		"app scope":             func(r *ActionRef, s *Snapshot) { s.Actions[0].Scope = "app" },
		"unknown scope":         func(r *ActionRef, s *Snapshot) { s.Actions[0].Scope = "unknown" },
		"disabled":              func(r *ActionRef, s *Snapshot) { s.Actions[0].Disabled = true },
		"absent control":        func(r *ActionRef, s *Snapshot) { s.Actions[0].Control = nil },
		"invalid control":       func(r *ActionRef, s *Snapshot) { s.Actions[0].Control.Trigger = "gesture" },
		"unsupported candidate": func(r *ActionRef, s *Snapshot) { s.Actions[0].Candidates[0].Supported = false },
		"invalid shortcut":      func(r *ActionRef, s *Snapshot) { s.Actions[0].Candidates[0].Shortcut = "Fn" },
		"changed candidate":     func(r *ActionRef, s *Snapshot) { s.Actions[0].Candidates[0].Raw = "Ctrl+L" },
		"ambiguous candidate": func(r *ActionRef, s *Snapshot) {
			s.Actions[0].Candidates = append(s.Actions[0].Candidates, s.Actions[0].Candidates[0])
		},
		"ambiguous action": func(r *ActionRef, s *Snapshot) { s.Actions = append(s.Actions, s.Actions[0]) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r, s := actionFixture()
			mutate(&r, &s)
			if _, err := ResolveAction(r, 7, s); err == nil {
				t.Fatal("unavailable action resolved")
			}
		})
	}
	r, s := actionFixture()
	if _, err := ResolveAction(r, 0, s); err == nil {
		t.Fatal("zero revision accepted")
	}
}

func TestResolveActionRequiresExplicitFnOptIn(t *testing.T) {
	ref, snapshot := actionFixture()
	snapshot.Actions[0].Control = &Control{
		Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish,
	}
	snapshot.Actions[0].Candidates[0].Raw = "ptt"
	snapshot.Actions[0].Candidates[0].Shortcut = "Fn"
	ref.CandidateRaw = "ptt"
	if _, err := ResolveAction(ref, 7, snapshot); err == nil {
		t.Fatal("Fn candidate resolved without Control.AllowFnKey")
	}
	snapshot.Actions[0].Control.AllowFnKey = true
	plan, err := ResolveAction(ref, 7, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Keys(), []bridge.Key{"Fn"}) || plan.Spec().Trigger != bridge.ActionHold ||
		plan.Spec().Request != bridge.RequestStart || plan.Spec().ReleaseRequest != bridge.RequestFinish {
		t.Fatalf("Fn PTT plan = %#v keys=%v", plan.Spec(), plan.Keys())
	}
}

func TestResolvedActionDoesNotAliasDiscoverySnapshot(t *testing.T) {
	r, s := actionFixture()
	plan, err := ResolveAction(r, 7, s)
	if err != nil {
		t.Fatal(err)
	}
	s.Actions[0].Control.Request = bridge.RequestCancel
	s.Actions[0].Candidates[0].Shortcut = "Control+L"
	s.Actions[0].Scope = "app"
	keys := plan.Keys()
	keys[0] = "Option"
	spec := plan.Spec()
	spec.ToolID = "other"
	if plan.Spec().ToolID != "tool" || plan.Spec().Revision != 7 || plan.Spec().Request != bridge.RequestToggle || !reflect.DeepEqual(plan.Keys(), []bridge.Key{"Control", "K"}) {
		t.Fatalf("plan mutated: %#v %v", plan.Spec(), plan.Keys())
	}
}
