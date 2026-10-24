//go:build darwin

package audioroute

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	coreAudioPath      = "/System/Library/Frameworks/CoreAudio.framework/CoreAudio"
	coreFoundationPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	cfStringUTF8       = uint32(0x08000100)
	maxCFStringBytes   = 64 << 10
)

type nativePropertyAddress struct {
	selector uint32
	scope    uint32
	element  uint32
}

type nativeAPI struct {
	hasProperty     func(uint32, *nativePropertyAddress) uint8
	getDataSize     func(uint32, *nativePropertyAddress, uint32, unsafe.Pointer, *uint32) int32
	getData         func(uint32, *nativePropertyAddress, uint32, unsafe.Pointer, *uint32, unsafe.Pointer) int32
	cfStringCreate  func(uintptr, *byte, uint32) uintptr
	cfStringLength  func(uintptr) int64
	cfStringMax     func(int64, uint32) int64
	cfStringCString func(uintptr, *byte, int64, uint32) uint8
	cfRelease       func(uintptr)
}

var nativeRuntime struct {
	once sync.Once
	api  *nativeAPI
	err  error
}

func newNativeAPI() (propertyAPI, error) {
	nativeRuntime.once.Do(func() {
		coreAudio, err := purego.Dlopen(coreAudioPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeRuntime.err = fmt.Errorf("audioroute: load CoreAudio: %w", err)
			return
		}
		coreFoundation, err := purego.Dlopen(coreFoundationPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeRuntime.err = fmt.Errorf("audioroute: load CoreFoundation: %w", err)
			return
		}
		api := &nativeAPI{}
		for _, binding := range []struct {
			library uintptr
			dst     any
			symbol  string
		}{
			{coreAudio, &api.hasProperty, "AudioObjectHasProperty"},
			{coreAudio, &api.getDataSize, "AudioObjectGetPropertyDataSize"},
			{coreAudio, &api.getData, "AudioObjectGetPropertyData"},
			{coreFoundation, &api.cfStringCreate, "CFStringCreateWithCString"},
			{coreFoundation, &api.cfStringLength, "CFStringGetLength"},
			{coreFoundation, &api.cfStringMax, "CFStringGetMaximumSizeForEncoding"},
			{coreFoundation, &api.cfStringCString, "CFStringGetCString"},
			{coreFoundation, &api.cfRelease, "CFRelease"},
		} {
			address, err := purego.Dlsym(binding.library, binding.symbol)
			if err != nil {
				nativeRuntime.err = fmt.Errorf("audioroute: resolve %s: %w", binding.symbol, err)
				return
			}
			purego.RegisterFunc(binding.dst, address)
		}
		nativeRuntime.api = api
	})
	if nativeRuntime.err != nil {
		return nil, nativeRuntime.err
	}
	return nativeRuntime.api, nil
}

func (a *nativeAPI) HasProperty(objectID uint32, address propertyAddress) bool {
	nativeAddress := toNativeAddress(address)
	return a.hasProperty(objectID, &nativeAddress) != 0
}

func (a *nativeAPI) PropertyDataSize(objectID uint32, address propertyAddress, qualifier []byte) (uint32, error) {
	nativeAddress := toNativeAddress(address)
	qualifierPointer := nativeBytesPointer(qualifier)
	var size uint32
	status := a.getDataSize(objectID, &nativeAddress, uint32(len(qualifier)), qualifierPointer, &size)
	runtime.KeepAlive(qualifier)
	if status != 0 {
		return 0, osStatusError("AudioObjectGetPropertyDataSize", status)
	}
	return size, nil
}

func (a *nativeAPI) PropertyData(objectID uint32, address propertyAddress, qualifier []byte, capacity uint32) ([]byte, error) {
	if capacity == 0 || capacity > maxPropertyBytes {
		return nil, fmt.Errorf("%w: invalid property capacity %d", ErrMalformedData, capacity)
	}
	nativeAddress := toNativeAddress(address)
	qualifierPointer := nativeBytesPointer(qualifier)
	data := make([]byte, int(capacity))
	dataPointer := nativeBytesPointer(data)
	actualSize := capacity
	status := a.getData(objectID, &nativeAddress, uint32(len(qualifier)), qualifierPointer, &actualSize, dataPointer)
	runtime.KeepAlive(qualifier)
	runtime.KeepAlive(data)
	if status != 0 {
		return nil, osStatusError("AudioObjectGetPropertyData", status)
	}
	if actualSize > capacity {
		return nil, fmt.Errorf("%w: Core Audio returned %d bytes for %d-byte buffer", ErrMalformedData, actualSize, capacity)
	}
	return data[:actualSize], nil
}

func (a *nativeAPI) DeviceUID(objectID uint32) (string, error) {
	return a.objectString(objectID, propertyAddress{selector: selectorDeviceUID, scope: scopeGlobal})
}

func (a *nativeAPI) DeviceIDForUID(uid string) (uint32, error) {
	if bytes.IndexByte([]byte(uid), 0) >= 0 {
		return 0, fmt.Errorf("%w: device UID contains NUL", ErrMalformedData)
	}
	cString := append([]byte(uid), 0)
	ref := a.cfStringCreate(0, &cString[0], cfStringUTF8)
	runtime.KeepAlive(cString)
	if ref == 0 {
		return 0, errors.New("audioroute: create UID qualifier CFString failed")
	}
	defer a.cfRelease(ref)
	qualifier := make([]byte, 8)
	binary.LittleEndian.PutUint64(qualifier, uint64(ref))
	data, err := readNativeProperty(a, systemObjectID, propertyAddress{selector: selectorTranslateUID, scope: scopeGlobal}, qualifier)
	if err != nil {
		return 0, err
	}
	return scalar(data, "translated device ID")
}

func (a *nativeAPI) ProcessBundleID(objectID uint32) (string, error) {
	return a.objectString(objectID, propertyAddress{selector: selectorProcessBundleID, scope: scopeGlobal})
}

func (a *nativeAPI) objectString(objectID uint32, address propertyAddress) (string, error) {
	if !a.HasProperty(objectID, address) {
		return "", errors.New("Core Audio string property is unavailable")
	}
	data, err := readNativeProperty(a, objectID, address, nil)
	if err != nil {
		return "", err
	}
	if len(data) != 8 {
		return "", fmt.Errorf("%w: CFString reference has %d bytes, want 8", ErrMalformedData, len(data))
	}
	ref := uintptr(binary.LittleEndian.Uint64(data))
	if ref == 0 {
		return "", errors.New("Core Audio returned a null CFString")
	}
	defer a.cfRelease(ref)
	length := a.cfStringLength(ref)
	if length < 0 {
		return "", fmt.Errorf("%w: negative CFString length", ErrMalformedData)
	}
	maxBytes := a.cfStringMax(length, cfStringUTF8)
	if maxBytes < 0 || maxBytes >= maxCFStringBytes {
		return "", fmt.Errorf("%w: CFString UTF-8 size %d exceeds limit", ErrMalformedData, maxBytes)
	}
	buffer := make([]byte, int(maxBytes)+1)
	if len(buffer) == 0 {
		buffer = make([]byte, 1)
	}
	if a.cfStringCString(ref, &buffer[0], int64(len(buffer)), cfStringUTF8) == 0 {
		return "", errors.New("Core Audio CFString UTF-8 conversion failed")
	}
	end := bytes.IndexByte(buffer, 0)
	if end < 0 {
		return "", fmt.Errorf("%w: unterminated CFString", ErrMalformedData)
	}
	return string(buffer[:end]), nil
}

func readNativeProperty(a *nativeAPI, objectID uint32, address propertyAddress, qualifier []byte) ([]byte, error) {
	size, err := a.PropertyDataSize(objectID, address, qualifier)
	if err != nil {
		return nil, err
	}
	if size > maxPropertyBytes {
		return nil, fmt.Errorf("%w: Core Audio string property size %d exceeds limit", ErrMalformedData, size)
	}
	if size == 0 {
		return nil, fmt.Errorf("%w: empty Core Audio string property", ErrMalformedData)
	}
	return a.PropertyData(objectID, address, qualifier, size)
}

func toNativeAddress(address propertyAddress) nativePropertyAddress {
	return nativePropertyAddress{selector: address.selector, scope: address.scope, element: address.element}
}

func nativeBytesPointer(data []byte) unsafe.Pointer {
	if len(data) == 0 {
		return nil
	}
	return unsafe.Pointer(&data[0])
}

func osStatusError(operation string, status int32) error {
	return fmt.Errorf("audioroute: %s failed with OSStatus %d (0x%08x)", operation, status, uint32(status))
}
