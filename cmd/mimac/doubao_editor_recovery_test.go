package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/platform/audioroute"
	"mimac.local/mimac/internal/platform/processidentity"
	"mimac.local/mimac/internal/voicetools"
)

func TestReconnectInputSourceRespectsSelection(t *testing.T) {
	for _, tc := range []struct {
		name, initial, userChoice string
		failFirstRestore          bool
		want                      []string
	}{
		{name: "replacement in same editor", initial: "ime", want: []string{"layout", "ime"}},
		{name: "different input source before recovery", initial: "other"},
		{name: "user picked another source during recovery", initial: "ime", userChoice: "other", want: []string{"layout"}},
		{name: "failed return to ime restores owned layout", initial: "ime", failFirstRestore: true, want: []string{"layout", "ime", "ime"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active := tc.initial
			var selections []string
			err := reconnectInputSource("ime", func() (string, error) { return active, nil }, func() (string, error) { return "layout", nil }, func(id string) error {
				selections = append(selections, id)
				if id == "ime" && tc.failFirstRestore && len(selections) == 2 {
					return errors.New("transient failure")
				}
				active = id
				if id == "layout" && tc.userChoice != "" {
					active = tc.userChoice
				}
				return nil
			})
			if err != nil || !slices.Equal(selections, tc.want) {
				t.Fatalf("selected %v, error %v", selections, err)
			}
			if tc.userChoice != "" && active != tc.userChoice {
				t.Fatalf("user choice changed to %q", active)
			}
		})
	}
}

type interruptedEditorProbe struct {
	changed     bool
	closed      bool
	focusChecks int
}

func (p *interruptedEditorProbe) CheckFocus() error {
	p.focusChecks++
	if p.changed {
		return errors.New("editor changed")
	}
	return nil
}
func (p *interruptedEditorProbe) Close() error { p.closed = true; return nil }

type recoverySelectionObserverProbe struct {
	editor       interruptedEditor
	err          error
	captureCalls int
	closeCalls   int
}

func (p *recoverySelectionObserverProbe) CaptureCurrentSelection() (interruptedEditor, error) {
	p.captureCalls++
	return p.editor, p.err
}

func (p *recoverySelectionObserverProbe) Close() error {
	p.closeCalls++
	return nil
}

func TestReplacementReconnectsSameEditorBeforeQueryingControl(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed bool
		want    []string
	}{
		{"same editor", false, []string{"reconnect", "query"}},
		{"different editor", true, []string{"query"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			editor := &interruptedEditorProbe{changed: tc.changed}
			recovery := newDoubaoEditorRecovery()
			recovery.interrupted = editor
			receipt := &editorRetirementReceipt{}
			receipt.confirmed.Store(true)
			recovery.interruptedReceipt = receipt
			recovery.dispatch = func(f func()) { f() }
			recovery.refresh = func() error { order = append(order, "reconnect"); return nil }
			route, _, _, _, _, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
				deps.editorRecovery = recovery
				deps.prepareControl = func(context.Context, voicetools.Locator, int32) error { order = append(order, "query"); return nil }
			})
			if err := route.Start(); err != nil {
				t.Fatal(err)
			}
			waitXiaomiRoute(t, route.Ready)
			if err := route.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(order, tc.want) || !editor.closed || recovery.interrupted != nil {
				t.Fatalf("order %v, editor closed %v", order, editor.closed)
			}
		})
	}
}

func TestDoubaoExitSnapshotWaitsForRouteRetirementProof(t *testing.T) {
	recovery := newDoubaoEditorRecovery()
	recovery.dispatch = func(f func()) { f() }
	editor := &interruptedEditorProbe{}
	observer := &recoverySelectionObserverProbe{editor: editor}
	oldRoute, _, identity, _, _, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
		deps.editorRecovery = recovery
	})
	receipt := oldRoute.editorRetirementReceipt
	oldRoute.mu.Lock()
	oldRoute.selectionAttempted = true
	oldRoute.mu.Unlock()
	recovery.mu.Lock()
	recovery.owner, recovery.origin = oldRoute, observer
	recovery.mu.Unlock()

	oldRoute.recordEditorOnExit(errors.Join(errors.New("receiver failed"), processidentity.ErrReadFailed))
	if observer.captureCalls != 1 || observer.closeCalls != 1 {
		t.Fatalf("origin calls = capture %d, close %d; want one each", observer.captureCalls, observer.closeCalls)
	}
	if recovery.interrupted != editor || recovery.interruptedReceipt != receipt || receipt.confirmed.Load() {
		t.Fatalf("interruption after fault = (%v, %p, retired=%v), want saved but unconfirmed", recovery.interrupted, recovery.interruptedReceipt, receipt.confirmed.Load())
	}

	var reconnects int
	recovery.refresh = func() error { reconnects++; return nil }
	newRoute, _, _, _, _, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
		deps.editorRecovery = recovery
	})
	defer closeCompatRouteForTest(t, newRoute)
	if err := recovery.prepareReplacement(context.Background(), newRoute); err != nil {
		t.Fatalf("prepare before retirement confirmation: %v", err)
	}
	if editor.focusChecks != 0 || reconnects != 0 || editor.closed {
		t.Fatalf("unconfirmed target was used: focus checks %d, reconnects %d, closed %v", editor.focusChecks, reconnects, editor.closed)
	}

	// Route Close's existing identity and bundle-client checks establish the
	// retirement receipt. The recovery code only consumes that receipt.
	identity.setError(processidentity.ErrIdentityChanged)
	if err := oldRoute.Close(context.Background()); err != nil {
		t.Fatalf("close retired input-method route: %v", err)
	}
	if !receipt.confirmed.Load() {
		t.Fatal("successful retired route close did not confirm the saved editor target")
	}
	if err := recovery.prepareReplacement(context.Background(), newRoute); err != nil {
		t.Fatalf("prepare after retirement confirmation: %v", err)
	}
	if editor.focusChecks != 1 || reconnects != 1 || !editor.closed || recovery.interrupted != nil {
		t.Fatalf("confirmed recovery = focus checks %d, reconnects %d, closed %v, pending %v", editor.focusChecks, reconnects, editor.closed, recovery.interrupted)
	}
}

func TestDoubaoExitSnapshotIsDiscardedWhenOriginalRouteDoesNotRetire(t *testing.T) {
	recovery := newDoubaoEditorRecovery()
	recovery.dispatch = func(f func()) { f() }
	editor := &interruptedEditorProbe{}
	observer := &recoverySelectionObserverProbe{editor: editor}
	oldRoute, _, _, _, _, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
		deps.editorRecovery = recovery
	})
	oldRoute.mu.Lock()
	oldRoute.selectionAttempted = true
	oldRoute.mu.Unlock()
	recovery.mu.Lock()
	recovery.owner, recovery.origin = oldRoute, observer
	recovery.mu.Unlock()
	oldRoute.recordEditorOnExit(processidentity.ErrReadFailed)

	if err := oldRoute.Close(context.Background()); err != nil {
		t.Fatalf("close still-live input-method route: %v", err)
	}
	if recovery.interrupted != nil || !editor.closed {
		t.Fatalf("non-retired route retained editor snapshot: pending %v, closed %v", recovery.interrupted, editor.closed)
	}
	if editor.focusChecks != 0 {
		t.Fatalf("non-retired route checked focus %d times", editor.focusChecks)
	}
}

func TestDoubaoEditorRecoveryUsesRouteRetirementNotLeaseRetiredState(t *testing.T) {
	recovery := newDoubaoEditorRecovery()
	editor := &interruptedEditorProbe{}
	receipt := &editorRetirementReceipt{}
	recovery.interrupted = editor
	recovery.interruptedReceipt = receipt
	var reconnects int
	recovery.refresh = func() error { reconnects++; return nil }

	route, _, _, _, requests, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
		deps.editorRecovery = recovery
	})
	defer closeCompatRouteForTest(t, route)
	route.editorRetirementReceipt = receipt
	route.selectionAttempted = true
	route.preparedSelectionConfirmed = true
	route.readSelection = func(context.Context, voicetools.Locator) (compatAudioSelection, error) {
		userChoice := "external-microphone"
		return compatAudioSelection{preferredUID: &userChoice}, nil
	}

	if err := route.Close(context.Background()); err != nil {
		t.Fatalf("close route after user changed microphone: %v", err)
	}
	if route.retired != true || receipt.confirmed.Load() {
		t.Fatalf("lease state retired=%v, process receipt=%v; user choice must not prove process exit", route.retired, receipt.confirmed.Load())
	}
	if len(requests.snapshot()) != 0 {
		t.Fatalf("user-selected microphone was overwritten: %v", requests.snapshot())
	}
	if recovery.interrupted != nil || !editor.closed || reconnects != 0 || editor.focusChecks != 0 {
		t.Fatalf("unconfirmed recovery used: pending=%v closed=%v reconnects=%d focusChecks=%d", recovery.interrupted, editor.closed, reconnects, editor.focusChecks)
	}
}

func TestDoubaoEditorRecoveryWaitsForHALClientListToDropRetiredPID(t *testing.T) {
	recovery := newDoubaoEditorRecovery()
	editor := &interruptedEditorProbe{}
	route, inspector, identity, _, _, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
		deps.editorRecovery = recovery
	})
	recovery.interrupted = editor
	recovery.interruptedReceipt = route.editorRetirementReceipt
	route.mu.Lock()
	route.selectionAttempted = true
	route.mu.Unlock()
	identity.setCheckErrors([]error{processidentity.ErrReadFailed, processidentity.ErrReadFailed})
	inspector.setBundleSequence(
		[]audioroute.ProcessInput{{Found: true, PID: route.pid}},
		nil,
	)

	if err := route.Close(context.Background()); err != nil {
		t.Fatalf("close after delayed Core Audio client removal: %v", err)
	}
	if !route.editorRetirementReceipt.confirmed.Load() || recovery.interrupted != editor {
		t.Fatalf("late HAL retirement was not confirmed: receipt=%v target=%v", route.editorRetirementReceipt.confirmed.Load(), recovery.interrupted)
	}
}

func TestDoubaoEditorRecoveryDoesNothingWhileProcessIdentityIsUncertain(t *testing.T) {
	recovery := newDoubaoEditorRecovery()
	recovery.dispatch = func(f func()) { f() }
	editor := &interruptedEditorProbe{}
	var reconnects int
	recovery.refresh = func() error { reconnects++; return nil }
	route, _, identity, _, _, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
		deps.editorRecovery = recovery
	})
	defer closeCompatRouteForTest(t, route)
	route.mu.Lock()
	route.selectionAttempted = true
	route.mu.Unlock()
	recovery.interrupted = editor
	recovery.interruptedReceipt = route.editorRetirementReceipt
	identity.setError(processidentity.ErrReadFailed)

	if err := route.Close(context.Background()); !errors.Is(err, audiosession.ErrCleanupPending) {
		t.Fatalf("close with indeterminate process identity = %v, want cleanup pending", err)
	}
	if route.editorRetirementReceipt.confirmed.Load() {
		t.Fatal("indeterminate process identity produced a retirement receipt")
	}
	newRoute, _, _, _, _, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil)
	defer closeCompatRouteForTest(t, newRoute)
	if err := recovery.prepareReplacement(context.Background(), newRoute); err != nil {
		t.Fatalf("prepare with indeterminate old identity: %v", err)
	}
	if editor.focusChecks != 0 || reconnects != 0 || editor.closed {
		t.Fatalf("indeterminate target was used: focus checks %d, reconnects %d, closed %v", editor.focusChecks, reconnects, editor.closed)
	}

	// Once the original process identity can be read again, normal close cleanup
	// retires the unconfirmed editor snapshot without using it.
	identity.setError(nil)
	if err := route.Close(context.Background()); err != nil {
		t.Fatalf("retry close after identity becomes readable: %v", err)
	}
	if recovery.interrupted != nil || !editor.closed {
		t.Fatalf("unconfirmed editor snapshot was retained: pending %v, closed %v", recovery.interrupted, editor.closed)
	}
}

func TestInputSourceReadFailureRestoresOnlyOwnedLayout(t *testing.T) {
	for _, change := range []bool{false, true} {
		active := "ime"
		count := 0
		failure := errors.New("input source read failed")
		err := reconnectInputSource("ime", func() (string, error) {
			count++
			if count == 2 {
				if change {
					active = "user-choice"
				}
				return "", failure
			}
			return active, nil
		}, func() (string, error) { return "layout", nil }, func(id string) error { active = id; return nil })
		want := "ime"
		if change {
			want = "user-choice"
		}
		if !errors.Is(err, failure) || active != want {
			t.Fatalf("error %v, left %q", err, active)
		}
	}
}
