//go:build darwin

package doubao

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	cfPropertyListBinaryFormatV1     uint32 = 200
	cfNotificationDeliverImmediately int64  = 4
)

type controlSettingsNativeAPI struct {
	base                               *microphoneRequestAPI
	cfGetTypeID                        func(uintptr) uintptr
	cfStringGetTypeID                  func() uintptr
	cfStringGetLength                  func(uintptr) int64
	cfStringGetCString                 func(uintptr, *byte, int64, uint32) uint8
	cfDictionaryGetTypeID              func() uintptr
	cfPropertyListCreateData           func(uintptr, uintptr, int64, uint64, *uintptr) uintptr
	cfDataGetLength                    func(uintptr) int64
	cfDataGetBytePtr                   func(uintptr) *byte
	cfNotificationCenterAddObserver    func(uintptr, uintptr, uintptr, uintptr, uintptr, int64)
	cfNotificationCenterRemoveObserver func(uintptr, uintptr, uintptr, uintptr)
	callback                           uintptr
}

var (
	controlSettingsNativeRuntime struct {
		once sync.Once
		api  *controlSettingsNativeAPI
		err  error
	}
	controlSettingsObserverSequence atomic.Uint64
)

type darwinControlSettingsTransport struct{}

type darwinControlSettingsSubscription struct {
	api          *controlSettingsNativeAPI
	center       uintptr
	observer     uintptr
	responseName uintptr
	object       uintptr
	registered   bool
}

func newControlSettingsTransport() controlSettingsTransport {
	return darwinControlSettingsTransport{}
}

func (darwinControlSettingsTransport) subscribe(session *controlSettingsQuerySession) (controlSettingsSubscription, error) {
	api, err := loadControlSettingsNativeAPI()
	if err != nil {
		return nil, err
	}
	return subscribeControlSettingsWithAPI(api, session)
}

func subscribeControlSettingsWithAPI(api *controlSettingsNativeAPI, session *controlSettingsQuerySession) (controlSettingsSubscription, error) {
	if api == nil || api.base == nil || session == nil {
		return nil, errors.New("CoreFoundation API and query session are required")
	}
	subscription := &darwinControlSettingsSubscription{api: api}
	var err error
	subscription.responseName, err = api.base.createString(voiceStateResponseNotificationName)
	if err != nil {
		return subscription, err
	}
	subscription.object, err = api.base.createString(session.requestID)
	if err != nil {
		return subscription, err
	}
	subscription.center = api.base.cfNotificationCenterGetDistributedCenter()
	if subscription.center == 0 {
		return subscription, errors.New("distributed notification center unavailable")
	}
	observer := uintptr(controlSettingsObserverSequence.Add(1))
	if observer == 0 {
		observer = uintptr(controlSettingsObserverSequence.Add(1))
	}
	subscription.observer = observer
	controlSettingsSessionMu.Lock()
	if activeControlSettingsSession != session || !session.accepting {
		controlSettingsSessionMu.Unlock()
		return subscription, errors.New("query session ended before observer registration")
	}
	session.observer = observer
	session.nativeState = subscription
	controlSettingsSessionMu.Unlock()

	// CF registers distributed notification delivery for the host's main thread.
	// MyGo owns and runs that main loop; this preparation worker never pumps it.
	api.cfNotificationCenterAddObserver(
		subscription.center,
		observer,
		api.callback,
		subscription.responseName,
		subscription.object,
		cfNotificationDeliverImmediately,
	)
	subscription.registered = true
	return subscription, nil
}

func (darwinControlSettingsTransport) postRequest(session *controlSettingsQuerySession) error {
	controlSettingsSessionMu.Lock()
	if activeControlSettingsSession != session || !session.accepting {
		controlSettingsSessionMu.Unlock()
		return errors.New("query session ended before request post")
	}
	subscription, ok := session.nativeState.(*darwinControlSettingsSubscription)
	controlSettingsSessionMu.Unlock()
	if !ok || subscription == nil || !subscription.registered {
		return errors.New("response observer was not registered")
	}
	requestName, err := subscription.api.base.createString(voiceStateRequestNotificationName)
	if err != nil {
		return err
	}
	defer subscription.api.base.cfRelease(requestName)
	if err := session.ctx.Err(); err != nil {
		return err
	}
	subscription.api.base.cfNotificationCenterPostNotification(
		subscription.center,
		requestName,
		subscription.object,
		0,
		1,
	)
	return nil
}

func (subscription *darwinControlSettingsSubscription) removeObserver() {
	if subscription == nil || !subscription.registered {
		return
	}
	subscription.api.cfNotificationCenterRemoveObserver(
		subscription.center,
		subscription.observer,
		subscription.responseName,
		subscription.object,
	)
	subscription.registered = false
}

func (subscription *darwinControlSettingsSubscription) release() {
	if subscription == nil || subscription.api == nil {
		return
	}
	if subscription.object != 0 {
		subscription.api.base.cfRelease(subscription.object)
		subscription.object = 0
	}
	if subscription.responseName != 0 {
		subscription.api.base.cfRelease(subscription.responseName)
		subscription.responseName = 0
	}
	runtime.KeepAlive(subscription)
}

func loadControlSettingsNativeAPI() (*controlSettingsNativeAPI, error) {
	controlSettingsNativeRuntime.once.Do(func() {
		base, err := loadMicrophoneRequestAPI()
		if err != nil {
			controlSettingsNativeRuntime.err = err
			return
		}
		api := &controlSettingsNativeAPI{base: base}
		for _, binding := range []struct {
			destination any
			symbol      string
		}{
			{&api.cfGetTypeID, "CFGetTypeID"},
			{&api.cfStringGetTypeID, "CFStringGetTypeID"},
			{&api.cfStringGetLength, "CFStringGetLength"},
			{&api.cfStringGetCString, "CFStringGetCString"},
			{&api.cfDictionaryGetTypeID, "CFDictionaryGetTypeID"},
			{&api.cfPropertyListCreateData, "CFPropertyListCreateData"},
			{&api.cfDataGetLength, "CFDataGetLength"},
			{&api.cfDataGetBytePtr, "CFDataGetBytePtr"},
			{&api.cfNotificationCenterAddObserver, "CFNotificationCenterAddObserver"},
			{&api.cfNotificationCenterRemoveObserver, "CFNotificationCenterRemoveObserver"},
		} {
			address, err := purego.Dlsym(base.library, binding.symbol)
			if err != nil {
				controlSettingsNativeRuntime.err = fmt.Errorf("doubao control settings query: resolve %s: %w", binding.symbol, err)
				return
			}
			purego.RegisterFunc(binding.destination, address)
		}
		api.callback = purego.NewCallback(controlSettingsNativeNotificationCallback)
		if api.callback == 0 {
			controlSettingsNativeRuntime.err = errors.New("doubao control settings query: create native callback failed")
			return
		}
		controlSettingsNativeRuntime.api = api
	})
	return controlSettingsNativeRuntime.api, controlSettingsNativeRuntime.err
}

func controlSettingsNativeNotificationCallback(_ uintptr, observer, name, object, userInfo uintptr) {
	api, err := loadControlSettingsNativeAPI()
	if err != nil {
		return
	}
	handleControlSettingsNativeNotification(api, observer, name, object, userInfo)
}

func handleControlSettingsNativeNotification(api *controlSettingsNativeAPI, observer, name, object, userInfo uintptr) {
	if api == nil {
		return
	}
	controlSettingsSessionMu.Lock()
	defer controlSettingsSessionMu.Unlock()
	session := activeControlSettingsSession
	if session == nil || !session.accepting || session.ctx.Err() != nil || session.observer != observer {
		return
	}
	subscription, ok := session.nativeState.(*darwinControlSettingsSubscription)
	if !ok || subscription == nil || subscription.api != api {
		return
	}
	if !api.matchesCFString(name, voiceStateResponseNotificationName) || !api.matchesCFString(object, session.requestID) {
		return
	}
	settings, err := api.decodeUserInfo(userInfo)
	sendControlSettingsResultLocked(session, settings, err)
}

func (api *controlSettingsNativeAPI) matchesCFString(value uintptr, expected string) bool {
	if value == 0 || api.cfGetTypeID(value) != api.cfStringGetTypeID() || api.cfStringGetLength(value) != int64(len(expected)) {
		return false
	}
	buffer := make([]byte, len(expected)+1)
	if api.cfStringGetCString(value, &buffer[0], int64(len(buffer)), cfStringEncodingUTF8) == 0 {
		return false
	}
	return string(buffer[:len(expected)]) == expected && buffer[len(expected)] == 0
}

func (api *controlSettingsNativeAPI) decodeUserInfo(userInfo uintptr) (ControlSettings, error) {
	if userInfo == 0 || api.cfGetTypeID(userInfo) != api.cfDictionaryGetTypeID() {
		return ControlSettings{}, fmt.Errorf("%w: userInfo must be a dictionary", ErrMalformedControlSettings)
	}
	data := api.cfPropertyListCreateData(0, userInfo, int64(cfPropertyListBinaryFormatV1), 0, nil)
	if data == 0 {
		return ControlSettings{}, fmt.Errorf("%w: serialize userInfo property list", ErrMalformedControlSettings)
	}
	defer api.base.cfRelease(data)
	length := api.cfDataGetLength(data)
	if length <= 0 || length > maxControlSettingsPropertyListSize {
		return ControlSettings{}, fmt.Errorf("%w: serialized plist size is outside 1..%d bytes", ErrMalformedControlSettings, maxControlSettingsPropertyListSize)
	}
	bytes := api.cfDataGetBytePtr(data)
	if bytes == nil {
		return ControlSettings{}, fmt.Errorf("%w: serialized plist has no byte buffer", ErrMalformedControlSettings)
	}
	copyOfBytes := append([]byte(nil), unsafe.Slice(bytes, int(length))...)
	runtime.KeepAlive(data)
	return decodeControlSettingsPayload(copyOfBytes)
}
