package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

const permissionGuideTraceProbeEvent permissionGuideTraceEvent = "diagnostic_probe"

func TestPermissionGuideTraceRecordUsesOnlyServiceAndAccessEnums(t *testing.T) {
	got := permissionGuideTraceRecord(
		time.Date(2026, 11, 23, 10, 11, 12, 123456789, time.FixedZone("test", 8*60*60)),
		42,
		permissionGuideTraceProbeEvent,
		permissionGuideInput,
		permissionGuideTraceWritten,
		permissionGuideDenied,
		permissionGuideGranted,
	)
	want := "permission_guide time=2026-11-23T02:11:12.123456789Z pid=42 event=diagnostic_probe service=ListenEvent result=written status=denied previous=granted"
	if got != want {
		t.Fatalf("trace record = %q, want %q", got, want)
	}
}

func TestPermissionGuideAccessTransitionsLogOnlyKnownStatusChanges(t *testing.T) {
	previous := permissionGuideSnapshot{rows: []permissionGuideRow{
		{id: permissionGuideInput, access: permissionGuideDenied},
		{id: permissionGuidePostEvent, access: permissionGuideUnknown},
		{id: permissionGuideBluetooth, access: permissionGuideNotDetermined},
	}}
	next := permissionGuideSnapshot{rows: []permissionGuideRow{
		{id: permissionGuideInput, access: permissionGuideGranted},
		{id: permissionGuidePostEvent, access: permissionGuideDenied},
		{id: permissionGuideBluetooth, access: permissionGuideNotDetermined},
	}}
	want := []permissionGuideAccessTransition{{id: permissionGuideInput, from: permissionGuideDenied, to: permissionGuideGranted}}
	if got := permissionGuideAccessTransitions(previous, next); !reflect.DeepEqual(got, want) {
		t.Fatalf("permission transitions = %+v, want %+v", got, want)
	}
	if got := permissionGuideAccessTransitions(next, next); len(got) != 0 {
		t.Fatalf("unchanged permission statuses produced transitions: %+v", got)
	}
}

func TestPermissionGuideTraceFileProbe(t *testing.T) {
	probeDir := os.Getenv("MIMAC_PERMISSION_GUIDE_TRACE_PROBE_DIR")
	if probeDir == "" {
		t.Skip("set MIMAC_PERMISSION_GUIDE_TRACE_PROBE_DIR to emit one synthetic user-log record")
	}
	dirInfo, err := os.Stat(probeDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm()&0077 != 0 {
		t.Fatalf("probe directory must already exist with private permissions: %v", err)
	}
	mygo.App.SetPath(mygo.PathUserData, probeDir)
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })
	permissionGuideTrace(
		permissionGuideTraceProbeEvent,
		permissionGuideInput,
		permissionGuideTraceWritten,
		permissionGuideDenied,
		permissionGuideGranted,
	)
	path, err := permissionGuideTraceFilePath()
	if err != nil {
		t.Fatalf("trace file path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "event=diagnostic_probe service=ListenEvent result=written status=denied previous=granted") {
		t.Fatalf("persistent trace did not contain the synthetic event: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("trace file must remain private: %v", err)
	}
}
