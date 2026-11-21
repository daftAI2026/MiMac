package permissionguide

import "testing"

func TestBluetoothRequestObserverDeduplicatesAndClearsCallbacks(t *testing.T) {
	var observer bluetoothRequestObserver
	firstCalls, latestCalls := 0, 0
	first := func() { firstCalls++ }
	latest := func() { latestCalls++ }

	if !observer.install(11, 21, first) {
		t.Fatal("first manager was not installed")
	}
	if observer.install(12, 22, latest) {
		t.Fatal("repeat request installed a second manager")
	}
	observer.updateHandler(latest)
	if observer.handlerFor(11, 22) != nil || observer.handlerFor(12, 22) != nil {
		t.Fatal("callback from a stale manager or delegate was accepted")
	}
	callback := observer.handlerFor(11, 21)
	if callback == nil {
		t.Fatal("current manager callback was not retained")
	}
	callback()
	if firstCalls != 0 || latestCalls != 1 {
		t.Fatalf("repeat request callback counts = first %d, latest %d", firstCalls, latestCalls)
	}

	manager, delegate := observer.take()
	if manager != 11 || delegate != 21 || observer.hasManager() {
		t.Fatalf("closed request IDs = %d/%d, pending=%t", manager, delegate, observer.hasManager())
	}
	if observer.handlerFor(11, 21) != nil {
		t.Fatal("closed request retained its callback")
	}
}
