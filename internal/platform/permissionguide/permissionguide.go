// Package permissionguide exposes read-only permission probes and lightweight
// native helpers used by the onboarding panel. Permission requests are
// separate explicit calls made only from user actions.
package permissionguide

// BluetoothAccess is the current CoreBluetooth authorization state.
type BluetoothAccess uint8

const (
	BluetoothAccessUnknown BluetoothAccess = iota
	BluetoothAccessNotDetermined
	BluetoothAccessRestricted
	BluetoothAccessDenied
	BluetoothAccessAuthorized
)

// Rect is a window frame in global screen points, with a top-left origin.
type Rect struct {
	X, Y, Width, Height float64
}
