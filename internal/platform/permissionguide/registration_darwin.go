//go:build darwin

package permissionguide

import (
	"context"
	"os/exec"
	"regexp"
	"time"
)

var signedRegistrationHash = regexp.MustCompile(`(?m)^CDHash=([0-9a-f]{40})$`)

// HasStaleRegistration checks recent system evidence for the app's own grant.
// Unknown log formats and unavailable diagnostics leave the normal flow intact.
func HasStaleRegistration(ctx context.Context, bundlePath string, service RegistrationService, since time.Time) bool {
	age := time.Since(since)
	if (service != ListenRegistration && service != AccessibilityRegistration) || since.IsZero() || age < 0 || age > 5*time.Minute {
		return false
	}
	bundleID, ok := MainBundleIdentifier()
	if !ok || (bundleID != "dev.mimac.app" && bundleID != "dev.mimac.app.dev") {
		return false
	}
	signed, err := exec.CommandContext(ctx, "/usr/bin/codesign", "-d", "--verbose=4", bundlePath).CombinedOutput()
	if err != nil {
		return false
	}
	hash := signedRegistrationHash.FindSubmatch(signed)
	if len(hash) != 2 {
		return false
	}
	predicate := `process == "tccd" AND eventMessage CONTAINS "` + bundleID + `" AND eventMessage CONTAINS "kTCCService` + string(service) + `"`
	recent, err := exec.CommandContext(ctx, "/usr/bin/log", "show", "--start", since.Local().Format("2006-01-02 15:04:05"), "--style", "compact", "--predicate", predicate).Output()
	return err == nil && staleRegistration(string(recent), bundleID, service, string(hash[1]))
}
