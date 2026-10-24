package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mimac.local/mimac/internal/pluginrt"
	"mimac.local/mimac/internal/voicetools"
)

func awaitToolCallback(t *testing.T, queue <-chan func()) func() {
	t.Helper()
	select {
	case callback := <-queue:
		return callback
	case <-time.After(2 * time.Second):
		t.Fatal("discovery completion missing")
		return nil
	}
}

func TestToolStopRejectsLateCompletionAndCancelsRead(t *testing.T) {
	started := make(chan context.Context, 1)
	finish := make(chan struct{})
	queue := make(chan func(), 2)
	tool := &voiceTool{active: true, discover: func(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
		started <- ctx
		<-finish
		return voicetools.Snapshot{State: voicetools.Ready}
	}}
	changed := 0
	tool.refresh(voicetools.Locator{}, func(f func()) { queue <- f }, func() { changed++ })
	ctx := <-started
	tool.stop()
	if ctx.Err() == nil {
		t.Fatal("stop did not cancel discovery")
	}
	close(finish)
	awaitToolCallback(t, queue)()
	if tool.active || tool.busy || tool.snapshot.State != "" || changed != 0 {
		t.Fatalf("late discovery resurrected disabled tool: %+v", tool)
	}
}

func TestToolRefreshIgnoresEarlierRead(t *testing.T) {
	queue := make(chan func(), 2)
	tool := &voiceTool{active: true, discover: func(context.Context, voicetools.Locator) voicetools.Snapshot {
		return voicetools.Snapshot{State: voicetools.ConfigMissing}
	}}
	changed := 0
	dispatch := func(f func()) { queue <- f }
	onChange := func() { changed++ }
	tool.refresh(voicetools.Locator{}, dispatch, onChange)
	old := awaitToolCallback(t, queue)
	tool.refresh(voicetools.Locator{}, dispatch, onChange)
	current := awaitToolCallback(t, queue)
	old()
	if !tool.busy || changed != 0 {
		t.Fatal("stale discovery completed current request")
	}
	current()
	if tool.busy || tool.snapshot.State != voicetools.ConfigMissing || changed != 1 {
		t.Fatal("current discovery missing")
	}
}

func TestToolPreferencesPersistActivationWithoutStartingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.json")
	tool := &voiceTool{id: "test-tool"}
	runtime, err := pluginrt.New(pluginrt.Manifest{ID: tool.id, Start: func() (pluginrt.StopFunc, error) {
		tool.active = true
		return func() error { tool.stop(); return nil }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := &shell{plugins: runtime, voiceToolsState: voiceToolsState{tools: []*voiceTool{tool}, toolPreferencesPath: path, toolPreferences: toolPreferences{Version: 1, Enabled: []string{"future-plugin"}}}}
	if err := s.setVoiceToolEnabled(tool, true); err != nil {
		t.Fatal(err)
	}
	stored, err := readToolPreferences(path)
	if err != nil || !reflect.DeepEqual(stored.Enabled, []string{"future-plugin", "test-tool"}) || !tool.active || s.output != nil {
		t.Fatalf("activation = %+v %v", stored, err)
	}
	if err := s.setVoiceToolEnabled(tool, false); err != nil {
		t.Fatal(err)
	}
	stored, err = readToolPreferences(path)
	if err != nil || !reflect.DeepEqual(stored.Enabled, []string{"future-plugin"}) || tool.active {
		t.Fatalf("deactivation = %+v %v", stored, err)
	}
}

func TestFailedToolPreferenceSaveDoesNotEnableTool(t *testing.T) {
	path := t.TempDir() // An existing directory cannot be replaced by rename.
	tool := &voiceTool{id: "test-tool"}
	runtime, err := pluginrt.New(pluginrt.Manifest{ID: tool.id, Start: func() (pluginrt.StopFunc, error) { t.Fatal("started after failed save"); return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	s := &shell{plugins: runtime, voiceToolsState: voiceToolsState{tools: []*voiceTool{tool}, toolPreferencesPath: path}}
	if s.setVoiceToolEnabled(tool, true) == nil || tool.active || runtime.Active(tool.id) {
		t.Fatal("failed save activated tool")
	}
}

func TestToolPreferencesRejectMalformedWithoutRewriting(t *testing.T) {
	for _, contents := range []string{`{"version":2,"enabled":[]}`, `{"version":1,"enabled":["a","a"]}`, `{"version":1,"enabled":[]} {}`, `{"version":1,"extra":true}`} {
		path := filepath.Join(t.TempDir(), "tools.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readToolPreferences(path); err == nil {
			t.Fatalf("accepted %s", contents)
		}
		after, _ := os.ReadFile(path)
		if string(after) != contents {
			t.Fatal("read modified preferences")
		}
	}
}

func TestCurrentVoiceToolIsExclusiveAndRequiresEnabledAdapter(t *testing.T) {
	first := &voiceTool{id: "first", active: true}
	second := &voiceTool{id: "second", active: true}
	disabled := &voiceTool{id: "disabled"}
	path := filepath.Join(t.TempDir(), "tools.json")
	s := &shell{voiceToolsState: voiceToolsState{tools: []*voiceTool{first, second, disabled}, toolPreferencesPath: path, toolPreferences: toolPreferences{Version: 1, Enabled: []string{first.id, second.id}}}}
	for _, tool := range []*voiceTool{first, second} {
		if err := s.selectVoiceTool(tool); err != nil {
			t.Fatal(err)
		}
		stored, err := readToolPreferences(path)
		if err != nil || stored.Current != tool.id || s.currentVoiceTool != tool.id {
			t.Fatalf("selected state: %+v %v", stored, err)
		}
	}
	if s.selectVoiceTool(disabled) == nil || s.currentVoiceTool != second.id {
		t.Fatal("disabled adapter became current")
	}
	if s.output != nil {
		t.Fatal("selecting current tool enabled key output")
	}
}

func TestDisablingCurrentAdapterClearsSelectionWithoutSelectingAnother(t *testing.T) {
	tool := &voiceTool{id: "first", active: true}
	other := &voiceTool{id: "second", active: true}
	runtime, err := pluginrt.New(pluginrt.Manifest{ID: tool.id, Start: func() (pluginrt.StopFunc, error) { return func() error { tool.stop(); return nil }, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Enable(tool.id); err != nil {
		t.Fatal(err)
	}
	s := &shell{plugins: runtime, voiceToolsState: voiceToolsState{tools: []*voiceTool{tool, other}, currentVoiceTool: tool.id, toolPreferencesPath: filepath.Join(t.TempDir(), "tools.json"), toolPreferences: toolPreferences{Version: 1, Enabled: []string{tool.id, other.id}, Current: tool.id}}}
	if err := s.setVoiceToolEnabled(tool, false); err != nil {
		t.Fatal(err)
	}
	stored, err := readToolPreferences(s.toolPreferencesPath)
	if err != nil || stored.Current != "" || s.currentVoiceTool != "" || tool.active || !other.active {
		t.Fatalf("disable did not clear only current target: %+v %v", stored, err)
	}
}

func TestCurrentToolSelectionFailureKeepsPreviousTarget(t *testing.T) {
	first := &voiceTool{id: "first", active: true}
	second := &voiceTool{id: "second", active: true}
	s := &shell{voiceToolsState: voiceToolsState{tools: []*voiceTool{first, second}, currentVoiceTool: first.id, toolPreferencesPath: t.TempDir(), toolPreferences: toolPreferences{Version: 1, Enabled: []string{first.id, second.id}, Current: first.id}}}
	if s.selectVoiceTool(second) == nil || s.currentVoiceTool != first.id {
		t.Fatal("failed save changed current target")
	}
}
