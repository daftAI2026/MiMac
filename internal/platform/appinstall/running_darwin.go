//go:build darwin

package appinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const maxInstallerApplications = 4096

var installerAppKit struct {
	once sync.Once
	err  error
}

func loadInstallerAppKit() error {
	installerAppKit.once.Do(func() {
		foundation, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			installerAppKit.err = err
			return
		}
		if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL); err != nil {
			_ = purego.Dlclose(foundation)
			installerAppKit.err = err
		}
	})
	return installerAppKit.err
}

func exactTargetRunning(ctx context.Context, bundleID, path string) (bool, error) {
	return withExactApplication(ctx, bundleID, path, false)
}

func terminateExactTarget(ctx context.Context, bundleID, path string) (bool, error) {
	return withExactApplication(ctx, bundleID, path, true)
}

func withExactApplication(ctx context.Context, bundleID, path string, terminate bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	canonical, err := canonicalInstallPath(path)
	if err != nil {
		return false, err
	}
	canonical = filepath.Clean(canonical)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := loadInstallerAppKit(); err != nil {
		return false, fmt.Errorf("load AppKit for installer: %w", err)
	}
	poolClass, appClass := objc.GetClass("NSAutoreleasePool"), objc.GetClass("NSRunningApplication")
	if poolClass == 0 || appClass == 0 {
		return false, errors.New("NSRunningApplication is unavailable")
	}
	pool := objc.ID(poolClass).Send(installerSelector("alloc")).Send(installerSelector("init"))
	if pool == 0 {
		return false, errors.New("create installer autorelease pool")
	}
	defer pool.Send(installerSelector("drain"))
	identifier := installerString(bundleID)
	if identifier == 0 {
		return false, errors.New("create bundle identifier string")
	}
	selector := installerSelector("runningApplicationsWithBundleIdentifier:")
	if !objc.Send[bool](objc.ID(appClass), installerSelector("respondsToSelector:"), selector) {
		return false, errors.New("running application query is unavailable")
	}
	applications := objc.Send[objc.ID](objc.ID(appClass), selector, identifier)
	if applications == 0 || !installerResponds(applications, "count") || !installerResponds(applications, "objectAtIndex:") {
		return false, errors.New("read running application list")
	}
	count := objc.Send[uintptr](applications, installerSelector("count"))
	if count > maxInstallerApplications {
		return false, errors.New("running application list exceeds safety bound")
	}
	var match objc.ID
	for index := uintptr(0); index < count; index++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		application := objc.Send[objc.ID](applications, installerSelector("objectAtIndex:"), index)
		if application == 0 || !installerSupportsApplication(application) {
			return false, errors.New("running application lacks identity properties")
		}
		if objc.Send[bool](application, installerSelector("isTerminated")) {
			continue
		}
		observedID, err := installerStringValue(application.Send(installerSelector("bundleIdentifier")))
		if err != nil {
			return false, err
		}
		if observedID != bundleID {
			return false, errors.New("running application bundle identifier changed during query")
		}
		bundleURL := application.Send(installerSelector("bundleURL"))
		observedPath, err := installerURLPath(bundleURL)
		if err != nil {
			return false, err
		}
		observedCanonical, err := canonicalInstallPath(observedPath)
		if err != nil {
			return false, fmt.Errorf("resolve running application bundle path: %w", err)
		}
		if filepath.Clean(observedCanonical) != canonical {
			continue
		}
		if match != 0 {
			return false, errors.New("multiple running processes match the exact install path")
		}
		match = application
	}
	if match == 0 {
		return false, nil
	}
	if !terminate {
		return true, nil
	}
	if !installerResponds(match, "terminate") {
		return false, errors.New("normal app termination is unavailable")
	}
	return objc.Send[bool](match, installerSelector("terminate")), nil
}

// A first-install destination has an existing Applications parent but no app
// yet. Resolve that parent without mistaking the missing leaf for a query
// failure. Still inspect registered processes: a running app's directory may
// have been removed without its process having exited.
func canonicalInstallPath(path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, os.ErrNotExist) {
		return canonical, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func installerSupportsApplication(application objc.ID) bool {
	for _, name := range []string{"isTerminated", "bundleIdentifier", "bundleURL"} {
		if !installerResponds(application, name) {
			return false
		}
	}
	return true
}

func installerURLPath(value objc.ID) (string, error) {
	if value == 0 || !installerResponds(value, "isFileURL") || !installerResponds(value, "URLByStandardizingPath") || !installerResponds(value, "path") {
		return "", errors.New("running application has no file bundle URL")
	}
	if !objc.Send[bool](value, installerSelector("isFileURL")) {
		return "", errors.New("running application bundle URL is not a file URL")
	}
	standardized := objc.Send[objc.ID](value, installerSelector("URLByStandardizingPath"))
	if standardized == 0 {
		return "", errors.New("standardize running application bundle URL")
	}
	return installerStringValue(standardized.Send(installerSelector("path")))
}

func installerStringValue(value objc.ID) (string, error) {
	if value == 0 || !installerResponds(value, "lengthOfBytesUsingEncoding:") || !installerResponds(value, "UTF8String") {
		return "", errors.New("read NSString from running application")
	}
	const nsUTF8StringEncoding = 4
	length := objc.Send[uintptr](value, installerSelector("lengthOfBytesUsingEncoding:"), uintptr(nsUTF8StringEncoding))
	if length == 0 || length > 4096 {
		return "", errors.New("invalid running application string length")
	}
	data := objc.Send[*byte](value, installerSelector("UTF8String"))
	if data == nil {
		return "", errors.New("running application string has no UTF-8 bytes")
	}
	result := string(unsafe.Slice(data, int(length)))
	if strings.ContainsRune(result, '\x00') || !filepath.IsAbs(result) && strings.Contains(result, "/") {
		return "", errors.New("invalid running application string")
	}
	return result, nil
}

func installerString(value string) objc.ID {
	class := objc.GetClass("NSString")
	if class == 0 {
		return 0
	}
	return objc.ID(class).Send(installerSelector("stringWithUTF8String:"), value+"\x00")
}

func installerResponds(object objc.ID, name string) bool {
	return object != 0 && objc.Send[bool](object, installerSelector("respondsToSelector:"), installerSelector(name))
}

func installerSelector(name string) objc.SEL { return objc.RegisterName(name) }
