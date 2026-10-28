package main

import (
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/voicetools"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestToolSelectionPersistsIdentityRatherThanOnlyShortcut(t *testing.T) {
	s, path := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+Space"))
	item := findToolMenuItem(s.mappingMenu(toolShortcutsTestButton).Items(), "听写  "+shortcutLabel("Control+Space"))
	if item == nil || item.Disabled || item.Click == nil {
		t.Fatal("missing selectable fixture action")
	}
	item.Click(item, nil)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"tool_actions"`, `"tool_id": "voice-typeless"`, `"action_id": "dictationMode"`, `"candidate_raw": "Ctrl+Space"`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("selected tool identity lost: missing %s in %s", field, data)
		}
	}
}

func TestActionReleaseUsesFrozenCandidateAfterRefresh(t *testing.T) {
	s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
	s.tools[0].snapshot.Actions[0].Control = &voicetools.Control{Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish}
	ref := voicetools.ActionRef{ToolID: "voice-typeless", ActionID: "dictationMode", CandidateRaw: "Ctrl+K"}
	s.saveToolAction(toolShortcutsTestButton, ref)
	sink := &toolShortcutsTestSink{}
	if err := s.coordinator.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	device, _ := bridge.NewDevice("test", 1)
	press, _ := bridge.NewPressToken(device, toolShortcutsTestButton, 1)
	if _, err := s.coordinator.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}}}); err != nil {
		t.Fatal(err)
	}
	s.tools[0].sequence++
	s.tools[0].snapshot = toolShortcutsTestSnapshot("Ctrl+L")
	if _, err := s.coordinator.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeUp, Press: press}}}); err != nil {
		t.Fatal(err)
	}
	want := []bridge.Effect{bridge.KeyDownEffect{Key: "Control"}, bridge.KeyDownEffect{Key: "K"}, bridge.KeyUpEffect{Key: "K"}, bridge.KeyUpEffect{Key: "Control"}}
	if !reflect.DeepEqual(sink.effects, want) {
		t.Fatalf("release used changed candidate: %#v", sink.effects)
	}
	// A new press cannot reuse the removed raw candidate.
	press, _ = bridge.NewPressToken(device, toolShortcutsTestButton, 2)
	if _, err := s.coordinator.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}, {Kind: devices.EdgeUp, Press: press}}}); err != nil {
		t.Fatal(err)
	}
	if len(sink.effects) != 4 {
		t.Fatal("stale reference silently adopted replacement")
	}
}

func TestSwitchingVoiceToolCancelsHeldActionUntilExplicitRebind(t *testing.T) {
	oldSnapshot := toolShortcutsTestSnapshot("Ctrl+K")
	oldSnapshot.Actions[0].Control = &voicetools.Control{Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish}
	s, mappingPath := newToolShortcutsTestShell(t, oldSnapshot)
	oldTool := s.tools[0]

	newSnapshot := toolShortcutsTestSnapshot("Ctrl+L")
	newSnapshot.Actions[0].Control = &voicetools.Control{Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish}
	newTool := &voiceTool{id: "voice-other", name: "Other", active: true, sequence: 9, snapshot: newSnapshot}
	s.tools = append(s.tools, newTool)
	s.toolPreferencesPath = mappingPath + ".voice-tools.json"
	s.toolPreferences = toolPreferences{Version: 1, Enabled: []string{oldTool.id, newTool.id}, Current: oldTool.id}

	oldRef := voicetools.ActionRef{ToolID: oldTool.id, ActionID: "dictationMode", CandidateRaw: "Ctrl+K"}
	newRef := voicetools.ActionRef{ToolID: newTool.id, ActionID: "dictationMode", CandidateRaw: "Ctrl+L"}
	s.saveToolAction(toolShortcutsTestButton, oldRef)

	sink := &toolShortcutsTestSink{}
	if err := s.coordinator.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("tool-switch-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(edges ...devices.Edge) {
		t.Helper()
		accepted, err := s.coordinator.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: edges})
		if err != nil || !accepted {
			t.Fatalf("input edges rejected: accepted=%v err=%v", accepted, err)
		}
	}
	press, err := bridge.NewPressToken(device, toolShortcutsTestButton, 1)
	if err != nil {
		t.Fatal(err)
	}
	deliver(devices.Edge{Kind: devices.EdgeDown, Press: press})
	if want := []bridge.Effect{bridge.KeyDownEffect{Key: "Control"}, bridge.KeyDownEffect{Key: "K"}}; !reflect.DeepEqual(sink.effects, want) {
		t.Fatalf("old tool hold did not start: %#v", sink.effects)
	}

	if err := s.selectVoiceTool(newTool); err != nil {
		t.Fatal(err)
	}
	if s.currentVoiceTool != newTool.id {
		t.Fatalf("current tool = %q, want %q", s.currentVoiceTool, newTool.id)
	}
	cancelled := []bridge.Effect{
		bridge.KeyDownEffect{Key: "Control"}, bridge.KeyDownEffect{Key: "K"},
		bridge.KeyUpEffect{Key: "K"}, bridge.KeyUpEffect{Key: "Control"},
	}
	if !reflect.DeepEqual(sink.effects, cancelled) {
		t.Fatalf("switch did not release old hold: %#v", sink.effects)
	}
	deliver(devices.Edge{Kind: devices.EdgeUp, Press: press})
	if !reflect.DeepEqual(sink.effects, cancelled) {
		t.Fatalf("late physical release repeated old action output: %#v", sink.effects)
	}

	stalePress, err := bridge.NewPressToken(device, toolShortcutsTestButton, 2)
	if err != nil {
		t.Fatal(err)
	}
	deliver(devices.Edge{Kind: devices.EdgeDown, Press: stalePress}, devices.Edge{Kind: devices.EdgeUp, Press: stalePress})
	if !reflect.DeepEqual(sink.effects, cancelled) {
		t.Fatalf("old button binding started after tool switch: %#v", sink.effects)
	}

	s.saveToolAction(toolShortcutsTestButton, newRef)
	newPress, err := bridge.NewPressToken(device, toolShortcutsTestButton, 3)
	if err != nil {
		t.Fatal(err)
	}
	deliver(devices.Edge{Kind: devices.EdgeDown, Press: newPress}, devices.Edge{Kind: devices.EdgeUp, Press: newPress})
	want := []bridge.Effect{
		bridge.KeyDownEffect{Key: "Control"}, bridge.KeyDownEffect{Key: "K"},
		bridge.KeyUpEffect{Key: "K"}, bridge.KeyUpEffect{Key: "Control"},
		bridge.KeyDownEffect{Key: "Control"}, bridge.KeyDownEffect{Key: "L"},
		bridge.KeyUpEffect{Key: "L"}, bridge.KeyUpEffect{Key: "Control"},
	}
	if !reflect.DeepEqual(sink.effects, want) {
		t.Fatalf("explicit new tool binding did not run its action: %#v", sink.effects)
	}
}

func TestToolActionUnavailableDoesNotFallBackToOrdinaryKeys(t *testing.T) {
	for _, reason := range []string{"disabled", "busy", "not-current", "not-ready", "app"} {
		t.Run(reason, func(t *testing.T) {
			s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
			s.saveToolAction(toolShortcutsTestButton, voicetools.ActionRef{ToolID: "voice-typeless", ActionID: "dictationMode", CandidateRaw: "Ctrl+K"})
			switch reason {
			case "disabled":
				s.tools[0].active = false
			case "busy":
				s.tools[0].busy = true
			case "not-current":
				s.currentVoiceTool = "another"
			case "not-ready":
				s.tools[0].snapshot.State = voicetools.ReadError
			case "app":
				s.tools[0].snapshot.Actions[0].Scope = "app"
			}
			// Even accidental stale ordinary keys must not shadow an action binding.
			s.coordinator.mappings = map[string][]bridge.Key{toolShortcutsTestButton: {"Return"}}
			sink := &toolShortcutsTestSink{}
			if err := s.coordinator.attachOutput(sink); err != nil {
				t.Fatal(err)
			}
			device, _ := bridge.NewDevice("test", 1)
			press, _ := bridge.NewPressToken(device, toolShortcutsTestButton, 1)
			if _, err := s.coordinator.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}, {Kind: devices.EdgeUp, Press: press}}}); err != nil {
				t.Fatal(err)
			}
			if len(sink.effects) != 0 || s.coordinator.report().Pressed != 0 {
				t.Fatalf("unavailable action emitted effects: %#v", sink.effects)
			}
		})
	}
}

func TestCoreBlockedActionIsNotSentToKeySink(t *testing.T) {
	d := newDeviceCoordinator()
	sink := &toolShortcutsTestSink{}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	device, _ := bridge.NewDevice("test", 1)
	if err := d.step(bridge.DeviceConnected(device)); err != nil {
		t.Fatal(err)
	}
	p1, _ := bridge.NewPressToken(device, "one", 1)
	p2, _ := bridge.NewPressToken(device, "two", 2)
	if err := d.step(bridge.OrdinaryBegan(p1, "Control", "K")); err != nil {
		t.Fatal(err)
	}
	plan, err := bridge.NewActionPlan(bridge.ActionSpec{ToolID: "tool", ActionID: "action", CandidateRaw: "Control+K", Revision: 1, Scope: bridge.ActionGlobal, Trigger: bridge.ActionTap, Request: bridge.RequestToggle, ReleaseRequest: bridge.RequestNone}, "Control", "K")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.step(bridge.TargetActionBegan(p2, plan)); err != nil {
		t.Fatal(err)
	}
	if len(sink.effects) != 2 || d.report().OutputFailureID != 0 {
		t.Fatalf("blocked decision reached sink: %#v", sink.effects)
	}
	if err = d.disconnect(); err != nil {
		t.Fatal(err)
	}
	if len(sink.effects) != 4 {
		t.Fatalf("ordinary cleanup lost: %#v", sink.effects)
	}
}

func TestUnavailableActionAloneCannotEnableOutput(t *testing.T) {
	s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
	s.saveToolAction(toolShortcutsTestButton, voicetools.ActionRef{ToolID: "voice-typeless", ActionID: "dictationMode", CandidateRaw: "Ctrl+K"})
	if !s.coordinator.hasExecutableMapping() {
		t.Fatal("available action cannot enable output")
	}
	s.tools[0].busy = true
	if s.coordinator.hasExecutableMapping() {
		t.Fatal("unavailable action permits permission request")
	}
}

func TestAmbiguousActionCannotAppearSelectable(t *testing.T) {
	snapshot := toolShortcutsTestSnapshot("Ctrl+K")
	snapshot.Actions = append(snapshot.Actions, snapshot.Actions[0])
	s, _ := newToolShortcutsTestShell(t, snapshot)
	menu := s.toolShortcutMenuItem(s.tools[0], legacyXiaomiProfileID, toolShortcutsTestButton)
	for _, item := range menu.Submenu {
		if item.Click != nil && !item.Disabled {
			t.Fatal("ambiguous action is selectable")
		}
	}
}
