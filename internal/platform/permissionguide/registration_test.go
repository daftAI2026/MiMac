package permissionguide

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRegistrationStreamPredicateUsesExactBundleAndService(t *testing.T) {
	const bundle = "dev.mimac.app.dev"
	for _, test := range []struct {
		service RegistrationService
		want    string
	}{
		{ListenRegistration, `composedMessage CONTAINS "kTCCServiceListenEvent"`},
		{AccessibilityRegistration, `composedMessage CONTAINS "kTCCServiceAccessibility"`},
	} {
		predicate, ok := registrationStreamPredicate(bundle, test.service)
		if !ok || !strings.Contains(predicate, `composedMessage CONTAINS "`+bundle+`"`) || !strings.Contains(predicate, test.want) || !strings.Contains(predicate, `process == "tccd"`) {
			t.Errorf("predicate for %s = %q, %t", test.service, predicate, ok)
		}
	}
	if _, ok := registrationStreamPredicate(bundle, "Camera"); ok {
		t.Fatal("unsupported TCC service received a registration stream")
	}
	if _, ok := registrationStreamPredicate("", ListenRegistration); ok {
		t.Fatal("empty bundle ID received a registration stream")
	}
}

func TestListenRegistrationNDJSONUsesFreshExactCodeMismatch(t *testing.T) {
	const bundle = "dev.mimac.app.dev"
	old, current := strings.Repeat("a", 40), strings.Repeat("b", 40)
	const at = "2026-11-21 22:22:40.901984+0800"
	message := "Failed to match existing code requirement for subject " + bundle + " and service kTCCServiceListenEvent\n" +
		"\tcdhash H\"" + old + "\"\n\tcdhash H\"" + current + "\""
	line, err := json.Marshal(map[string]string{"timestamp": at, "messageType": "Default", "eventMessage": message})
	if err != nil {
		t.Fatal(err)
	}
	gotAt, gotMessage, ok := parseRegistrationStreamRecord(string(line))
	wantAt := time.Date(2026, 11, 21, 22, 22, 40, 901_984_000, time.FixedZone("UTC+8", 8*60*60))
	if !ok || !gotAt.Equal(wantAt) || gotMessage != message {
		t.Fatalf("parsed ListenEvent NDJSON = (%v, %q, %t), want (%v, %q, true)", gotAt, gotMessage, ok, wantAt, message)
	}
	evidence := registrationEvidenceFromStreamEvent(gotMessage, bundle, ListenRegistration, current, gotAt)
	if !evidence.Stale || evidence.StaleAt != gotAt || evidence.StaleCodeHash != old || evidence.CurrentCodeHash != current {
		t.Fatalf("exact ListenEvent mismatch = %+v", evidence)
	}
	for _, test := range []struct {
		name    string
		message string
		bundle  string
		service RegistrationService
		hash    string
	}{
		{"wrong service", message, bundle, AccessibilityRegistration, current},
		{"wrong bundle", message, "dev.mimac.app", ListenRegistration, current},
		{"wrong current CDHash", message, bundle, ListenRegistration, old},
		{"same old and current CDHash", strings.Replace(message, current, old, 1), bundle, ListenRegistration, old},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := registrationEvidenceFromStreamEvent(test.message, test.bundle, test.service, test.hash, gotAt); got.Stale {
				t.Fatalf("mismatch proof accepted wrong identity: %+v", got)
			}
		})
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
	for _, service := range []RegistrationService{ListenRegistration, AccessibilityRegistration} {
		t.Run(string(service), func(t *testing.T) {
			mutation := "Publishing <TCCDEvent: type=Modify, service=kTCCService" + string(service) + ", identifier_type=Bundle ID, identifier=" + bundle + ">"
			if got := registrationEvidenceFromStreamEvent(mutation, bundle, service, current, at); !got.Mutated || !got.Changed {
				t.Fatalf("same-service mutation evidence = %+v", got)
			}
			update := "Update Access Record: kTCCService" + string(service) + " for " + bundle + " to Allowed (System Set) (v1)\n" +
				"\tCodeReq: cdhash H\"" + current + "\""
			if got := registrationEvidenceFromStreamEvent(update, bundle, service, current, at); !got.CurrentWrite {
				t.Fatalf("current-code write evidence = %+v", got)
			}
		})
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
