//go:build !darwin

package symbols

import (
	"errors"
	"image"
)

// Image is available only on macOS, where AppKit provides SF Symbols.
func Image(_ string, _ bool) (image.Image, error) {
	return nil, errors.New("symbols: AppKit SF Symbols are available only on macOS")
}
