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
	watcher := startRegistrationChangeWatcher(proof, currentHash)
	if watcher == nil {
		return nil, nil
	}
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

	probe, err := runRegistrationProbe(ctx, runningBundle, service)
	if err != nil || probe.status != "denied" {
		DiscardDragRegistrationProof(proof)
		return nil, nil
	}
	evidence := watcher.evidenceAfterFreshProbe(ctx, probe.startedAt)
	if evidence.Mutated || evidence.CurrentWrite || !evidence.Stale ||
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
	// Keep -W: it waits for the fresh probe process to exit and close its
	// stdout receipt before this read. A nonzero `open` result alone is
	// inconclusive; the unique receipt must still contain one complete status.
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

// startRegistrationChangeWatcher records this service's live TCC events before
// the fresh probe and keeps observing until the proof is consumed or expires.
func startRegistrationChangeWatcher(proof *DragRegistrationProof, currentHash string) *registrationLogWatcher {
	predicate, ok := registrationStreamPredicate(proof.bundleID, proof.service)
	if !ok {
		proof.invalidated.Store(true)
		return nil
	}
	watchCtx, cancel := context.WithTimeout(context.Background(), dragRegistrationProofTTL)
	cmd := exec.CommandContext(watchCtx, "/usr/bin/log", "stream", "--level", "info", "--style", "ndjson", "--predicate", predicate)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		proof.invalidated.Store(true)
		return nil
	}
	if err := cmd.Start(); err != nil {
		cancel()
		proof.invalidated.Store(true)
		return nil
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
	return watcher
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
