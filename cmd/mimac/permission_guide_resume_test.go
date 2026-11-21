package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPermissionResumeRestoresOnlyTheRequestedRowRegardlessOfGrant(t *testing.T) {
	path := filepath.Join(t.TempDir(), permissionGuideResumeFile)
	for _, tc := range []struct {
		name   string
		access permissionGuideAccess
		want   bool
	}{
		{"new grant", permissionGuideGranted, true},
		{"still denied", permissionGuideDenied, true},
		{"unreadable state", permissionGuideUnknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := permissionGuideContinuation{id: permissionGuideInput}
			if err := savePermissionGuideResume(path, want); err != nil {
				t.Fatal(err)
			}
			restored, err := takePermissionGuideResume(path)
			if err != nil || restored != want {
				t.Fatalf("restored permission = %v, %v", restored, err)
			}
			snapshot := permissionGuideSnapshot{rows: []permissionGuideRow{
				{id: permissionGuidePostEvent, access: permissionGuideGranted},
				{id: permissionGuideInput, access: tc.access},
			}}
			if got := permissionGuideResumeTarget(restored, false, onboardingStepPermissions, snapshot); (got == permissionGuideInput) != tc.want {
				t.Fatalf("resume = %v, want %v", got, tc.want)
			}
			if permissionGuideCanRestore(restored.id, permissionGuideSnapshot{rows: snapshot.rows[:1]}) {
				t.Fatal("a continuation cannot target a capability that is no longer configured")
			}
			if repeated, err := takePermissionGuideResume(path); repeated.id != 0 || err != nil {
				t.Fatalf("consumed permission repeated: %v, %v", repeated, err)
			}
		})
	}
}

func TestPermissionResumeRestoresOnboardingOnlyWhilePermissionsArePending(t *testing.T) {
	path := filepath.Join(t.TempDir(), permissionGuideResumeFile)
	want := permissionGuideContinuation{id: permissionGuideInput, onboarding: true, finishVisit: true}
	if err := savePermissionGuideResume(path, want); err != nil {
		t.Fatal(err)
	}
	restored, err := takePermissionGuideResume(path)
	if err != nil || restored != want {
		t.Fatalf("restored onboarding = %v, %v", restored, err)
	}
	rows := []permissionGuideRow{
		{id: permissionGuideInput, access: permissionGuideDenied},
		{id: permissionGuidePostEvent, access: permissionGuideNotGranted},
	}
	snapshot := permissionGuideSnapshot{rows: rows}
	if !permissionGuideResumesOnboardingVisit(restored, true, onboardingStepPermissions, snapshot) {
		t.Fatal("restart lost the active first-use permission visit")
	}
	if permissionGuideResumesOnboardingVisit(restored, false, onboardingStepPermissions, snapshot) ||
		permissionGuideResumesOnboardingVisit(restored, true, onboardingStepDevice, snapshot) {
		t.Fatal("a recovered visit cannot finish outside the first-use permission step")
	}
	if got := permissionGuideResumeTarget(restored, true, onboardingStepPermissions, snapshot); got != permissionGuideInput {
		t.Fatalf("still pending input monitoring was not selected: %v", got)
	}
	if permissionGuideResumeTarget(restored, true, onboardingStepDevice, snapshot) != 0 ||
		permissionGuideResumeTarget(restored, false, onboardingStepPermissions, snapshot) != 0 {
		t.Fatal("a setup continuation escaped its original surface")
	}
	rows[0].access = permissionGuideGranted
	if got := permissionGuideResumeTarget(restored, true, onboardingStepPermissions, snapshot); got != permissionGuidePostEvent {
		t.Fatalf("the next missing permission was not selected: %v", got)
	}
	complete := permissionGuideSnapshot{rows: append([]permissionGuideRow(nil), rows...)}
	complete.rows[1].access = permissionGuideGranted
	state := onboardingState{
		permissionGuideOpened: true,
		permissionActionTaken: permissionGuideResumesOnboardingVisit(restored, true, onboardingStepPermissions, snapshot),
	}
	if closeGuide, finishVisit := state.completePermissionVisit(snapshot, complete); !closeGuide || !finishVisit {
		t.Fatalf("last grant after restart = close %t, finish %t", closeGuide, finishVisit)
	}
	rows[1].access = permissionGuideGranted
	if permissionGuideResumeTarget(restored, true, onboardingStepPermissions, snapshot) != 0 {
		t.Fatal("setup should retain its summary without reopening a completed guide")
	}
	if err := os.WriteFile(path, []byte("onboarding:input-monitoring@1795104400"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := takePermissionGuideResume(path); err != nil || !got.onboarding || got.finishVisit {
		t.Fatalf("legacy onboarding record changed its authorization intent: %v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("post-event"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := takePermissionGuideResume(path); got != (permissionGuideContinuation{id: permissionGuidePostEvent}) || err != nil {
		t.Fatalf("legacy ordinary settings token = %v, %v", got, err)
	}
}

func TestPermissionGuideResumeOnlyYieldsFirstFrameToLiveSettings(t *testing.T) {
	continuation := permissionGuideContinuation{id: permissionGuideInput, onboarding: true, finishVisit: true}
	partial := permissionGuideSnapshot{actionsEnabled: true, rows: []permissionGuideRow{
		{id: permissionGuideInput, access: permissionGuideGranted},
		{id: permissionGuidePostEvent, access: permissionGuideNotGranted},
	}}
	for _, tc := range []struct {
		name         string
		continuation permissionGuideContinuation
		step         onboardingStep
		control      permissionGuideAccess
		actions      bool
		settings     bool
		want         bool
		wantQuery    bool
	}{
		{"route second item", continuation, onboardingStepPermissions, permissionGuideNotGranted, true, true, true, true},
		{"settings closed", continuation, onboardingStepPermissions, permissionGuideNotGranted, true, false, false, true},
		{"both granted", continuation, onboardingStepPermissions, permissionGuideGranted, true, true, false, false},
		{"normal first use", permissionGuideContinuation{}, onboardingStepPermissions, permissionGuideNotGranted, true, true, false, false},
		{"general entry", permissionGuideContinuation{id: permissionGuideInput}, onboardingStepPermissions, permissionGuideNotGranted, true, true, false, false},
		{"different step", continuation, onboardingStepDevice, permissionGuideNotGranted, true, true, false, false},
		{"no route", continuation, onboardingStepPermissions, permissionGuideRestricted, true, true, false, false},
		{"disabled actions", continuation, onboardingStepPermissions, permissionGuideNotGranted, false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			partial.actionsEnabled = tc.actions
			partial.rows[1].access = tc.control
			queried := false
			got := permissionGuideResumesWithSettings(tc.continuation, tc.step, partial, func() bool {
				queried = true
				return tc.settings
			})
			if got != tc.want || queried != tc.wantQuery {
				t.Fatalf("inactive=%v queried Settings=%v, want %v and %v", got, queried, tc.want, tc.wantQuery)
			}
		})
	}
}

func TestPermissionResumeReopensAnExplicitReviewOnPermissions(t *testing.T) {
	completed := onboardingRecord{Version: onboardingRecordVersion, Started: true, Completed: true}
	if shouldPresentOnboarding(completed, true, true, nil) {
		t.Fatal("completed setup should not start again on ordinary launch")
	}
	continuation := permissionGuideContinuation{id: permissionGuideInput, onboarding: true, finishVisit: true}
	if step := onboardingInitialStep(completed, false, continuation); step != onboardingStepPermissions {
		t.Fatalf("relaunch after explicit permission visit opened step %v", step)
	}
	if step := onboardingInitialStep(completed, false, permissionGuideContinuation{id: permissionGuideInput}); step != onboardingStepWelcome {
		t.Fatalf("ordinary settings continuation sent setup to step %v", step)
	}
}
