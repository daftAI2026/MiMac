package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/platform/permissionguide"
)

const privateTraceMaxBytes = 1 << 20

type permissionGuideTraceEvent string

const (
	permissionGuideTraceDragThreshold     permissionGuideTraceEvent = "drag_threshold"
	permissionGuideTraceSettingsCheck     permissionGuideTraceEvent = "settings_check"
	permissionGuideTracePreflight         permissionGuideTraceEvent = "registration_preflight"
	permissionGuideTracePermissionCheck   permissionGuideTraceEvent = "permission_check"
	permissionGuideTraceRegistrationReset permissionGuideTraceEvent = "registration_reset"
	permissionGuideTraceNativeDrop        permissionGuideTraceEvent = "native_drop"
	permissionGuideTraceResumeSave        permissionGuideTraceEvent = "resume_save"
	permissionGuideTraceResumeConsume     permissionGuideTraceEvent = "resume_consume"
	permissionGuideTraceResumeRestore     permissionGuideTraceEvent = "resume_restore"
	permissionGuideTracePermissionChange  permissionGuideTraceEvent = "permission_change"
)

type permissionGuideTraceResult string

const (
	permissionGuideTraceStarted     permissionGuideTraceResult = "started"
	permissionGuideTraceReady       permissionGuideTraceResult = "ready"
	permissionGuideTraceNotReady    permissionGuideTraceResult = "not_ready"
	permissionGuideTraceSkipped     permissionGuideTraceResult = "skipped"
	permissionGuideTraceProofReady  permissionGuideTraceResult = "proof_ready"
	permissionGuideTraceNoProof     permissionGuideTraceResult = "no_proof"
	permissionGuideTraceUnavailable permissionGuideTraceResult = "unavailable"
	permissionGuideTraceObserved    permissionGuideTraceResult = "observed"
	permissionGuideTraceReset       permissionGuideTraceResult = "reset"
	permissionGuideTraceNoReset     permissionGuideTraceResult = "not_reset"
	permissionGuideTraceTimeout     permissionGuideTraceResult = "timeout"
	permissionGuideTraceError       permissionGuideTraceResult = "error"
	permissionGuideTraceCanceled    permissionGuideTraceResult = "canceled"
	permissionGuideTraceSubmitted   permissionGuideTraceResult = "submitted"
	permissionGuideTraceRejected    permissionGuideTraceResult = "rejected"
	permissionGuideTraceFailed      permissionGuideTraceResult = "failed"
	permissionGuideTraceStale       permissionGuideTraceResult = "stale"
	permissionGuideTraceSaved       permissionGuideTraceResult = "saved"
	permissionGuideTraceConsumed    permissionGuideTraceResult = "consumed"
	permissionGuideTraceEmpty       permissionGuideTraceResult = "empty"
	permissionGuideTraceRestored    permissionGuideTraceResult = "restored"
	permissionGuideTraceNotRestored permissionGuideTraceResult = "not_restored"
	permissionGuideTraceChanged     permissionGuideTraceResult = "changed"
	permissionGuideTraceWritten     permissionGuideTraceResult = "written"
)

var permissionGuideTraceState sync.Mutex

type permissionGuideAccessTransition struct {
	id   permissionGuideID
	from permissionGuideAccess
	to   permissionGuideAccess
}

func permissionGuideAccessTransitions(previous, next permissionGuideSnapshot) []permissionGuideAccessTransition {
	var changes []permissionGuideAccessTransition
	for _, oldRow := range previous.rows {
		newAccess, exists := permissionGuideAccessForID(next, oldRow.id)
		if !exists || oldRow.access == newAccess || !permissionGuideKnownAccess(oldRow.access) || !permissionGuideKnownAccess(newAccess) {
			continue
		}
		changes = append(changes, permissionGuideAccessTransition{id: oldRow.id, from: oldRow.access, to: newAccess})
	}
	return changes
}

func permissionGuideKnownAccess(access permissionGuideAccess) bool {
	switch access {
	case permissionGuideNotDetermined, permissionGuideNotGranted, permissionGuideDenied, permissionGuideRestricted, permissionGuideGranted:
		return true
	default:
		return false
	}
}

func permissionGuideTraceRecord(at time.Time, pid int, event permissionGuideTraceEvent, id permissionGuideID, result permissionGuideTraceResult, status, previous permissionGuideAccess) string {
	return fmt.Sprintf("permission_guide time=%s pid=%d event=%s service=%s result=%s status=%s previous=%s",
		at.UTC().Format(time.RFC3339Nano), pid, event, permissionGuideTraceServiceName(id), result,
		permissionGuideTraceAccessName(status), permissionGuideTraceAccessName(previous))
}

func permissionGuideTrace(event permissionGuideTraceEvent, id permissionGuideID, result permissionGuideTraceResult, status, previous permissionGuideAccess) {
	message := permissionGuideTraceRecord(time.Now(), os.Getpid(), event, id, result, status, previous)
	permissionGuideTraceState.Lock()
	defer permissionGuideTraceState.Unlock()
	path, err := permissionGuideTraceFilePath()
	if err != nil {
		return
	}
	appendPrivateTrace(path, message)
}

// Callers serialize writes to each individual trace file.
func appendPrivateTrace(path, message string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return
	}
	if info.Size() >= privateTraceMaxBytes {
		if err := file.Truncate(0); err != nil {
			return
		}
	}
	_, _ = file.WriteString(message + "\n")
}

func permissionGuideTraceFilePath() (string, error) {
	scope := "unpackaged"
	if mygo.App.IsPackaged() {
		var ok bool
		scope, ok = permissionguide.MainBundleIdentifier()
		if !ok || !permissionGuideTraceSafeBundleID(scope) {
			return "", errors.New("permission trace bundle identity is unavailable")
		}
	}
	return hostDataPath("permission-guide-" + scope + ".log")
}

func permissionGuideTraceSafeBundleID(bundleID string) bool {
	if bundleID == "" || len(bundleID) > 180 || bundleID == "." || bundleID == ".." {
		return false
	}
	for _, r := range bundleID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func permissionGuideTraceErrorResult(err error) permissionGuideTraceResult {
	switch {
	case err == nil:
		return permissionGuideTraceObserved
	case errors.Is(err, context.DeadlineExceeded):
		return permissionGuideTraceTimeout
	case errors.Is(err, context.Canceled):
		return permissionGuideTraceCanceled
	default:
		return permissionGuideTraceError
	}
}

func permissionGuideTraceRegistrationAccess(access permissionguide.RegistrationAccess) permissionGuideAccess {
	switch access {
	case permissionguide.RegistrationAccessDenied:
		return permissionGuideDenied
	case permissionguide.RegistrationAccessGranted:
		return permissionGuideGranted
	default:
		return permissionGuideUnknown
	}
}

func permissionGuideTraceServiceName(id permissionGuideID) string {
	switch id {
	case permissionGuideInput:
		return "ListenEvent"
	case permissionGuidePostEvent:
		return "Accessibility"
	case permissionGuideBluetooth:
		return "Bluetooth"
	default:
		return "unknown"
	}
}

func permissionGuideTraceAccessName(access permissionGuideAccess) string {
	switch access {
	case permissionGuideNotDetermined:
		return "not_determined"
	case permissionGuideNotGranted:
		return "not_granted"
	case permissionGuideDenied:
		return "denied"
	case permissionGuideRestricted:
		return "restricted"
	case permissionGuideGranted:
		return "granted"
	default:
		return "unknown"
	}
}
