// Package processtap owns a native process tap and its input aggregate.
//
// It only exposes the aggregate input UID. That UID is not a playback route and
// does not mean a target application has selected or started capturing it.
package processtap

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrFactoryRequired     = errors.New("processtap: native handle factory is required")
	ErrNativeHandleMissing = errors.New("processtap: native factory returned no handle")
	ErrInputUIDMissing     = errors.New("processtap: native handle returned no input UID")
)

type nativeHandle interface {
	InputUID() string
	Close() error
}

type nativeHandleFactory func() (nativeHandle, error)

// Owner retains the native endpoint until Close succeeds. InputUID is cached
// after successful creation, so reading it never calls into the native layer.
// Close calls are serialized, but the owner mutex is not held during native
// teardown. Call Open and Close on a worker because the native calls are
// synchronous and aren't cancellable here.
type Owner struct {
	mu      sync.RWMutex
	closeMu sync.Mutex

	handle   nativeHandle
	inputUID string
	ready    bool
}

// Open creates the process tap and public input aggregate from the native
// library at libraryPath. A partial native handle is returned with an error so
// the caller can retain it and retry Close. A non-nil Owner must be closed even
// when Open returns an error.
func Open(libraryPath string) (*Owner, error) {
	handle, err := openNativeHandle(libraryPath)
	return ownerFromHandle(handle, err)
}

// openWith is the fakeable owner-construction seam used by package tests.
func openWith(factory nativeHandleFactory) (*Owner, error) {
	if factory == nil {
		return nil, ErrFactoryRequired
	}

	handle, err := factory()
	return ownerFromHandle(handle, err)
}

func ownerFromHandle(handle nativeHandle, err error) (*Owner, error) {
	if handle == nil {
		if err != nil {
			return nil, fmt.Errorf("processtap: open: %w", err)
		}
		return nil, ErrNativeHandleMissing
	}

	owner := &Owner{handle: handle}
	if err != nil {
		return owner, fmt.Errorf("processtap: open: %w", err)
	}

	uid := handle.InputUID()
	if uid == "" {
		return owner, ErrInputUIDMissing
	}
	owner.inputUID = uid
	owner.ready = true
	return owner, nil
}

// InputUID returns the aggregate's input UID only after native creation
// succeeds and until teardown begins. It does not indicate that a consumer has
// selected the endpoint, received permission, or started capture.
func (o *Owner) InputUID() string {
	if o == nil {
		return ""
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if !o.ready {
		return ""
	}
	return o.inputUID
}

// Close requires the host to stop its PCM producer and confirm that consumers
// have stopped using this endpoint. This owner cannot stop another process.
// Close destroys the aggregate and tap through the native handle. On failure
// the handle remains owned for a later retry. Native teardown must run on a
// worker, never on the UI thread.
func (o *Owner) Close() error {
	if o == nil {
		return nil
	}
	o.closeMu.Lock()
	defer o.closeMu.Unlock()

	o.mu.Lock()
	handle := o.handle
	if handle == nil {
		o.ready = false
		o.inputUID = ""
		o.mu.Unlock()
		return nil
	}
	o.ready = false
	o.inputUID = ""
	o.mu.Unlock()

	if err := handle.Close(); err != nil {
		return fmt.Errorf("processtap: close: %w", err)
	}

	o.mu.Lock()
	// closeMu guarantees that no other Close can replace the owned handle.
	// Avoid comparing interface values here: nativeHandle implementations are
	// allowed to have non-comparable dynamic value types.
	o.handle = nil
	o.mu.Unlock()
	return nil
}
