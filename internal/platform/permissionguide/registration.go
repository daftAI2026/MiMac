package permissionguide

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

type RegistrationService string

type RegistrationAccess uint8

const (
	RegistrationAccessUnknown RegistrationAccess = iota
	RegistrationAccessDenied
	RegistrationAccessGranted
)

type RegistrationEvidence struct {
	Stale, Changed, Mutated, CurrentWrite bool
	StaleAt                               time.Time
	StaleCodeHash, CurrentCodeHash        string
}

// DragRegistrationProof is an opaque, short-lived, one-use proof that a fresh
// read-only probe observed this exact signed app using an older TCC identity.
// Its atomic state prevents concurrent reuse through the same proof pointer.
type DragRegistrationProof struct {
	bundlePath      string
	bundleID        string
	service         RegistrationService
	staleCodeHash   string
	currentCodeHash string
	preparedAt      time.Time
	invalidated     atomic.Bool
	consumed        atomic.Bool
	watcherCancel   func()
}

// Valid reports whether the proof is still pending, unexpired, and not
// invalidated by a same-service TCC change observed by its background watcher.
func (p *DragRegistrationProof) Valid(now time.Time) bool {
	return p != nil && !p.consumed.Load() && p.validAt(now)
}

func (p *DragRegistrationProof) validAt(now time.Time) bool {
	return p != nil && !p.invalidated.Load() &&
		!p.preparedAt.IsZero() && !now.Before(p.preparedAt) && now.Sub(p.preparedAt) < 30*time.Second &&
		validDragRegistrationService(p.service) && p.bundlePath != "" && p.bundleID != "" &&
		p.staleCodeHash != "" && p.currentCodeHash != "" && p.staleCodeHash != p.currentCodeHash
}

const (
	RegistrationProbeFlag                         = "--permission-registration-probe"
	ListenRegistration        RegistrationService = "ListenEvent"
	AccessibilityRegistration RegistrationService = "Accessibility"
)

var registrationCodeHash = regexp.MustCompile(`cdhash H"([a-fA-F0-9]{40})"`)

func validDragRegistrationService(service RegistrationService) bool {
	return service == ListenRegistration || service == AccessibilityRegistration
}

func registrationStreamPredicate(bundleID string, service RegistrationService) (string, bool) {
	if bundleID == "" || !validDragRegistrationService(service) {
		return "", false
	}
	return `process == "tccd" AND composedMessage CONTAINS "` + bundleID + `" AND composedMessage CONTAINS "kTCCService` + string(service) + `"`, true
}

func registrationChangeEvent(line, bundleID string, service RegistrationService) bool {
	change := ", service=kTCCService" + string(service) + ", identifier_type=Bundle ID, identifier=" + bundleID + ">"
	return strings.Contains(line, "TCCDEvent: type=") && strings.Contains(line, change) &&
		(strings.Contains(line, "TCCDEvent: type=Insert,") || strings.Contains(line, "TCCDEvent: type=Modify,") || strings.Contains(line, "TCCDEvent: type=Delete,"))
}

func (p *DragRegistrationProof) consume(now time.Time, bundlePath string, service RegistrationService, access RegistrationAccess) bool {
	if p == nil || !p.consumed.CompareAndSwap(false, true) {
		return false
	}
	if p.watcherCancel != nil {
		p.watcherCancel()
	}
	if !p.validAt(now) || access != RegistrationAccessDenied || !validDragRegistrationService(service) ||
		service != p.service || p.bundlePath != bundlePath || p.staleCodeHash == "" || p.currentCodeHash == "" ||
		p.staleCodeHash == p.currentCodeHash {
		return false
	}
	return true
}

func invalidateRegistrationProofIfPending(proof *DragRegistrationProof) {
	if proof != nil && !proof.consumed.Load() {
		proof.invalidated.Store(true)
	}
}

type registrationStreamRecord struct {
	Timestamp    string `json:"timestamp"`
	EventMessage string `json:"eventMessage"`
}

// parseRegistrationStreamRecord reads one unified-log NDJSON event. The log
// command's initial Filtering banner is intentionally not treated as an event.
func parseRegistrationStreamRecord(line string) (time.Time, string, bool) {
	if strings.HasPrefix(strings.TrimSpace(line), "Filtering") {
		return time.Time{}, "", false
	}
	var record registrationStreamRecord
	if err := json.Unmarshal([]byte(line), &record); err != nil || record.Timestamp == "" || record.EventMessage == "" {
		return time.Time{}, "", false
	}
	const layout = "2006-01-02 15:04:05.999999999-0700"
	observed, err := time.Parse(layout, record.Timestamp)
	if err != nil {
		return time.Time{}, "", false
	}
	return observed, record.EventMessage, true
}

// registrationEvidenceFromStreamEvent extracts only evidence from one exact
// app/service message emitted by tccd. The stream predicate narrows traffic;
// these checks keep a broader or changed predicate fail closed.
func registrationEvidenceFromStreamEvent(message, bundleID string, service RegistrationService, currentHash string, at time.Time) RegistrationEvidence {
	if !validDragRegistrationService(service) || bundleID == "" || at.IsZero() {
		return RegistrationEvidence{}
	}
	serviceName := "kTCCService" + string(service)
	var evidence RegistrationEvidence
	mismatch := "Failed to match existing code requirement for subject " + bundleID + " and service " + serviceName
	if header, _, _ := strings.Cut(message, "\n"); header == mismatch {
		hashes := registrationCodeHash.FindAllStringSubmatch(message, 3)
		evidence.Stale = len(hashes) == 2 && currentHash != "" &&
			strings.EqualFold(hashes[1][1], currentHash) && !strings.EqualFold(hashes[0][1], hashes[1][1])
		if evidence.Stale {
			evidence.StaleAt = at
			evidence.StaleCodeHash = strings.ToLower(hashes[0][1])
			evidence.CurrentCodeHash = strings.ToLower(hashes[1][1])
		}
	}
	update := "Update Access Record: " + serviceName + " for " + bundleID + " to "
	if strings.Contains(message, update) {
		hashes := registrationCodeHash.FindAllStringSubmatch(message, 2)
		evidence.CurrentWrite = len(hashes) == 1 && currentHash != "" && strings.EqualFold(hashes[0][1], currentHash)
	}
	evidence.Mutated = registrationChangeEvent(message, bundleID, service)
	evidence.Changed = evidence.Mutated &&
		(strings.Contains(message, "TCCDEvent: type=Insert,") || strings.Contains(message, "TCCDEvent: type=Modify,"))
	return evidence
}
