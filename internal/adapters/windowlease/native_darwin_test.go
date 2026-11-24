//go:build darwin

package windowlease

import (
	"testing"
	"unsafe"
)

func TestWindowAXCallbackAcceptsOnlyTheRetainedElement(t *testing.T) {
	const observer uintptr = 73
	const element uintptr = 91
	events := make(chan WindowEvent, 2)
	api := &presentationNativeAPI{cfEqual: func(a, b uintptr) uint8 {
		if a == b {
			return 1
		}
		return 0
	}}
	windowAXObservers.Store(observer, windowAXEventTarget{api: api, element: element, events: events})
	t.Cleanup(func() { windowAXObservers.Delete(observer) })

	dispatchWindowAXNotification(observer, element+1, 0, windowAXMovedRefcon)
	select {
	case event := <-events:
		t.Fatalf("unbound AX element produced event %d", event)
	default:
	}
	dispatchWindowAXNotification(observer, element, 0, windowAXMovedRefcon)
	select {
	case event := <-events:
		if event != WindowEventMoved {
			t.Fatalf("bound AX event = %d, want move", event)
		}
	default:
		t.Fatal("bound AX element event was not forwarded")
	}
}

func TestAXGeometryRejectsNonAXValuesBeforeReadingThem(t *testing.T) {
	api := &presentationNativeAPI{
		cfGetTypeID:      func(uintptr) uintptr { return 1 },
		axValueGetTypeID: func() uintptr { return 2 },
		axValueGetType:   func(uintptr) uint32 { panic("AXValueGetType received a non-AXValue CF object") },
		axValueGetValue:  func(uintptr, uint32, unsafe.Pointer) uint8 { panic("AXValueGetValue received a non-AXValue CF object") },
	}
	if _, ok := api.pointValue(17); ok {
		t.Fatal("non-AXValue position was accepted")
	}
	if _, ok := api.sizeValue(17); ok {
		t.Fatal("non-AXValue size was accepted")
	}
}
