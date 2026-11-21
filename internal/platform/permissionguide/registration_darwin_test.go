package permissionguide

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestListenRegistrationLiveEvidenceMustFollowFreshProbe(t *testing.T) {
	const bundle = "dev.mimac.app.dev"
	old, current := strings.Repeat("a", 40), strings.Repeat("b", 40)
	probeStarted := time.Date(2026, 11, 21, 22, 22, 40, 902_000_000, time.FixedZone("UTC+8", 8*60*60))
	message := "Failed to match existing code requirement for subject " + bundle + " and service kTCCServiceListenEvent\n" +
		"\tcdhash H\"" + old + "\"\n\tcdhash H\"" + current + "\""
	watcher := &registrationLogWatcher{evidence: make(chan RegistrationEvidence, 2), done: make(chan struct{})}
	watcher.evidence <- registrationEvidenceFromStreamEvent(message, bundle, ListenRegistration, current, probeStarted.Add(-time.Millisecond))
	watcher.evidence <- registrationEvidenceFromStreamEvent(message, bundle, ListenRegistration, current, probeStarted.Add(time.Millisecond))
	got := watcher.evidenceAfterFreshProbe(context.Background(), probeStarted)
	if !got.Stale || !got.StaleAt.After(probeStarted) || got.StaleCodeHash != old || got.CurrentCodeHash != current {
		t.Fatalf("fresh ListenEvent evidence = %+v", got)
	}
}
