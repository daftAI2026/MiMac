// Package distributednotification provides a narrow object-only distributed
// notification post used by platform-specific adapters.
package distributednotification

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupportedPlatform = errors.New("distributed notification: unsupported platform")
	ErrInvalidString       = errors.New("distributed notification: name and object must be valid UTF-8 without NUL")
)

type coreFoundationAPI struct {
	cfStringCreateWithCString                func(uintptr, *byte, uint32) uintptr
	cfNotificationCenterGetDistributedCenter func() uintptr
	cfNotificationCenterPostNotification     func(uintptr, uintptr, uintptr, uintptr, uint8)
	cfRelease                                func(uintptr)
}

const cfStringEncodingUTF8 uint32 = 0x08000100

// PostObject posts name with object as a CFString, nil userInfo, and immediate
// delivery. A nil return reports only that the local CoreFoundation post call
// completed; distributed notifications provide no receiver acknowledgement.
func PostObject(ctx context.Context, name, object string) error {
	return postObject(ctx, name, object)
}

func postObjectWithAPI(ctx context.Context, name, object string, api *coreFoundationAPI) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" || !validCString(name) || !validCString(object) {
		return ErrInvalidString
	}
	if api == nil || api.cfStringCreateWithCString == nil ||
		api.cfNotificationCenterGetDistributedCenter == nil ||
		api.cfNotificationCenterPostNotification == nil || api.cfRelease == nil {
		return errors.New("distributed notification: CoreFoundation API unavailable")
	}

	nameRef, err := createString(api, name)
	if err != nil {
		return err
	}
	defer api.cfRelease(nameRef)

	objectRef, err := createString(api, object)
	if err != nil {
		return err
	}
	defer api.cfRelease(objectRef)

	if err := ctx.Err(); err != nil {
		return err
	}
	center := api.cfNotificationCenterGetDistributedCenter()
	if center == 0 {
		return errors.New("distributed notification: distributed center unavailable")
	}
	api.cfNotificationCenterPostNotification(center, nameRef, objectRef, 0, 1)
	return nil
}

func validCString(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func createString(api *coreFoundationAPI, value string) (uintptr, error) {
	encoded := make([]byte, len(value)+1)
	copy(encoded, value)
	ref := api.cfStringCreateWithCString(0, &encoded[0], cfStringEncodingUTF8)
	runtime.KeepAlive(encoded)
	if ref == 0 {
		return 0, errors.New("distributed notification: create CFString failed")
	}
	return ref, nil
}
