package main

import (
	"path/filepath"
	"testing"
)

func TestPermissionResumeConsumesOnlyTheRequestedGrant(t *testing.T) {
	path := filepath.Join(t.TempDir(), permissionGuideResumeFile)
	for _, tc := range []struct {
		name   string
		access permissionGuideAccess
		want   bool
	}{
		{"new grant", permissionGuideGranted, true},
		{"cancelled grant", permissionGuideDenied, false},
		{"unreadable state", permissionGuideUnknown, false},
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
			if got := shouldResumePermissionGuide(id, snapshot); got != tc.want {
				t.Fatalf("resume = %v, want %v", got, tc.want)
			}
			if repeated, err := takePermissionGuideResume(path); repeated != 0 || err != nil {
				t.Fatalf("consumed permission repeated: %v, %v", repeated, err)
			}
		})
	}
}
