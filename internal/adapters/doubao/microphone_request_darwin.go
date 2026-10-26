//go:build darwin

package doubao

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
)

const cfStringEncodingUTF8 uint32 = 0x08000100

type microphoneRequestAPI struct {
	library                                  uintptr
	cfStringCreateWithCString                func(uintptr, *byte, uint32) uintptr
	cfDictionaryCreateMutable                func(uintptr, int64, uintptr, uintptr) uintptr
	cfDictionarySetValue                     func(uintptr, uintptr, uintptr)
	cfNotificationCenterGetDistributedCenter func() uintptr
	cfNotificationCenterPostNotification     func(uintptr, uintptr, uintptr, uintptr, uint8)
	cfRelease                                func(uintptr)
	dictionaryKeyCallbacks                   uintptr
	dictionaryValueCallbacks                 uintptr
}

var microphoneRequestRuntime struct {
	once sync.Once
	api  *microphoneRequestAPI
	err  error
}

func postSelectedMicrophoneIDNotification(ctx context.Context, request MicrophoneSelectionRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	api, err := loadMicrophoneRequestAPI()
	if err != nil {
		return err
	}
	return postSelectedMicrophoneIDNotificationWithAPI(ctx, request, api)
}

func postSelectedMicrophoneIDNotificationWithAPI(ctx context.Context, request MicrophoneSelectionRequest, api *microphoneRequestAPI) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if api == nil {
		return errors.New("doubao microphone request: CoreFoundation API unavailable")
	}
	name, err := api.createString(selectedMicrophoneNotificationName)
	if err != nil {
		return err
	}
	defer api.cfRelease(name)

	var userInfo uintptr
	if request.selectedMicrophoneID != nil {
		userInfo = api.cfDictionaryCreateMutable(0, 0, api.dictionaryKeyCallbacks, api.dictionaryValueCallbacks)
		if userInfo == 0 {
			return errors.New("doubao microphone request: create userInfo dictionary failed")
		}
		defer api.cfRelease(userInfo)

		key, createErr := api.createString(selectedMicrophoneUserInfoKey)
		if createErr != nil {
			return createErr
		}
		defer api.cfRelease(key)
		value, createErr := api.createString(*request.selectedMicrophoneID)
		if createErr != nil {
			return createErr
		}
		defer api.cfRelease(value)
		api.cfDictionarySetValue(userInfo, key, value)
		runtime.KeepAlive(request)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	center := api.cfNotificationCenterGetDistributedCenter()
	if center == 0 {
		return errors.New("doubao microphone request: distributed notification center unavailable")
	}
	api.cfNotificationCenterPostNotification(center, name, 0, userInfo, 1)
	return nil
}

func (api *microphoneRequestAPI) createString(value string) (uintptr, error) {
	encoded := make([]byte, len(value)+1)
	copy(encoded, value)
	result := api.cfStringCreateWithCString(0, &encoded[0], cfStringEncodingUTF8)
	runtime.KeepAlive(encoded)
	if result == 0 {
		return 0, errors.New("doubao microphone request: create CFString failed")
	}
	return result, nil
}

func loadMicrophoneRequestAPI() (*microphoneRequestAPI, error) {
	microphoneRequestRuntime.once.Do(func() {
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			microphoneRequestRuntime.err = fmt.Errorf("doubao microphone request: load CoreFoundation: %w", err)
			return
		}
		keepLibrary := false
		defer func() {
			if !keepLibrary {
				_ = purego.Dlclose(cf)
			}
		}()

		api := &microphoneRequestAPI{library: cf}
		for _, binding := range []struct {
			destination any
			symbol      string
		}{
			{&api.cfStringCreateWithCString, "CFStringCreateWithCString"},
			{&api.cfDictionaryCreateMutable, "CFDictionaryCreateMutable"},
			{&api.cfDictionarySetValue, "CFDictionarySetValue"},
			{&api.cfNotificationCenterGetDistributedCenter, "CFNotificationCenterGetDistributedCenter"},
			{&api.cfNotificationCenterPostNotification, "CFNotificationCenterPostNotification"},
			{&api.cfRelease, "CFRelease"},
		} {
			address, err := purego.Dlsym(cf, binding.symbol)
			if err != nil {
				microphoneRequestRuntime.err = fmt.Errorf("doubao microphone request: resolve %s: %w", binding.symbol, err)
				return
			}
			purego.RegisterFunc(binding.destination, address)
		}
		for symbol, destination := range map[string]*uintptr{
			"kCFTypeDictionaryKeyCallBacks":   &api.dictionaryKeyCallbacks,
			"kCFTypeDictionaryValueCallBacks": &api.dictionaryValueCallbacks,
		} {
			address, err := purego.Dlsym(cf, symbol)
			if err != nil {
				microphoneRequestRuntime.err = fmt.Errorf("doubao microphone request: resolve %s: %w", symbol, err)
				return
			}
			*destination = address
		}
		if api.dictionaryKeyCallbacks == 0 || api.dictionaryValueCallbacks == 0 {
			microphoneRequestRuntime.err = errors.New("doubao microphone request: CoreFoundation dictionary callbacks unavailable")
			return
		}
		microphoneRequestRuntime.api = api
		keepLibrary = true
	})
	return microphoneRequestRuntime.api, microphoneRequestRuntime.err
}
