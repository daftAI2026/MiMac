//go:build darwin

package permissionguide

import (
	"fmt"
	"sync"
	"syscall"

	"github.com/ebitengine/purego"
)

const hiservicesFrameworkPath = "/System/Library/Frameworks/ApplicationServices.framework/Frameworks/HIServices.framework/HIServices"

var accessibilityTrustAPI struct {
	once             sync.Once
	handle           uintptr
	isProcessTrusted func() uint8
	err              error
}

func loadAccessibilityTrustAPI() error {
	accessibilityTrustAPI.once.Do(func() {
		handle, err := purego.Dlopen(hiservicesFrameworkPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			accessibilityTrustAPI.err = fmt.Errorf("permissionguide: load HIServices: %w", err)
			return
		}
		address, err := purego.Dlsym(handle, "AXIsProcessTrusted")
		if err != nil {
			_ = purego.Dlclose(handle)
			accessibilityTrustAPI.err = fmt.Errorf("permissionguide: resolve AXIsProcessTrusted: %w", err)
			return
		}
		purego.RegisterFunc(&accessibilityTrustAPI.isProcessTrusted, address)
		accessibilityTrustAPI.handle = handle
	})
	return accessibilityTrustAPI.err
}

// ReadAccessibilityAccess returns the current AXIsProcessTrusted result.
// It loads the public API once but queries the permission on every call.
func ReadAccessibilityAccess() (bool, error) {
	if err := loadAccessibilityTrustAPI(); err != nil {
		return false, err
	}
	if accessibilityTrustAPI.isProcessTrusted == nil || accessibilityTrustAPI.handle == 0 {
		return false, fmt.Errorf("permissionguide: AXIsProcessTrusted API unavailable")
	}
	return accessibilityTrustAPI.isProcessTrusted() != 0, nil
}

// AccessibilityPermissionTitle follows the privacy category name on this OS.
func AccessibilityPermissionTitle() string {
	version, _ := syscall.Sysctl("kern.osproductversion")
	return accessibilityPermissionTitle(version)
}
