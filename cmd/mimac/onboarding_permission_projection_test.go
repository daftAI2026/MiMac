package main

import (
	"testing"
	"time"
)

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

func TestPermissionGuideRefreshPublishesOneSnapshotToBothCards(t *testing.T) {
	previous := permissionGuideSnapshot{
		actionsEnabled: true,
		appBundlePath:  "/test/MiMac Dev.app",
		rows:           []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideNotGranted}},
	}
	s := &shell{
		snapshot: previous,
		onboarding: onboardingState{
			step:               onboardingStepPermissions,
			permissionSnapshot: previous,
		},
	}
	next := permissionGuideSnapshot{
		actionsEnabled: true,
		appBundlePath:  previous.appBundlePath,
		rows:           []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideGranted}},
	}
	guideChanged, onboardingChanged := s.publishPermissionGuideSnapshot(next)
	if !guideChanged || !onboardingChanged {
		t.Fatalf("publication changed guide/onboarding = %t/%t, want true/true", guideChanged, onboardingChanged)
	}
	if !samePermissionGuideSnapshot(s.snapshot, s.onboarding.permissionSnapshot) ||
		!samePermissionGuideSnapshot(s.snapshot, next) {
		t.Fatal("the permission panel and main setup card received different native snapshots")
	}
	if guideChanged, onboardingChanged := s.publishPermissionGuideSnapshot(next); guideChanged || onboardingChanged {
		t.Fatalf("unchanged snapshot invalidation flags = %t/%t, want false/false", guideChanged, onboardingChanged)
	}
}

func TestPermissionGuideReadbackCadenceFallsBackAfterBoundedWindow(t *testing.T) {
	now := time.Unix(100, 0)
	pending := permissionGuideSnapshot{rows: []permissionGuideRow{{id: permissionGuideBluetooth, access: permissionGuideNotDetermined}}}
	s := &shell{snapshot: pending, followActive: true, readbackUntil: now.Add(permissionGuideReadbackDuration)}
	if got := s.permissionGuidePollIntervalAt(now); got != permissionGuideReadbackInterval {
		t.Fatalf("active Settings poll interval = %s, want %s", got, permissionGuideReadbackInterval)
	}
	if got := s.permissionGuidePollIntervalAt(now.Add(permissionGuideReadbackDuration + time.Nanosecond)); got != permissionGuidePollInterval {
		t.Fatalf("expired poll interval = %s, want %s", got, permissionGuidePollInterval)
	}
	s.snapshot.rows[0].access = permissionGuideGranted
	if got := s.permissionGuidePollIntervalAt(now); got != permissionGuidePollInterval {
		t.Fatalf("completed permission poll interval = %s, want %s", got, permissionGuidePollInterval)
	}
}

func TestPermissionGuideCanceledTimerTokenIsStale(t *testing.T) {
	if permissionGuideTimerCurrent(3, 4) {
		t.Fatal("a callback from a canceled timer generation was accepted")
	}
	if !permissionGuideTimerCurrent(4, 4) {
		t.Fatal("the current timer generation was rejected")
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
