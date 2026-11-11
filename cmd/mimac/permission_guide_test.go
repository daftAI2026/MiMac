package main

import (
	"mimac.local/mimac/internal/devices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/platform/permissionguide"
)

func TestPermissionGuideUnknownNeverAppearsGranted(t *testing.T) {
	rows := permissionGuideRows(permissionGuideSnapshot{rows: []permissionGuideRow{
		{id: permissionGuideInput, title: "输入监控", access: permissionGuideUnknown},
		{id: permissionGuidePostEvent, title: "辅助功能", access: permissionGuideUnknown},
	}})
	for _, row := range rows {
		if row.access == permissionGuideGranted || permissionGuideAccessLabel(row.access) != "无法读取" {
			t.Fatalf("unknown %q was presented as %q", row.title, permissionGuideAccessLabel(row.access))
		}
	}
}

func TestPermissionGuideAccessProbesPreserveUnknownAndNotDetermined(t *testing.T) {
	if got := permissionGuideInputAccess(xiaomi.ListenAccessUnknown); got != permissionGuideUnknown {
		t.Fatalf("Input Monitoring API error mapped to %v", got)
	}
	if got := permissionGuideInputAccess(xiaomi.ListenAccessNotDetermined); got != permissionGuideNotDetermined {
		t.Fatalf("Input Monitoring first request mapped to %v", got)
	}
	if got := permissionGuideInputAccess(xiaomi.ListenAccessGranted); got != permissionGuideGranted {
		t.Fatalf("Input Monitoring grant mapped to %v", got)
	}
	if got := permissionGuideOutputAccess(keyoutput.PermissionStateUnknown); got != permissionGuideUnknown {
		t.Fatalf("Post Event query error mapped to %v", got)
	}
	if got := permissionGuideOutputAccess(keyoutput.PermissionStateNotGranted); got != permissionGuideNotGranted {
		t.Fatalf("Post Event missing grant mapped to %v", got)
	}
	if got := permissionGuideBluetoothAccess(permissionguide.BluetoothAccessNotDetermined); got != permissionGuideNotDetermined {
		t.Fatalf("Bluetooth notDetermined mapped to %v", got)
	}
	if got := permissionGuideBluetoothAccess(permissionguide.BluetoothAccessUnknown); got != permissionGuideUnknown {
		t.Fatalf("Bluetooth API error mapped to %v", got)
	}
}

func TestPermissionGuideUnknownInputOffersRetryAndExplicitRequestForUndetermined(t *testing.T) {
	unknown := permissionGuideActionLabel(permissionGuideRow{id: permissionGuideInput, access: permissionGuideUnknown})
	if unknown != "重试" {
		t.Fatalf("unknown query action = %q, want retry", unknown)
	}
	undetermined := permissionGuideActionLabel(permissionGuideRow{id: permissionGuideInput, access: permissionGuideNotDetermined})
	if undetermined != "授权…" {
		t.Fatalf("notDetermined action = %q, want explicit request", undetermined)
	}
}

func TestPermissionGuideCapabilityRowsFollowEnabledCapabilities(t *testing.T) {
	// The selected plugin is shown before pairing succeeds so a new user can
	// authorize the HID listener without starting or probing that device.
	beforePairing := permissionGuideCapabilityIDs(true, false, false)
	if len(beforePairing) != 1 || beforePairing[0] != permissionGuideInput {
		t.Fatalf("unpaired selected device rows = %v", beforePairing)
	}
	configured := permissionGuideCapabilityIDs(true, true, true)
	want := []permissionGuideID{permissionGuideInput, permissionGuidePostEvent, permissionGuideBluetooth}
	if len(configured) != len(want) {
		t.Fatalf("configured rows = %v, want %v", configured, want)
	}
	for index := range want {
		if configured[index] != want[index] {
			t.Fatalf("configured rows = %v, want %v", configured, want)
		}
	}
	withoutAudio := permissionGuideCapabilityIDs(true, true, false)
	if len(withoutAudio) != 2 || withoutAudio[1] != permissionGuidePostEvent {
		t.Fatalf("rows without selected audio route = %v", withoutAudio)
	}
}

func TestPermissionGuideRowsAreCopiedAndBluetoothCannotUseAppDrag(t *testing.T) {
	snapshot := permissionGuideSnapshot{rows: []permissionGuideRow{{id: permissionGuideInput}}}
	rows := permissionGuideRows(snapshot)
	rows[0].title = "changed"
	if snapshot.rows[0].title == "changed" {
		t.Fatal("permission rows share mutable storage")
	}
	if !permissionGuideAllowsAppDrag(permissionGuideInput) || !permissionGuideAllowsAppDrag(permissionGuidePostEvent) {
		t.Fatal("Input Monitoring and Assistive Access should accept the app bundle drag")
	}
	if permissionGuideAllowsAppDrag(permissionGuideBluetooth) {
		t.Fatal("Bluetooth must not show the app-drag prompt")
	}
}

func TestPermissionGuideBundleIdentityMustMatchPackagedAppName(t *testing.T) {
	cases := []struct {
		bundleID string
		name     string
		packaged bool
		want     bool
	}{
		{"dev.mimac.app", "MiMac", true, true},
		{"dev.mimac.app.dev", "MiMac Dev", true, true},
		{"dev.mimac.app", "MiMac Dev", true, false},
		{"dev.mimac.app.dev", "MiMac", true, false},
		{"dev.mimac.app.dev", "MiMac Dev", false, false},
		{"other.bundle", "MiMac", true, false},
	}
	for _, tc := range cases {
		if got := permissionGuideBundleIdentityAllowed(tc.bundleID, tc.name, tc.packaged); got != tc.want {
			t.Errorf("identity(%q, %q, %t) = %t, want %t", tc.bundleID, tc.name, tc.packaged, got, tc.want)
		}
	}
}

func TestPermissionGuideWindowHeightTracksRequiredRows(t *testing.T) {
	noRows := permissionGuideWindowHeight(permissionGuideSnapshot{})
	twoRows := permissionGuideWindowHeight(permissionGuideSnapshot{rows: make([]permissionGuideRow, 2)})
	threeRows := permissionGuideWindowHeight(permissionGuideSnapshot{rows: make([]permissionGuideRow, 3)})
	if noRows >= twoRows || twoRows >= 442 || threeRows != 442 {
		t.Fatalf("heights no/two/three = %d/%d/%d", noRows, twoRows, threeRows)
	}
}

func TestPermissionGuideCompanionBoundsUsesOnlyFreeWorkArea(t *testing.T) {
	current := mygo.Rectangle{X: 40, Y: 20, Width: 500, Height: 369}
	settings := permissionguide.Rect{X: 400, Y: 80, Width: 600, Height: 700}
	workArea := mygo.Rectangle{X: 0, Y: 0, Width: 1600, Height: 900}
	got, ok := permissionGuideCompanionBounds(settings, workArea, current)
	if !ok || got.X != 1016 || got.Y != 96 || got.Width != current.Width || got.Height != current.Height {
		t.Fatalf("companion bounds = %#v, %t", got, ok)
	}
	tooSmall := mygo.Rectangle{X: 0, Y: 0, Width: 1000, Height: 900}
	if _, ok := permissionGuideCompanionBounds(permissionguide.Rect{X: 100, Y: 40, Width: 800, Height: 700}, tooSmall, current); ok {
		t.Fatal("guide moved when Settings leaves no adjacent space")
	}
}

func TestPermissionGuideSelectedDeviceIncludesCaptureAndKeyOutputBeforeMappings(t *testing.T) {
	s := &shell{deviceProfiles: []devicePlugin{{ModuleID: "device", Profile: devices.Profile{ID: "selected"}}}, selectedProfile: "selected"}
	ids := s.requiredPermissionGuideIDs()
	if len(ids) != 2 || ids[0] != permissionGuideInput || ids[1] != permissionGuidePostEvent {
		t.Fatalf("initial device permissions = %v", ids)
	}
}

func TestPermissionGuideMissingAccessUsesOneGrantAction(t *testing.T) {
	for _, id := range []permissionGuideID{permissionGuideInput, permissionGuidePostEvent, permissionGuideBluetooth} {
		for _, access := range []permissionGuideAccess{permissionGuideNotDetermined, permissionGuideNotGranted, permissionGuideDenied} {
			if got := permissionGuideActionLabel(permissionGuideRow{id: id, access: access}); got != "授权…" {
				t.Errorf("id=%v access=%v label=%q, want 授权…", id, access, got)
			}
		}
	}
}

func TestPermissionGuideGrantCompletesOrOpensTheSameSettingsPath(t *testing.T) {
	cases := []struct {
		name          string
		id            permissionGuideID
		before, after permissionGuideAccess
		want          string
	}{
		{"first input request", permissionGuideInput, permissionGuideNotDetermined, permissionGuideNotDetermined, "request,refresh,settings"},
		{"denied input", permissionGuideInput, permissionGuideDenied, permissionGuideDenied, "refresh,settings"},
		{"post event remains missing", permissionGuidePostEvent, permissionGuideNotGranted, permissionGuideNotGranted, "request,refresh,settings"},
		{"native grant succeeds", permissionGuidePostEvent, permissionGuideNotGranted, permissionGuideGranted, "request,refresh"},
		{"query unknown", permissionGuideInput, permissionGuideUnknown, permissionGuideNotGranted, "refresh"},
		{"query becomes unknown", permissionGuideInput, permissionGuideNotDetermined, permissionGuideUnknown, "request,refresh"},
		{"already granted", permissionGuideInput, permissionGuideGranted, permissionGuideGranted, ""},
		{"restricted", permissionGuideInput, permissionGuideRestricted, permissionGuideRestricted, ""},
		{"bluetooth prompt pending", permissionGuideBluetooth, permissionGuideNotDetermined, permissionGuideNotDetermined, "request,refresh"},
		{"bluetooth denied", permissionGuideBluetooth, permissionGuideDenied, permissionGuideDenied, "refresh,settings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			runPermissionGuideGrant(tc.id, tc.before, func(id permissionGuideID) {
				if id != tc.id {
					t.Fatal("wrong request target")
				}
				events = append(events, "request")
			}, func() permissionGuideAccess { events = append(events, "refresh"); return tc.after }, func(id permissionGuideID) {
				if id != tc.id {
					t.Fatal("wrong settings target")
				}
				events = append(events, "settings")
			})
			if got := strings.Join(events, ","); got != tc.want {
				t.Fatalf("actions=%q want %q", got, tc.want)
			}
		})
	}
}

func TestPermissionGuideJoinRequiresAnExplicitSearchAndStableWindow(t *testing.T) {
	now := time.Unix(10, 0)
	sample := permissionguide.WindowSnapshot{ID: 40, Frame: permissionguide.Rect{X: 30, Y: 50, Width: 600, Height: 700}}
	s := permissionGuideState{}
	if ready, expired := s.permissionGuideSettingsJoinReady(sample, true, now); ready || !expired {
		t.Fatal("idle panel followed an unrelated settings window")
	}
	s.settingsSearchUntil = now.Add(permissionGuideSettingsSearchDuration)
	if ready, _ := s.permissionGuideSettingsJoinReady(sample, true, now); ready {
		t.Fatal("first observation treated as stable")
	}
	moved := sample
	moved.Frame.X++
	if ready, _ := s.permissionGuideSettingsJoinReady(moved, true, now.Add(100*time.Millisecond)); ready {
		t.Fatal("changed frame treated as stable")
	}
	replacement := moved
	replacement.ID++
	if ready, _ := s.permissionGuideSettingsJoinReady(replacement, true, now.Add(200*time.Millisecond)); ready {
		t.Fatal("different window with same frame treated as stable")
	}
	if ready, expired := s.permissionGuideSettingsJoinReady(replacement, true, now.Add(300*time.Millisecond)); !ready || expired {
		t.Fatal("two identical observations did not join")
	}
	s.followActive = true
	s.followWindowID = replacement.ID
	if ready, _ := s.permissionGuideSettingsJoinReady(moved, true, now.Add(400*time.Millisecond)); ready {
		t.Fatal("replacement window bypassed initial stability")
	}
	if ready, _ := s.permissionGuideSettingsJoinReady(moved, true, now.Add(500*time.Millisecond)); !ready {
		t.Fatal("stable replacement did not join")
	}
	s.followActive = false
	s.settingsOpenedFor = permissionGuideInput
	if ready, expired := s.permissionGuideSettingsJoinReady(moved, true, s.settingsSearchUntil); ready || !expired {
		t.Fatal("deadline allowed another join")
	}
	if s.settingsOpenedFor != 0 {
		t.Fatal("expired search can be rearmed by permission polling")
	}
	s.settingsSearchUntil = now.Add(permissionGuideSettingsSearchDuration)
	s.settingsCandidate = sample
	s.permissionGuideSettingsJoinReady(permissionguide.WindowSnapshot{}, false, now)
	if ready, _ := s.permissionGuideSettingsJoinReady(sample, true, now); ready {
		t.Fatal("missing observation preserved a stale candidate")
	}
}
