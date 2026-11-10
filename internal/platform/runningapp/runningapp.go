// Package runningapp finds a running application by its bundle identifier and
// complete on-disk paths. It identifies processes only; callers must capture
// and recheck a processidentity.Identity before relying on the returned PID.
package runningapp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupported    = errors.New("runningapp: unsupported platform")
	ErrInvalidTarget  = errors.New("runningapp: invalid application identity")
	ErrNotRunning     = errors.New("runningapp: target application is not running")
	ErrAmbiguous      = errors.New("runningapp: more than one target process is running")
	ErrUnexpectedPath = errors.New("runningapp: a running process with this bundle identifier has a different path")
	ErrReadFailed     = errors.New("runningapp: unable to read running application identity")
)

const maxIdentityStringBytes = 1 << 16

type target struct {
	bundleID       string
	bundlePath     string
	executablePath string
}

// applicationSnapshot is one NSRunningApplication observation. Native
// enumeration supplies all active processes with the requested bundle ID;
// uniquePID deliberately rejects any same-ID process at another path because
// bundle-wide notifications reach every copy.
type applicationSnapshot struct {
	bundleID       string
	bundlePath     string
	executablePath string
	pid            int32
	terminated     bool
}

// Find returns the PID of the sole active process matching bundleID, appPath,
// and executablePath. It rejects a second process or any active process with
// the same bundle ID at another path. It does not launch an app, inspect its
// environment, or establish a durable process lease.
func Find(ctx context.Context, bundleID, appPath, executablePath string) (int32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	identity, err := validateTarget(bundleID, appPath, executablePath)
	if err != nil {
		return 0, err
	}
	return findNative(ctx, identity)
}

// Exists reports whether any active application has the bundle identity.
// Unlike Find it does not grant a PID/path lease; callers use it only to
// refuse conflicting access, never to control an application.
func Exists(ctx context.Context, bundleID string) (bool, error) {
	if !validIdentityString(bundleID) || strings.TrimSpace(bundleID) == "" {
		return false, ErrInvalidTarget
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pid, err := findNative(ctx, target{bundleID: bundleID})
	if errors.Is(err, ErrNotRunning) {
		return false, nil
	}
	if errors.Is(err, ErrAmbiguous) {
		return true, nil
	}
	return pid > 0, err
}

func validateTarget(bundleID, appPath, executablePath string) (target, error) {
	if !validIdentityString(bundleID) || strings.TrimSpace(bundleID) == "" ||
		!validIdentityString(appPath) || !filepath.IsAbs(appPath) ||
		!validIdentityString(executablePath) || !filepath.IsAbs(executablePath) {
		return target{}, ErrInvalidTarget
	}
	appPath = filepath.Clean(appPath)
	executablePath = filepath.Clean(executablePath)
	relative, err := filepath.Rel(appPath, executablePath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return target{}, ErrInvalidTarget
	}
	return target{bundleID: bundleID, bundlePath: appPath, executablePath: executablePath}, nil
}

func validIdentityString(value string) bool {
	return len(value) > 0 && len(value) <= maxIdentityStringBytes &&
		utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func uniquePID(identity target, applications []applicationSnapshot) (int32, error) {
	var pid int32
	matched := 0
	for _, application := range applications {
		if application.terminated || application.bundleID != identity.bundleID {
			continue
		}
		if application.pid <= 0 || !filepath.IsAbs(application.bundlePath) || !filepath.IsAbs(application.executablePath) {
			return 0, ErrReadFailed
		}
		if identity.bundlePath != "" && (filepath.Clean(application.bundlePath) != identity.bundlePath ||
			filepath.Clean(application.executablePath) != identity.executablePath) {
			return 0, ErrUnexpectedPath
		}
		matched++
		pid = application.pid
	}
	if matched == 0 {
		return 0, ErrNotRunning
	}
	if matched != 1 {
		return 0, ErrAmbiguous
	}
	return pid, nil
}
