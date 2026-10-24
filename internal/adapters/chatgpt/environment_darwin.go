//go:build darwin

package chatgpt

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"mimac.local/mimac/internal/voicetools"
)

const (
	ctlKern          = 1
	kernProcArgs2    = 49 // Defined by Apple's SDK only under __APPLE_API_UNSTABLE.
	processArgsLimit = 1 << 20
)

type environmentNativeAPI struct {
	sysctl func(name *int32, nameLen uint32, old *byte, oldLen *uintptr, new *byte, newLen uintptr) int32
}

var environmentNative struct {
	once sync.Once
	api  environmentNativeAPI
	err  error
}

func resolveRunningConfigHome(ctx context.Context, appPath string) (string, voicetools.State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", voicetools.ReadError, err
	}
	if appPath == "" || !filepath.IsAbs(appPath) {
		return "", voicetools.Unsupported, nil
	}
	appPath = filepath.Clean(appPath)

	// AppKit objects and their autorelease pool must remain on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := loadEnvironmentNativeAPI(); err != nil {
		return "", voicetools.ReadError, err
	}
	poolClass := objc.GetClass("NSAutoreleasePool")
	if poolClass == 0 {
		return "", voicetools.ReadError, errEnvironmentAPI
	}
	pool := objc.ID(poolClass).Send(envSel("alloc")).Send(envSel("init"))
	if pool == 0 {
		return "", voicetools.ReadError, errEnvironmentAPI
	}
	defer pool.Send(envSel("drain"))

	appBundleClass := objc.GetClass("NSBundle")
	runningAppClass := objc.GetClass("NSRunningApplication")
	if appBundleClass == 0 || runningAppClass == 0 {
		return "", voicetools.ReadError, errEnvironmentAPI
	}
	verifiedBundle := objc.ID(appBundleClass).Send(envSel("bundleWithPath:"), envString(appPath))
	if verifiedBundle == 0 || !envStringEquals(verifiedBundle.Send(envSel("bundleIdentifier")), bundleIdentifier) {
		return "", voicetools.Unsupported, nil
	}
	bundleURL := verifiedBundle.Send(envSel("bundleURL"))
	executableURL := verifiedBundle.Send(envSel("executableURL"))
	if bundleURL == 0 || executableURL == 0 {
		return "", voicetools.Unsupported, nil
	}
	verifiedBundlePath, ok := envURLPath(bundleURL)
	if !ok || filepath.Clean(verifiedBundlePath) != appPath {
		return "", voicetools.Unsupported, nil
	}
	executablePath, ok := envURLPath(executableURL)
	if !ok || !filepath.IsAbs(executablePath) {
		return "", voicetools.Unsupported, nil
	}

	applications := objc.ID(runningAppClass).Send(envSel("runningApplicationsWithBundleIdentifier:"), envString(bundleIdentifier))
	if applications == 0 {
		return "", voicetools.ReadError, errEnvironmentAPI
	}
	count := objc.Send[uintptr](applications, envSel("count"))
	if count > 4096 {
		return "", voicetools.ReadError, errEnvironmentAPI
	}

	var target objc.ID
	var targetPID int32
	matched := uintptr(0)
	for index := uintptr(0); index < count; index++ {
		application := objc.Send[objc.ID](applications, envSel("objectAtIndex:"), index)
		if application == 0 || objc.Send[bool](application, envSel("isTerminated")) {
			continue
		}
		if !envStringEquals(application.Send(envSel("bundleIdentifier")), bundleIdentifier) ||
			!envURLsEqual(application.Send(envSel("bundleURL")), bundleURL) ||
			!envURLsEqual(application.Send(envSel("executableURL")), executableURL) {
			continue
		}
		pid := objc.Send[int32](application, envSel("processIdentifier"))
		if pid <= 0 {
			continue
		}
		matched++
		target, targetPID = application, pid
	}
	if matched == 0 {
		return "", voicetools.NotRunning, nil
	}
	if matched != 1 {
		return "", voicetools.Ambiguous, nil
	}
	if err := ctx.Err(); err != nil {
		return "", voicetools.ReadError, err
	}
	if !envRunningApplicationMatches(target, targetPID, bundleURL, executableURL) {
		return "", voicetools.NotRunning, nil
	}

	raw := make([]byte, processArgsLimit)
	defer clear(raw)
	oldLen := uintptr(len(raw))
	mib := [3]int32{ctlKern, kernProcArgs2, targetPID}
	if environmentNative.api.sysctl(&mib[0], uint32(len(mib)), &raw[0], &oldLen, nil, 0) != 0 {
		return "", voicetools.ReadError, errProcessEnvironmentRead
	}
	if oldLen == 0 || oldLen > uintptr(len(raw)) {
		return "", voicetools.ReadError, errProcessEnvironmentRead
	}
	if err := ctx.Err(); err != nil {
		return "", voicetools.ReadError, err
	}
	if !envRunningApplicationMatches(target, targetPID, bundleURL, executableURL) {
		return "", voicetools.NotRunning, nil
	}

	environment, err := parseProcessEnvironment(raw[:int(oldLen)], executablePath)
	clear(raw)
	if err != nil {
		return "", voicetools.Unsupported, nil
	}
	configHome, err := configHomeFromEnvironment(environment)
	if err != nil {
		return "", voicetools.Unsupported, nil
	}
	if err := ctx.Err(); err != nil {
		return "", voicetools.ReadError, err
	}
	return configHome, voicetools.Ready, nil
}

func envRunningApplicationMatches(application objc.ID, pid int32, bundleURL, executableURL objc.ID) bool {
	return application != 0 &&
		!objc.Send[bool](application, envSel("isTerminated")) &&
		objc.Send[int32](application, envSel("processIdentifier")) == pid &&
		envStringEquals(application.Send(envSel("bundleIdentifier")), bundleIdentifier) &&
		envURLsEqual(application.Send(envSel("bundleURL")), bundleURL) &&
		envURLsEqual(application.Send(envSel("executableURL")), executableURL)
}

func envURLsEqual(left, right objc.ID) bool {
	if left == 0 || right == 0 || !objc.Send[bool](left, envSel("isFileURL")) || !objc.Send[bool](right, envSel("isFileURL")) {
		return false
	}
	// NSBundle may return a relative NSURL with a base, while LaunchServices
	// returns an absolute one. Compare standardized absolute file URLs so the
	// same executable is not rejected because of its URL representation.
	left = left.Send(envSel("absoluteURL")).Send(envSel("URLByStandardizingPath"))
	right = right.Send(envSel("absoluteURL")).Send(envSel("URLByStandardizingPath"))
	return left != 0 && right != 0 && objc.Send[bool](left, envSel("isEqual:"), right)
}

func envStringEquals(value objc.ID, expected string) bool {
	return value != 0 && objc.Send[bool](value, envSel("isEqualToString:"), envString(expected))
}

func envURLPath(url objc.ID) (string, bool) {
	if url == 0 {
		return "", false
	}
	return envNSString(url.Send(envSel("path")))
}

func envNSString(value objc.ID) (string, bool) {
	if value == 0 {
		return "", false
	}
	// NSUTF8StringEncoding is 4. UTF8String is NUL-terminated and valid while
	// the autorelease pool is alive.
	length := objc.Send[uintptr](value, envSel("lengthOfBytesUsingEncoding:"), uintptr(4))
	if length == 0 || length > 1<<20 {
		return "", false
	}
	data := objc.Send[*byte](value, envSel("UTF8String"))
	if data == nil {
		return "", false
	}
	return string(unsafe.Slice(data, int(length))), true
}

func envString(value string) objc.ID {
	class := objc.GetClass("NSString")
	if class == 0 {
		return 0
	}
	return objc.ID(class).Send(envSel("stringWithUTF8String:"), value+"\x00")
}

func envSel(name string) objc.SEL { return objc.RegisterName(name) }

var errEnvironmentAPI = errors.New("chatgpt: native process API unavailable")
var errProcessEnvironmentRead = errors.New("chatgpt: running process environment could not be read")

func loadEnvironmentNativeAPI() error {
	environmentNative.once.Do(func() {
		// Load the frameworks once so their Objective-C classes are registered.
		foundation, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			environmentNative.err = errEnvironmentAPI
			return
		}
		appKit, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			environmentNative.err = errEnvironmentAPI
			return
		}
		libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			environmentNative.err = errEnvironmentAPI
			return
		}
		address, err := purego.Dlsym(libSystem, "sysctl")
		if err != nil {
			environmentNative.err = errEnvironmentAPI
			return
		}
		_ = foundation
		_ = appKit
		purego.RegisterFunc(&environmentNative.api.sysctl, address)
	})
	return environmentNative.err
}
