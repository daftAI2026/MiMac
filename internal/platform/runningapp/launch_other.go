//go:build !darwin

package runningapp

import "context"

func finishedLaunchingNative(context.Context, int32) (bool, error) { return false, ErrUnsupported }
