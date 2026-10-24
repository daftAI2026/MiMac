//go:build !darwin

package inputsource

import "errors"

func CurrentID() (string, error) { return "", errors.New("inputsource: macOS is required") }
