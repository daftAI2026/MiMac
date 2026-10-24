//go:build !darwin

package keyoutput

import "errors"

func loadEventAPI() (*eventAPI, error) {
	return nil, errors.New("keyoutput: CoreGraphics output is available only on macOS")
}
