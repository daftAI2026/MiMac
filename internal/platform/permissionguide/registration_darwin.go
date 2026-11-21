//go:build darwin

package permissionguide

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var signedRegistrationHash = regexp.MustCompile(`(?m)^CDHash=([0-9a-f]{40})$`)

const dragRegistrationProofTTL = 30 * time.Second

const registrationStreamEvidenceGrace = 100 * time.Millisecond

func ownRegistrationBundle(service RegistrationService) (string, bool) {
	if !validDragRegistrationService(service) {
		return "", false
	}
	bundleID, ok := MainBundleIdentifier()
	return bundleID, ok && (bundleID == "dev.mimac.app" || bundleID == "dev.mimac.app.dev")
}

// PrepareDragRegistration runs a fresh, read-only preflight worker and captures
// the exact older/current code-hash mismatch for one service. It never writes
// TCC state. No proof is returned unless the probe is freshly denied and the
// current packaged app's signing identity matches the log evidence.
func PrepareDragRegistration(ctx context.Context, bundlePath string, service RegistrationService) (*DragRegistrationProof, error) {
	bundleID, ok := ownRegistrationBundle(service)
	if !ok {
		return nil, nil
	}
	runningBundle, err := registrationAppBundle()
	if err != nil || filepath.Clean(bundlePath) != runningBundle {
		return nil, nil
	}
	currentHash, err := registrationBundleHash(ctx, runningBundle)
	if err != nil {
		return nil, nil
	}
	proof := &DragRegistrationProof{
		bundlePath:      runningBundle,
		bundleID:        bundleID,
		service:         service,
		currentCodeHash: currentHash,
		preparedAt:      time.Now(),
	}
	watcher, started := startRegistrationChangeWatcher(proof, currentHash)
	if !started {
		return nil, nil
	}
	if service == AccessibilityRegistration {
		select {
		case err := <-watcher.ready:
			if err != nil {
				DiscardDragRegistrationProof(proof)
				return nil, nil
			}
		case <-ctx.Done():
			DiscardDragRegistrationProof(proof)
			return nil, ctx.Err()
		}
	}

	probe, err := runRegistrationProbe(ctx, runningBundle, service)
	if err != nil || probe.status != "denied" {
		DiscardDragRegistrationProof(proof)
		return nil, nil
	}
	var evidence RegistrationEvidence
	if service == AccessibilityRegistration {
		evidence = watcher.evidenceAfterFreshProbe(ctx, probe.startedAt)
	} else {
		evidence, err = readRegistrationEvidence(ctx, bundleID, service, currentHash, probe.startedAt)
	}
	if err != nil || evidence.Mutated || evidence.CurrentWrite || !evidence.Stale ||
		!strings.EqualFold(evidence.CurrentCodeHash, currentHash) || evidence.StaleCodeHash == "" ||
		strings.EqualFold(evidence.StaleCodeHash, evidence.CurrentCodeHash) || proof.invalidated.Load() {
		DiscardDragRegistrationProof(proof)
		return nil, nil
	}
	proof.staleCodeHash = evidence.StaleCodeHash
	return proof, nil
}

// RepairDragRegistrationBeforeDelivery consumes one proof and resets the
// matching service only while the running process still reads it as denied.
// Preparation and change observation happen in the background; this method
// performs no Launch Services invocation, codesign, or log query.
func RepairDragRegistrationBeforeDelivery(ctx context.Context, proof *DragRegistrationProof, access RegistrationAccess) (bool, error) {
	if proof == nil {
		return false, nil
	}
	runningBundle, err := registrationAppBundle()
	if err != nil {
		proof.consume(time.Now(), "", proof.service, access)
		return false, nil
	}
	if !proof.consume(time.Now(), runningBundle, proof.service, access) {
		return false, nil
	}
	bundleID, ok := ownRegistrationBundle(proof.service)
	if !ok || bundleID != proof.bundleID {
		return false, nil
	}
	if filepath.Clean(proof.bundlePath) != runningBundle {
		return false, nil
	}
	if err := resetOwnRegistration(ctx, proof.service); err != nil {
		return false, err
	}
	return true, nil
}

// DiscardDragRegistrationProof stops its bounded watcher when a prepared drag
// is replaced or the permission guide closes without consuming it.
func DiscardDragRegistrationProof(proof *DragRegistrationProof) {
	if proof == nil {
		return
	}
	proof.invalidated.Store(true)
	if proof.watcherCancel != nil {
		proof.watcherCancel()
	}
}

func resetOwnRegistration(ctx context.Context, service RegistrationService) error {
	bundleID, ok := ownRegistrationBundle(service)
	if !ok {
		return errors.New("unsupported permission registration identity")
	}
	return exec.CommandContext(ctx, "/usr/bin/tccutil", "reset", string(service), bundleID).Run()
}

type registrationProbe struct {
	status    string
	startedAt time.Time
}

func runRegistrationProbe(ctx context.Context, bundle string, service RegistrationService) (registrationProbe, error) {
	if !validDragRegistrationService(service) {
		return registrationProbe{}, errors.New("unsupported permission registration service")
	}
	scratch, err := os.MkdirTemp("", "mimac-permission-probe-")
	if err != nil {
		return registrationProbe{}, err
	}
	defer os.RemoveAll(scratch)
	receipt := filepath.Join(scratch, "result")
	startedAt := time.Now().Truncate(time.Millisecond).Add(-time.Millisecond)
	args := []string{"-n", "-g", "-W", "--stdout", receipt, bundle, "--args", RegistrationProbeFlag, string(service)}
	if err := exec.CommandContext(ctx, "/usr/bin/open", args...).Run(); err != nil && ctx.Err() != nil {
		return registrationProbe{}, ctx.Err()
	}
	if ctx.Err() != nil {
		return registrationProbe{}, ctx.Err()
	}
	data, err := os.ReadFile(receipt)
	if err != nil {
		return registrationProbe{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 1 || (fields[0] != "denied" && fields[0] != "granted" && fields[0] != "unknown") {
		return registrationProbe{}, errors.New("fresh permission probe returned an invalid result")
	}
	return registrationProbe{status: fields[0], startedAt: startedAt}, nil
}

func registrationBundleHash(ctx context.Context, bundle string) (string, error) {
	signed, err := exec.CommandContext(ctx, "/usr/bin/codesign", "-d", "--verbose=4", bundle).CombinedOutput()
	if err != nil {
		return "", err
	}
	hash := signedRegistrationHash.FindSubmatch(signed)
	if len(hash) != 2 {
		return "", errors.New("permission registration app has no code directory hash")
	}
	return strings.ToLower(string(hash[1])), nil
}

func readRegistrationEvidence(ctx context.Context, bundleID string, service RegistrationService, currentHash string, since time.Time) (RegistrationEvidence, error) {
	if since.IsZero() || time.Since(since) < 0 || time.Since(since) > 5*time.Second {
		return RegistrationEvidence{}, errors.New("permission probe evidence expired")
	}
	predicate := `process == "tccd" AND eventMessage CONTAINS "` + bundleID + `" AND eventMessage CONTAINS "kTCCService` + string(service) + `"`
	recent, err := exec.CommandContext(ctx, "/usr/bin/log", "show", "--info", "--start", since.Local().Format("2006-01-02 15:04:05"), "--style", "compact", "--predicate", predicate).Output()
	if err != nil {
		return RegistrationEvidence{}, err
	}
	return registrationEvidenceBetween(string(recent), bundleID, service, currentHash, since, time.Now()), nil
}

type registrationLogWatcher struct {
	ready     chan error
	evidence  chan RegistrationEvidence
	done      chan struct{}
	readyOnce sync.Once
}

func (w *registrationLogWatcher) signalReady(err error) {
	w.readyOnce.Do(func() { w.ready <- err })
}

// evidenceAfterFreshProbe collects only matching app/service records produced
// during this fresh probe. The short grace lets unified logging deliver the
// denial message without waiting for a historical `log show` query.
func (w *registrationLogWatcher) evidenceAfterFreshProbe(ctx context.Context, since time.Time) RegistrationEvidence {
	timer := time.NewTimer(registrationStreamEvidenceGrace)
	defer timer.Stop()
	for {
		select {
		case evidence := <-w.evidence:
			if evidence.StaleAt.After(since) {
				return evidence
			}
		case <-w.done:
			return RegistrationEvidence{}
		case <-timer.C:
			return RegistrationEvidence{}
		case <-ctx.Done():
			return RegistrationEvidence{}
		}
	}
}

// startRegistrationChangeWatcher keeps the existing compact observer for
// ListenEvent. Accessibility uses one NDJSON stream for both fresh mismatch
// evidence and continued mutation invalidation.
func startRegistrationChangeWatcher(proof *DragRegistrationProof, currentHash string) (*registrationLogWatcher, bool) {
	if proof.service == AccessibilityRegistration {
		return startAccessibilityRegistrationChangeWatcher(proof, currentHash)
	}
	if !startCompactRegistrationChangeWatcher(proof) {
		return nil, false
	}
	return nil, true
}

func startAccessibilityRegistrationChangeWatcher(proof *DragRegistrationProof, currentHash string) (*registrationLogWatcher, bool) {
	watchCtx, cancel := context.WithTimeout(context.Background(), dragRegistrationProofTTL)
	predicate := `process == "tccd" AND composedMessage CONTAINS "` + proof.bundleID + `" AND composedMessage CONTAINS "kTCCService` + string(proof.service) + `"`
	cmd := exec.CommandContext(watchCtx, "/usr/bin/log", "stream", "--level", "info", "--style", "ndjson", "--predicate", predicate)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		proof.invalidated.Store(true)
		return nil, false
	}
	if err := cmd.Start(); err != nil {
		cancel()
		proof.invalidated.Store(true)
		return nil, false
	}
	watcher := &registrationLogWatcher{
		ready: make(chan error, 1), evidence: make(chan RegistrationEvidence, 32), done: make(chan struct{}),
	}
	proof.watcherCancel = cancel
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
	scan:
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(strings.TrimSpace(line), "Filtering") {
				watcher.signalReady(nil)
				continue
			}
			at, message, ok := parseRegistrationStreamRecord(line)
			if !ok {
				continue
			}
			event := registrationEvidenceFromStreamEvent(message, proof.bundleID, proof.service, currentHash, at)
			if event.Mutated || event.CurrentWrite {
				proof.invalidated.Store(true)
				cancel()
				break
			}
			if event.Stale {
				select {
				case watcher.evidence <- event:
				default:
					// An incomplete stream cannot prove the registration is still stale.
					proof.invalidated.Store(true)
					cancel()
					break scan
				}
			}
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			cancel()
		}
		waitErr := cmd.Wait()
		if scanErr != nil {
			watcher.signalReady(scanErr)
		} else if watchCtx.Err() == nil && waitErr != nil {
			watcher.signalReady(waitErr)
		} else {
			watcher.signalReady(errors.New("permission log stream ended before readiness"))
		}
		invalidateRegistrationProofIfPending(proof)
		close(watcher.done)
	}()
	return watcher, true
}

func startCompactRegistrationChangeWatcher(proof *DragRegistrationProof) bool {
	watchCtx, cancel := context.WithTimeout(context.Background(), dragRegistrationProofTTL)
	predicate := `process == "tccd" AND eventMessage CONTAINS "TCCDEvent" AND eventMessage CONTAINS "service=kTCCService` + string(proof.service) + `" AND eventMessage CONTAINS "identifier=` + proof.bundleID + `"`
	cmd := exec.CommandContext(watchCtx, "/usr/bin/log", "stream", "--level", "info", "--style", "compact", "--predicate", predicate)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		proof.invalidated.Store(true)
		return false
	}
	if err := cmd.Start(); err != nil {
		cancel()
		proof.invalidated.Store(true)
		return false
	}
	proof.watcherCancel = cancel
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if registrationChangeEvent(scanner.Text(), proof.bundleID, proof.service) {
				proof.invalidated.Store(true)
				cancel()
				break
			}
		}
		_ = cmd.Wait()
		if watchCtx.Err() == nil && !proof.consumed.Load() {
			proof.invalidated.Store(true)
		}
	}()
	return true
}

func registrationChangeEvent(line, bundleID string, service RegistrationService) bool {
	change := ", service=kTCCService" + string(service) + ", identifier_type=Bundle ID, identifier=" + bundleID + ">"
	return strings.Contains(line, "TCCDEvent: type=") && strings.Contains(line, change) &&
		(strings.Contains(line, "TCCDEvent: type=Insert,") || strings.Contains(line, "TCCDEvent: type=Modify,") || strings.Contains(line, "TCCDEvent: type=Delete,"))
}

func CanProbeOwnRegistration(service RegistrationService) bool {
	_, ok := ownRegistrationBundle(service)
	return ok
}

func registrationAppBundle() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	macOSDir := filepath.Dir(executable)
	contentsDir := filepath.Dir(macOSDir)
	bundle := filepath.Dir(contentsDir)
	if filepath.Base(macOSDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" || filepath.Ext(bundle) != ".app" {
		return "", errors.New("permission registration requires a packaged app")
	}
	return filepath.Clean(bundle), nil
}
