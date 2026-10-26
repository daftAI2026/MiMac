//go:build darwin

package processtap

import (
	"errors"
	"mimac.local/mimac/internal/platform/audioroute"
	"testing"
	"unsafe"
)

func TestNativeCloseRetainsEndpointUntilIdle(t *testing.T) {
	calls := 0
	busy := true
	h := &darwinHandle{raw: 123, uid: "owned", api: &endpointAPI{close: func(uintptr) int32 { calls++; return 0 }}, activity: func(uid string) (audioroute.DeviceActivity, error) {
		if uid != "owned" {
			t.Fatalf("wrong UID %s", uid)
		}
		return audioroute.DeviceActivity{Exists: true, Running: busy}, nil
	}}
	if err := h.Close(); !errors.Is(err, ErrEndpointInUse) || calls != 0 || h.raw != 123 || h.uid != "owned" {
		t.Fatalf("destroyed active endpoint: err=%v calls=%d raw=%d", err, calls, h.raw)
	}
	busy = false
	if err := h.Close(); err != nil || calls != 1 || h.raw != 0 || h.uid != "" {
		t.Fatalf("idle retry failed: %v calls=%d", err, calls)
	}
	if err := h.Close(); err != nil || calls != 1 {
		t.Fatalf("close not idempotent: %v", err)
	}
}

func TestNativeCloseRetainsOnUnknownActivity(t *testing.T) {
	fault := errors.New("property unavailable")
	calls := 0
	h := &darwinHandle{raw: 123, uid: "owned", api: &endpointAPI{close: func(uintptr) int32 { calls++; return 0 }}, activity: func(string) (audioroute.DeviceActivity, error) { return audioroute.DeviceActivity{}, fault }}
	if err := h.Close(); !errors.Is(err, fault) || calls != 0 || h.raw != 123 {
		t.Fatalf("unknown activity closed: %v calls=%d", err, calls)
	}
}

func TestNativeCloseAllowsMissingEndpointAndPartialCreate(t *testing.T) {
	for _, uid := range []string{"owned", ""} {
		calls := 0
		queries := 0
		h := &darwinHandle{raw: 123, uid: uid, api: &endpointAPI{close: func(uintptr) int32 { calls++; return 0 }}, activity: func(string) (audioroute.DeviceActivity, error) { queries++; return audioroute.DeviceActivity{}, nil }}
		if err := h.Close(); err != nil || calls != 1 {
			t.Fatalf("close missing/partial: %v", err)
		}
		if uid == "" && queries != 0 {
			t.Fatal("queried unpublished partial endpoint")
		}
	}
}

func TestNativeCloseRetainsAfterNativeFailure(t *testing.T) {
	h := &darwinHandle{raw: 123, uid: "owned", api: &endpointAPI{close: func(uintptr) int32 { return -1 }}, activity: func(string) (audioroute.DeviceActivity, error) { return audioroute.DeviceActivity{Exists: true}, nil }}
	if err := h.Close(); err == nil || h.raw != 123 || h.uid != "owned" {
		t.Fatalf("lost failed native handle: %v", err)
	}
}

func TestPartialCreateRecoversUIDForActivityGuard(t *testing.T) {
	closes := 0
	api := &endpointAPI{create: func(status *int32) uintptr { *status = -1; return 123 }, copyUID: func(_ uintptr, out *byte, size int32) int32 {
		copy(unsafe.Slice(out, int(size)), []byte("partial-public\x00"))
		return 0
	}, close: func(uintptr) int32 { closes++; return 0 }}
	h, err := createEndpointHandle(api)
	if err == nil || h == nil || h.uid != "partial-public" {
		t.Fatalf("partial UID lost: h=%+v err=%v", h, err)
	}
	h.activity = func(uid string) (audioroute.DeviceActivity, error) {
		return audioroute.DeviceActivity{Exists: true, Running: true}, nil
	}
	if err := h.Close(); !errors.Is(err, ErrEndpointInUse) || closes != 0 {
		t.Fatalf("partial active aggregate closed: %v closes=%d", err, closes)
	}
}
