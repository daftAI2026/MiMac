//go:build darwin

// Package inputsource reads the selected keyboard input source without changing it.
package inputsource

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

var api struct {
	once        sync.Once
	err         error
	copyCurrent func() uintptr
	property    func(uintptr, uintptr) uintptr
	stringBytes func(uintptr, *byte, int64, uint32) uint8
	release     func(uintptr)
	idKey       uintptr
}

// CurrentID must be called on the host main thread. Only the framework symbols
// are cached; the selected source is read anew for each action start.
func CurrentID() (string, error) {
	api.once.Do(func() {
		carbon, err := purego.Dlopen("/System/Library/Frameworks/Carbon.framework/Carbon", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			api.err = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			api.err = err
			return
		}
		for _, b := range []struct {
			library uintptr
			dst     any
			name    string
		}{
			{carbon, &api.copyCurrent, "TISCopyCurrentKeyboardInputSource"},
			{carbon, &api.property, "TISGetInputSourceProperty"},
			{cf, &api.stringBytes, "CFStringGetCString"},
			{cf, &api.release, "CFRelease"},
		} {
			address, err := purego.Dlsym(b.library, b.name)
			if err != nil {
				api.err = fmt.Errorf("inputsource: %s: %w", b.name, err)
				return
			}
			purego.RegisterFunc(b.dst, address)
		}
		address, err := purego.Dlsym(carbon, "kTISPropertyInputSourceID")
		if err != nil {
			api.err = err
			return
		}
		// Dlsym returns a native address, not a Go pointer. Copy the exported
		// CFStringRef through libc, as the HID adapter does for native symbols.
		libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			api.err = err
			return
		}
		copyAddress, err := purego.Dlsym(libSystem, "memcpy")
		if err != nil {
			api.err = err
			return
		}
		var copyNative func(*uintptr, uintptr, uintptr) uintptr
		purego.RegisterFunc(&copyNative, copyAddress)
		copyNative(&api.idKey, address, unsafe.Sizeof(api.idKey))
	})
	if api.err != nil {
		return "", api.err
	}
	source := api.copyCurrent()
	if source == 0 {
		return "", errors.New("inputsource: no selected source")
	}
	defer api.release(source)
	id := api.property(source, api.idKey)
	if id == 0 {
		return "", errors.New("inputsource: missing source ID")
	}
	var buffer [512]byte
	if api.stringBytes(id, &buffer[0], int64(len(buffer)), 0x08000100) == 0 {
		return "", errors.New("inputsource: source ID conversion failed")
	}
	end := bytes.IndexByte(buffer[:], 0)
	if end <= 0 {
		return "", errors.New("inputsource: invalid source ID")
	}
	return string(buffer[:end]), nil
}
