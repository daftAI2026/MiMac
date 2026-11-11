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
