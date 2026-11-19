package main

import "testing"

func TestGeneralPermissionsProjectEachAppGrantIndependently(t *testing.T) {
	input := permissionGuideRowFor(permissionGuideInput)
	input.access = permissionGuideGranted
	control := permissionGuideRowFor(permissionGuidePostEvent)
	control.access = permissionGuideNotGranted
	bluetooth := permissionGuideRowFor(permissionGuideBluetooth)
	bluetooth.access = permissionGuideUnknown

	rows := generalPermissionRows(permissionGuideSnapshot{rows: []permissionGuideRow{input, control, bluetooth}})
	if len(rows) != 2 || rows[0].Title != input.title || rows[1].Title != control.title {
		t.Fatalf("unexpected app permission rows: %+v", rows)
	}
	if rows[0].ID != int32(input.id) || rows[1].ID != int32(control.id) {
		t.Fatalf("permissions did not keep their management targets: %+v", rows)
	}
	if !rows[0].Granted || rows[0].Status != "已授权" || rows[1].Granted || rows[1].Status != "未授权" {
		t.Fatalf("permissions did not keep their individual system states: %+v", rows)
	}
}
