//go:build !darwin

package readyindicator

import "errors"

// Control is inert outside macOS; callers keep the static MyGo fallback.
type Control struct{}

// New reports that the native Core Animation view is unavailable.
func New(uintptr) (*Control, error) {
	return nil, errors.New("readyindicator: native view is available only on macOS")
}

// Update reports that the native view is unavailable.
func (*Control) Update(Rect, uint32, bool) error {
	return errors.New("readyindicator: native view is available only on macOS")
}

// Close is a no-op outside macOS.
func (*Control) Close() {}
