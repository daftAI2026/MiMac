//go:build !darwin

package distributednotification

import "context"

func postObject(context.Context, string, string) error {
	return ErrUnsupportedPlatform
}
