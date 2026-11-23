//go:build !darwin && !linux && !freebsd && !netbsd

package dotscope

import "errors"

func bindNative(string, *nativeAPI) (func(), error) {
	return nil, errors.New("voice dots: native model unavailable on this platform")
}
