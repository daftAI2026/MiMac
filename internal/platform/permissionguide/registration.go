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

var registrationLogHeader = regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.`)
var registrationCodeHash = regexp.MustCompile(`cdhash H"([a-fA-F0-9]{40})"`)

// registrationEvidence extracts only the exact bundle/service's stale code
// requirement. Callers use registrationEvidenceBetween for time-bounded proof.
func registrationEvidence(log, bundleID string, service RegistrationService, codeHash string) RegistrationEvidence {
	return registrationEvidenceBetween(log, bundleID, service, codeHash, time.Time{}, time.Time{})
}

func registrationEvidenceBetween(log, bundleID string, service RegistrationService, codeHash string, since, until time.Time) RegistrationEvidence {
	serviceName := "kTCCService" + string(service)
	mismatch := "Failed to match existing code requirement for subject " + bundleID + " and service " + serviceName
	update := "Update Access Record: " + serviceName + " for " + bundleID + " to "
	change := ", service=" + serviceName + ", identifier_type=Bundle ID, identifier=" + bundleID + ">"
	entries := registrationLogEntries(log, since, until)
	var evidence RegistrationEvidence
	for i := 0; i < len(entries); {
		line := entries[i]
		i++
		if !registrationLogHeader.MatchString(line) {
			continue
		}
		j := i
		for j < len(entries) && !registrationLogHeader.MatchString(entries[j]) {
			j++
		}
		entry := strings.Join(entries[i-1:j], "\n")
		if strings.Contains(line, "TCCDEvent: type=") && strings.Contains(line, change) {
			evidence.Mutated = true
			evidence.Stale = false
			evidence.StaleAt = time.Time{}
			evidence.StaleCodeHash = ""
			evidence.CurrentCodeHash = ""
			evidence.Changed = strings.Contains(line, "TCCDEvent: type=Insert,") || strings.Contains(line, "TCCDEvent: type=Modify,")
			if strings.Contains(line, "TCCDEvent: type=Delete,") {
				evidence.CurrentWrite = false
			}
		}
		if strings.Contains(line, update) {
			hashes := registrationCodeHash.FindAllStringSubmatch(entry, 2)
			evidence.CurrentWrite = len(hashes) == 1 && strings.EqualFold(hashes[0][1], codeHash)
		}
		if strings.Contains(line, mismatch) {
			hashes := registrationCodeHash.FindAllStringSubmatch(entry, 3)
			evidence.Stale = len(hashes) == 2 && strings.EqualFold(hashes[1][1], codeHash) && !strings.EqualFold(hashes[0][1], codeHash)
			if evidence.Stale {
				evidence.StaleAt = registrationEventTime(line)
				evidence.StaleCodeHash = strings.ToLower(hashes[0][1])
				evidence.CurrentCodeHash = strings.ToLower(hashes[1][1])
			}
		}
		i = j
	}
	return evidence
}

func registrationLogEntries(log string, since, until time.Time) []string {
	entries := strings.Split(log, "\n")
	if since.IsZero() && until.IsZero() {
		return entries
	}
	filtered := make([]string, 0, len(entries))
	for i := 0; i < len(entries); {
		if !registrationLogHeader.MatchString(entries[i]) {
			i++
			continue
		}
		start := i
		i++
		for i < len(entries) && !registrationLogHeader.MatchString(entries[i]) {
			i++
		}
		observed := registrationEventTime(entries[start])
		if observed.IsZero() || (!since.IsZero() && !observed.After(since)) || (!until.IsZero() && observed.After(until)) {
			continue
		}
		filtered = append(filtered, entries[start:i]...)
	}
	return filtered
}

func validDragRegistrationService(service RegistrationService) bool {
	return service == ListenRegistration || service == AccessibilityRegistration
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

func registrationEventTime(line string) time.Time {
	const compactTimestamp = "2006-01-02 15:04:05.000"
	if len(line) < len(compactTimestamp) {
		return time.Time{}
	}
	observed, _ := time.ParseInLocation(compactTimestamp, line[:len(compactTimestamp)], time.Local)
	return observed
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
