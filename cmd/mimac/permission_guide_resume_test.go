package main

import (
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
			if err := savePermissionGuideResume(path, permissionGuideInput); err != nil {
				t.Fatal(err)
			}
			id, err := takePermissionGuideResume(path)
			if err != nil || id != permissionGuideInput {
				t.Fatalf("restored permission = %v, %v", id, err)
			}
			snapshot := permissionGuideSnapshot{rows: []permissionGuideRow{
				{id: permissionGuidePostEvent, access: permissionGuideGranted},
				{id: permissionGuideInput, access: tc.access},
			}}
			if got := permissionGuideCanRestore(id, snapshot); got != tc.want {
				t.Fatalf("resume = %v, want %v", got, tc.want)
			}
			if permissionGuideCanRestore(id, permissionGuideSnapshot{rows: snapshot.rows[:1]}) {
				t.Fatal("a continuation cannot target a capability that is no longer configured")
			}
			if repeated, err := takePermissionGuideResume(path); repeated != 0 || err != nil {
				t.Fatalf("consumed permission repeated: %v, %v", repeated, err)
			}
		})
	}
}
