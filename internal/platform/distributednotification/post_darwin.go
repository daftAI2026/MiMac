//go:build darwin

package distributednotification

import (
	"context"
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

type runtimeAPI struct {
	once sync.Once
	api  *coreFoundationAPI
	err  error
}

var sharedRuntime runtimeAPI

func postObject(ctx context.Context, name, object string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" || !validCString(name) || !validCString(object) {
		return ErrInvalidString
	}
	api, err := loadCoreFoundationAPI()
	if err != nil {
		return err
	}
	return postObjectWithAPI(ctx, name, object, api)
}

func loadCoreFoundationAPI() (*coreFoundationAPI, error) {
	sharedRuntime.once.Do(func() {
		library, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			sharedRuntime.err = fmt.Errorf("distributed notification: load CoreFoundation: %w", err)
			return
		}
		keepLibrary := false
		defer func() {
			if !keepLibrary {
				_ = purego.Dlclose(library)
			}
		}()

		api := &coreFoundationAPI{}
		for _, binding := range []struct {
			destination any
			symbol      string
		}{
			{&api.cfStringCreateWithCString, "CFStringCreateWithCString"},
			{&api.cfNotificationCenterGetDistributedCenter, "CFNotificationCenterGetDistributedCenter"},
			{&api.cfNotificationCenterPostNotification, "CFNotificationCenterPostNotification"},
			{&api.cfRelease, "CFRelease"},
		} {
			address, err := purego.Dlsym(library, binding.symbol)
			if err != nil {
				sharedRuntime.err = fmt.Errorf("distributed notification: resolve %s: %w", binding.symbol, err)
				return
			}
			purego.RegisterFunc(binding.destination, address)
		}
		sharedRuntime.api = api
		keepLibrary = true
	})
	return sharedRuntime.api, sharedRuntime.err
}
