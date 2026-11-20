package main

import "testing"

func TestPermissionGuideRepairTextAppearsOnlyForProvenPendingGrant(t *testing.T) {
	id := permissionGuideInput
	s := &shell{}
	snapshot := permissionGuideSnapshot{
		actionsEnabled: true,
		rows:           []permissionGuideRow{{id: id, access: permissionGuideDenied}},
	}
	for _, test := range []struct {
		name    string
		attempt *permissionGuideRepairAttempt
		access  permissionGuideAccess
		want    bool
	}{
		{"first visit or cancelled drop", nil, permissionGuideDenied, false},
		{"accepted drop but no proof", &permissionGuideRepairAttempt{id: id}, permissionGuideDenied, false},
		{"stale record", &permissionGuideRepairAttempt{id: id, stale: true}, permissionGuideDenied, true},
		{"stale record from another service", &permissionGuideRepairAttempt{id: permissionGuidePostEvent, stale: true}, permissionGuideDenied, false},
		{"granted after drop", &permissionGuideRepairAttempt{id: id, stale: true}, permissionGuideGranted, false},
		{"grant not yet asked", &permissionGuideRepairAttempt{id: id, stale: true}, permissionGuideNotDetermined, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s.repairAttempt = test.attempt
			snapshot.rows[0].access = test.access
			title, detail := s.permissionGuideRepairCopy(snapshot)
			if got := title != "" && detail != ""; got != test.want {
				t.Fatalf("repair text visible = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPermissionGuideRepairSurvivesAnUnresolvedRequest(t *testing.T) {
	id := permissionGuideInput
	snapshot := permissionGuideSnapshot{
		actionsEnabled: true,
		rows:           []permissionGuideRow{{id: id}},
	}
	for _, access := range []permissionGuideAccess{
		permissionGuideUnknown, permissionGuideNotDetermined, permissionGuideNotGranted, permissionGuideDenied,
	} {
		snapshot.rows[0].access = access
		if !permissionGuideRepairPending(snapshot, id) {
			t.Fatalf("lost accepted drop in transitional access state %v", access)
		}
	}
	for _, access := range []permissionGuideAccess{permissionGuideGranted, permissionGuideRestricted} {
		snapshot.rows[0].access = access
		if permissionGuideRepairPending(snapshot, id) {
			t.Fatalf("kept accepted drop in terminal access state %v", access)
		}
	}
}

func TestPermissionGuideResetRequiresOneProvenDropForTheStillMissingService(t *testing.T) {
	snapshot := permissionGuideSnapshot{actionsEnabled: true, rows: []permissionGuideRow{
		{id: permissionGuideInput, access: permissionGuideGranted},
		{id: permissionGuidePostEvent, access: permissionGuideNotGranted},
		{id: permissionGuideBluetooth, access: permissionGuideDenied},
	}}
	for _, test := range []struct {
		attempt permissionGuideRepairAttempt
		want    bool
	}{
		{permissionGuideRepairAttempt{id: permissionGuidePostEvent}, false},
		{permissionGuideRepairAttempt{id: permissionGuideInput, stale: true}, false},
		{permissionGuideRepairAttempt{id: permissionGuideBluetooth, stale: true}, false},
		{permissionGuideRepairAttempt{id: permissionGuidePostEvent, stale: true}, true},
	} {
		if got := test.attempt.takeReset(snapshot); got != test.want {
			t.Errorf("reset for %v with evidence=%t: got %t", test.attempt.id, test.attempt.stale, got)
		}
		if test.attempt.takeReset(snapshot) {
			t.Errorf("second reset for %v", test.attempt.id)
		}
	}
}
