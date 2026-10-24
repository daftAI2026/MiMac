//go:build !darwin

package audiooutput

import "errors"

func openNativeQueue(Config, func(slot int)) (nativeQueue, error) {
	return nil, errors.New("audiooutput: native AudioQueue is available only on macOS")
}
