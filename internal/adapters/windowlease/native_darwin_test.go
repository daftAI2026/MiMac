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

func TestBatchOptionalStringsDistinguishAbsentFromUnreadable(t *testing.T) {
	var code int32
	api := &presentationNativeAPI{
		cfGetTypeID:      func(v uintptr) uintptr { return v },
		cfStringTypeID:   func() uintptr { return 1 },
		cfStringLength:   func(uintptr) int64 { return 0 },
		axValueGetTypeID: func() uintptr { return 2 },
		axValueGetType:   func(uintptr) uint32 { return 5 },
		axValueGetValue: func(_ uintptr, _ uint32, out unsafe.Pointer) uint8 {
			*(*int32)(out) = code
			return 1
		},
	}
	for _, sample := range []struct {
		value      uintptr
		errorCode  int32
		knownEmpty bool
	}{
		{1, 0, true}, {2, -25205, true}, {2, -25212, true},
		{2, -25204, false}, {3, 0, false}, {0, 0, false},
	} {
		code = sample.errorCode
		empty, known := api.optionalStringEmpty(sample.value)
		if empty != sample.knownEmpty || known != sample.knownEmpty {
			t.Fatalf("value %d, AX error %d: empty=%t known=%t", sample.value, code, empty, known)
		}
	}
}
