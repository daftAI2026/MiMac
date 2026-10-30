//go:build !darwin

package symbols

import (
	"errors"
	"image"
)

// Image is available only on macOS, where AppKit provides SF Symbols.
func Image(_ string, _ bool, _ float64) (image.Image, error) {
	return nil, errors.New("symbols: AppKit SF Symbols are available only on macOS")
}

// Images is available only on macOS, where AppKit provides SF Symbols.
func Images(_ []string, _ bool, _, _ float64) ([]image.Image, error) {
	return nil, errors.New("symbols: AppKit SF Symbols are available only on macOS")
}
