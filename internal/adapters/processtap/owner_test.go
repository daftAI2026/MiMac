package processtap

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeNativeHandle struct {
	mu          sync.Mutex
	uid         string
	closeErrors []error
	closeCalls  int
	closeStart  chan struct{}
	closeWait   chan struct{}
}

// A value of this type cannot be compared because it contains a slice. The
// owner must not depend on interface dynamic values being comparable.
type nonComparableNativeHandle struct {
	uid        string
	state      *int
	nonCompare []byte
}

func (h nonComparableNativeHandle) InputUID() string { return h.uid }

func (h nonComparableNativeHandle) Close() error {
	(*h.state)++
	return nil
}

func (h *fakeNativeHandle) InputUID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.uid
}

func (h *fakeNativeHandle) Close() error {
	h.mu.Lock()
	h.closeCalls++
	closeStart, closeWait := h.closeStart, h.closeWait
	var err error
	if len(h.closeErrors) != 0 {
		err = h.closeErrors[0]
		h.closeErrors = h.closeErrors[1:]
	}
	h.mu.Unlock()

	if closeStart != nil {
		select {
		case closeStart <- struct{}{}:
		default:
		}
	}
	if closeWait != nil {
		<-closeWait
	}
	return err
}

func (h *fakeNativeHandle) closes() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closeCalls
}

func TestOpenWithExposesUIDOnlyAfterSuccessfulPreparation(t *testing.T) {
	handle := &fakeNativeHandle{uid: "dev.mimac.input.test"}
	owner, err := openWith(func() (nativeHandle, error) { return handle, nil })
	if err != nil {
		t.Fatalf("openWith: %v", err)
	}
	if owner == nil {
		t.Fatal("openWith returned a nil owner")
	}
	if got := owner.InputUID(); got != handle.uid {
		t.Fatalf("InputUID() = %q, want %q", got, handle.uid)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := owner.InputUID(); got != "" {
		t.Fatalf("InputUID() after Close = %q, want empty", got)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	if got := handle.closes(); got != 1 {
		t.Fatalf("native Close calls = %d, want 1", got)
	}
}

func TestOpenWithRetainsPartialHandleForCleanup(t *testing.T) {
	createErr := errors.New("aggregate creation failed after tap creation")
	closeErr := errors.New("partial endpoint cleanup failed")
	handle := &fakeNativeHandle{uid: "dev.mimac.input.partial", closeErrors: []error{closeErr}}
	owner, err := openWith(func() (nativeHandle, error) { return handle, createErr })
	if !errors.Is(err, createErr) {
		t.Fatalf("openWith error = %v, want create error", err)
	}
	if owner == nil {
		t.Fatal("partial native handle was not retained in an owner")
	}
	if got := owner.InputUID(); got != "" {
		t.Fatalf("InputUID() for partial creation = %q, want empty", got)
	}
	if err := owner.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first Close = %v, want cleanup error", err)
	}
	if got := owner.InputUID(); got != "" {
		t.Fatalf("InputUID() after failed Close = %q, want empty", got)
	}
	if got := handle.closes(); got != 1 {
		t.Fatalf("native Close calls after failure = %d, want 1", got)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("Close retry: %v", err)
	}
	if got := handle.closes(); got != 2 {
		t.Fatalf("native Close calls after retry = %d, want 2", got)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("Close after successful retry: %v", err)
	}
	if got := handle.closes(); got != 2 {
		t.Fatalf("native Close calls after idempotent retry = %d, want 2", got)
	}
}

func TestOpenWithRejectsMissingFactoryHandleAndUID(t *testing.T) {
	if owner, err := openWith(nil); owner != nil || !errors.Is(err, ErrFactoryRequired) {
		t.Fatalf("openWith(nil) = (%v, %v), want nil owner and factory error", owner, err)
	}
	if owner, err := openWith(func() (nativeHandle, error) { return nil, nil }); owner != nil || !errors.Is(err, ErrNativeHandleMissing) {
		t.Fatalf("openWith(nil handle) = (%v, %v), want nil owner and missing-handle error", owner, err)
	}

	handle := &fakeNativeHandle{}
	owner, err := openWith(func() (nativeHandle, error) { return handle, nil })
	if owner == nil || !errors.Is(err, ErrInputUIDMissing) {
		t.Fatalf("openWith(empty UID) = (%v, %v), want retained owner and missing-UID error", owner, err)
	}
	if got := owner.InputUID(); got != "" {
		t.Fatalf("InputUID() for missing UID = %q, want empty", got)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("cleanup handle with missing UID: %v", err)
	}
}

func TestCloseSupportsNonComparableNativeHandle(t *testing.T) {
	closeCalls := 0
	handle := nonComparableNativeHandle{
		uid:        "dev.mimac.input.non-comparable",
		state:      &closeCalls,
		nonCompare: []byte{1},
	}
	owner, err := openWith(func() (nativeHandle, error) { return handle, nil })
	if err != nil {
		t.Fatalf("openWith: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("native Close calls = %d, want 1", closeCalls)
	}
}

func TestInputUIDDoesNotBlockDuringNativeClose(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	handle := &fakeNativeHandle{
		uid:        "dev.mimac.input.closing",
		closeStart: started,
		closeWait:  release,
	}
	owner, err := openWith(func() (nativeHandle, error) { return handle, nil })
	if err != nil {
		t.Fatal(err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- owner.Close() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("native Close did not start")
	}

	uidDone := make(chan string, 1)
	go func() { uidDone <- owner.InputUID() }()
	select {
	case got := <-uidDone:
		if got != "" {
			t.Fatalf("InputUID() during Close = %q, want empty", got)
		}
	case <-time.After(time.Second):
		t.Fatal("InputUID() blocked behind native Close")
	}

	close(release)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after native call was released")
	}
}
