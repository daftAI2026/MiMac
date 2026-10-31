package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/wetype"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/pluginrt"
	"mimac.local/mimac/internal/voicetools"
)

type fakeInputSourceController struct {
	current      string
	currentCalls int
	selectCalls  []string
	currentErr   error
	selectErr    error
	selectedAs   string
	events       *[]string
}

func (f *fakeInputSourceController) CurrentID() (string, error) {
	f.currentCalls++
	if f.currentErr != nil {
		return "", f.currentErr
	}
	return f.current, nil
}

func (f *fakeInputSourceController) SelectEnabledID(id string) error {
	f.selectCalls = append(f.selectCalls, id)
	if f.events != nil {
		*f.events = append(*f.events, "select:"+id)
	}
	if f.selectErr != nil {
		return f.selectErr
	}
	if f.selectedAs != "" {
		f.current = f.selectedAs
	} else {
		f.current = id
	}
	return nil
}

const testDoubaoSourceID = "com.bytedance.inputmethod.doubaoime.pinyin"

func testInputSourceTool(id string) *voiceTool {
	return &voiceTool{
		id:       id,
		name:     "Test input method",
		kind:     voiceInputMethod,
		active:   true,
		sequence: 1,
		requiredInputSource: func(actionID string) (string, bool) {
			if actionID == "dictation" {
				return testDoubaoSourceID, true
			}
			return "", false
		},
		snapshot: voicetools.Snapshot{
			State: voicetools.Ready,
			Actions: []voicetools.Action{{
				ID: "dictation", Scope: "global", Control: &voicetools.Control{Trigger: bridge.ActionTap, Request: bridge.RequestToggle, ReleaseRequest: bridge.RequestNone},
				Candidates: []voicetools.Candidate{{Raw: "Control+Space", Shortcut: "Control+Space", Supported: true}},
			}},
		},
	}
}

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

func beginQueuedVoiceToolDiscovery(t *testing.T, s *shell, tool *voiceTool, result voicetools.Snapshot) (chan struct{}, chan func()) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	queue := make(chan func(), 2)
	discover := func(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
		close(started)
		select {
		case <-release:
			return result
		case <-ctx.Done():
			return result
		}
	}
	s.refreshVoiceToolUsingWithDispatch(tool, discover, func(callback func()) { queue <- callback })
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery did not start")
	}
	return release, queue
}

func TestExplicitBusyToolSelectionEnsuresInputSourceAfterItsDiscovery(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(fmt.Sprintf("alreadyCurrent=%t", current), func(t *testing.T) {
			tool := testInputSourceTool("voice-doubao")
			currentID := "another-tool"
			if current {
				currentID = tool.id
			}
			controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
			s := &shell{voiceToolsState: voiceToolsState{
				tools: []*voiceTool{tool}, currentVoiceTool: currentID, inputSource: controller,
				toolPreferencesPath: filepath.Join(t.TempDir(), "tools.json"),
				toolPreferences:     toolPreferences{Version: 1, Enabled: []string{tool.id}, Current: currentID},
			}}
			release, queue := beginQueuedVoiceToolDiscovery(t, s, tool, tool.snapshot)
			if !tool.busy {
				t.Fatal("controlled discovery did not enter busy state")
			}
			if err := s.selectVoiceTool(tool); err != nil {
				t.Fatal(err)
			}
			if s.currentVoiceTool != tool.id || controller.current != "com.apple.keylayout.ABC" || len(controller.selectCalls) != 0 {
				t.Fatalf("busy selection changed TIS before discovery completed: current=%q source=%q calls=%v", s.currentVoiceTool, controller.current, controller.selectCalls)
			}
			close(release)
			completion := awaitToolCallback(t, queue)
			completion()
			completion() // A replay cannot consume the same explicit intent twice.
			if controller.current != testDoubaoSourceID || !reflect.DeepEqual(controller.selectCalls, []string{testDoubaoSourceID}) || controller.currentCalls != 2 {
				t.Fatalf("explicit selection was not fulfilled once after its discovery: source=%q reads=%d calls=%v", controller.current, controller.currentCalls, controller.selectCalls)
			}
			if s.pendingInputSource != nil {
				t.Fatal("fulfilled selection intent was retained")
			}
		})
	}
}

func TestBackgroundVoiceToolDiscoveryDoesNotSelectInputSource(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	s := &shell{voiceToolsState: voiceToolsState{tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller}}
	release, queue := beginQueuedVoiceToolDiscovery(t, s, tool, tool.snapshot)
	close(release)
	awaitToolCallback(t, queue)()
	if controller.current != "com.apple.keylayout.ABC" || controller.currentCalls != 0 || len(controller.selectCalls) != 0 {
		t.Fatalf("background discovery changed TIS: source=%q reads=%d calls=%v", controller.current, controller.currentCalls, controller.selectCalls)
	}
}

func TestSwitchingAwayDropsIntentAndExplicitReselectionQueuesNewOne(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	other := &voiceTool{id: "voice-other", active: true, snapshot: voicetools.Snapshot{State: voicetools.Ready}}
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	path := filepath.Join(t.TempDir(), "tools.json")
	s := &shell{voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool, other}, currentVoiceTool: other.id, inputSource: controller,
		toolPreferencesPath: path,
		toolPreferences:     toolPreferences{Version: 1, Enabled: []string{tool.id, other.id}, Current: other.id},
	}}
	release, queue := beginQueuedVoiceToolDiscovery(t, s, tool, tool.snapshot)
	if err := s.selectVoiceTool(tool); err != nil {
		t.Fatal(err)
	}
	firstPending := s.pendingInputSource
	if firstPending == nil {
		t.Fatal("explicit selection did not retain its in-flight intent")
	}
	if err := s.selectVoiceTool(other); err != nil {
		t.Fatal(err)
	}
	if s.pendingInputSource != nil {
		t.Fatal("switching tools retained the older input-source intent")
	}
	close(release)
	awaitToolCallback(t, queue)()
	if s.currentVoiceTool != other.id || controller.current != "com.apple.keylayout.ABC" || len(controller.selectCalls) != 0 {
		t.Fatalf("old completion changed TIS after a tool switch: current=%q source=%q calls=%v", s.currentVoiceTool, controller.current, controller.selectCalls)
	}

	secondRelease, secondQueue := beginQueuedVoiceToolDiscovery(t, s, tool, tool.snapshot)
	if err := s.selectVoiceTool(tool); err != nil {
		t.Fatal(err)
	}
	if s.pendingInputSource == nil || s.pendingInputSource == firstPending || s.pendingInputSource.sequence != tool.sequence {
		t.Fatalf("switching back did not create a fresh tool+sequence intent: first=%+v next=%+v sequence=%d", firstPending, s.pendingInputSource, tool.sequence)
	}
	close(secondRelease)
	awaitToolCallback(t, secondQueue)()
	if controller.current != testDoubaoSourceID || !reflect.DeepEqual(controller.selectCalls, []string{testDoubaoSourceID}) {
		t.Fatalf("fresh explicit reselection was not fulfilled: source=%q calls=%v", controller.current, controller.selectCalls)
	}
}

func TestDiscoveryRestartAndStopCannotRevivePendingSelection(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	s := &shell{voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, currentVoiceTool: "another-tool", inputSource: controller,
		toolPreferencesPath: filepath.Join(t.TempDir(), "tools.json"),
		toolPreferences:     toolPreferences{Version: 1, Enabled: []string{tool.id, "another-tool"}, Current: "another-tool"},
	}}
	queue := make(chan func(), 4)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	firstResult := tool.snapshot
	firstDiscover := func(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
		close(firstStarted)
		select {
		case <-firstRelease:
		case <-ctx.Done():
		}
		return firstResult
	}
	s.refreshVoiceToolUsingWithDispatch(tool, firstDiscover, func(callback func()) { queue <- callback })
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first discovery did not start")
	}
	if err := s.selectVoiceTool(tool); err != nil {
		t.Fatal(err)
	}

	secondStarted := make(chan struct{})
	secondRelease := make(chan struct{})
	secondDiscover := func(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
		close(secondStarted)
		select {
		case <-secondRelease:
		case <-ctx.Done():
		}
		return firstResult
	}
	s.refreshVoiceToolUsingWithDispatch(tool, secondDiscover, func(callback func()) { queue <- callback })
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("restarted discovery did not start")
	}
	oldCompletion := awaitToolCallback(t, queue)
	oldCompletion()
	close(secondRelease)
	awaitToolCallback(t, queue)()
	if controller.current != "com.apple.keylayout.ABC" || len(controller.selectCalls) != 0 || s.pendingInputSource != nil {
		t.Fatalf("restart revived the earlier sequence intent: source=%q calls=%v pending=%+v", controller.current, controller.selectCalls, s.pendingInputSource)
	}

	thirdRelease, thirdQueue := beginQueuedVoiceToolDiscovery(t, s, tool, firstResult)
	if err := s.selectVoiceTool(tool); err != nil {
		t.Fatal(err)
	}
	tool.stop()
	close(thirdRelease)
	awaitToolCallback(t, thirdQueue)()
	tool.active = true
	fourthRelease, fourthQueue := beginQueuedVoiceToolDiscovery(t, s, tool, firstResult)
	close(fourthRelease)
	awaitToolCallback(t, fourthQueue)()
	if controller.current != "com.apple.keylayout.ABC" || len(controller.selectCalls) != 0 || s.pendingInputSource != nil {
		t.Fatalf("stopped tool completion changed TIS: source=%q calls=%v", controller.current, controller.selectCalls)
	}
}

func TestBusyCleanupAndQuitDiscardPendingInputSourceSelection(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*shell)
		wantStatus string
	}{
		{name: "route transition busy", prepare: func(s *shell) { s.remoteState.cleanupInFlight = true }, wantStatus: inputSourceDeferredToolError},
		{name: "audio quit pending", prepare: func(s *shell) { s.audioQuitPending = true }, wantStatus: inputSourceDeferredToolError},
		{name: "cleanup failed", prepare: func(s *shell) { s.audioSession = &audioSessionHost{}; s.report.AudioCleanupFailed = true }, wantStatus: inputSourceCleanupFailedToolError},
		{name: "quit", prepare: func(s *shell) { s.closing = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := testInputSourceTool("voice-doubao")
			controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
			s := &shell{voiceToolsState: voiceToolsState{
				tools: []*voiceTool{tool}, currentVoiceTool: "another-tool", inputSource: controller,
				toolPreferencesPath: filepath.Join(t.TempDir(), "tools.json"),
				toolPreferences:     toolPreferences{Version: 1, Enabled: []string{tool.id, "another-tool"}, Current: "another-tool"},
			}}
			release, queue := beginQueuedVoiceToolDiscovery(t, s, tool, tool.snapshot)
			if err := s.selectVoiceTool(tool); err != nil {
				t.Fatal(err)
			}
			test.prepare(s)
			close(release)
			awaitToolCallback(t, queue)()
			if controller.current != "com.apple.keylayout.ABC" || len(controller.selectCalls) != 0 {
				t.Fatalf("ineligible late completion changed TIS: source=%q calls=%v", controller.current, controller.selectCalls)
			}
			if s.toolError != test.wantStatus {
				t.Fatalf("completion status = %q, want %q", s.toolError, test.wantStatus)
			}
			if s.pendingInputSource != nil {
				t.Fatal("ineligible intent was retained for a later automatic retry")
			}
		})
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
	tool.refresh(voicetools.Locator{}, func(f func()) { queue <- f }, func(uint64) { changed++ })
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
	onChange := func(uint64) { changed++ }
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

func TestRequiredInputSourceUsesOnlyExecutableActionsAndOneExactTarget(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	if got, ok := requiredInputSourceForSelection(tool); !ok || got != testDoubaoSourceID {
		t.Fatalf("required source = %q, %t", got, ok)
	}

	tool.snapshot.Actions = append(tool.snapshot.Actions, voicetools.Action{
		ID: "unsupported", Control: &voicetools.Control{Trigger: bridge.ActionTap, Request: bridge.RequestToggle},
		Candidates: []voicetools.Candidate{{Raw: "not supported", Supported: false}},
	})
	tool.requiredInputSource = func(actionID string) (string, bool) {
		if actionID == "dictation" {
			return testDoubaoSourceID, true
		}
		return "", false
	}
	if got, ok := requiredInputSourceForSelection(tool); !ok || got != testDoubaoSourceID {
		t.Fatalf("unsupported action affected source target: %q, %t", got, ok)
	}

	tool.snapshot.Actions = append(tool.snapshot.Actions, voicetools.Action{
		ID: "second", Scope: "global", Control: &voicetools.Control{Trigger: bridge.ActionTap, Request: bridge.RequestToggle, ReleaseRequest: bridge.RequestNone},
		Candidates: []voicetools.Candidate{{Raw: "Option+Space", Shortcut: "Option+Space", Supported: true}},
	})
	tool.requiredInputSource = func(actionID string) (string, bool) {
		if actionID == "dictation" {
			return testDoubaoSourceID, true
		}
		if actionID == "second" {
			return "com.example.other-ime", true
		}
		return "", false
	}
	if got, ok := requiredInputSourceForSelection(tool); ok || got != "" {
		t.Fatalf("conflicting action sources produced a target: %q, %t", got, ok)
	}

	tool.requiredInputSource = func(string) (string, bool) { return "", false }
	if got, ok := requiredInputSourceForSelection(tool); ok || got != "" {
		t.Fatalf("unknown source produced a target: %q, %t", got, ok)
	}
	tool.snapshot.State = voicetools.ConfigMissing
	if got, ok := requiredInputSourceForSelection(tool); ok || got != "" {
		t.Fatalf("non-ready snapshot produced a target: %q, %t", got, ok)
	}
}

func TestUnresolvedWeTypePTTDoesNotSelectInputSource(t *testing.T) {
	snapshot := voicetools.Snapshot{State: voicetools.Ready, Actions: []voicetools.Action{{
		ID: "voicePTTShortcut", Scope: "unknown",
		Control:    &voicetools.Control{Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish, AllowFnKey: true},
		Candidates: []voicetools.Candidate{{Raw: "[63]/0/1", Shortcut: "Fn", Supported: true}},
	}}}
	tool := &voiceTool{id: "voice-wetype", name: "WeType", kind: voiceInputMethod, active: true, sequence: 1,
		requiredInputSource: wetype.RequiredInputSourceID, snapshot: snapshot}
	ref := voicetools.ActionRef{ToolID: tool.id, ActionID: "voicePTTShortcut", CandidateRaw: "[63]/0/1"}
	if _, err := voicetools.ResolveAction(ref, tool.sequence, snapshot); err == nil {
		t.Fatal("unknown-scope WeType PTT unexpectedly resolved")
	}
	if source, required := requiredInputSourceForSelection(tool); required || source != "" {
		t.Fatalf("unresolved WeType PTT required source %q", source)
	}

	t.Run("direct selection", func(t *testing.T) {
		controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
		s := &shell{voiceToolsState: voiceToolsState{
			tools: []*voiceTool{tool}, currentVoiceTool: "another-tool", inputSource: controller,
			toolPreferencesPath: filepath.Join(t.TempDir(), "tools.json"),
			toolPreferences:     toolPreferences{Version: 1, Enabled: []string{tool.id, "another-tool"}, Current: "another-tool"},
		}}
		if err := s.selectVoiceTool(tool); err != nil {
			t.Fatal(err)
		}
		if controller.current != "com.apple.keylayout.ABC" || len(controller.selectCalls) != 0 {
			t.Fatalf("unresolved WeType PTT changed TIS: source=%q calls=%v", controller.current, controller.selectCalls)
		}
	})

	t.Run("busy discovery completion", func(t *testing.T) {
		controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
		busyTool := &voiceTool{id: tool.id, name: tool.name, kind: tool.kind, active: true, sequence: 1,
			requiredInputSource: wetype.RequiredInputSourceID, snapshot: snapshot}
		s := &shell{voiceToolsState: voiceToolsState{
			tools: []*voiceTool{busyTool}, currentVoiceTool: "another-tool", inputSource: controller,
			toolPreferencesPath: filepath.Join(t.TempDir(), "tools.json"),
			toolPreferences:     toolPreferences{Version: 1, Enabled: []string{busyTool.id, "another-tool"}, Current: "another-tool"},
		}}
		release, queue := beginQueuedVoiceToolDiscovery(t, s, busyTool, snapshot)
		if err := s.selectVoiceTool(busyTool); err != nil {
			t.Fatal(err)
		}
		close(release)
		awaitToolCallback(t, queue)()
		if controller.current != "com.apple.keylayout.ABC" || len(controller.selectCalls) != 0 {
			t.Fatalf("unresolved busy WeType PTT changed TIS: source=%q calls=%v", controller.current, controller.selectCalls)
		}
	})
}

func TestExplicitRemoteEnableDefersInputSourceEnsureForBusyTool(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	runtime, err := pluginrt.New(pluginrt.Manifest{ID: remotePlugin, Start: func() (pluginrt.StopFunc, error) {
		return func() error { return nil }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := &shell{plugins: runtime, voiceToolsState: voiceToolsState{tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller}}
	release, queue := beginQueuedVoiceToolDiscovery(t, s, tool, tool.snapshot)
	s.enableRemote(true)
	if !runtime.Active(remotePlugin) || s.pendingInputSource == nil || len(controller.selectCalls) != 0 {
		t.Fatalf("explicit enable setup: active=%t pending=%+v calls=%v", runtime.Active(remotePlugin), s.pendingInputSource, controller.selectCalls)
	}
	close(release)
	awaitToolCallback(t, queue)()
	if controller.current != testDoubaoSourceID || !reflect.DeepEqual(controller.selectCalls, []string{testDoubaoSourceID}) {
		t.Fatalf("explicit ensure was lost after discovery: source=%q calls=%v", controller.current, controller.selectCalls)
	}
}

func TestPendingExplicitInputSourceEnsureDoesNotRequireRemoteEnabled(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	s := &shell{voiceToolsState: voiceToolsState{tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller}}
	release, queue := beginQueuedVoiceToolDiscovery(t, s, tool, tool.snapshot)
	if err := s.ensureSelectedVoiceInputSource(); err != nil {
		t.Fatal(err)
	}
	if s.remoteEnabled() || s.pendingInputSource == nil {
		t.Fatalf("explicit ensure setup: remote=%t pending=%+v", s.remoteEnabled(), s.pendingInputSource)
	}
	close(release)
	awaitToolCallback(t, queue)()
	if controller.current != testDoubaoSourceID || !reflect.DeepEqual(controller.selectCalls, []string{testDoubaoSourceID}) {
		t.Fatalf("pending explicit ensure required remote enabled: source=%q calls=%v", controller.current, controller.selectCalls)
	}
}

func TestRemoteStopClearsPendingInputSourceBeforeDiscoveryCompletes(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	runtime, err := pluginrt.New(pluginrt.Manifest{ID: remotePlugin, Start: func() (pluginrt.StopFunc, error) {
		return func() error { return nil }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	stopQueue := make(chan func(), 1)
	s := &shell{plugins: runtime, remoteState: remoteState{postMain: func(callback func()) { stopQueue <- callback }}, voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller,
	}}
	release, discoveryQueue := beginQueuedVoiceToolDiscovery(t, s, tool, tool.snapshot)
	s.enableRemote(true)
	if !runtime.Active(remotePlugin) || s.pendingInputSource == nil {
		t.Fatalf("explicit remote enable did not establish pending intent: active=%t pending=%+v", runtime.Active(remotePlugin), s.pendingInputSource)
	}
	s.beginRemoteStop()
	if s.pendingInputSource != nil {
		t.Fatal("remote stop retained a pending TIS intent")
	}
	awaitToolCallback(t, stopQueue)()
	if runtime.Active(remotePlugin) || s.remoteState.stopPending {
		t.Fatalf("remote stop did not complete before discovery: active=%t pending=%t", runtime.Active(remotePlugin), s.remoteState.stopPending)
	}
	close(release)
	awaitToolCallback(t, discoveryQueue)()
	if controller.current != "com.apple.keylayout.ABC" || len(controller.selectCalls) != 0 {
		t.Fatalf("discovery changed TIS after remote stop: source=%q calls=%v", controller.current, controller.selectCalls)
	}
}

func TestSelectingAndReselectingCurrentToolEnsuresInputSource(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	s := &shell{voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, currentVoiceTool: "another-tool", inputSource: controller,
		toolPreferencesPath: filepath.Join(t.TempDir(), "tools.json"),
		toolPreferences:     toolPreferences{Version: 1, Enabled: []string{tool.id, "another-tool"}, Current: "another-tool"},
	}}
	if err := s.selectVoiceTool(tool); err != nil {
		t.Fatal(err)
	}
	if s.currentVoiceTool != tool.id || controller.current != testDoubaoSourceID || !reflect.DeepEqual(controller.selectCalls, []string{testDoubaoSourceID}) {
		t.Fatalf("explicit selection did not ensure TIS: current=%q source=%q calls=%v", s.currentVoiceTool, controller.current, controller.selectCalls)
	}
	if controller.currentCalls != 2 {
		t.Fatalf("selection performed %d current-ID reads, want pre-check and one readback", controller.currentCalls)
	}
	if err := s.selectVoiceTool(tool); err != nil {
		t.Fatal(err)
	}
	if controller.currentCalls != 3 || len(controller.selectCalls) != 1 {
		t.Fatalf("already-current reselect did not only verify once: currentReads=%d selectCalls=%v", controller.currentCalls, controller.selectCalls)
	}

	controller.current = "com.apple.keylayout.ABC" // Model a user switching input sources externally.
	if err := s.selectVoiceTool(tool); err != nil {
		t.Fatal(err)
	}
	if controller.current != testDoubaoSourceID || !reflect.DeepEqual(controller.selectCalls, []string{testDoubaoSourceID, testDoubaoSourceID}) || controller.currentCalls != 5 {
		t.Fatalf("same-tool reselect did not restore TIS once: source=%q reads=%d calls=%v", controller.current, controller.currentCalls, controller.selectCalls)
	}
}

func TestInputSourceReadbackMismatchIsReportedAndTargetRemainsSelected(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC", selectedAs: "com.example.other-ime"}
	path := filepath.Join(t.TempDir(), "tools.json")
	s := &shell{voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, currentVoiceTool: "another-tool", inputSource: controller,
		toolPreferencesPath: path,
		toolPreferences:     toolPreferences{Version: 1, Enabled: []string{tool.id}, Current: "another-tool"},
	}}
	if err := s.selectVoiceTool(tool); err == nil || s.toolError != inputSourceFailedToolError {
		t.Fatalf("readback mismatch error=%v status=%q", err, s.toolError)
	}
	stored, err := readToolPreferences(path)
	if err != nil || stored.Current != tool.id || s.currentVoiceTool != tool.id || controller.currentCalls != 2 || len(controller.selectCalls) != 1 {
		t.Fatalf("unconfirmed selection state: stored=%+v current=%q reads=%d selections=%v err=%v", stored, s.currentVoiceTool, controller.currentCalls, controller.selectCalls, err)
	}
}

func TestRetiringRouteDefersInputSourceWithoutPendingSwitch(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	s := &shell{liveAudio: liveAudioState{retiring: true}, voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller,
	}}
	if err := s.selectVoiceTool(tool); err == nil || s.toolError != inputSourceDeferredToolError {
		t.Fatalf("retiring-route reselect error=%v user error=%q", err, s.toolError)
	}
	if len(controller.selectCalls) != 0 || controller.current != "com.apple.keylayout.ABC" {
		t.Fatalf("TIS changed while route was retiring: current=%q calls=%v", controller.current, controller.selectCalls)
	}

	s.liveAudio.retiring = false
	if err := s.selectVoiceTool(tool); err != nil || controller.current != testDoubaoSourceID {
		t.Fatalf("explicit retry after idle failed: err=%v source=%q", err, controller.current)
	}
}

func TestFailedAudioCleanupOwnerKeepsInputSourceErrorPriority(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	s := &shell{
		liveAudio: liveAudioState{connection: &liveAudioConnection{}, toolID: tool.id},
		report:    hostReport{AudioCleanupFailed: true, AudioError: "injected owner close failure"},
		voiceToolsState: voiceToolsState{
			tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller,
		},
	}
	if err := s.selectVoiceTool(tool); err == nil || s.toolError != inputSourceCleanupFailedToolError {
		t.Fatalf("cleanup failure error=%v status=%q", err, s.toolError)
	}
	if len(controller.selectCalls) != 0 || controller.current != "com.apple.keylayout.ABC" {
		t.Fatalf("TIS changed while failed route owner was retained: source=%q calls=%v", controller.current, controller.selectCalls)
	}
}

func TestSuccessfulInputSourceEnsureClearsPriorCleanupError(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	s := &shell{
		liveAudio: liveAudioState{connection: &liveAudioConnection{}, toolID: tool.id},
		report:    hostReport{AudioCleanupFailed: true, AudioError: "injected owner close failure"},
		voiceToolsState: voiceToolsState{
			tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller,
		},
	}
	if err := s.ensureSelectedVoiceInputSource(); !errors.Is(err, errInputSourceCleanupFailed) || s.toolError != inputSourceCleanupFailedToolError {
		t.Fatalf("retained-owner gate error=%v status=%q", err, s.toolError)
	}
	if len(controller.selectCalls) != 0 {
		t.Fatal("input source changed while cleanup owner was retained")
	}

	// Existing owner recovery has completed; a later explicit ensure is safe.
	s.liveAudio.connection = nil
	s.report.AudioCleanupFailed = false
	if err := s.ensureSelectedVoiceInputSource(); err != nil {
		t.Fatal(err)
	}
	if controller.current != testDoubaoSourceID || s.toolError != "" {
		t.Fatalf("successful ensure did not clear its prior error: source=%q status=%q", controller.current, s.toolError)
	}
}

type inputSourceReleaseFailureSink struct{ failKeyUp bool }

func (s *inputSourceReleaseFailureSink) Submit(effects []bridge.Effect) error {
	if s.failKeyUp {
		for _, effect := range effects {
			if _, isKeyUp := effect.(bridge.KeyUpEffect); isKeyUp {
				return errors.New("injected release failure")
			}
		}
	}
	return nil
}
func (*inputSourceReleaseFailureSink) ReleaseAll() error  { return nil }
func (*inputSourceReleaseFailureSink) Close() error       { return nil }
func (*inputSourceReleaseFailureSink) Held() []bridge.Key { return nil }

func TestDifferentToolReleaseFailureTakesPriorityOverInputSourceHint(t *testing.T) {
	oldTool := testInputSourceTool("voice-old-doubao")
	newTool := testInputSourceTool("voice-doubao")
	d := newDeviceCoordinator()
	sink := &inputSourceReleaseFailureSink{}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("input-source-release-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.DeviceConnected(device)); err != nil {
		t.Fatal(err)
	}
	plan, err := bridge.NewActionPlan(bridge.ActionSpec{
		ToolID: oldTool.id, ActionID: "dictation", CandidateRaw: "Control+Space", Revision: 1,
		Scope: bridge.ActionGlobal, Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish,
	}, "Control", "Space")
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "hid:0007:0052", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.TargetActionBegan(press, plan)); err != nil {
		t.Fatal(err)
	}
	sink.failKeyUp = true
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	path := filepath.Join(t.TempDir(), "tools.json")
	s := &shell{coordinator: d, voiceToolsState: voiceToolsState{
		tools: []*voiceTool{oldTool, newTool}, currentVoiceTool: oldTool.id, inputSource: controller,
		toolPreferencesPath: path,
		toolPreferences:     toolPreferences{Version: 1, Enabled: []string{oldTool.id, newTool.id}, Current: oldTool.id},
	}}
	if err := s.selectVoiceTool(newTool); err == nil || s.toolError != "原工具按键未能释放" {
		t.Fatalf("release failure error=%v user error=%q", err, s.toolError)
	}
	if s.currentVoiceTool != newTool.id || len(controller.selectCalls) != 0 || controller.current != "com.apple.keylayout.ABC" {
		t.Fatalf("TIS changed before confirming release: current=%q calls=%v", controller.current, controller.selectCalls)
	}
}

func TestCurrentToolHeldActionDefersReselectWithoutCancelling(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	d := newDeviceCoordinator()
	sink := &inputSourceReleaseFailureSink{}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("input-source-held-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.DeviceConnected(device)); err != nil {
		t.Fatal(err)
	}
	plan, err := bridge.NewActionPlan(bridge.ActionSpec{
		ToolID: tool.id, ActionID: "dictation", CandidateRaw: "Control+Space", Revision: 1,
		Scope: bridge.ActionGlobal, Trigger: bridge.ActionHold, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestFinish,
	}, "Control", "Space")
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "hid:0007:0052", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.TargetActionBegan(press, plan)); err != nil {
		t.Fatal(err)
	}
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	s := &shell{coordinator: d, voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller,
	}}
	before := d.core.Snapshot()
	if len(before.HeldKeys) == 0 {
		t.Fatal("fixture did not start a held action")
	}
	if err := s.selectVoiceTool(tool); err == nil || s.toolError != inputSourceDeferredToolError {
		t.Fatalf("held-action reselect error=%v status=%q", err, s.toolError)
	}
	after := d.core.Snapshot()
	if len(after.HeldKeys) == 0 || !reflect.DeepEqual(before.HeldKeys, after.HeldKeys) || controller.currentCalls != 0 || len(controller.selectCalls) != 0 {
		t.Fatalf("busy reselect cancelled or touched source: before=%+v after=%+v reads=%d selections=%v", before.HeldKeys, after.HeldKeys, controller.currentCalls, controller.selectCalls)
	}
}

func TestCurrentToolCoreSessionDefersReselectWithoutSelecting(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	core, err := bridge.NewCore(bridge.NewAudioBinding())
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("input-source-session-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.RouteAvailable(true), bridge.ScopeAvailable(false)} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	press, err := bridge.NewPressToken(device, "hid:0007:003e", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(bridge.VoiceBegan(press)); err != nil {
		t.Fatal(err)
	}
	if !core.Snapshot().HasSession {
		t.Fatal("fixture did not start an active Core session")
	}
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	s := &shell{coordinator: &deviceCoordinator{core: core}, voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller,
	}}
	if err := s.selectVoiceTool(tool); err == nil || s.toolError != inputSourceDeferredToolError {
		t.Fatalf("Core-session reselect error=%v status=%q", err, s.toolError)
	}
	if !core.Snapshot().HasSession || controller.currentCalls != 0 || len(controller.selectCalls) != 0 {
		t.Fatalf("Core session or input source changed: hasSession=%t reads=%d selections=%v", core.Snapshot().HasSession, controller.currentCalls, controller.selectCalls)
	}
}

func TestToolActivationDoesNotSelectInputSource(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	tool.active = false
	controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
	runtime, err := pluginrt.New(pluginrt.Manifest{ID: tool.id, Start: func() (pluginrt.StopFunc, error) {
		tool.active = true
		return func() error { tool.stop(); return nil }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := &shell{plugins: runtime, voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, inputSource: controller,
		toolPreferencesPath: filepath.Join(t.TempDir(), "tools.json"),
		toolPreferences:     toolPreferences{Version: 1},
	}}
	if err := s.setVoiceToolEnabled(tool, true); err != nil {
		t.Fatal(err)
	}
	if len(controller.selectCalls) != 0 || controller.current != "com.apple.keylayout.ABC" {
		t.Fatalf("plugin activation changed system input source: current=%q calls=%v", controller.current, controller.selectCalls)
	}
}

func TestApplicationAndAppleVoiceSelectionDoNotTouchInputSource(t *testing.T) {
	for _, tool := range []*voiceTool{
		{id: "voice-chatgpt", name: "ChatGPT", kind: voiceApplication, active: true, snapshot: voicetools.Snapshot{State: voicetools.Ready}},
		{id: appleSpeechToolID, name: "Apple Speech", kind: voiceRecognition, active: true, snapshot: voicetools.Snapshot{State: voicetools.Ready}},
	} {
		t.Run(tool.id, func(t *testing.T) {
			controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC"}
			s := &shell{voiceToolsState: voiceToolsState{
				tools: []*voiceTool{tool}, currentVoiceTool: "another-tool", inputSource: controller,
				toolPreferencesPath: filepath.Join(t.TempDir(), "tools.json"),
				toolPreferences:     toolPreferences{Version: 1, Enabled: []string{tool.id}, Current: "another-tool"},
			}}
			if err := s.selectVoiceTool(tool); err != nil {
				t.Fatal(err)
			}
			if controller.currentCalls != 0 || len(controller.selectCalls) != 0 || controller.current != "com.apple.keylayout.ABC" {
				t.Fatalf("non-TIS tool selection touched source: source=%q reads=%d calls=%v", controller.current, controller.currentCalls, controller.selectCalls)
			}
		})
	}
}

func TestExplicitRemoteEnableEnsuresInputSourceButResumeDoesNot(t *testing.T) {
	for _, requestPermission := range []bool{true, false} {
		t.Run(fmt.Sprintf("requestPermission=%t", requestPermission), func(t *testing.T) {
			tool := testInputSourceTool("voice-doubao")
			var events []string
			controller := &fakeInputSourceController{current: "com.apple.keylayout.ABC", events: &events}
			runtime, err := pluginrt.New(pluginrt.Manifest{ID: remotePlugin, Start: func() (pluginrt.StopFunc, error) {
				events = append(events, "remote-start")
				return func() error { return nil }, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			s := &shell{plugins: runtime, voiceToolsState: voiceToolsState{
				tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller,
			}}
			s.enableRemote(requestPermission)
			want := []string{"remote-start"}
			if requestPermission {
				want = []string{"select:" + testDoubaoSourceID, "remote-start"}
				if controller.current != testDoubaoSourceID {
					t.Fatalf("explicit remote enable left source %q", controller.current)
				}
			} else if controller.current != "com.apple.keylayout.ABC" {
				t.Fatalf("automatic resume changed source to %q", controller.current)
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("lifecycle order = %v, want %v", events, want)
			}
		})
	}
}

func TestExplicitRemoteEnableKeepsRunningWhenInputSourceEnsureFails(t *testing.T) {
	tool := testInputSourceTool("voice-doubao")
	controller := &fakeInputSourceController{
		current:   "com.apple.keylayout.ABC",
		selectErr: errors.New("input source is disabled"),
	}
	runtime, err := pluginrt.New(pluginrt.Manifest{ID: remotePlugin, Start: func() (pluginrt.StopFunc, error) {
		return func() error { return nil }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := &shell{plugins: runtime, voiceToolsState: voiceToolsState{
		tools: []*voiceTool{tool}, currentVoiceTool: tool.id, inputSource: controller,
	}}
	s.enableRemote(true)
	if !runtime.Active(remotePlugin) || s.toolError != inputSourceFailedToolError || controller.current != "com.apple.keylayout.ABC" {
		t.Fatalf("source failure incorrectly blocked remote or reported ready: active=%t toolError=%q source=%q",
			runtime.Active(remotePlugin), s.toolError, controller.current)
	}
}
