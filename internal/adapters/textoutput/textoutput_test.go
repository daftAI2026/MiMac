package textoutput

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeNative struct {
	current         snapshot
	secure          bool
	noneditable     bool
	captureRequests []captureRequest
	insertErr       error
	insertCalls     []string
	closeCalls      int
}

func (n *fakeNative) capture(ctx context.Context, request captureRequest) (snapshot, error) {
	if err := ctx.Err(); err != nil {
		return snapshot{}, err
	}
	n.captureRequests = append(n.captureRequests, request)
	if n.secure {
		return snapshot{}, ErrSecureInput
	}
	if n.noneditable {
		return snapshot{}, ErrNotEditable
	}
	n.current = snapshot{processID: 101, element: 202, selection: 303}
	return n.current, nil
}

func (n *fakeNative) checkFocus(expected snapshot) error {
	if n.current != expected {
		return ErrTargetChanged
	}
	return nil
}
func (n *fakeNative) sameField(a, b snapshot) bool {
	return a.processID == b.processID && a.element == b.element
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

func TestCaptureRequestBindsPIDAndPassesPrepareIntent(t *testing.T) {
	n := &fakeNative{}
	target, err := captureRequestWithContext(context.Background(), n, captureRequest{pid: 101, prepareEditor: true})
	if err != nil || target == nil {
		t.Fatalf("prepare request = (%v, %v), want captured target", target, err)
	}
	if n.captureRequests[0] != (captureRequest{pid: 101, prepareEditor: true}) {
		t.Fatalf("native capture request = %+v, want bound prepare request", n.captureRequests[0])
	}
	_ = target.Close()

	n = &fakeNative{}
	if target, err := captureRequestWithContext(context.Background(), n, captureRequest{pid: 202}); target != nil || !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("wrong-PID request = (%v, %v), want (nil, ErrTargetChanged)", target, err)
	}
	if n.closeCalls != 1 {
		t.Fatalf("mismatched captured target close calls = %d, want 1", n.closeCalls)
	}
}

type requestRecordingNative struct {
	fakeNative
	requests chan captureRequest
}

func (n *requestRecordingNative) capture(ctx context.Context, request captureRequest) (snapshot, error) {
	captured, err := n.fakeNative.capture(ctx, request)
	if err == nil {
		n.requests <- request
	}
	return captured, err
}

func TestDictationAndObservationCaptureIntentsStaySeparate(t *testing.T) {
	for _, test := range []struct {
		name          string
		prepareEditor bool
	}{
		{name: "Apple and Hex BeginDictation", prepareEditor: true},
		{name: "Doubao ObserveDictationSelection"},
	} {
		t.Run(test.name, func(t *testing.T) {
			n := &requestRecordingNative{requests: make(chan captureRequest, 1)}
			var captureOwner interface{ Close() error }
			if test.prepareEditor {
				captureOwner = beginDictationForProcess(n, 101, true)
			} else {
				captureOwner = observeDictationSelectionForProcess(n, 101)
			}
			select {
			case request := <-n.requests:
				if request != (captureRequest{pid: 101, prepareEditor: test.prepareEditor}) {
					t.Fatalf("native request = %+v, want PID 101 prepareEditor=%t", request, test.prepareEditor)
				}
			case <-time.After(time.Second):
				t.Fatal("dictation capture worker did not reach native adapter")
			}
			_ = captureOwner.Close()
			waitForEditorProbeRelease(t)
		})
	}
}

func TestGenericCaptureUsesReadOnlyRequest(t *testing.T) {
	n := &fakeNative{}
	target, err := captureWith(n)
	if err != nil || target == nil {
		t.Fatalf("generic Capture = (%v, %v), want captured target", target, err)
	}
	if len(n.captureRequests) != 1 || n.captureRequests[0] != (captureRequest{}) {
		t.Fatalf("generic Capture request = %#v, want read-only zero request", n.captureRequests)
	}
	_ = target.Close()
}

type cancelCaptureFakeNative struct {
	fakeNative
	started    chan struct{}
	canceled   chan struct{}
	startOnce  sync.Once
	cancelOnce sync.Once
}

func (n *cancelCaptureFakeNative) capture(ctx context.Context, request captureRequest) (snapshot, error) {
	n.startOnce.Do(func() { close(n.started) })
	<-ctx.Done()
	n.cancelOnce.Do(func() { close(n.canceled) })
	return snapshot{}, ctx.Err()
}

func TestDictationCloseCancelsPendingBoundedCapture(t *testing.T) {
	n := &cancelCaptureFakeNative{started: make(chan struct{}), canceled: make(chan struct{})}
	d := beginDictationForProcess(n, 101, true)
	select {
	case <-n.started:
	case <-time.After(time.Second):
		t.Fatal("capture worker did not start")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-n.canceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the pending capture worker")
	}
	if err := d.Insert("must not be written after cancellation"); !errors.Is(err, ErrAlreadySubmitted) {
		t.Fatalf("Insert after Close = %v, want ErrAlreadySubmitted", err)
	}
	waitForEditorProbeRelease(t)
	if len(n.insertCalls) != 0 {
		t.Fatalf("canceled capture reached native Insert: %#v", n.insertCalls)
	}
}

func waitForEditorProbeRelease(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !editorProbe.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("capture worker did not release its probe lease")
		}
		time.Sleep(time.Millisecond)
	}
	editorProbe.Unlock()
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
