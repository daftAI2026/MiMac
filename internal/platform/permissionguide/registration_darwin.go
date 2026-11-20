//go:build darwin

package permissionguide

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"time"
)

var signedRegistrationHash = regexp.MustCompile(`(?m)^CDHash=([0-9a-f]{40})$`)

func ownRegistrationBundle(service RegistrationService) (string, bool) {
	if service != ListenRegistration && service != AccessibilityRegistration {
		return "", false
	}
	bundleID, ok := MainBundleIdentifier()
	return bundleID, ok && (bundleID == "dev.mimac.app" || bundleID == "dev.mimac.app.dev")
}

// ResetOwnRegistration clears one service for this bundle after the caller
// proves that its existing record belongs to an older code requirement.
func ResetOwnRegistration(ctx context.Context, service RegistrationService) error {
	bundleID, ok := ownRegistrationBundle(service)
	if !ok {
		return errors.New("unsupported permission registration identity")
	}
	return exec.CommandContext(ctx, "/usr/bin/tccutil", "reset", string(service), bundleID).Run()
}

// ReadRegistrationEvidence checks recent system evidence for the app's own grant.
// Unknown log formats and unavailable diagnostics leave the normal flow intact.
func ReadRegistrationEvidence(ctx context.Context, bundlePath string, service RegistrationService, since time.Time) RegistrationEvidence {
	age := time.Since(since)
	if since.IsZero() || age < 0 || age > 5*time.Minute {
		return RegistrationEvidence{}
	}
	bundleID, ok := ownRegistrationBundle(service)
	if !ok {
		return RegistrationEvidence{}
	}
	signed, err := exec.CommandContext(ctx, "/usr/bin/codesign", "-d", "--verbose=4", bundlePath).CombinedOutput()
	if err != nil {
		return RegistrationEvidence{}
	}
	hash := signedRegistrationHash.FindSubmatch(signed)
	if len(hash) != 2 {
		return RegistrationEvidence{}
	}
	predicate := `process == "tccd" AND eventMessage CONTAINS "` + bundleID + `" AND eventMessage CONTAINS "kTCCService` + string(service) + `"`
	recent, err := exec.CommandContext(ctx, "/usr/bin/log", "show", "--start", since.Local().Format("2006-01-02 15:04:05"), "--style", "compact", "--predicate", predicate).Output()
	if err != nil {
		return RegistrationEvidence{}
	}
	return registrationEvidence(string(recent), bundleID, service, string(hash[1]))
}
