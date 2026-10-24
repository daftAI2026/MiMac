//go:build !darwin

package processtap

import "errors"

func openNativeHandle(string) (nativeHandle, error) {
	return nil, errors.New("process tap: macOS is required")
}
