//go:build darwin

package processtap

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/ebitengine/purego"
	"path/filepath"
	"strings"
	"sync"
)

type endpointAPI struct {
	create  func(*int32) uintptr
	copyUID func(uintptr, *byte, int32) int32
	close   func(uintptr) int32
}

var endpointLibraries = struct {
	sync.Mutex
	values map[string]*endpointAPI
}{values: make(map[string]*endpointAPI)}

func loadEndpointAPI(path string) (*endpointAPI, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return nil, errors.New("process tap: absolute library path required")
	}
	endpointLibraries.Lock()
	defer endpointLibraries.Unlock()
	if api := endpointLibraries.values[path]; api != nil {
		return api, nil
	}
	library, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	api := &endpointAPI{}
	for _, b := range []struct {
		name string
		dst  any
	}{{"MiMacProcessTapCreate", &api.create}, {"MiMacProcessTapCopyUID", &api.copyUID}, {"MiMacProcessTapClose", &api.close}} {
		symbol, err := purego.Dlsym(library, b.name)
		if err != nil {
			_ = purego.Dlclose(library)
			return nil, err
		}
		purego.RegisterFunc(b.dst, symbol)
	}
	// Function pointers and partial owners remain valid until process exit.
	endpointLibraries.values[path] = api
	return api, nil
}

type darwinHandle struct {
	api *endpointAPI
	raw uintptr
	uid string
}

func openNativeHandle(path string) (nativeHandle, error) {
	api, err := loadEndpointAPI(path)
	if err != nil {
		return nil, err
	}
	var status int32
	raw := api.create(&status)
	if raw == 0 {
		return nil, fmt.Errorf("process tap: create status %d, no handle", status)
	}
	handle := &darwinHandle{api: api, raw: raw}
	if status != 0 {
		return handle, fmt.Errorf("process tap: create status %d", status)
	}
	var buffer [256]byte
	if status = api.copyUID(raw, &buffer[0], int32(len(buffer))); status != 0 {
		return handle, fmt.Errorf("process tap: UID status %d", status)
	}
	end := bytes.IndexByte(buffer[:], 0)
	if end <= 0 {
		return handle, errors.New("process tap: missing input UID")
	}
	handle.uid = string(buffer[:end])
	return handle, nil
}
func (h *darwinHandle) InputUID() string { return h.uid }
func (h *darwinHandle) Close() error {
	if h.raw == 0 {
		return nil
	}
	if status := h.api.close(h.raw); status != 0 {
		return fmt.Errorf("process tap: close status %d", status)
	}
	h.raw = 0
	h.uid = ""
	return nil
}
