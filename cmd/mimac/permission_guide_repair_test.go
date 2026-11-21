package main

import (
	"testing"
	"time"

	"mimac.local/mimac/internal/platform/permissionguide"
)

func TestPermissionGuideRepairTextAppearsOnlyAfterFailure(t *testing.T) {
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
		{"repair failed", &permissionGuideRepairAttempt{id: id, failed: true}, permissionGuideDenied, true},
		{"failure from another service", &permissionGuideRepairAttempt{id: permissionGuidePostEvent, failed: true}, permissionGuideDenied, false},
		{"granted after drop", &permissionGuideRepairAttempt{id: id, failed: true}, permissionGuideGranted, false},
		{"grant not yet asked", &permissionGuideRepairAttempt{id: id, failed: true}, permissionGuideNotDetermined, false},
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

func TestPermissionGuideRepairEligibilityIsLimitedToDeniedListRows(t *testing.T) {
	snapshot := permissionGuideSnapshot{actionsEnabled: true, rows: []permissionGuideRow{
		{id: permissionGuideInput, access: permissionGuideDenied},
		{id: permissionGuidePostEvent, access: permissionGuideNotGranted},
		{id: permissionGuideBluetooth, access: permissionGuideDenied},
	}}
	for _, test := range []struct {
		id   permissionGuideID
		want bool
	}{
		{permissionGuideInput, true},
		{permissionGuidePostEvent, true},
		{permissionGuideBluetooth, false},
	} {
		if got := permissionGuideMayRepair(snapshot, test.id); got != test.want {
			t.Errorf("may repair %v = %t, want %t", test.id, got, test.want)
		}
	}
	snapshot.rows[0].access = permissionGuideGranted
	if permissionGuideMayRepair(snapshot, permissionGuideInput) {
		t.Fatal("granted permission remained eligible for repair")
	}
}

func TestPermissionGuideRegistrationServiceMapping(t *testing.T) {
	for _, test := range []struct {
		id      permissionGuideID
		service permissionguide.RegistrationService
		ok      bool
	}{
		{permissionGuideInput, permissionguide.ListenRegistration, true},
		{permissionGuidePostEvent, permissionguide.AccessibilityRegistration, true},
		{permissionGuideBluetooth, "", false},
	} {
		service, ok := permissionGuideRegistrationService(test.id)
		if ok != test.ok || service != test.service {
			t.Errorf("registration service for %v = %q, %t; want %q, %t", test.id, service, ok, test.service, test.ok)
		}
	}
}

func TestDragRegistrationPreparationConsumesOnlyMatchingFreshProofOnce(t *testing.T) {
	const bundle = "/Applications/MiMac Dev.app"
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	newPreparation := func() *permissionGuideDragRegistrationPreparation {
		return &permissionGuideDragRegistrationPreparation{
			id: permissionGuideInput, bundle: bundle, generation: 7, preparedAt: now.Add(-time.Second),
			ready: true, proof: &permissionguide.DragRegistrationProof{},
		}
	}
	prep := newPreparation()
	prep.proof = nil // No stale identity was found; de-duplicate this probe briefly.
	if prep.reusable(now, permissionGuideInput, 7, bundle) == false {
		t.Fatal("matching preparation was not reusable")
	}
	prep.proof = &permissionguide.DragRegistrationProof{}
	proof, taken := prep.take(now, permissionGuideInput, 7, bundle)
	if !taken || proof == nil {
		t.Fatal("matching current preparation did not yield its proof")
	}
	if _, again := prep.take(now, permissionGuideInput, 7, bundle); again {
		t.Fatal("preparation yielded more than one proof")
	}

	for _, test := range []struct {
		name       string
		id         permissionGuideID
		generation uint64
		bundle     string
		at         time.Time
		ready      bool
	}{
		{"wrong service", permissionGuidePostEvent, 7, bundle, now, true},
		{"wrong generation", permissionGuideInput, 8, bundle, now, true},
		{"wrong bundle", permissionGuideInput, 7, "/Applications/Other.app", now, true},
		{"not ready", permissionGuideInput, 7, bundle, now, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := newPreparation()
			candidate.ready = test.ready
			if _, ok := candidate.take(test.at, test.id, test.generation, test.bundle); ok {
				t.Fatal("mismatched preparation was consumed")
			}
		})
	}

	expired := newPreparation()
	if _, ok := expired.take(now.Add(permissionGuideDragRegistrationPreparationTTL), permissionGuideInput, 7, bundle); !ok {
		t.Fatal("expired matching prep should be consumed as a no-op")
	}
	if expired.proof != nil {
		t.Fatal("expired proof remained available")
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

func TestGestureCanReceivePreflightBeforeMainCompletion(t *testing.T) {
	p := &permissionGuideDragRegistrationPreparation{result: make(chan *permissionguide.DragRegistrationProof, 1)}
	proof := &permissionguide.DragRegistrationProof{}
	p.result <- proof
	p.receive()
	if !p.ready || p.proof != proof {
		t.Fatal("gesture did not receive the published proof")
	}
	p.proof = nil // Already consumed by this gesture.
	p.receive()
	if p.proof != nil {
		t.Fatal("queued UI completion restored a consumed proof")
	}
}
