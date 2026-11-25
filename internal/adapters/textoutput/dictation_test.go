package textoutput

import (
	"errors"
	"testing"
)

func TestDictationDoesNotWaitForOrRetargetAnEditor(t *testing.T) {
	for _, test := range []struct {
		name       string
		keyDownPID int32
		late       bool
		cancelled  bool
	}{
		{name: "editor did not answer before finish", keyDownPID: 101, late: true},
		{name: "user switched applications", keyDownPID: 202},
		{name: "late response to cancelled dictation", keyDownPID: 101, cancelled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &DictationTarget{}
			n := &fakeNative{}
			target, err := captureWith(n)
			if err != nil {
				t.Fatal(err)
			}
			if test.cancelled {
				_ = d.Close()
			}
			if !test.late {
				d.attach(test.keyDownPID, target)
			}
			want := ErrNoInsertionTarget
			if test.cancelled {
				want = ErrAlreadySubmitted
			}
			if err := d.Insert("合成短句"); !errors.Is(err, want) {
				t.Fatal(err)
			}
			if test.late {
				d.attach(test.keyDownPID, target)
			}
			_ = d.Close()
			if n.closeCalls != 1 || len(n.insertCalls) != 0 {
				t.Fatal("late or unrelated editor retained or written")
			}
		})
	}
}

func TestDictationWritesOnceWhenOriginalEditorIsReady(t *testing.T) {
	d := &DictationTarget{}
	n := &fakeNative{}
	target, err := captureWith(n)
	if err != nil {
		t.Fatal(err)
	}
	d.attach(101, target)
	if err := d.Insert("合成短句"); err != nil {
		t.Fatal(err)
	}
	if err := d.Insert("合成短句"); !errors.Is(err, ErrAlreadySubmitted) {
		t.Fatal(err)
	}
	_ = d.Close()
	if n.closeCalls != 1 || len(n.insertCalls) != 1 {
		t.Fatal("expected one write and one release")
	}
}

func TestDictationKeepsFinalWhenOriginalSelectionMoves(t *testing.T) {
	d := &DictationTarget{}
	n := &fakeNative{}
	target, err := captureWith(n)
	if err != nil {
		t.Fatal(err)
	}
	d.attach(101, target)
	n.current.selection++
	if err := d.Insert("合成短句"); !errors.Is(err, ErrNoInsertionTarget) {
		t.Fatal(err)
	}
	if len(n.insertCalls) != 0 {
		t.Fatal("wrote to the changed selection")
	}
	_ = d.Close()
}

func TestFocusChangeAfterReplacementDoesNotOfferDuplicateText(t *testing.T) {
	d := &DictationTarget{}
	n := &fakeNative{insertErr: errors.Join(errReplacementAccepted, ErrTargetChanged)}
	target, err := captureWith(n)
	if err != nil {
		t.Fatal(err)
	}
	d.attach(101, target)
	defer d.Close()
	if err := d.Insert("合成短句"); err != nil || len(n.insertCalls) != 1 {
		t.Fatalf("accepted replacement: writes=%d err=%v", len(n.insertCalls), err)
	}
}

func TestInterruptedCompositionKeepsEditorAndRechecksSelection(t *testing.T) {
	n := &fakeNative{}
	initial, err := captureWith(n)
	if err != nil {
		t.Fatal(err)
	}
	d := &DictationTarget{target: initial}
	defer d.Close()
	current, err := d.CaptureCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if err := current.CheckFocus(); err != nil {
		t.Fatal(err)
	}
	n.current.selection++
	if err := current.CheckFocus(); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("selection changed: %v", err)
	}
	if len(n.insertCalls) != 0 {
		t.Fatal("read-only focus validation inserted text")
	}
}
