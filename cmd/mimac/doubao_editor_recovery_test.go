package main

import (
	"context"
	"errors"
	"slices"
	"testing"

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
	changed bool
	closed  bool
}

func (p *interruptedEditorProbe) CheckFocus() error {
	if p.changed {
		return errors.New("editor changed")
	}
	return nil
}
func (p *interruptedEditorProbe) Close() error { p.closed = true; return nil }

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
