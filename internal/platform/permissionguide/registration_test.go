package permissionguide

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStaleRegistrationRequiresExactCurrentCodeAndLatestEvidence(t *testing.T) {
	const header = "2026-11-20 00:07:32.104 Df tccd: "
	const bundle = "dev.mimac.app.dev"
	old, current := strings.Repeat("a", 40), strings.Repeat("b", 40)
	observation := header + "Failed to match existing code requirement for subject " + bundle + " and service kTCCServiceListenEvent\n" +
		"\tcdhash H\"" + old + "\"\n\tcdhash H\"" + current + "\"\n"
	modify := header + "Publishing <TCCDEvent: type=Modify, service=kTCCServiceListenEvent, identifier_type=Bundle ID, identifier=" + bundle + ">\n"
	update := header + "Update Access Record: kTCCServiceListenEvent for " + bundle + " to Allowed (System Set) (v1)\n" +
		"\t CodeReq: cdhash H\"" + current + "\"\n"
	for _, test := range []struct {
		name       string
		log        string
		stale      bool
		mutated    bool
		currentSet bool
	}{
		{"exact old to current mismatch", observation, true, false, false},
		{"same service changed after mismatch", observation + modify, false, true, false},
		{"same service changed before mismatch", modify + observation, true, true, false},
		{"new current record then stale preflight", update + modify + observation, true, true, true},
		{"other service change", strings.ReplaceAll(modify, "ListenEvent", "Accessibility") + observation, true, false, false},
		{"other service mismatch", strings.ReplaceAll(observation, "ListenEvent", "Accessibility"), false, false, false},
		{"other app mismatch", strings.ReplaceAll(observation, bundle, "dev.mimac.app"), false, false, false},
		{"current hash unknown", strings.ReplaceAll(observation, current, old), false, false, false},
		{"generic denial", header + "denied", false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := registrationEvidence(test.log, bundle, ListenRegistration, current)
			if got.Stale != test.stale || got.Mutated != test.mutated || got.CurrentWrite != test.currentSet {
				t.Fatalf("evidence = %+v, want stale=%t mutated=%t currentWrite=%t", got, test.stale, test.mutated, test.currentSet)
			}
			if got.Stale && (got.StaleCodeHash != old || got.CurrentCodeHash != current) {
				t.Fatalf("stale identity = %s -> %s, want %s -> %s", got.StaleCodeHash, got.CurrentCodeHash, old, current)
			}
		})
	}
}

func TestRegistrationEvidenceWindowRejectsPreviousProbeLogReplay(t *testing.T) {
	const bundle = "dev.mimac.app.dev"
	old, current := strings.Repeat("a", 40), strings.Repeat("b", 40)
	mismatch := func(at string) string {
		return at + " Df tccd: Failed to match existing code requirement for subject " + bundle + " and service kTCCServiceListenEvent\n" +
			"\tcdhash H\"" + old + "\"\n\tcdhash H\"" + current + "\"\n"
	}
	log := mismatch("2026-11-20 00:07:31.900") + mismatch("2026-11-20 00:07:32.100")
	since := time.Date(2026, 11, 20, 0, 7, 32, 0, time.Local)
	got := registrationEvidenceBetween(log, bundle, ListenRegistration, current, since, since.Add(time.Second))
	if !got.Stale || !got.StaleAt.Equal(time.Date(2026, 11, 20, 0, 7, 32, 100_000_000, time.Local)) {
		t.Fatalf("fresh-window evidence = %+v", got)
	}
	oldOnly := registrationEvidenceBetween(mismatch("2026-11-20 00:07:31.900"), bundle, ListenRegistration, current, since, since.Add(time.Second))
	if oldOnly.Stale {
		t.Fatalf("previous probe evidence replayed: %+v", oldOnly)
	}
}

func TestRegistrationChangeInvalidationRequiresExactServiceAndBundle(t *testing.T) {
	const bundle = "dev.mimac.app.dev"
	line := "Publishing <TCCDEvent: type=Modify, service=kTCCServiceListenEvent, identifier_type=Bundle ID, identifier=" + bundle + ">"
	for _, test := range []struct {
		name    string
		line    string
		service RegistrationService
		bundle  string
		want    bool
	}{
		{"matching insert", strings.ReplaceAll(line, "Modify", "Insert"), ListenRegistration, bundle, true},
		{"matching modify", line, ListenRegistration, bundle, true},
		{"matching delete", strings.ReplaceAll(line, "Modify", "Delete"), ListenRegistration, bundle, true},
		{"wrong service", strings.ReplaceAll(line, "ListenEvent", "Accessibility"), ListenRegistration, bundle, false},
		{"wrong bundle", strings.ReplaceAll(line, bundle, "dev.mimac.app"), ListenRegistration, bundle, false},
		{"non event", "TCC access read completed", ListenRegistration, bundle, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := registrationChangeEvent(test.line, test.bundle, test.service); got != test.want {
				t.Fatalf("registrationChangeEvent() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRegistrationNDJSONStreamRecordExtractsExactFreshMismatch(t *testing.T) {
	const bundle = "dev.mimac.app.dev"
	old, current := strings.Repeat("a", 40), strings.Repeat("b", 40)
	at := "2026-11-21 04:28:24.009495+0800"
	message := "Failed to match existing code requirement for subject " + bundle + " and service kTCCServiceAccessibility\n" +
		"\tcdhash H\"" + old + "\"\n\tcdhash H\"" + current + "\""
	line, err := json.Marshal(registrationStreamRecord{Timestamp: at, EventMessage: message})
	if err != nil {
		t.Fatal(err)
	}
	gotAt, gotMessage, ok := parseRegistrationStreamRecord(string(line))
	wantAt := time.Date(2026, 11, 21, 4, 28, 24, 9_495_000, time.FixedZone("UTC+8", 8*60*60))
	if !ok || !gotAt.Equal(wantAt) || gotMessage != message {
		t.Fatalf("parsed NDJSON record = (%v, %q, %t), want (%v, %q, true)", gotAt, gotMessage, ok, wantAt, message)
	}
	if _, _, ok := parseRegistrationStreamRecord(`Filtering the log data using "process == \"tccd\""`); ok {
		t.Fatal("Filtering banner was treated as an event")
	}

	evidence := registrationEvidenceFromStreamEvent(gotMessage, bundle, AccessibilityRegistration, current, gotAt)
	if !evidence.Stale || evidence.StaleAt != gotAt || evidence.StaleCodeHash != old || evidence.CurrentCodeHash != current {
		t.Fatalf("exact mismatch evidence = %+v", evidence)
	}
	for _, test := range []struct {
		name      string
		message   string
		service   RegistrationService
		bundle    string
		current   string
		wantStale bool
	}{
		{"wrong bundle", gotMessage, AccessibilityRegistration, "dev.mimac.app", current, false},
		{"wrong service", gotMessage, ListenRegistration, bundle, current, false},
		{"wrong current hash", gotMessage, AccessibilityRegistration, bundle, old, false},
		{"same hashes", strings.ReplaceAll(gotMessage, current, old), AccessibilityRegistration, bundle, old, false},
		{"missing hash", strings.Split(gotMessage, "\n")[0], AccessibilityRegistration, bundle, current, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := registrationEvidenceFromStreamEvent(test.message, test.bundle, test.service, test.current, gotAt)
			if got.Stale != test.wantStale {
				t.Fatalf("stream stale evidence = %+v, want %t", got, test.wantStale)
			}
		})
	}
}

func TestRegistrationNDJSONStreamKeepsMutationAndCurrentWriteEvidence(t *testing.T) {
	const bundle = "dev.mimac.app.dev"
	current := strings.Repeat("b", 40)
	at := time.Date(2026, 11, 21, 4, 28, 24, 9_495_000, time.FixedZone("UTC+8", 8*60*60))
	mutation := "Publishing <TCCDEvent: type=Modify, service=kTCCServiceAccessibility, identifier_type=Bundle ID, identifier=" + bundle + ">"
	if got := registrationEvidenceFromStreamEvent(mutation, bundle, AccessibilityRegistration, current, at); !got.Mutated || !got.Changed {
		t.Fatalf("same-service mutation evidence = %+v", got)
	}
	update := "Update Access Record: kTCCServiceAccessibility for " + bundle + " to Allowed (System Set) (v1)\n" +
		"\tCodeReq: cdhash H\"" + current + "\""
	if got := registrationEvidenceFromStreamEvent(update, bundle, AccessibilityRegistration, current, at); !got.CurrentWrite {
		t.Fatalf("current-code write evidence = %+v", got)
	}
}

func TestRegistrationStreamExitInvalidatesOnlyPendingProof(t *testing.T) {
	pending := &DragRegistrationProof{}
	invalidateRegistrationProofIfPending(pending)
	if !pending.invalidated.Load() {
		t.Fatal("an ended or failed log stream left its pending proof valid")
	}

	consumed := &DragRegistrationProof{}
	consumed.consumed.Store(true)
	invalidateRegistrationProofIfPending(consumed)
	if consumed.invalidated.Load() {
		t.Fatal("normal stream shutdown invalidated an already-consumed proof")
	}
}

func TestDragRegistrationProofIsOneUseExactAndFresh(t *testing.T) {
	const bundle = "/Applications/MiMac Dev.app"
	const service = ListenRegistration
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	newProof := func() *DragRegistrationProof {
		return &DragRegistrationProof{
			bundlePath: bundle, bundleID: "dev.mimac.app.dev", service: service,
			staleCodeHash: strings.Repeat("a", 40), currentCodeHash: strings.Repeat("b", 40),
			preparedAt: now.Add(-time.Second),
		}
	}
	proof := newProof()
	if !proof.consume(now, bundle, service, RegistrationAccessDenied) {
		t.Fatal("fresh, matching denial proof was rejected")
	}
	if proof.consume(now, bundle, service, RegistrationAccessDenied) {
		t.Fatal("one proof authorized two resets")
	}
	for _, test := range []struct {
		name    string
		bundle  string
		service RegistrationService
		access  RegistrationAccess
		at      time.Time
	}{
		{"grant now", bundle, service, RegistrationAccessGranted, now},
		{"unknown now", bundle, service, RegistrationAccessUnknown, now},
		{"wrong bundle", "/Applications/Other.app", service, RegistrationAccessDenied, now},
		{"wrong service", bundle, AccessibilityRegistration, RegistrationAccessDenied, now},
		{"expired", bundle, service, RegistrationAccessDenied, now.Add(31 * time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if newProof().consume(test.at, test.bundle, test.service, test.access) {
				t.Fatal("invalid proof was accepted")
			}
		})
	}
}
