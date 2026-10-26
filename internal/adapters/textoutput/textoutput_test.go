package textoutput

import (
	"errors"
	"strings"
	"testing"
)

type fakeNative struct {
	current     snapshot
	secure      bool
	noneditable bool
	insertErr   error
	insertCalls []string
	closeCalls  int
}

func (n *fakeNative) capture() (snapshot, error) {
	if n.secure {
		return snapshot{}, ErrSecureInput
	}
	if n.noneditable {
		return snapshot{}, ErrNotEditable
	}
	n.current = snapshot{processID: 101, element: 202, selection: 303}
	return n.current, nil
}

func (n *fakeNative) insert(expected snapshot, text string) error {
	if n.current != expected {
		return ErrTargetChanged
	}
	n.insertCalls = append(n.insertCalls, text)
	return n.insertErr
}

func (n *fakeNative) close(snapshot) error {
	n.closeCalls++
	return nil
}

func TestCaptureFreezesAndSubmitsOnce(t *testing.T) {
	n := &fakeNative{}
	target, err := captureWith(n)
	if err != nil {
		t.Fatal(err)
	}

	if err := target.Insert("hello 世界"); err != nil {
		t.Fatal(err)
	}
	if len(n.insertCalls) != 1 || n.insertCalls[0] != "hello 世界" {
		t.Fatalf("insert calls = %#v", n.insertCalls)
	}
	if err := target.Insert("again"); !errors.Is(err, ErrAlreadySubmitted) {
		t.Fatalf("second Insert error = %v, want ErrAlreadySubmitted", err)
	}
	if len(n.insertCalls) != 1 {
		t.Fatalf("second Insert reached native interface: %#v", n.insertCalls)
	}
}

func TestInsertRejectsChangedTargetAndConsumesAttempt(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*snapshot)
	}{
		{name: "process", change: func(s *snapshot) { s.processID++ }},
		{name: "focused element", change: func(s *snapshot) { s.element++ }},
		{name: "selection", change: func(s *snapshot) { s.selection++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			n := &fakeNative{}
			target, err := captureWith(n)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&n.current)

			if err := target.Insert("must stay out of the new focus"); !errors.Is(err, ErrTargetChanged) {
				t.Fatalf("Insert error = %v, want ErrTargetChanged", err)
			}
			if len(n.insertCalls) != 0 {
				t.Fatalf("changed target reached output call: %#v", n.insertCalls)
			}
			if err := target.Insert("retry"); !errors.Is(err, ErrAlreadySubmitted) {
				t.Fatalf("retry error = %v, want ErrAlreadySubmitted", err)
			}
		})
	}
}

func TestCaptureRejectsSecureAndNoneditableTargets(t *testing.T) {
	for _, test := range []struct {
		name        string
		secure      bool
		noneditable bool
		err         error
	}{
		{name: "secure", secure: true, err: ErrSecureInput},
		{name: "noneditable", noneditable: true, err: ErrNotEditable},
	} {
		t.Run(test.name, func(t *testing.T) {
			n := &fakeNative{secure: test.secure, noneditable: test.noneditable}
			if target, err := captureWith(n); target != nil || !errors.Is(err, test.err) {
				t.Fatalf("Capture = (%v, %v), want (nil, %v)", target, err, test.err)
			}
		})
	}
}

func TestCloseReleasesOnceAndPreventsInsert(t *testing.T) {
	n := &fakeNative{}
	target, err := captureWith(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	if n.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", n.closeCalls)
	}
	if err := target.Insert("closed"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Insert after Close error = %v, want ErrClosed", err)
	}
	if len(n.insertCalls) != 0 {
		t.Fatalf("closed target reached output call: %#v", n.insertCalls)
	}
}

func TestEmptyRecognitionDoesNotSubmit(t *testing.T) {
	n := &fakeNative{}
	target, err := captureWith(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Insert(""); err != nil {
		t.Fatal(err)
	}
	if len(n.insertCalls) != 0 {
		t.Fatalf("empty recognition reached output call: %#v", n.insertCalls)
	}
	if err := target.Insert("final"); err != nil {
		t.Fatal(err)
	}
	if len(n.insertCalls) != 1 {
		t.Fatalf("non-empty final output calls = %#v", n.insertCalls)
	}
}

func TestInsertValidatesTextAndNativeFailureIsOneShot(t *testing.T) {
	n := &fakeNative{}
	target, err := captureWith(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Insert("bad\x00text"); !errors.Is(err, ErrNUL) {
		t.Fatalf("NUL Insert error = %v, want ErrNUL", err)
	}
	if err := target.Insert(string([]byte{0xff})); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("invalid UTF-8 Insert error = %v, want ErrInvalidUTF8", err)
	}
	if err := target.Insert(strings.Repeat("x", MaxTextBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized Insert error = %v, want ErrTooLarge", err)
	}
	if len(n.insertCalls) != 0 {
		t.Fatalf("invalid text reached output call: %#v", n.insertCalls)
	}

	n.insertErr = errors.New("AX request failed")
	if err := target.Insert("one attempt"); err == nil {
		t.Fatal("Insert error = nil, want native request failure")
	}
	if err := target.Insert("must not retry"); !errors.Is(err, ErrAlreadySubmitted) {
		t.Fatalf("second Insert error = %v, want ErrAlreadySubmitted", err)
	}
	if len(n.insertCalls) != 1 {
		t.Fatalf("native call count = %d, want 1", len(n.insertCalls))
	}
}
