package windowlease

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

const (
	visibilityTestPID     = int32(4162)
	visibilityTestFocus   = int32(9001)
	visibilityTestWindow  = uint32(52574)
	visibilityTestElement = uintptr(707)
)

func TestCaptureHiddenBaselineRequiresVisibleEmptyAppAndFocusedEditor(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*visibilityTestProbe)
	}{
		{name: "empty visible app"},
		{name: "prehidden app", setup: func(p *visibilityTestProbe) { p.hidden = true }},
		{name: "on-screen candidate baseline", setup: func(p *visibilityTestProbe) { p.putWindow(visibilityTestWindowRecord(true), true) }},
		{name: "window on another space", setup: func(p *visibilityTestProbe) { p.putWindow(visibilityTestWindowRecord(false), true) }},
		{name: "no focused editor", setup: func(p *visibilityTestProbe) { p.focusedPID = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := newVisibilityTestProbe()
			if test.setup != nil {
				test.setup(probe)
			}
			baseline, err := CaptureHiddenBaseline(visibilityTestPID, func() error { return nil }, probe)
			wantSuccess := test.name == "empty visible app"
			if (err == nil) != wantSuccess || (baseline != nil) != wantSuccess {
				t.Fatalf("CaptureHiddenBaseline() = (%v, %v), want success=%v", baseline != nil, err, wantSuccess)
			}
			if probe.hideWrites != 0 {
				t.Fatalf("baseline capture performed %d Hidden writes", probe.hideWrites)
			}
		})
	}
}

// A created-window wake can arrive before AX finishes populating the element.
func TestPositionLeaseWakesForFullyClassifiedRecorder(t *testing.T) {
	probe := newVisibilityTestProbe()
	baseline, err := CaptureBaseline(visibilityTestPID, func() error { return nil }, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.putWindow(visibilityTestWindowRecord(true), true)
	probe.incompleteFirstAXRead = true
	probe.sendApplicationEvent(ApplicationEventWindowCreated)
	lease, outcome, err := baseline.Suppress(context.Background(), visibilityTestEligible, time.Second, time.Hour, 0)
	if err != nil || outcome != OutcomeSuppressed || lease == nil {
		t.Fatalf("notification replay: outcome=%v err=%v", outcome, err)
	}
	if err := lease.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probe.events.closeCount != 1 || probe.hideWrites != 0 {
		t.Fatalf("discovery observer closed %d times; app hidden %d times", probe.events.closeCount, probe.hideWrites)
	}
}

func TestWindowCreatedPrepaintClaimsExactOffscreenWindowAndRestoresOnWeTypeShow(t *testing.T) {
	probe := newVisibilityTestProbe()
	baseline := captureVisibilityTestBaseline(t, probe)
	probe.putWindow(visibilityTestWindowRecord(false), true)
	probe.sendApplicationEvent(ApplicationEventWindowCreated)
	prepaintEligible := func(window Window) bool { return visibilityTestEligible(window) && !window.OnScreen }
	lease, outcome, err := baseline.SuppressWithWindowCreatedPrepaint(context.Background(), visibilityTestEligible, prepaintEligible, time.Second, time.Millisecond)
	if err != nil || outcome != OutcomeSuppressedPrepaint || lease == nil {
		t.Fatalf("prepaint Suppress() = (%v, %q, %v)", lease != nil, outcome, err)
	}
	if state := probe.snapshot(); state.hideWrites != 1 || !state.hidden {
		t.Fatalf("prepaint claim: writes=%d hidden=%v", state.hideWrites, state.hidden)
	}
	probe.setWindowOnScreen(visibilityTestWindow, true)
	probe.setHiddenExternally(false) // WeType takes display ownership after the lease.
	if err := lease.Maintain(context.Background(), nil); err != nil {
		t.Fatalf("Maintain() after WeType show: %v", err)
	}
	if state := probe.snapshot(); state.hideWrites != 1 || state.hidden || state.retained != 0 || state.observerCloses != 1 {
		t.Fatalf("after WeType show: writes=%d hidden=%v refs=%d closes=%d", state.hideWrites, state.hidden, state.retained, state.observerCloses)
	}
}

func TestWindowCreatedPrepaintRequiresNewUniqueAXAndExactRecorder(t *testing.T) {
	prepaintEligible := func(window Window) bool {
		return visibilityTestEligible(window) && !window.OnScreen && window.Frame.Size == (Size{Width: 200, Height: 130})
	}
	t.Run("old offscreen CG row without event", func(t *testing.T) {
		probe := newVisibilityTestProbe()
		baseline := captureVisibilityTestBaseline(t, probe)
		probe.putWindow(visibilityTestWindowRecord(false), true)
		lease, outcome, err := baseline.SuppressWithWindowCreatedPrepaint(context.Background(), visibilityTestEligible, prepaintEligible, 5*time.Millisecond, time.Millisecond)
		if err != nil || outcome != OutcomeUnchanged || lease != nil || probe.snapshot().hideWrites != 0 {
			t.Fatalf("old offscreen row: lease=%v outcome=%q err=%v writes=%d", lease != nil, outcome, err, probe.snapshot().hideWrites)
		}
	})
	t.Run("candidate event", func(t *testing.T) {
		probe := newVisibilityTestProbe()
		baseline := captureVisibilityTestBaseline(t, probe)
		candidate := visibilityTestWindowRecord(false)
		candidate.Frame.Size = Size{Width: 510, Height: 32}
		candidate.Size = candidate.Frame.Size
		probe.putWindow(candidate, true)
		probe.sendApplicationEvent(ApplicationEventWindowCreated)
		lease, outcome, err := baseline.SuppressWithWindowCreatedPrepaint(context.Background(), visibilityTestEligible, prepaintEligible, 5*time.Millisecond, time.Millisecond)
		if err != nil || outcome != OutcomeUnchanged || lease != nil || probe.snapshot().hideWrites != 0 {
			t.Fatalf("candidate event: lease=%v outcome=%q err=%v writes=%d", lease != nil, outcome, err, probe.snapshot().hideWrites)
		}
	})
	t.Run("CG row without AX membership", func(t *testing.T) {
		probe := newVisibilityTestProbe()
		baseline := captureVisibilityTestBaseline(t, probe)
		probe.putWindow(visibilityTestWindowRecord(false), false)
		probe.sendApplicationEvent(ApplicationEventWindowCreated)
		lease, outcome, err := baseline.SuppressWithWindowCreatedPrepaint(context.Background(), visibilityTestEligible, prepaintEligible, 5*time.Millisecond, time.Millisecond)
		if err != nil || outcome != OutcomeUnchanged || lease != nil || probe.snapshot().hideWrites != 0 || probe.snapshot().retained != 0 {
			t.Fatalf("CG-only event: lease=%v outcome=%q err=%v writes=%d refs=%d", lease != nil, outcome, err, probe.snapshot().hideWrites, probe.snapshot().retained)
		}
	})
	t.Run("focus changed", func(t *testing.T) {
		probe := newVisibilityTestProbe()
		baseline := captureVisibilityTestBaseline(t, probe)
		probe.putWindow(visibilityTestWindowRecord(false), true)
		probe.changeFocusOnNextSnapshot = true
		probe.sendApplicationEvent(ApplicationEventWindowCreated)
		lease, outcome, err := baseline.SuppressWithWindowCreatedPrepaint(context.Background(), visibilityTestEligible, prepaintEligible, 5*time.Millisecond, time.Millisecond)
		if err != nil || outcome != OutcomeUnchanged || lease != nil || probe.snapshot().hideWrites != 0 {
			t.Fatalf("focus change: lease=%v outcome=%q err=%v writes=%d", lease != nil, outcome, err, probe.snapshot().hideWrites)
		}
	})
	t.Run("another window is onscreen", func(t *testing.T) {
		probe := newVisibilityTestProbe()
		baseline := captureVisibilityTestBaseline(t, probe)
		probe.putWindow(visibilityTestWindowRecord(false), true)
		other := visibilityTestWindowRecord(true)
		other.ID++
		other.Element++
		probe.putWindow(other, false)
		probe.sendApplicationEvent(ApplicationEventWindowCreated)
		lease, outcome, err := baseline.SuppressWithWindowCreatedPrepaint(context.Background(), visibilityTestEligible, prepaintEligible, time.Second, time.Millisecond)
		if err != nil || outcome != OutcomeUnchanged || lease != nil || probe.snapshot().hideWrites != 0 {
			t.Fatalf("onscreen peer: lease=%v outcome=%q err=%v writes=%d", lease != nil, outcome, err, probe.snapshot().hideWrites)
		}
	})
}

func TestWindowCreatedPrepaintOnscreenRaceUsesExistingPath(t *testing.T) {
	probe := newVisibilityTestProbe()
	baseline := captureVisibilityTestBaseline(t, probe)
	probe.putWindow(visibilityTestWindowRecord(false), true)
	probe.windowOnScreenOnIDRead = true
	probe.sendApplicationEvent(ApplicationEventWindowCreated)
	prepaintEligible := func(window Window) bool { return visibilityTestEligible(window) && !window.OnScreen }
	lease, outcome, err := baseline.SuppressWithWindowCreatedPrepaint(context.Background(), visibilityTestEligible, prepaintEligible, time.Second, time.Millisecond)
	if err != nil || outcome != OutcomeSuppressed || lease == nil {
		t.Fatalf("ordinary on-screen fallback: lease=%v outcome=%q err=%v", lease != nil, outcome, err)
	}
	if probe.snapshot().hideWrites != 1 {
		t.Fatalf("fallback hide writes=%d, want one ordinary hide", probe.snapshot().hideWrites)
	}
	if err := lease.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWindowCreatedPrepaintYieldToWeTypeShowUsesExistingPath(t *testing.T) {
	probe := newVisibilityTestProbe()
	baseline := captureVisibilityTestBaseline(t, probe)
	probe.putWindow(visibilityTestWindowRecord(false), true)
	probe.showOnFirstHide = true
	probe.sendApplicationEvent(ApplicationEventWindowCreated)
	prepaintEligible := func(window Window) bool { return visibilityTestEligible(window) && !window.OnScreen }
	lease, outcome, err := baseline.SuppressWithWindowCreatedPrepaint(context.Background(), visibilityTestEligible, prepaintEligible, time.Second, time.Millisecond)
	if err != nil || outcome != OutcomeSuppressed || lease == nil {
		t.Fatalf("ordinary path after WeType show: lease=%v outcome=%q err=%v", lease != nil, outcome, err)
	}
	if probe.snapshot().hideWrites != 2 {
		t.Fatalf("hide writes=%d, want one prepaint attempt and one on-screen claim", probe.snapshot().hideWrites)
	}
	if err := lease.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDelayedRecorderDuringActiveSession(t *testing.T) {
	for _, endFirst := range []bool{false, true} {
		name := "created"
		if endFirst {
			name = "session ends first"
		}
		t.Run(name, func(t *testing.T) {
			probe := newVisibilityTestProbe()
			baseline, err := CaptureBaseline(visibilityTestPID, func() error { return nil }, probe)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			type result struct {
				lease   *Lease
				outcome Outcome
				err     error
			}
			finished := make(chan result, 1)
			go func() {
				lease, outcome, err := baseline.Suppress(ctx, visibilityTestEligible, 5*time.Millisecond, time.Hour, 0)
				finished <- result{lease, outcome, err}
			}()
			select {
			case early := <-finished:
				t.Fatalf("discovery ended before the recorder or the utterance: %v", early.outcome)
			case <-time.After(40 * time.Millisecond):
			}
			if endFirst {
				cancel()
			} else {
				probe.putWindow(visibilityTestWindowRecord(true), true)
				probe.sendApplicationEvent(ApplicationEventWindowCreated)
			}
			got := <-finished
			if probe.events.closeCount != 1 {
				t.Fatalf("observer closed %d times", probe.events.closeCount)
			}
			if endFirst {
				if got.lease != nil || got.outcome != OutcomeCancelled || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("after session end: %+v", got)
				}
				return
			}
			if got.lease == nil || got.outcome != OutcomeSuppressed || got.err != nil {
				t.Fatalf("late recorder: %+v", got)
			}
			if err := got.lease.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHiddenLeaseWaitsForAXMatchAndSuccessfulDismissalRead(t *testing.T) {
	probe := newVisibilityTestProbe()
	baseline := captureVisibilityTestBaseline(t, probe)
	window := visibilityTestWindowRecord(true)
	probe.putWindow(window, false)
	probe.incompleteFirstAXRead = true // CG appeared before AXWindows populated.

	lease, outcome, err := baseline.Suppress(context.Background(), visibilityTestEligible, 500*time.Millisecond, time.Millisecond)
	if err != nil || outcome != OutcomeSuppressed || lease == nil {
		t.Fatalf("Suppress() = (%v, %q, %v), want a lease", lease != nil, outcome, err)
	}
	state := probe.snapshot()
	if state.hideWrites != 1 || !state.hidden {
		t.Fatalf("Hidden state after claim: writes=%d hidden=%v", state.hideWrites, state.hidden)
	}
	if state.retained != 1 {
		t.Fatalf("retained candidate refs=%d, want exactly one lease ref", state.retained)
	}

	ended := make(chan struct{})
	finished := make(chan error, 1)
	go func() { finished <- lease.Maintain(context.Background(), ended) }()
	close(ended)

	// The text window may still be in AXWindows while the final utterance is
	// being committed. Neither a failed read nor continued membership releases
	// the app-wide hidden lease.
	probe.mu.Lock()
	probe.nextSnapshotError = ErrVisibilityUnavailable
	probe.mu.Unlock()
	probe.sendApplicationEvent(ApplicationEventWindowCreated)
	select {
	case err := <-finished:
		t.Fatalf("lease finished before a successful AXWindows exclusion: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if !probe.snapshot().hidden {
		t.Fatal("app became visible while the exact recorder element remained in AXWindows")
	}
	probe.mu.Lock()
	probe.axWindows = nil
	probe.mu.Unlock()
	probe.sendApplicationEvent(ApplicationEventWindowCreated)
	if err := receiveVisibilityResult(t, finished); err != nil {
		t.Fatalf("Maintain() error = %v", err)
	}
	state = probe.snapshot()
	if state.hidden || state.hideWrites != 2 {
		t.Fatalf("state after exact dismissal: hidden=%v writes=%d, want visible after one restore", state.hidden, state.hideWrites)
	}
	if state.retained != 0 || state.observerCloses != 1 {
		t.Fatalf("cleanup refs=%d observer closes=%d, want zero refs and one close", state.retained, state.observerCloses)
	}
}

func TestHiddenLeaseRetriesOnlyDuringInitialSamePanelClaim(t *testing.T) {
	probe := newVisibilityTestProbe()
	baseline := captureVisibilityTestBaseline(t, probe)
	probe.putWindow(visibilityTestWindowRecord(true), true)
	probe.hideNoopCount = 1 // WeType immediately undoes the first initial write.

	lease, outcome, err := baseline.Suppress(context.Background(), visibilityTestEligible, 200*time.Millisecond, time.Millisecond)
	if err != nil || outcome != OutcomeSuppressed || lease == nil {
		t.Fatalf("Suppress() = (%v, %q, %v), want the same-panel guarded retry", lease != nil, outcome, err)
	}
	state := probe.snapshot()
	if state.hideWrites != 2 || !state.hidden {
		t.Fatalf("initial same-panel attempts: writes=%d hidden=%v", state.hideWrites, state.hidden)
	}

	// Once returned to the session worker, an externally visible app releases
	// this lease without another hide attempt.
	probe.setHiddenExternally(false)
	if err := lease.Maintain(context.Background(), nil); err != nil {
		t.Fatalf("Maintain() after external unhide: %v", err)
	}
	state = probe.snapshot()
	if state.hideWrites != 2 || state.hidden {
		t.Fatalf("post-interaction writes=%d hidden=%v; want no re-hide", state.hideWrites, state.hidden)
	}
	if state.retained != 0 || state.observerCloses != 1 {
		t.Fatalf("cleanup refs=%d observer closes=%d", state.retained, state.observerCloses)
	}
}

func TestHiddenLeaseRestoresOnCancellationAndIdentityLossNeverTouchesReusedPID(t *testing.T) {
	t.Run("cancel restores owned Hidden", func(t *testing.T) {
		probe := newVisibilityTestProbe()
		baseline := captureVisibilityTestBaseline(t, probe)
		probe.putWindow(visibilityTestWindowRecord(true), true)
		lease, _, err := baseline.Suppress(context.Background(), visibilityTestEligible, 200*time.Millisecond, time.Millisecond)
		if err != nil || lease == nil {
			t.Fatalf("Suppress() = (%v, %v)", lease != nil, err)
		}
		if err := lease.Restore(context.Background()); err != nil {
			t.Fatalf("Restore() error = %v", err)
		}
		state := probe.snapshot()
		if state.hidden || state.retained != 0 || state.observerCloses != 1 {
			t.Fatalf("restore state hidden=%v refs=%d closes=%d", state.hidden, state.retained, state.observerCloses)
		}
	})

	t.Run("identity loss does not unhide replacement process", func(t *testing.T) {
		probe := newVisibilityTestProbe()
		identityValid := true
		check := func() error {
			if !identityValid {
				return errors.New("process identity changed")
			}
			return nil
		}
		baseline, err := CaptureHiddenBaseline(visibilityTestPID, check, probe)
		if err != nil {
			t.Fatal(err)
		}
		probe.putWindow(visibilityTestWindowRecord(true), true)
		lease, _, err := baseline.Suppress(context.Background(), visibilityTestEligible, 200*time.Millisecond, time.Millisecond)
		if err != nil || lease == nil {
			t.Fatalf("Suppress() = (%v, %v)", lease != nil, err)
		}
		identityValid = false
		if err := lease.Restore(context.Background()); !errors.Is(err, ErrIdentityChanged) {
			t.Fatalf("Restore() error = %v, want identity error", err)
		}
		state := probe.snapshot()
		if state.hideWrites != 1 || !state.hidden {
			t.Fatalf("identity loss wrote Hidden or changed replacement state: writes=%d hidden=%v", state.hideWrites, state.hidden)
		}
		if state.retained != 0 || state.observerCloses != 1 {
			t.Fatalf("identity loss refs=%d closes=%d", state.retained, state.observerCloses)
		}
	})
}

func TestHiddenLeaseRestoresWhenFocusOrAnotherWindowChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*visibilityTestProbe)
	}{
		{name: "focus changed", mutate: func(p *visibilityTestProbe) { p.focusedPID++ }},
		{name: "foreign app window appeared", mutate: func(p *visibilityTestProbe) { p.addForeignAXWindow(818) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := newVisibilityTestProbe()
			baseline := captureVisibilityTestBaseline(t, probe)
			probe.putWindow(visibilityTestWindowRecord(true), true)
			lease, _, err := baseline.Suppress(context.Background(), visibilityTestEligible, 200*time.Millisecond, time.Millisecond)
			if err != nil || lease == nil {
				t.Fatalf("Suppress() = (%v, %v)", lease != nil, err)
			}
			test.mutate(probe)
			if err := lease.Maintain(context.Background(), nil); err != nil {
				t.Fatalf("Maintain() error = %v", err)
			}
			state := probe.snapshot()
			if state.hidden || state.hideWrites != 2 {
				t.Fatalf("changed app scope hidden=%v writes=%d, want immediate restore", state.hidden, state.hideWrites)
			}
			if state.retained != 0 || state.observerCloses != 1 {
				t.Fatalf("changed app scope refs=%d closes=%d", state.retained, state.observerCloses)
			}
		})
	}
}

func TestHiddenLeaseFailsOpenOnIdentityFocusOrForeignWindowBeforeMutation(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*visibilityTestProbe, *bool)
	}{
		{name: "identity changed", setup: func(_ *visibilityTestProbe, identity *bool) { *identity = false }},
		{name: "focused app changed", setup: func(p *visibilityTestProbe, _ *bool) { p.changeFocusOnNextSnapshot = true }},
		{name: "another app window appeared", setup: func(p *visibilityTestProbe, _ *bool) { p.addForeignOnNextSnapshot = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := newVisibilityTestProbe()
			identityValid := true
			check := func() error {
				if !identityValid {
					return errors.New("process identity changed")
				}
				return nil
			}
			baseline, err := CaptureHiddenBaseline(visibilityTestPID, check, probe)
			if err != nil {
				t.Fatal(err)
			}
			probe.putWindow(visibilityTestWindowRecord(true), true)
			test.setup(probe, &identityValid)
			lease, outcome, err := baseline.Suppress(context.Background(), visibilityTestEligible, 100*time.Millisecond, time.Millisecond)
			if lease != nil || outcome == OutcomeSuppressed {
				t.Fatalf("Suppress() = (%v, %q, %v), want fail-open", lease != nil, outcome, err)
			}
			state := probe.snapshot()
			if state.hideWrites != 0 || state.hidden {
				t.Fatalf("mutation guard wrote Hidden: writes=%d hidden=%v", state.hideWrites, state.hidden)
			}
			if state.retained != 0 || state.observerCloses != state.observerStarts {
				t.Fatalf("guard cleanup refs=%d closes=%d", state.retained, state.observerCloses)
			}
		})
	}
}

func TestHiddenLeaseRestoresIfAXReadFailsImmediatelyAfterWrite(t *testing.T) {
	probe := newVisibilityTestProbe()
	baseline := captureVisibilityTestBaseline(t, probe)
	probe.putWindow(visibilityTestWindowRecord(true), true)
	probe.failVisibilityReadAfterWrites = true
	lease, outcome, err := baseline.Suppress(context.Background(), visibilityTestEligible, 200*time.Millisecond, time.Millisecond)
	if err == nil || outcome == OutcomeSuppressed || lease != nil {
		t.Fatalf("Suppress() = (%v, %q, %v), want fail-open", lease != nil, outcome, err)
	}
	state := probe.snapshot()
	if state.hidden || state.hideWrites != 2 {
		t.Fatalf("failed post-write read did not restore baseline: hidden=%v writes=%d", state.hidden, state.hideWrites)
	}
	if state.retained != 0 || state.observerCloses != 1 {
		t.Fatalf("failed post-write cleanup refs=%d closes=%d", state.retained, state.observerCloses)
	}
}

func TestHiddenLeaseDiscoveryCancellationAndNonRecorderDoNotChangeApp(t *testing.T) {
	t.Run("cancel before fresh window", func(t *testing.T) {
		probe := newVisibilityTestProbe()
		baseline := captureVisibilityTestBaseline(t, probe)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		lease, outcome, err := baseline.Suppress(ctx, visibilityTestEligible, time.Second, time.Millisecond)
		if !errors.Is(err, context.Canceled) || outcome != OutcomeCancelled || lease != nil {
			t.Fatalf("Suppress() = (%v, %q, %v), want cancelled", lease != nil, outcome, err)
		}
		state := probe.snapshot()
		if state.hideWrites != 0 || state.observerCloses != state.observerStarts {
			t.Fatalf("cancel writes=%d observer starts=%d closes=%d", state.hideWrites, state.observerStarts, state.observerCloses)
		}
	})

	t.Run("candidate negative", func(t *testing.T) {
		probe := newVisibilityTestProbe()
		baseline := captureVisibilityTestBaseline(t, probe)
		window := visibilityTestWindowRecord(true)
		window.Role = "AXGroup"
		probe.putWindow(window, true)
		lease, outcome, err := baseline.Suppress(context.Background(), visibilityTestEligible, 200*time.Millisecond, time.Millisecond)
		if err != nil || outcome != OutcomeUnchanged || lease != nil {
			t.Fatalf("Suppress() = (%v, %q, %v), want unchanged", lease != nil, outcome, err)
		}
		if probe.hideWrites != 0 || probe.retained[visibilityTestElement] != 0 || probe.events.closeCount != 1 {
			t.Fatalf("negative writes=%d refs=%d observer closes=%d", probe.hideWrites, probe.retained[visibilityTestElement], probe.events.closeCount)
		}
	})
}

func captureVisibilityTestBaseline(t *testing.T, probe *visibilityTestProbe) *HiddenBaseline {
	t.Helper()
	baseline, err := CaptureHiddenBaseline(visibilityTestPID, func() error { return nil }, probe)
	if err != nil {
		t.Fatalf("CaptureHiddenBaseline() error = %v", err)
	}
	return baseline
}

func visibilityTestEligible(window Window) bool {
	return window.OwnerPID == visibilityTestPID && window.ID == visibilityTestWindow &&
		window.Layer == 17 && window.Role == "AXWindow" && window.Subrole == "AXSystemDialog" &&
		window.AXMatchCount == 1 && window.Element == visibilityTestElement && window.PositionSettable
}

func visibilityTestWindowRecord(onScreen bool) Window {
	return Window{
		ID: visibilityTestWindow, OwnerPID: visibilityTestPID, Layer: 17,
		Frame: Rect{Origin: Point{X: 860, Y: 980}, Size: Size{Width: 200, Height: 130}}, OnScreen: onScreen,
		Role: "AXWindow", Subrole: "AXSystemDialog", Element: visibilityTestElement,
		Position: Point{X: 860, Y: 980}, Size: Size{Width: 200, Height: 130},
		AXMatchCount: 1, PositionSettable: true,
	}
}

func receiveVisibilityResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for visibility lease result")
		return nil
	}
}

type visibilityTestProbe struct {
	mu                            sync.Mutex
	windows                       map[uint32]Window
	axWindows                     []uintptr
	hidden                        bool
	focusedPID                    int32
	retained                      map[uintptr]int
	hideWrites                    int
	hideNoopCount                 int
	nextSnapshotError             error
	failVisibilityReadAfterWrites bool
	changeFocusOnNextSnapshot     bool
	addForeignOnNextSnapshot      bool
	incompleteFirstAXRead         bool
	windowOnScreenOnIDRead        bool
	showOnFirstHide               bool
	events                        *visibilityTestEvents
}

type visibilityTestSnapshot struct {
	hidden         bool
	hideWrites     int
	retained       int
	observerStarts int
	observerCloses int
}

func (p *visibilityTestProbe) snapshot() visibilityTestSnapshot {
	p.mu.Lock()
	state := visibilityTestSnapshot{
		hidden: p.hidden, hideWrites: p.hideWrites,
		retained: p.retained[visibilityTestElement],
	}
	p.mu.Unlock()
	p.events.mu.Lock()
	state.observerStarts = p.events.watchCount
	state.observerCloses = p.events.closeCount
	p.events.mu.Unlock()
	return state
}

func newVisibilityTestProbe() *visibilityTestProbe {
	probe := &visibilityTestProbe{
		windows: make(map[uint32]Window), focusedPID: visibilityTestFocus,
		retained: make(map[uintptr]int), events: &visibilityTestEvents{events: make(chan ApplicationNotice, 8)},
	}
	probe.events.release = probe.Release
	return probe
}

func (p *visibilityTestProbe) putWindow(window Window, axVisible bool) {
	p.mu.Lock()
	p.windows[window.ID] = window
	if axVisible {
		p.axWindows = append(p.axWindows[:0], window.Element)
	}
	p.mu.Unlock()
}

func (p *visibilityTestProbe) addForeignAXWindow(element uintptr) {
	p.mu.Lock()
	p.axWindows = append(p.axWindows, element)
	p.mu.Unlock()
}

func (p *visibilityTestProbe) setHiddenExternally(hidden bool) {
	p.mu.Lock()
	p.hidden = hidden
	p.mu.Unlock()
}

func (p *visibilityTestProbe) sendApplicationEvent(event ApplicationEvent) {
	notice := ApplicationNotice{Event: event}
	if event == ApplicationEventWindowCreated {
		notice.Element = visibilityTestElement
		p.Retain(notice.Element)
	}
	p.events.events <- notice
}

func (p *visibilityTestProbe) WindowNumbers(pid int32) (map[uint32]struct{}, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[uint32]struct{})
	for id, window := range p.windows {
		if window.OwnerPID == pid && window.OnScreen {
			result[id] = struct{}{}
		}
	}
	return result, nil
}

func (p *visibilityTestProbe) Windows(pid int32, newerThan map[uint32]struct{}) ([]Window, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]Window, 0, len(p.windows))
	for id, window := range p.windows {
		if window.OwnerPID != pid || !window.OnScreen {
			continue
		}
		if _, old := newerThan[id]; old {
			continue
		}
		if p.incompleteFirstAXRead {
			p.incompleteFirstAXRead = false
			window.AXMatchCount = 0
			window.Element = 0
			p.windows[id] = window
			result = append(result, window)
			full := visibilityTestWindowRecord(true)
			p.windows[id] = full
			p.axWindows = []uintptr{full.Element}
			continue
		}
		if window.Element != 0 {
			p.retained[window.Element]++
		}
		result = append(result, window)
	}
	return result, nil
}

func (p *visibilityTestProbe) WindowByID(pid int32, id uint32) (Window, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.windowOnScreenOnIDRead {
		p.windowOnScreenOnIDRead = false
		window := p.windows[id]
		window.OnScreen = true
		p.windows[id] = window
	}
	window, ok := p.windows[id]
	return window, ok && window.OwnerPID == pid, nil
}

func (p *visibilityTestProbe) WindowForPrepaint(pid int32, element uintptr) (Window, int, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	onScreenCount := 0
	for _, window := range p.windows {
		if window.OwnerPID == pid && window.OnScreen {
			onScreenCount++
		}
	}
	for _, window := range p.windows {
		if window.OwnerPID == pid && window.Element == element {
			return window, onScreenCount, window.AXMatchCount == 1, nil
		}
	}
	return Window{}, onScreenCount, false, nil
}

func (p *visibilityTestProbe) setWindowOnScreen(id uint32, onScreen bool) {
	p.mu.Lock()
	window := p.windows[id]
	window.OnScreen = onScreen
	p.windows[id] = window
	p.mu.Unlock()
}

func (p *visibilityTestProbe) SetPosition(element uintptr, point Point) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, window := range p.windows {
		if window.Element == element {
			window.Position = point
			window.Frame.Origin = point
			p.windows[id] = window
		}
	}
	return nil
}

func (p *visibilityTestProbe) Position(element uintptr) (Point, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, window := range p.windows {
		if window.Element == element {
			return window.Position, nil
		}
	}
	return Point{}, ErrVisibilityUnavailable
}

func (p *visibilityTestProbe) Size(element uintptr) (Size, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, window := range p.windows {
		if window.Element == element {
			return window.Size, nil
		}
	}
	return Size{}, ErrVisibilityUnavailable
}

func (p *visibilityTestProbe) ElementPID(element uintptr) (int32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, window := range p.windows {
		if window.Element == element {
			return window.OwnerPID, nil
		}
	}
	return 0, ErrVisibilityUnavailable
}

func (p *visibilityTestProbe) Retain(element uintptr) uintptr {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retained[element]++
	return element
}

func (p *visibilityTestProbe) Release(element uintptr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retained[element]--
}

func (p *visibilityTestProbe) ApplicationVisibility(pid int32, element uintptr) (ApplicationVisibility, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.changeFocusOnNextSnapshot {
		p.changeFocusOnNextSnapshot = false
		p.focusedPID++
	}
	if p.addForeignOnNextSnapshot {
		p.addForeignOnNextSnapshot = false
		p.axWindows = append(p.axWindows, visibilityTestElement+1)
	}
	if p.failVisibilityReadAfterWrites && p.hideWrites > 0 {
		p.failVisibilityReadAfterWrites = false
		return ApplicationVisibility{}, ErrVisibilityUnavailable
	}
	if p.nextSnapshotError != nil {
		err := p.nextSnapshotError
		p.nextSnapshotError = nil
		return ApplicationVisibility{}, err
	}
	state := ApplicationVisibility{Hidden: p.hidden, WindowCount: len(p.axWindows), FocusedPID: p.focusedPID}
	for _, current := range p.axWindows {
		if element != 0 && current == element {
			state.ContainsElement = true
		}
	}
	return state, nil
}

func (p *visibilityTestProbe) SetApplicationHidden(pid int32, hidden bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hideWrites++
	if hidden && p.showOnFirstHide {
		p.showOnFirstHide = false
		p.hidden = false
		window := p.windows[visibilityTestWindow]
		window.OnScreen = true
		p.windows[visibilityTestWindow] = window
		return nil
	}
	if hidden && p.hideNoopCount > 0 {
		p.hideNoopCount--
		return nil
	}
	p.hidden = hidden
	return nil
}

func (p *visibilityTestProbe) WatchApplication(pid int32) (ApplicationEventSource, error) {
	p.events.mu.Lock()
	p.events.watchCount++
	p.events.mu.Unlock()
	return p.events, nil
}

type visibilityTestEvents struct {
	events     chan ApplicationNotice
	release    func(uintptr)
	mu         sync.Mutex
	watchCount int
	closeCount int
	closeOnce  sync.Once
}

func (e *visibilityTestEvents) Events() <-chan ApplicationNotice { return e.events }

func (e *visibilityTestEvents) Close() {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closeCount++
		e.mu.Unlock()
		close(e.events)
		for notice := range e.events {
			if notice.Element != 0 && e.release != nil {
				e.release(notice.Element)
			}
		}
	})
}
