//go:build darwin

package voicetools

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const maxApplicationCandidates = 4096

var applicationDiscoveryNative struct {
	once   sync.Once
	appKit uintptr
	err    error
}

// registeredApplicationCandidates uses Launch Services' indexed application
// list and NSRunningApplication. It does not search directories or launch apps.
func registeredApplicationCandidates(ctx context.Context, bundleIdentifier string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bundleIdentifier == "" {
		return nil, errors.New("voicetools: empty bundle identifier")
	}

	// Keep AppKit objects and the autorelease pool on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := loadApplicationDiscoveryAPI(); err != nil {
		return nil, err
	}
	poolClass := objc.GetClass("NSAutoreleasePool")
	workspaceClass := objc.GetClass("NSWorkspace")
	runningApplicationClass := objc.GetClass("NSRunningApplication")
	if poolClass == 0 || workspaceClass == 0 || runningApplicationClass == 0 {
		return nil, errApplicationDiscoveryAPI
	}
	pool := objc.ID(poolClass).Send(appDiscoverySel("alloc")).Send(appDiscoverySel("init"))
	if pool == 0 {
		return nil, errApplicationDiscoveryAPI
	}
	defer pool.Send(appDiscoverySel("drain"))

	workspace := objc.ID(workspaceClass).Send(appDiscoverySel("sharedWorkspace"))
	registeredSelector := appDiscoverySel("URLsForApplicationsWithBundleIdentifier:")
	runningSelector := appDiscoverySel("runningApplicationsWithBundleIdentifier:")
	if workspace == 0 ||
		!objc.Send[bool](workspace, appDiscoverySel("respondsToSelector:"), registeredSelector) ||
		!objc.Send[bool](objc.ID(runningApplicationClass), appDiscoverySel("respondsToSelector:"), runningSelector) {
		return nil, errApplicationDiscoveryAPI
	}

	identifier := appDiscoveryString(bundleIdentifier)
	if identifier == 0 {
		return nil, errApplicationDiscoveryAPI
	}
	registered := objc.Send[objc.ID](workspace, registeredSelector, identifier)
	running := objc.Send[objc.ID](objc.ID(runningApplicationClass), runningSelector, identifier)
	if registered == 0 || running == 0 {
		return nil, errApplicationDiscoveryAPI
	}

	paths := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	if err := appendApplicationURLPaths(registered, &paths, seen); err != nil {
		return nil, err
	}
	if err := appendRunningApplicationPaths(running, bundleIdentifier, &paths, seen); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return paths, nil
}

func loadApplicationDiscoveryAPI() error {
	applicationDiscoveryNative.once.Do(func() {
		applicationDiscoveryNative.appKit, applicationDiscoveryNative.err = purego.Dlopen(
			"/System/Library/Frameworks/AppKit.framework/AppKit",
			purego.RTLD_NOW|purego.RTLD_LOCAL,
		)
	})
	if applicationDiscoveryNative.err != nil || applicationDiscoveryNative.appKit == 0 {
		return errApplicationDiscoveryAPI
	}
	return nil
}

func appendApplicationURLPaths(array objc.ID, paths *[]string, seen map[string]struct{}) error {
	count, err := applicationArrayCount(array)
	if err != nil {
		return err
	}
	for index := uintptr(0); index < count; index++ {
		url := objc.Send[objc.ID](array, appDiscoverySel("objectAtIndex:"), index)
		appendApplicationURLPath(url, paths, seen)
	}
	return nil
}

func appendRunningApplicationPaths(array objc.ID, expectedIdentifier string, paths *[]string, seen map[string]struct{}) error {
	count, err := applicationArrayCount(array)
	if err != nil {
		return err
	}
	for index := uintptr(0); index < count; index++ {
		application := objc.Send[objc.ID](array, appDiscoverySel("objectAtIndex:"), index)
		if application == 0 ||
			objc.Send[bool](application, appDiscoverySel("isTerminated")) ||
			!applicationStringEquals(application.Send(appDiscoverySel("bundleIdentifier")), expectedIdentifier) {
			continue
		}
		appendApplicationURLPath(application.Send(appDiscoverySel("bundleURL")), paths, seen)
	}
	return nil
}

func applicationArrayCount(array objc.ID) (uintptr, error) {
	if array == 0 {
		return 0, errApplicationDiscoveryAPI
	}
	count := objc.Send[uintptr](array, appDiscoverySel("count"))
	if count > maxApplicationCandidates {
		return 0, errApplicationDiscoveryAPI
	}
	return count, nil
}

func appendApplicationURLPath(url objc.ID, paths *[]string, seen map[string]struct{}) {
	if url == 0 || !objc.Send[bool](url, appDiscoverySel("isFileURL")) {
		return
	}
	absolute := url.Send(appDiscoverySel("absoluteURL"))
	if absolute == 0 {
		return
	}
	standardized := absolute.Send(appDiscoverySel("URLByStandardizingPath"))
	if standardized == 0 {
		return
	}
	path, ok := applicationNSString(standardized.Send(appDiscoverySel("path")))
	if !ok || !filepath.IsAbs(path) {
		return
	}
	path = filepath.Clean(path)
	if _, exists := seen[path]; exists {
		return
	}
	seen[path] = struct{}{}
	*paths = append(*paths, path)
}

func applicationStringEquals(value objc.ID, expected string) bool {
	return value != 0 && objc.Send[bool](value, appDiscoverySel("isEqualToString:"), appDiscoveryString(expected))
}

func applicationNSString(value objc.ID) (string, bool) {
	if value == 0 {
		return "", false
	}
	const utf8Encoding = 4
	length := objc.Send[uintptr](value, appDiscoverySel("lengthOfBytesUsingEncoding:"), uintptr(utf8Encoding))
	if length == 0 || length > 1<<20 {
		return "", false
	}
	data := objc.Send[*byte](value, appDiscoverySel("UTF8String"))
	if data == nil {
		return "", false
	}
	return string(unsafe.Slice(data, int(length))), true
}

func appDiscoveryString(value string) objc.ID {
	class := objc.GetClass("NSString")
	if class == 0 {
		return 0
	}
	return objc.ID(class).Send(appDiscoverySel("stringWithUTF8String:"), value+"\x00")
}

func appDiscoverySel(name string) objc.SEL { return objc.RegisterName(name) }

var errApplicationDiscoveryAPI = errors.New("voicetools: macOS application discovery API unavailable")
