package main

import (
	"errors"
	"mimac.local/mimac/internal/devices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
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
	if got := permissionGuideAccessibilityAccess(false, errors.New("native query unavailable")); got != permissionGuideUnknown {
		t.Fatalf("Accessibility query error mapped to %v", got)
	}
	if got := permissionGuideAccessibilityAccess(false, nil); got != permissionGuideNotGranted {
		t.Fatalf("Accessibility missing grant mapped to %v", got)
	}
	if got := permissionGuideAccessibilityAccess(true, nil); got != permissionGuideGranted {
		t.Fatalf("Accessibility grant mapped to %v", got)
	}
	if got := permissionGuideBluetoothAccess(permissionguide.BluetoothAccessNotDetermined); got != permissionGuideNotDetermined {
		t.Fatalf("Bluetooth notDetermined mapped to %v", got)
	}
	if got := permissionGuideBluetoothAccess(permissionguide.BluetoothAccessUnknown); got != permissionGuideUnknown {
		t.Fatalf("Bluetooth API error mapped to %v", got)
	}
}

func TestPermissionGuideUnknownInputOffersSettingsAndExplicitRequestForUndetermined(t *testing.T) {
	unknown := permissionGuideActionLabel(permissionGuideRow{id: permissionGuideInput, access: permissionGuideUnknown})
	if unknown != "打开设置" {
		t.Fatalf("unknown query action = %q, want direct settings", unknown)
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
	if noRows >= twoRows || twoRows >= 474 || threeRows != 474 {
		t.Fatalf("heights no/two/three = %d/%d/%d", noRows, twoRows, threeRows)
	}
}

func TestPermissionGuideCompanionBoundsAlignsTopAndStaysVisible(t *testing.T) {
	current := mygo.Rectangle{X: 40, Y: 20, Width: 500, Height: 369}
	settings := permissionguide.Rect{X: 400, Y: 80, Width: 600, Height: 700}
	workArea := mygo.Rectangle{X: 0, Y: 0, Width: 1600, Height: 900}
	got, ok := permissionGuideCompanionBounds(settings, workArea, current)
	if !ok || got.X != 1016 || got.Y != 80 || got.Width != current.Width || got.Height != current.Height {
		t.Fatalf("companion bounds = %#v, %t", got, ok)
	}
	tooSmall := mygo.Rectangle{X: 0, Y: 0, Width: 1000, Height: 900}
	if got, ok := permissionGuideCompanionBounds(permissionguide.Rect{X: 100, Y: 40, Width: 800, Height: 700}, tooSmall, current); !ok || got.X != 500 || got.Y != 40 {
		t.Fatalf("guide should clamp to the work area when neither side fits: %v %t", got, ok)
	}
	if got, ok := permissionGuideCompanionBounds(permissionguide.Rect{X: 700, Y: 80, Width: 300, Height: 600}, mygo.Rectangle{Width: 1800, Height: 900}, current); !ok || got.X != 1016 {
		t.Fatalf("when both sides fit, prefer right independently of old position: %v %t", got, ok)
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
		{"input settings list", permissionGuideInput, permissionGuideNotDetermined, permissionGuideNotDetermined, "settings"},
		{"denied input", permissionGuideInput, permissionGuideDenied, permissionGuideDenied, "settings"},
		{"post event settings list", permissionGuidePostEvent, permissionGuideNotGranted, permissionGuideNotGranted, "settings"},
		{"native bluetooth grant succeeds", permissionGuideBluetooth, permissionGuideNotDetermined, permissionGuideGranted, "request,refresh"},
		{"query unknown", permissionGuideInput, permissionGuideUnknown, permissionGuideNotGranted, "settings"},
		{"native bluetooth query becomes unknown", permissionGuideBluetooth, permissionGuideNotDetermined, permissionGuideUnknown, "request,refresh"},
		{"already granted", permissionGuideInput, permissionGuideGranted, permissionGuideGranted, ""},
		{"restricted", permissionGuideInput, permissionGuideRestricted, permissionGuideRestricted, ""},
		{"bluetooth prompt pending", permissionGuideBluetooth, permissionGuideNotDetermined, permissionGuideNotDetermined, "request,refresh"},
		{"bluetooth denied", permissionGuideBluetooth, permissionGuideDenied, permissionGuideDenied, "settings"},
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

func TestPermissionGuideUnreadableAccessOffersDirectSettings(t *testing.T) {
	if got := permissionGuideActionLabel(permissionGuideRow{id: permissionGuideInput, access: permissionGuideUnknown}); got != "打开设置" {
		t.Fatalf("unreadable access action=%q, want direct settings", got)
	}
}

func TestPermissionGuideListPermissionsOpenSettingsWithoutAnExtraPrompt(t *testing.T) {
	for _, id := range []permissionGuideID{permissionGuideInput, permissionGuidePostEvent} {
		for _, access := range []permissionGuideAccess{permissionGuideNotDetermined, permissionGuideNotGranted, permissionGuideDenied, permissionGuideUnknown} {
			opened := false
			runPermissionGuideGrant(id, access, func(permissionGuideID) {
				t.Fatalf("list permission %d unexpectedly requested a second system prompt", id)
			}, func() permissionGuideAccess {
				t.Fatalf("list permission %d unexpectedly used a native-request refresh", id)
				return permissionGuideUnknown
			}, func(got permissionGuideID) {
				if got != id {
					t.Fatal("wrong settings target")
				}
				opened = true
			})
			if !opened {
				t.Fatalf("list permission %d did not open its settings target", id)
			}
		}
	}
}

func TestPermissionGuidePresentationOnlyRoutesAnExistingSettingsApp(t *testing.T) {
	for _, test := range []struct {
		name             string
		running, enabled bool
		rows             []permissionGuideRow
		want             permissionGuideID
	}{
		{name: "closed", enabled: true, rows: []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideDenied}}},
		{name: "existing", running: true, enabled: true, rows: []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideDenied}}, want: permissionGuideInput},
		{name: "skip granted", running: true, enabled: true, rows: []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideGranted}, {id: permissionGuidePostEvent, access: permissionGuideNotGranted}}, want: permissionGuidePostEvent},
		{name: "no bluetooth prompt", running: true, enabled: true, rows: []permissionGuideRow{{id: permissionGuideBluetooth, access: permissionGuideNotDetermined}}},
		{name: "restricted", running: true, enabled: true, rows: []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideRestricted}}},
		{name: "disabled", running: true, rows: []permissionGuideRow{{id: permissionGuideInput, access: permissionGuideDenied}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var opened permissionGuideID
			joins := 0
			runPermissionGuidePresentation(permissionGuideSnapshot{actionsEnabled: test.enabled, rows: test.rows}, test.running, func(id permissionGuideID) { opened = id }, func() { joins++ })
			if opened != test.want {
				t.Errorf("opened=%v, want%v", opened, test.want)
			}
			if test.want == 0 && joins != 1 || test.want != 0 && joins != 0 {
				t.Errorf("duplicate or missing join: %d", joins)
			}
		})
	}
}
