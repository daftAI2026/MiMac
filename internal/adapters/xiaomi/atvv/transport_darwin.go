//go:build darwin

package atvv

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

type darwinTransportAPI struct {
	createFn      func(uint64, uintptr) uint64
	requestStopFn func(uint64, uint32) int32
	closeFn       func(uint64) int32
	callback      uintptr
}

var darwinTransportLibraries = struct {
	sync.Mutex
	byPath map[string]*darwinTransportAPI
}{byPath: make(map[string]*darwinTransportAPI)}

var nativeTransportCallback struct {
	sync.Once
	address uintptr
}

func nativeATVVCallback(context uint64, kind int32, status int32, payload unsafe.Pointer, length int32) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if transport := lookupTransportCallback(context); transport != nil {
				transport.recordNativeError(fmt.Errorf("atvv transport: recovered native callback panic: %v", recovered))
			}
		}
	}()
	deliverNativeEvent(context, kind, status, payload, length)
}

func loadDarwinTransportAPI(libraryPath string) (*darwinTransportAPI, error) {
	if !filepath.IsAbs(libraryPath) || strings.IndexByte(libraryPath, 0) >= 0 {
		return nil, errors.New("atvv transport: native library path must be absolute and contain no NUL")
	}

	darwinTransportLibraries.Lock()
	defer darwinTransportLibraries.Unlock()
	if api := darwinTransportLibraries.byPath[libraryPath]; api != nil {
		return api, nil
	}
	library, err := purego.Dlopen(libraryPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("atvv transport: load native library: %w", err)
	}
	keepLoaded := false
	defer func() {
		if !keepLoaded {
			_ = purego.Dlclose(library)
		}
	}()
	api := &darwinTransportAPI{}
	for _, binding := range []struct {
		symbol string
		dst    any
	}{
		{symbol: "MiMacATVVTransportCreate", dst: &api.createFn},
		{symbol: "MiMacATVVTransportRequestStop", dst: &api.requestStopFn},
		{symbol: "MiMacATVVTransportClose", dst: &api.closeFn},
	} {
		address, err := purego.Dlsym(library, binding.symbol)
		if err != nil {
			return nil, fmt.Errorf("atvv transport: resolve %s: %w", binding.symbol, err)
		}
		purego.RegisterFunc(binding.dst, address)
	}
	nativeTransportCallback.Do(func() {
		nativeTransportCallback.address = purego.NewCallback(nativeATVVCallback)
	})
	if nativeTransportCallback.address == 0 {
		return nil, errors.New("atvv transport: create callback trampoline failed")
	}
	api.callback = nativeTransportCallback.address
	darwinTransportLibraries.byPath[libraryPath] = api
	keepLoaded = true
	// Keep the dlopen handle alive for the process lifetime. Native transports
	// may outlive a caller timeout while their callback registry entry is kept.
	return api, nil
}

func (a *darwinTransportAPI) create(context uint64) uint64 {
	return a.createFn(context, a.callback)
}

func (a *darwinTransportAPI) requestStop(handle uint64, streamID uint32) int32 {
	return a.requestStopFn(handle, streamID)
}

func (a *darwinTransportAPI) close(handle uint64) int32 {
	return a.closeFn(handle)
}

func OpenTransport(libraryPath string, handler NativeEventHandler) (*Transport, error) {
	api, err := loadDarwinTransportAPI(libraryPath)
	if err != nil {
		return nil, err
	}
	return newTransportWithAPI(api, handler, defaultTransportCloseTimeout)
}
