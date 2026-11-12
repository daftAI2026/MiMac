//go:build !darwin

package permissionguide

import "errors"

func ClosedHandCursorImage() (CursorGraphic, error) {
	return CursorGraphic{}, errors.New("permissionguide: native cursor requires macOS")
}
