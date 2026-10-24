//go:build darwin

package atvv

import (
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
)

func TestNativeATVVCallbackUsesPointerABI(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	var gotAddress uintptr
	transport := newFakeTransport(t, api, 0, func(event NativeEvent) error {
		if event.Kind == NativeEventAudioValue && len(event.Bytes) > 0 {
			gotAddress = uintptr(unsafe.Pointer(&event.Bytes[0]))
		}
		return nil
	})
	t.Cleanup(func() { _ = transport.Close() })
	token, _, _ := api.snapshot()

	callbackPointer := purego.NewCallback(nativeATVVCallback)
	var callback func(uint64, int32, int32, unsafe.Pointer, int32)
	purego.RegisterFunc(&callback, callbackPointer)
	payload := []byte{0x34, 0x12}
	callback(token, int32(NativeEventAudioValue), nativeStatusOK, unsafe.Pointer(&payload[0]), int32(len(payload)))

	if gotAddress != uintptr(unsafe.Pointer(&payload[0])) {
		t.Fatalf("callback payload address=%#x, want %#x", gotAddress, uintptr(unsafe.Pointer(&payload[0])))
	}
}
