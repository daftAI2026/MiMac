//go:build !darwin

package permissionguide

import (
	"context"
	"time"
)

func HasStaleRegistration(context.Context, string, RegistrationService, time.Time) bool {
	return false
}
