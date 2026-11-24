package permissionguide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistrationProbeReceiptRequiresOneCompleteStatusLine(t *testing.T) {
	for _, test := range []struct {
		name       string
		data       string
		wantStatus string
		wantError  bool
		wantWait   bool
	}{
		{name: "denied", data: "denied\n", wantStatus: "denied"},
		{name: "granted", data: "granted\n", wantStatus: "granted"},
		{name: "explicit unknown", data: "unknown\n", wantStatus: "unknown"},
		{name: "closed empty", data: "", wantError: true, wantWait: true},
		{name: "partial line", data: "denied", wantError: true, wantWait: true},
		{name: "extra content", data: "unknown\nextra\n", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receipt")
			if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if test.wantWait {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			got, err := readRegistrationProbeReceipt(ctx, path)
			if test.wantWait && !errors.Is(err, context.Canceled) {
				t.Fatalf("incomplete receipt = %q, %v; want bounded wait, not a status", got, err)
			}
			if test.wantWait {
				return
			}
			if (err != nil) != test.wantError || got != test.wantStatus {
				t.Fatalf("receipt = %q, %v; want %q, error=%t", got, err, test.wantStatus, test.wantError)
			}
		})
	}
}

func TestRegistrationProbeReceiptWaitsForACompleteFreshResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	writeErr := make(chan error, 1)
	timer := time.AfterFunc(20*time.Millisecond, func() {
		writeErr <- os.WriteFile(path, []byte("unknown\n"), 0600)
	})
	defer timer.Stop()
	startedAt := time.Now()
	got, err := readRegistrationProbeReceipt(ctx, path)
	if err != nil || got != "unknown" {
		t.Fatalf("waited receipt = %q, %v", got, err)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed >= 500*time.Millisecond {
		t.Fatalf("receipt wait added a fixed delay: %v", elapsed)
	}
}

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

func TestRepeatedIdenticalListenMismatchObservationsDoNotOverflow(t *testing.T) {
	const bundle = "dev.mimac.app.dev"
	old, current := strings.Repeat("a", 40), strings.Repeat("b", 40)
	message := "Failed to match existing code requirement for subject " + bundle + " and service kTCCServiceListenEvent\n" +
		"\tcdhash H\"" + old + "\"\n\tcdhash H\"" + current + "\""
	probeStarted := time.Date(2026, 11, 23, 16, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))

	for _, count := range []int{33, 64} {
		t.Run(fmt.Sprintf("%d observations", count), func(t *testing.T) {
			proof := &DragRegistrationProof{}
			watcher := &registrationLogWatcher{evidence: make(chan RegistrationEvidence, 1), done: make(chan struct{})}
			firstAt := probeStarted.Add(-time.Duration(count/2) * time.Millisecond)
			for i := 0; i < count; i++ {
				at := firstAt.Add(time.Duration(i) * time.Millisecond)
				line, err := json.Marshal(registrationStreamRecord{
					Timestamp: at.Format("2006-01-02 15:04:05.999999999-0700"), EventMessage: message,
				})
				if err != nil {
					t.Fatal(err)
				}
				observedAt, observedMessage, ok := parseRegistrationStreamRecord(string(line))
				if !ok {
					t.Fatalf("observation %d did not parse as unified-log NDJSON", i)
				}
				event := registrationEvidenceFromStreamEvent(observedMessage, bundle, ListenRegistration, current, observedAt)
				if !event.Stale || !watcher.observeRegistrationEvidence(proof, event) {
					t.Fatalf("identical observation %d/%d was rejected: %+v", i+1, count, event)
				}
			}
			if proof.invalidated.Load() {
				t.Fatalf("%d identical observations invalidated the proof", count)
			}
			got := watcher.evidenceAfterFreshProbe(context.Background(), probeStarted)
			wantLatest := firstAt.Add(time.Duration(count-1) * time.Millisecond)
			if !got.Stale || got.StaleAt.Before(probeStarted) || !got.StaleAt.Equal(wantLatest) ||
				got.StaleCodeHash != old || got.CurrentCodeHash != current {
				t.Fatalf("fresh coalesced evidence = %+v, want latest %v with exact hashes", got, wantLatest)
			}
		})
	}
}

func TestListenRegistrationWatcherStillInvalidatesAmbiguousOrChangedEvidence(t *testing.T) {
	const bundle = "dev.mimac.app.dev"
	old, otherOld, current := strings.Repeat("a", 40), strings.Repeat("c", 40), strings.Repeat("b", 40)
	observedAt := time.Date(2026, 11, 23, 16, 30, 1, 0, time.FixedZone("UTC+8", 8*60*60))
	mismatch := func(staleHash string) string {
		return "Failed to match existing code requirement for subject " + bundle + " and service kTCCServiceListenEvent\n" +
			"\tcdhash H\"" + staleHash + "\"\n\tcdhash H\"" + current + "\""
	}
	first := registrationEvidenceFromStreamEvent(mismatch(old), bundle, ListenRegistration, current, observedAt)

	for _, test := range []struct {
		name         string
		message      string
		seed         bool
		wantStale    bool
		wantMutation bool
		wantWrite    bool
	}{
		{name: "different old hash", message: mismatch(otherOld), seed: true, wantStale: true},
		{name: "same-service mutation", message: "Publishing <TCCDEvent: type=Modify, service=kTCCServiceListenEvent, identifier_type=Bundle ID, identifier=" + bundle + ">", wantMutation: true},
		{name: "current-code write", message: "Update Access Record: kTCCServiceListenEvent for " + bundle + " to Allowed (System Set) (v1)\n\tCodeReq: cdhash H\"" + current + "\"", wantWrite: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof := &DragRegistrationProof{}
			watcher := &registrationLogWatcher{evidence: make(chan RegistrationEvidence, 1), done: make(chan struct{})}
			if test.seed && !watcher.observeRegistrationEvidence(proof, first) {
				t.Fatal("initial exact mismatch was rejected")
			}
			event := registrationEvidenceFromStreamEvent(test.message, bundle, ListenRegistration, current, observedAt.Add(time.Millisecond))
			if event.Stale != test.wantStale || event.Mutated != test.wantMutation || event.CurrentWrite != test.wantWrite {
				t.Fatalf("test record evidence = %+v", event)
			}
			if watcher.observeRegistrationEvidence(proof, event) || !proof.invalidated.Load() {
				t.Fatalf("ambiguous or changed evidence left proof valid: %+v", event)
			}
		})
	}
}

// Exercise process startup and command completion without changing a TCC record.
func TestRegistrationResetWaitsForCommandReceipt(t *testing.T) {
	err := runRegistrationReset(context.Background(), func(ctx context.Context) error {
		return exec.CommandContext(ctx, "/bin/sleep", "0.35").Run()
	})
	if err != nil {
		t.Fatalf("a slow successful command lost its receipt: %v", err)
	}
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = runRegistrationReset(short, func(ctx context.Context) error {
		return exec.CommandContext(ctx, "/bin/sleep", "1").Run()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled system command was not identified as a deadline: %v", err)
	}
}
