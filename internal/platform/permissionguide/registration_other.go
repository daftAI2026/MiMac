//go:build !darwin

package permissionguide

import (
	"context"
	"errors"
	"time"
)

func ReadRegistrationEvidence(context.Context, string, RegistrationService, time.Time) RegistrationEvidence {
	return RegistrationEvidence{}
}

func ResetOwnRegistration(context.Context, RegistrationService) error {
	return errors.New("permission registration unavailable")
}
