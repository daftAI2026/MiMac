//go:build darwin

// Package inputsource reads and explicitly selects enabled keyboard input sources.
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
	once         sync.Once
	err          error
	copyCurrent  func() uintptr
	property     func(uintptr, uintptr) uintptr
	createList   func(uintptr, uint8) uintptr
	selectSource func(uintptr) int32
	arrayCount   func(uintptr) int64
	arrayValue   func(uintptr, int64) uintptr
	stringBytes  func(uintptr, *byte, int64, uint32) uint8
	release      func(uintptr)
	idKey        uintptr
}

func loadAPI() error {
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
			{carbon, &api.createList, "TISCreateInputSourceList"},
			{carbon, &api.selectSource, "TISSelectInputSource"},
			{cf, &api.arrayCount, "CFArrayGetCount"},
			{cf, &api.arrayValue, "CFArrayGetValueAtIndex"},
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
		return api.err
	}
	return nil
}

// CurrentID must be called on the host main thread. Only framework symbols
// are cached; the selected source is read anew for each action start.
func CurrentID() (string, error) {
	if err := loadAPI(); err != nil {
		return "", err
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

// SelectEnabledID must be called on the host main thread. It enumerates only
// enabled input sources and selects an exact ID match. The host verifies the
// selected ID once with CurrentID after this call returns.
func SelectEnabledID(id string) error {
	return selectEnabledNative(id)
}

func selectEnabledNative(id string) error {
	if err := loadAPI(); err != nil {
		return err
	}
	return selectEnabledFromList(id, enabledSourceListAPI{
		createList: api.createList,
		arrayCount: api.arrayCount,
		arrayValue: api.arrayValue,
		sourceID: func(source uintptr) (string, error) {
			property := api.property(source, api.idKey)
			if property == 0 {
				return "", nil
			}
			var buffer [512]byte
			if api.stringBytes(property, &buffer[0], int64(len(buffer)), 0x08000100) == 0 {
				return "", errors.New("inputsource: enabled source ID conversion failed")
			}
			end := bytes.IndexByte(buffer[:], 0)
			if end <= 0 {
				return "", errors.New("inputsource: enabled source ID is invalid")
			}
			return string(buffer[:end]), nil
		},
		selectSource: func(source uintptr) error {
			status := api.selectSource(source)
			if status != 0 {
				return fmt.Errorf("inputsource: TISSelectInputSource failed with status %d", status)
			}
			return nil
		},
		release: api.release,
	})
}
