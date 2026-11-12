//go:build !darwin

package permissionguide

import "errors"

var errAccessibilityAccessUnsupported = errors.New("permissionguide: Accessibility status is unsupported on this platform")

// ReadAccessibilityAccess reports that the macOS Accessibility check is not
// available on this platform.
func ReadAccessibilityAccess() (bool, error) {
	return false, errAccessibilityAccessUnsupported
}

func AccessibilityPermissionTitle() string { return accessibilityPermissionTitle("") }
