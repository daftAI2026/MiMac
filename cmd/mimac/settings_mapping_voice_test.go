package main

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

const mappingVoiceSelectionTestWidth = 220
const mappingVoiceSelectionTestHeight = 40

func mappingVoiceSelectionTester(s *shell, active []*voiceTool) *ui.Tester {
	a := appAppearance(false, 0)
	s.navigationSymbols = map[symbolKey]*ui.Bitmap{{name: "chevron.up.chevron.down", dark: false, size: accessoryIconSize}: nil}
	return ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Fill().AlignItems(ui.Center).Children(func() {
			s.mappingVoiceSelection(c, a, active, mappingVoiceSelectionTestWidth, mappingVoiceSelectionTestHeight)
		})
	}, 320, 64)
}

func TestMappingVoiceSelectionOneAndSeveralEnabledUseSameNativeMenuButton(t *testing.T) {
	selected := &voiceTool{id: "voice-selected", name: "Selected Voice", active: true}
	other := &voiceTool{id: "voice-other", name: "Other Voice", active: true}
	one := &shell{voiceToolsState: voiceToolsState{tools: []*voiceTool{selected}, currentVoiceTool: selected.id}}
	many := &shell{voiceToolsState: voiceToolsState{tools: []*voiceTool{selected, other}, currentVoiceTool: selected.id}}
	oneView := mappingVoiceSelectionTester(one, one.tools)
	manyView := mappingVoiceSelectionTester(many, many.tools)

	oneRect, found := oneView.Find("选择语音工具")
	if !found {
		t.Fatal("one enabled tool did not render the native menu button")
	}
	manyRect, found := manyView.Find("选择语音工具")
	if !found {
		t.Fatal("multiple enabled tools did not render the native menu button")
	}
	if oneRect != manyRect || oneRect.W != mappingVoiceSelectionTestWidth || oneRect.H != mappingVoiceSelectionTestHeight {
		t.Fatalf("single and multiple menu button bounds differ: one=%+v many=%+v", oneRect, manyRect)
	}
	if !bytes.Equal(oneView.Image().Pix, manyView.Image().Pix) {
		t.Fatal("single and multiple tool selections changed the native menu button rendering")
	}

	for _, test := range []struct {
		view  *ui.Tester
		items []string
	}{
		{oneView, []string{"Selected Voice"}},
		{manyView, []string{"Selected Voice", "Other Voice"}},
	} {
		if err := test.view.Click("选择语音工具"); err != nil {
			t.Fatal(err)
		}
		if got := test.view.Menu(); !reflect.DeepEqual(got, test.items) {
			t.Fatalf("native voice menu items = %v, want %v", got, test.items)
		}
		if err := test.view.ChooseMenuItem(test.items[0]); err != nil {
			t.Fatal(err)
		}
	}
	if one.currentVoiceTool != selected.id || many.currentVoiceTool != selected.id {
		t.Fatal("choosing the checked current tool unexpectedly changed selection")
	}
}

func TestMappingVoiceSelectionNativeMenuChoosesWhenNothingIsSelected(t *testing.T) {
	first := &voiceTool{id: "voice-first", name: "First Voice", active: true}
	second := &voiceTool{id: "voice-second", name: "Second Voice", active: true}
	path := filepath.Join(t.TempDir(), "tools.json")
	s := &shell{voiceToolsState: voiceToolsState{
		tools: []*voiceTool{first, second}, toolPreferencesPath: path,
		toolPreferences: toolPreferences{Version: 1, Enabled: []string{first.id, second.id}},
	}}
	view := mappingVoiceSelectionTester(s, s.tools)
	if _, found := view.Find("选择语音工具"); !found {
		t.Fatal("active tools with no current selection did not render the native menu button")
	}
	if err := view.Click("选择语音工具"); err != nil {
		t.Fatal(err)
	}
	if got, want := view.Menu(), []string{first.name, second.name}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unselected menu items = %v, want %v", got, want)
	}
	if err := view.ChooseMenuItem(second.name); err != nil {
		t.Fatal(err)
	}
	if s.currentVoiceTool != second.id {
		t.Fatalf("menu choice selected %q, want %q", s.currentVoiceTool, second.id)
	}
	if saved, err := readToolPreferences(path); err != nil || saved.Current != second.id {
		t.Fatalf("menu choice was not persisted once: %+v, %v", saved, err)
	}
}

func TestMappingVoiceSelectionSoleInputSourceToolCanBeReselectedFromMenu(t *testing.T) {
	tool := testInputSourceTool("voice-input")
	selectChecks := 0
	required := tool.requiredInputSource
	tool.requiredInputSource = func(actionID string) (string, bool) {
		selectChecks++
		return required(actionID)
	}
	s := &shell{isolatedDevelopment: true, voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, currentVoiceTool: tool.id,
	}}
	view := mappingVoiceSelectionTester(s, s.tools)
	if err := view.Click("选择语音工具"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem(tool.name); err != nil {
		t.Fatal(err)
	}
	if selectChecks == 0 {
		t.Fatal("choosing the current sole tool did not take the explicit reselection path")
	}
	if s.currentVoiceTool != tool.id {
		t.Fatalf("reselection changed current tool to %q", s.currentVoiceTool)
	}
}

func TestMappingVoiceSelectionZeroEnabledKeepsToolPageEntry(t *testing.T) {
	inactive := &voiceTool{id: "voice-disabled", name: "Disabled Voice"}
	s := &shell{voiceToolsState: voiceToolsState{tools: []*voiceTool{inactive}}}
	view := mappingVoiceSelectionTester(s, nil)
	entry, found := view.Find("选择语音工具")
	if !found {
		t.Fatal("zero enabled tools lost the existing choose-tool page entry")
	}
	if entry.W != mappingVoiceSelectionTestWidth || entry.H != mappingVoiceSelectionTestHeight {
		t.Fatalf("zero-tool page entry bounds = %+v", entry)
	}
	if got := view.Menu(); len(got) != 0 {
		t.Fatalf("zero enabled tools rendered a menu: %v", got)
	}
	if s.currentVoiceTool != "" || s.page != "" {
		t.Fatalf("rendering zero-tool entry caused selection or navigation side effects: current=%q page=%q", s.currentVoiceTool, s.page)
	}
	if inactive.active {
		t.Fatalf("rendering zero-tool entry activated a tool: %+v", inactive)
	}
}
