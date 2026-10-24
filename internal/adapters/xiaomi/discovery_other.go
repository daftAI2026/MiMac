//go:build !darwin

package xiaomi

import "errors"

func DiscoverHID() ([]HIDDevice, error) {
	return nil, errors.New("xiaomi: HID discovery requires macOS")
}
