// Package siriglass lazily attaches the verified native Siri glass renderer
// to a MyGo window. The renderer is optional presentation only.
package siriglass

import "errors"

// ErrUnsupported means that the native renderer is unavailable on this host.
var ErrUnsupported = errors.New("siriglass: native renderer is unsupported on this host")
