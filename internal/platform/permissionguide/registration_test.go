package permissionguide

import (
	"strings"
	"testing"
)

func TestStaleRegistrationRequiresCurrentCodeAndLatestEvidence(t *testing.T) {
	const header = "2026-11-20 00:07:32.104 Df tccd: "
	const bundle = "dev.mimac.app.dev"
	old, current := strings.Repeat("a", 40), strings.Repeat("b", 40)
	observation := header + "Failed to match existing code requirement for subject " + bundle + " and service kTCCServiceListenEvent\n" +
		"\tcdhash H\"" + old + "\"\n\tcdhash H\"" + current + "\"\n"
	change := header + "Publishing <TCCDEvent: type=Modify, service=kTCCServiceListenEvent, identifier_type=Bundle ID, identifier=" + bundle + ">\n"
	for _, test := range []struct {
		name string
		log  string
		want bool
	}{
		{"current code", observation, true},
		{"after a change", change + observation, true},
		{"superseded by a change", observation + change, false},
		{"other permission change", observation + strings.ReplaceAll(change, "ListenEvent", "Accessibility"), true},
		{"other service", strings.ReplaceAll(observation, "ListenEvent", "Accessibility"), false},
		{"other app", strings.ReplaceAll(observation, bundle, "dev.mimac.app"), false},
		{"unknown current code", strings.ReplaceAll(observation, current, old), false},
		{"generic denial", header + "denied", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := staleRegistration(test.log, bundle, ListenRegistration, current); got != test.want {
				t.Fatalf("stale registration = %t, want %t", got, test.want)
			}
		})
	}
}
