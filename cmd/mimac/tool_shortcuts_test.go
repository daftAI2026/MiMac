package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/typeless"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/voicetools"
)

const toolShortcutsTestButton = "hid:0007:0052"

func toolShortcutsTestProfile(id string) devices.Profile {
	return devices.Profile{
		ID:   id,
		Name: "Test Remote",
		Buttons: []devices.Button{
			{ID: toolShortcutsTestButton, Label: "上"},
		},
	}
}

func toolShortcutsTestSnapshot(values ...string) voicetools.Snapshot {
	snapshot := typeless.Snapshot{FeatureShortcutBindings: typeless.ShortcutBindings{Status: typeless.StatusPresent, DictationMode: typeless.ActionBinding{Status: typeless.StatusPresent, Values: values}}}
	result := voicetools.Snapshot{State: voicetools.Ready}
	for _, action := range typeless.Normalize(snapshot)[:3] {
		row := voicetools.Action{ID: string(action.ID), Name: action.Label, Scope: "global", Control: &voicetools.Control{Trigger: bridge.ActionTap, Request: bridge.RequestToggle, ReleaseRequest: bridge.RequestNone}}
		for _, candidate := range action.Candidates {
			row.Candidates = append(row.Candidates, voicetools.Candidate{Raw: candidate.Raw, Shortcut: candidate.Shortcut, Supported: candidate.Status == typeless.StatusPresent})
		}
		result.Actions = append(result.Actions, row)
	}
	return result
}

func newToolShortcutsTestShell(t *testing.T, snapshot voicetools.Snapshot) (*shell, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "MiMac", "mappings.json")
	profileID := legacyXiaomiProfileID
	return &shell{
		settingsState: settingsState{
			profile:     emptyProfile(),
			mappingPath: path,
		},
		voiceToolsState: voiceToolsState{
			currentVoiceTool: "voice-typeless",
			inputSource:      &fakeInputSourceController{current: "com.apple.keylayout.ABC"},
			tools:            []*voiceTool{{id: "voice-typeless", name: "Typeless", active: true, sequence: 7, snapshot: snapshot}},
		},
		selectedProfile: profileID,
		deviceProfiles:  []devicePlugin{{Profile: toolShortcutsTestProfile(profileID)}},
		coordinator:     newDeviceCoordinator(),
	}, path
}

func findToolMenuItem(items []*mygo.MenuItem, label string) *mygo.MenuItem {
	for _, item := range items {
		if item.Label == label {
			return item
		}
		if found := findToolMenuItem(item.Submenu, label); found != nil {
			return found
		}
	}
	return nil
}

func findToolMenuItemPrefix(items []*mygo.MenuItem, prefix string) *mygo.MenuItem {
	for _, item := range items {
		if strings.HasPrefix(item.Label, prefix) {
			return item
		}
		if found := findToolMenuItemPrefix(item.Submenu, prefix); found != nil {
			return found
		}
	}
	return nil
}

func requireNoToolShortcutImport(t *testing.T, s *shell, path string) {
	t.Helper()
	if _, exists := s.profile.Profiles[legacyXiaomiProfileID][toolShortcutsTestButton]; exists {
		t.Fatal("stale Typeless menu changed the in-memory mapping")
	}
	if _, exists := s.profile.ToolActions[legacyXiaomiProfileID][toolShortcutsTestButton]; exists {
		t.Fatal("stale menu changed action reference")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale Typeless menu wrote a mapping file: stat error=%v", err)
	}
}

func TestTypelessMenuBindsIdentityAndTapsOnce(t *testing.T) {
	s, path := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+Space"))
	sink := &toolShortcutsTestSink{}

	menu := s.mappingMenu(toolShortcutsTestButton)
	importMenu := findToolMenuItem(menu.Items(), "从 Typeless 选用")
	if importMenu == nil || len(importMenu.Submenu) == 0 {
		t.Fatal("mapping menu has no Typeless native submenu")
	}
	if first := importMenu.Submenu[0]; !first.Disabled || first.Label != "绑定工具动作" {
		t.Fatalf("Typeless submenu disclosure = %#v", first)
	}
	item := findToolMenuItem(importMenu.Submenu, "听写  "+shortcutLabel("Control+Space"))
	if item == nil || item.Disabled || item.Click == nil {
		t.Fatalf("supported Typeless candidate is not selectable: %#v", item)
	}
	item.Click(item, nil)

	if s.output != nil || s.coordinator.sink != nil {
		t.Fatal("selecting a copied shortcut enabled key output")
	}
	persisted, err := readProfile(path)
	if err != nil || persisted.ToolActions[legacyXiaomiProfileID][toolShortcutsTestButton] != (voicetools.ActionRef{ToolID: "voice-typeless", ActionID: "dictationMode", CandidateRaw: "Ctrl+Space"}) {
		t.Fatalf("copied shortcut was not persisted canonically: profile=%#v err=%v", persisted, err)
	}
	if _, exists := s.coordinator.mappings[toolShortcutsTestButton]; exists {
		t.Fatal("action downgraded to ordinary mapping")
	}

	if err := s.coordinator.attachOutput(sink); err != nil {
		t.Fatal(err)
	}

	device, err := bridge.NewDevice("test-remote", 1)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, toolShortcutsTestButton, 1)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.coordinator.accept(devices.InputEvent{
		State:  devices.InputStateConnected,
		Device: device,
		Edges:  []devices.Edge{{Kind: devices.EdgeDown, Press: press}},
	})
	if err != nil || !accepted {
		t.Fatalf("shortcut down: accepted=%v err=%v", accepted, err)
	}
	accepted, err = s.coordinator.accept(devices.InputEvent{
		State:  devices.InputStateConnected,
		Device: device,
		Edges:  []devices.Edge{{Kind: devices.EdgeUp, Press: press}},
	})
	if err != nil || !accepted {
		t.Fatalf("shortcut up: accepted=%v err=%v", accepted, err)
	}
	if len(sink.effects) != 1 {
		t.Fatalf("want one tap effect, got %#v", sink.effects)
	}
	tap, ok := sink.effects[0].(bridge.TargetTapEffect)
	if !ok || !reflect.DeepEqual(tap.Keys(), []bridge.Key{"Control", "Space"}) {
		t.Fatalf("incorrect tap: %#v", sink.effects)
	}

	if s.coordinator.core.Snapshot().HasSession || s.coordinator.report().Pressed != 0 {
		t.Fatalf("shortcut copy opened a voice session or left a press held: report=%+v", s.coordinator.report())
	}
}

func TestTypelessShortcutMenuDisablesUnsupportedAndUnsetEntries(t *testing.T) {
	snapshot := toolShortcutsTestSnapshot("Fn")

	s, path := newToolShortcutsTestShell(t, snapshot)
	menu := s.mappingMenu(toolShortcutsTestButton)
	importMenu := findToolMenuItem(menu.Items(), "从 Typeless 选用")
	if importMenu == nil {
		t.Fatal("mapping menu has no Typeless submenu")
	}
	unsupported := findToolMenuItemPrefix(importMenu.Submenu, "听写：暂不支持此快捷键")
	if unsupported == nil || !unsupported.Disabled || unsupported.Click != nil {
		t.Fatalf("unsupported raw candidate should be visibly disabled: %#v", unsupported)
	}
	if unset := findToolMenuItemPrefix(importMenu.Submenu, "Ask Anything：未设置快捷键"); unset == nil || !unset.Disabled {
		t.Fatalf("empty present action should have a disabled unset row: %#v", unset)
	}
	requireNoToolShortcutImport(t, s, path)
}

func TestTypelessShortcutMenuUnreadConfigurationNavigatesToVoiceSettings(t *testing.T) {
	s, _ := newToolShortcutsTestShell(t, voicetools.Snapshot{})
	s.tools[0].active = false
	menu := s.mappingMenu(toolShortcutsTestButton)
	read := findToolMenuItem(menu.Items(), "语音工具设置…")
	if read == nil || read.Disabled || read.Click == nil {
		t.Fatalf("unread configuration has no enabled read entry: %#v", read)
	}
	read.Click(read, nil)
	if s.page != pageVoice {
		t.Fatalf("read entry navigated to %q, want %q", s.page, pageVoice)
	}
}

func TestOldTypelessMenuCannotImportAfterReadOrDeviceStateChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*shell)
	}{
		{name: "current tool changed", mutate: func(s *shell) { s.currentVoiceTool = "another-tool" }},
		{name: "plugin disabled", mutate: func(s *shell) { s.tools[0].stop() }},
		{name: "last read failed", mutate: func(s *shell) { s.tools[0].snapshot.State = voicetools.ReadError }},
		{name: "scope became app", mutate: func(s *shell) { s.tools[0].snapshot.Actions[0].Scope = "app" }},
		{name: "scope became unknown", mutate: func(s *shell) { s.tools[0].snapshot.Actions[0].Scope = "unknown" }},
		{name: "action disabled", mutate: func(s *shell) { s.tools[0].snapshot.Actions[0].Disabled = true }},
		{name: "candidate unsupported", mutate: func(s *shell) { s.tools[0].snapshot.Actions[0].Candidates[0].Supported = false }},
		{
			name: "configuration reread",
			mutate: func(s *shell) {
				s.tools[0].sequence++
				s.tools[0].busy = true
			},
		},
		{
			name: "snapshot changed without sequence update",
			mutate: func(s *shell) {
				s.tools[0].snapshot = toolShortcutsTestSnapshot("Ctrl+V")
			},
		},
		{
			name: "device changed",
			mutate: func(s *shell) {
				other := toolShortcutsTestProfile("test.other-device")
				s.deviceProfiles = append(s.deviceProfiles, devicePlugin{Profile: other})
				s.selectedProfile = other.ID
			},
		},
		{
			name:   "mapping load failed",
			mutate: func(s *shell) { s.mappingLoadFailed = true },
		},
		{
			name:   "settings closing",
			mutate: func(s *shell) { s.closing = true },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, path := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
			menu := s.mappingMenu(toolShortcutsTestButton)
			item := findToolMenuItem(menu.Items(), "听写  "+shortcutLabel("Control+K"))
			if item == nil || item.Click == nil {
				t.Fatalf("test candidate missing: %#v", item)
			}
			test.mutate(s)
			item.Click(item, nil)
			requireNoToolShortcutImport(t, s, path)
		})
	}
}

type toolShortcutsTestSink struct {
	effects []bridge.Effect
}

func (s *toolShortcutsTestSink) Submit(effects []bridge.Effect) error {
	s.effects = append(s.effects, effects...)
	return nil
}

func (s *toolShortcutsTestSink) ReleaseAll() error  { return nil }
func (s *toolShortcutsTestSink) Close() error       { return nil }
func (s *toolShortcutsTestSink) Held() []bridge.Key { return nil }

func TestToolShortcutMenuBoundsCandidateCount(t *testing.T) {
	values := make([]string, 200)
	for i := range values {
		values[i] = fmt.Sprintf("Ctrl+F%d", i%12+1)
	}
	snapshot := toolShortcutsTestSnapshot(values...)
	for i := range snapshot.Actions[0].Candidates {
		snapshot.Actions[0].Candidates[i].Raw = fmt.Sprintf("candidate-%d", i)
	}
	s, _ := newToolShortcutsTestShell(t, snapshot)
	menu := s.toolShortcutMenuItem(s.tools[0], legacyXiaomiProfileID, toolShortcutsTestButton)
	selectable := 0
	for _, item := range menu.Submenu {
		if item.Click != nil {
			selectable++
		}
	}
	if selectable != maxToolShortcutCandidates {
		t.Fatalf("menu candidate count = %d", selectable)
	}
	if findToolMenuItem(menu.Submenu, "其余 192 个快捷键") == nil {
		t.Fatal("omitted candidates not indicated")
	}
}

func TestDisabledActionCannotOfferItsStoredCandidate(t *testing.T) {
	action := voicetools.Action{ID: "disabled", Name: "语音", Disabled: true, Candidates: []voicetools.Candidate{{Raw: "Control+K", Shortcut: "Control+K", Supported: true}}}
	if label := toolActionLabel(action); label != "已关闭快捷键" {
		t.Fatal(label)
	}
	tool := &voiceTool{name: "test", snapshot: voicetools.Snapshot{State: voicetools.Ready, Actions: []voicetools.Action{action}}}
	s := &shell{}
	item := s.toolShortcutMenuItem(tool, "", "方式")
	for _, entry := range item.Submenu {
		if entry.Click != nil && !entry.Disabled {
			t.Fatal("explicitly disabled action offered a shortcut")
		}
	}
}

func TestToolShortcutCopyRequiresGlobalScopeAndValidKey(t *testing.T) {
	for _, test := range []struct {
		name, scope, shortcut string
	}{
		{"application", "app", "Control+Shift+V"},
		{"unknown", "unknown", "Control+K"},
		{"missing", "", "Control+K"},
		{"unsupported key", "global", "Fn"},
		{"invalid order", "global", "K+Control"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := toolShortcutsTestSnapshot("Ctrl+K")
			snapshot.Actions = snapshot.Actions[:1]
			snapshot.Actions[0].Scope = test.scope
			snapshot.Actions[0].Candidates[0].Shortcut = test.shortcut
			snapshot.Actions[0].Candidates[0].Supported = true
			s, path := newToolShortcutsTestShell(t, snapshot)
			menu := s.toolShortcutMenuItem(s.tools[0], legacyXiaomiProfileID, toolShortcutsTestButton)
			for _, entry := range menu.Submenu {
				if entry.Click != nil && !entry.Disabled {
					t.Fatalf("non-global or invalid candidate offered by menu: %#v", entry)
				}
			}
			action := snapshot.Actions[0]
			candidate := action.Candidates[0]
			if s.toolCandidateIsCurrent(s.tools[0], legacyXiaomiProfileID, toolShortcutsTestButton, 7, action.ID, candidate.Raw, candidate.Shortcut) {
				t.Fatal("candidate passed click-time global copy check")
			}
			requireNoToolShortcutImport(t, s, path)
		})
	}
}

func TestFnShortcutRequiresActionControlOptIn(t *testing.T) {
	action := voicetools.Action{
		ID: "wetype-ptt", Name: "按住说话", Scope: "global", Gesture: "hold",
		Control: &voicetools.Control{
			Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish, AllowFnKey: true,
		},
		Candidates: []voicetools.Candidate{{Raw: "ptt", Shortcut: "Fn", Supported: true}},
	}
	candidate := action.Candidates[0]
	if !canBindToolAction(action, candidate) {
		t.Fatal("Fn candidate rejected despite explicit AllowFnKey opt-in")
	}

	withoutOptIn := action
	control := *action.Control
	control.AllowFnKey = false
	withoutOptIn.Control = &control
	if canBindToolAction(withoutOptIn, candidate) {
		t.Fatal("Fn candidate accepted without explicit AllowFnKey opt-in")
	}

	snapshot := voicetools.Snapshot{State: voicetools.Ready, Actions: []voicetools.Action{action}}
	s, _ := newToolShortcutsTestShell(t, snapshot)
	item := findToolMenuItem(s.toolShortcutMenuItem(s.tools[0], legacyXiaomiProfileID, toolShortcutsTestButton).Submenu, "按住说话  Fn")
	if item == nil || item.Disabled || item.Click == nil {
		t.Fatalf("Fn PTT menu entry is not selectable: %#v", item)
	}

	s.tools[0].snapshot.Actions[0].Control.AllowFnKey = false
	item = findToolMenuItemPrefix(s.toolShortcutMenuItem(s.tools[0], legacyXiaomiProfileID, toolShortcutsTestButton).Submenu, "按住说话：暂不支持此快捷键")
	if item == nil || !item.Disabled || item.Click != nil {
		t.Fatalf("Fn menu entry remained selectable without opt-in: %#v", item)
	}
}

func TestGestureTextAloneCannotAuthorizeToolRouting(t *testing.T) {
	snapshot := toolShortcutsTestSnapshot("Ctrl+K")
	snapshot.Actions[0].Gesture = "tap-to-start-stop"
	snapshot.Actions[0].Control = nil
	s, path := newToolShortcutsTestShell(t, snapshot)
	menu := s.toolShortcutMenuItem(s.tools[0], legacyXiaomiProfileID, toolShortcutsTestButton)
	for _, item := range menu.Submenu {
		if item.Click != nil && !item.Disabled {
			t.Fatal("gesture string offered executable action")
		}
	}
	requireNoToolShortcutImport(t, s, path)
}
