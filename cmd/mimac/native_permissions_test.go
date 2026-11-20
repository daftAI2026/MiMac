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
	if len(rows) != 3 || rows[0].Title != input.title || rows[1].Title != control.title || rows[2].Title != bluetooth.title {
		t.Fatalf("unexpected app permission rows: %+v", rows)
	}
	if rows[0].ID != int32(input.id) || rows[1].ID != int32(control.id) || rows[2].ID != int32(bluetooth.id) {
		t.Fatalf("permissions did not keep their management targets: %+v", rows)
	}
	if !rows[0].Granted || rows[0].Status != "已授权" || rows[1].Granted || rows[1].Status != "未授权" || rows[2].Granted || rows[2].Status != "无法读取" {
		t.Fatalf("permissions did not keep their individual system states: %+v", rows)
	}
	withoutAudio := generalPermissionRows(permissionGuideSnapshot{rows: []permissionGuideRow{input, control}})
	if len(withoutAudio) != 2 {
		t.Fatalf("Bluetooth is only shown when included in the capabilities snapshot: %+v", withoutAudio)
	}
}
