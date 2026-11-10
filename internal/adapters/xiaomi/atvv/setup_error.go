package atvv

import (
	"errors"
	"fmt"
)

var (
	ErrBluetoothPermission = errors.New("Bluetooth permission required")
	ErrBluetoothPoweredOff = errors.New("Bluetooth is powered off")
	ErrRemoteNotConnected  = errors.New("remote is not connected")
)

// Native diagnostics preserve the status while exposing only actionable,
// verified setup reasons. Unknown failures remain unknown.
func nativeFailure(owner string, status int32) error {
	var reason error
	switch status {
	case -10:
		reason = ErrRemoteNotConnected
	case -21:
		reason = ErrBluetoothPermission
	case -22:
		reason = ErrBluetoothPoweredOff
	}
	if reason != nil {
		return fmt.Errorf("atvv %s: native failure status=%d: %w", owner, status, reason)
	}
	return fmt.Errorf("atvv %s: native failure status=%d", owner, status)
}
