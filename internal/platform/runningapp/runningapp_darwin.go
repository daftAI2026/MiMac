//go:build darwin

package runningapp

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const maxRunningApplications = 4096

var runningApplicationNative struct {
	once       sync.Once
	foundation uintptr
	appKit     uintptr
	err        error
}

func findNative(ctx context.Context, identity target) (int32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Objective-C objects and the autorelease pool stay on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := loadRunningApplicationAPI(); err != nil {
		return 0, err
	}

	poolClass := objc.GetClass("NSAutoreleasePool")
	runningApplicationClass := objc.GetClass("NSRunningApplication")
	if poolClass == 0 || runningApplicationClass == 0 {
		return 0, ErrReadFailed
	}
	pool := objc.ID(poolClass).Send(runningApplicationSelector("alloc")).Send(runningApplicationSelector("init"))
	if pool == 0 {
		return 0, ErrReadFailed
	}
	defer pool.Send(runningApplicationSelector("drain"))

	listSelector := runningApplicationSelector("runningApplicationsWithBundleIdentifier:")
	if !objc.Send[bool](objc.ID(runningApplicationClass), runningApplicationSelector("respondsToSelector:"), listSelector) {
		return 0, ErrReadFailed
	}
	bundleIdentifier := runningApplicationString(identity.bundleID)
	if bundleIdentifier == 0 {
		return 0, ErrReadFailed
	}
	applications := objc.Send[objc.ID](objc.ID(runningApplicationClass), listSelector, bundleIdentifier)
	if applications == 0 || !runningApplicationResponds(applications, "count") || !runningApplicationResponds(applications, "objectAtIndex:") {
		return 0, ErrReadFailed
	}
	count := objc.Send[uintptr](applications, runningApplicationSelector("count"))
	if count > maxRunningApplications {
		return 0, ErrReadFailed
	}

	snapshots := make([]applicationSnapshot, 0, count)
	for index := uintptr(0); index < count; index++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		application := objc.Send[objc.ID](applications, runningApplicationSelector("objectAtIndex:"), index)
		if application == 0 || !runningApplicationSupportsRequiredProperties(application) {
			return 0, ErrReadFailed
		}
		if objc.Send[bool](application, runningApplicationSelector("isTerminated")) {
			continue
		}
		pid := objc.Send[int32](application, runningApplicationSelector("processIdentifier"))
		if pid <= 0 {
			return 0, ErrReadFailed
		}
		observedBundleID, err := runningApplicationStringValue(application.Send(runningApplicationSelector("bundleIdentifier")))
		if err != nil {
			return 0, err
		}
		if observedBundleID != identity.bundleID {
			// The API was filtered by bundle ID. A mismatching active result is
			// inconsistent identity data, so do not quietly ignore it.
			return 0, ErrReadFailed
		}
		bundlePath, err := runningApplicationURLPath(application.Send(runningApplicationSelector("bundleURL")))
		if err != nil {
			return 0, err
		}
		executablePath, err := runningApplicationURLPath(application.Send(runningApplicationSelector("executableURL")))
		if err != nil {
			return 0, err
		}
		if !filepath.IsAbs(bundlePath) || !filepath.IsAbs(executablePath) || !utf8.ValidString(bundlePath) || !utf8.ValidString(executablePath) ||
			strings.ContainsRune(bundlePath, '\x00') || strings.ContainsRune(executablePath, '\x00') {
			return 0, ErrReadFailed
		}
		if objc.Send[bool](application, runningApplicationSelector("isTerminated")) {
			continue
		}
		if currentPID := objc.Send[int32](application, runningApplicationSelector("processIdentifier")); currentPID != pid {
			return 0, ErrReadFailed
		}
		snapshots = append(snapshots, applicationSnapshot{
			bundleID:       observedBundleID,
			bundlePath:     filepath.Clean(bundlePath),
			executablePath: filepath.Clean(executablePath),
			pid:            pid,
		})
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return uniquePID(identity, snapshots)
}

func loadRunningApplicationAPI() error {
	runningApplicationNative.once.Do(func() {
		foundation, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			runningApplicationNative.err = fmt.Errorf("%w: load Foundation: %v", ErrReadFailed, err)
			return
		}
		appKit, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			_ = purego.Dlclose(foundation)
			runningApplicationNative.err = fmt.Errorf("%w: load AppKit: %v", ErrReadFailed, err)
			return
		}
		runningApplicationNative.foundation = foundation
		runningApplicationNative.appKit = appKit
	})
	return runningApplicationNative.err
}

func runningApplicationSupportsRequiredProperties(application objc.ID) bool {
	for _, selector := range []string{
		"isTerminated", "processIdentifier", "bundleIdentifier", "bundleURL", "executableURL",
	} {
		if !runningApplicationResponds(application, selector) {
			return false
		}
	}
	return true
}

func runningApplicationResponds(object objc.ID, selectorName string) bool {
	return object != 0 && objc.Send[bool](object, runningApplicationSelector("respondsToSelector:"), runningApplicationSelector(selectorName))
}

func runningApplicationURLPath(url objc.ID) (string, error) {
	if url == 0 {
		return "", ErrReadFailed
	}
	for _, selector := range []string{"isFileURL", "absoluteURL", "URLByStandardizingPath", "path"} {
		if !runningApplicationResponds(url, selector) {
			return "", ErrReadFailed
		}
	}
	if !objc.Send[bool](url, runningApplicationSelector("isFileURL")) {
		return "", ErrReadFailed
	}
	absolute := objc.Send[objc.ID](url, runningApplicationSelector("absoluteURL"))
	if absolute == 0 {
		return "", ErrReadFailed
	}
	standardized := objc.Send[objc.ID](absolute, runningApplicationSelector("URLByStandardizingPath"))
	if standardized == 0 {
		return "", ErrReadFailed
	}
	path := objc.Send[objc.ID](standardized, runningApplicationSelector("path"))
	return runningApplicationStringValue(path)
}

func runningApplicationStringValue(value objc.ID) (string, error) {
	if value == 0 || !runningApplicationResponds(value, "lengthOfBytesUsingEncoding:") || !runningApplicationResponds(value, "UTF8String") {
		return "", ErrReadFailed
	}
	const nsUTF8StringEncoding = 4
	length := objc.Send[uintptr](value, runningApplicationSelector("lengthOfBytesUsingEncoding:"), uintptr(nsUTF8StringEncoding))
	if length == 0 || length > maxIdentityStringBytes {
		return "", ErrReadFailed
	}
	data := objc.Send[*byte](value, runningApplicationSelector("UTF8String"))
	if data == nil {
		return "", ErrReadFailed
	}
	result := string(unsafe.Slice(data, int(length)))
	if !validIdentityString(result) {
		return "", ErrReadFailed
	}
	return result, nil
}

func runningApplicationString(value string) objc.ID {
	class := objc.GetClass("NSString")
	if class == 0 {
		return 0
	}
	return objc.ID(class).Send(runningApplicationSelector("stringWithUTF8String:"), value+"\x00")
}

func runningApplicationSelector(name string) objc.SEL { return objc.RegisterName(name) }
