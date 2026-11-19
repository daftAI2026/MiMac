package main

import "testing"

func TestPermissionGuidePublishesCurrentSetupProjection(t *testing.T) {
	s := &shell{}
	missing := permissionGuideSnapshot{actionsEnabled: true, appBundlePath: "/test/MiMac Dev.app", rows: []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideNotGranted}}}
	if !s.updateOnboardingPermissions(missing) {
		t.Fatal("initial permission result not published")
	}
	granted := permissionGuideSnapshot{actionsEnabled: true, appBundlePath: missing.appBundlePath, rows: []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideGranted}}}
	if !s.updateOnboardingPermissions(granted) || s.onboarding.permissionSnapshot.rows[0].access != permissionGuideGranted {
		t.Fatal("setup kept an old authorization result")
	}
	if s.updateOnboardingPermissions(granted) {
		t.Fatal("unchanged result would redraw setup again")
	}
	revoked := permissionGuideSnapshot{actionsEnabled: true, appBundlePath: granted.appBundlePath, rows: []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideUnknown}}}
	if !s.updateOnboardingPermissions(revoked) || s.onboarding.permissionSnapshot.rows[0].access != permissionGuideUnknown {
		t.Fatal("query failure left setup showing its previous grant")
	}
}

func TestOnboardingFinishesPermissionVisitOnFinalGrant(t *testing.T) {
	partial := permissionGuideSnapshot{rows: []permissionGuideRow{
		{id: permissionGuideInput, access: permissionGuideGranted},
		{id: permissionGuidePostEvent, access: permissionGuideNotGranted},
	}}
	granted := permissionGuideSnapshot{rows: []permissionGuideRow{
		{id: permissionGuideInput, access: permissionGuideGranted},
		{id: permissionGuidePostEvent, access: permissionGuideGranted},
	}}
	for _, tc := range []struct {
		name       string
		onboarding bool
		action     bool
		from, to   permissionGuideSnapshot
		closeGuide bool
		quit       bool
	}{
		{"explicit first-use grant", true, true, partial, granted, true, true},
		{"recovered first-use guide", true, false, partial, granted, true, false},
		{"ordinary settings", false, false, partial, granted, false, false},
		{"already granted", true, true, granted, granted, false, false},
		{"still partial", true, true, partial, partial, false, false},
		{"required capability removed", true, true, partial, permissionGuideSnapshot{rows: granted.rows[:1]}, true, false},
		{"unavailable capabilities", true, true, partial, permissionGuideSnapshot{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := onboardingState{permissionGuideOpened: tc.onboarding, permissionActionTaken: tc.action}
			closed, quit := state.completePermissionVisit(tc.from, tc.to)
			if closed != tc.closeGuide || quit != tc.quit {
				t.Fatalf("completion = %v, %v; want %v, %v", closed, quit, tc.closeGuide, tc.quit)
			}
			if state.permissionActionTaken != (tc.action && !tc.closeGuide) {
				t.Fatal("completion must consume the explicit action only on final grant")
			}
		})
	}
}
