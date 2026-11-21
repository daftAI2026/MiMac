package permissionguide

import "sync"

// bluetoothRequestObserver keeps the one explicit authorization request and
// its notification tied to the manager that produced the callback.
type bluetoothRequestObserver struct {
	mu            sync.Mutex
	manager       uintptr
	delegate      uintptr
	onStateUpdate func()
}

func (o *bluetoothRequestObserver) hasManager() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.manager != 0
}

func (o *bluetoothRequestObserver) install(manager, delegate uintptr, onStateUpdate func()) bool {
	if manager == 0 || delegate == 0 {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.manager != 0 {
		return false
	}
	o.manager, o.delegate, o.onStateUpdate = manager, delegate, onStateUpdate
	return true
}

func (o *bluetoothRequestObserver) updateHandler(onStateUpdate func()) {
	if onStateUpdate == nil {
		return
	}
	o.mu.Lock()
	if o.manager != 0 {
		o.onStateUpdate = onStateUpdate
	}
	o.mu.Unlock()
}

func (o *bluetoothRequestObserver) handlerFor(manager, delegate uintptr) func() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if manager == 0 || manager != o.manager || delegate != o.delegate {
		return nil
	}
	return o.onStateUpdate
}

func (o *bluetoothRequestObserver) take() (manager, delegate uintptr) {
	o.mu.Lock()
	manager, delegate = o.manager, o.delegate
	o.manager, o.delegate, o.onStateUpdate = 0, 0, nil
	o.mu.Unlock()
	return manager, delegate
}
