//go:build !darwin

package runningapp

import "context"

func findNative(context.Context, target) (int32, error) {
	return 0, ErrUnsupported
}
