//go:build !darwin

package statuscolor

import (
	"errors"

	"github.com/egoist/mygo/ui"
)

// SystemGreen is available only on macOS, where AppKit supplies the semantic color.
func SystemGreen(bool) (ui.Color, error) {
	return ui.Color{}, errors.New("statuscolor: AppKit system colors are available only on macOS")
}
