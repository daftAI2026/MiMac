package permissionguide

import (
	"regexp"
	"strings"
)

type RegistrationService string

const (
	ListenRegistration        RegistrationService = "ListenEvent"
	AccessibilityRegistration RegistrationService = "Accessibility"
)

var registrationLogHeader = regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.`)
var registrationCodeHash = regexp.MustCompile(`cdhash H"([a-fA-F0-9]{40})"`)

// A denial alone cannot distinguish a missing grant, a cancelled prompt or an
// older code requirement. Recognize only an explicit mismatch for this binary.
func staleRegistration(log, bundleID string, service RegistrationService, codeHash string) bool {
	serviceName := "kTCCService" + string(service)
	mismatch := "Failed to match existing code requirement for subject " + bundleID + " and service " + serviceName
	change := ", service=" + serviceName + ", identifier_type=Bundle ID, identifier=" + bundleID + ">"
	entries := strings.Split(log, "\n")
	stale := false
	for i := 0; i < len(entries); {
		line := entries[i]
		i++
		if !registrationLogHeader.MatchString(line) {
			continue
		}
		if strings.Contains(line, "TCCDEvent: type=") && strings.Contains(line, change) {
			stale = false
		}
		if !strings.HasSuffix(line, mismatch) {
			continue
		}
		j := i
		for j < len(entries) && !registrationLogHeader.MatchString(entries[j]) {
			j++
		}
		hashes := registrationCodeHash.FindAllStringSubmatch(strings.Join(entries[i:j], "\n"), 3)
		stale = len(hashes) == 2 && strings.EqualFold(hashes[1][1], codeHash) && !strings.EqualFold(hashes[0][1], codeHash)
		i = j
	}
	return stale
}
