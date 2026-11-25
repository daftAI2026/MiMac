package textoutput

import (
	"context"
	"errors"
	"sync"
)

// ErrNoInsertionTarget means no insertion was attempted. Recognition and
// the choice of how to present the final text are independent of AX support.
var ErrNoInsertionTarget = errors.New("textoutput: no insertion target for this dictation")

// The editor confirmed replacement before a separate caret adjustment failed.
var errReplacementAccepted = errors.New("textoutput: replacement already accepted")

// DictationTarget holds a best-effort snapshot for automatic text delivery.
// Slow or unavailable editors must not delay receiving the first audio frame.
type DictationTarget struct {
	mu            sync.Mutex
	target        *Target
	used          bool
	captureCancel context.CancelFunc
}

// DictationSelectionObserver exposes only read-only editor recovery methods.
type DictationSelectionObserver interface {
	CaptureCurrentSelection() (*Target, error)
	Close() error
}

var editorProbe sync.Mutex

// BeginDictation records the foreground app at key-down. AX inspection runs
// off the input thread. Never queue a lookup to be made against a later focus.
func BeginDictation() *DictationTarget {
	pid, err := frontmostProcessID()
	if err != nil || pid <= 0 {
		return &DictationTarget{}
	}
	return beginDictationForProcess(defaultNative(), pid, true)
}

// ObserveDictationSelection records the current editor location for recovery
// without asking the native adapter to enable renderer accessibility.
func ObserveDictationSelection() DictationSelectionObserver {
	pid, err := frontmostProcessID()
	if err != nil || pid <= 0 {
		return &DictationTarget{}
	}
	return observeDictationSelectionForProcess(defaultNative(), pid)
}

func observeDictationSelectionForProcess(n native, pid int32) DictationSelectionObserver {
	return beginDictationForProcess(n, pid, false)
}

func beginDictationForProcess(n native, pid int32, prepareEditor bool) *DictationTarget {
	d := &DictationTarget{}
	if pid <= 0 || n == nil || !editorProbe.TryLock() {
		return d
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.captureCancel = cancel
	d.mu.Unlock()
	go func() {
		defer editorProbe.Unlock()
		target, _ := captureRequestWithContext(ctx, n, captureRequest{pid: pid, prepareEditor: prepareEditor})
		d.attach(pid, target)
	}()
	return d
}

func (d *DictationTarget) attach(pid int32, target *Target) {
	d.mu.Lock()
	cancel := d.captureCancel
	d.captureCancel = nil
	if target != nil && !d.used && target.snapshot.processID == pid {
		d.target = target
		d.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if target != nil {
		_ = target.Close()
	}
}

// CaptureCurrentSelection remembers where an interrupted input method left its
// composition. The original field must still be focused, but the input method
// may have moved its selection since key-down.
func (d *DictationTarget) CaptureCurrentSelection() (*Target, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.used || d.target == nil {
		return nil, ErrNoInsertionTarget
	}
	t := d.target
	current, err := captureWith(t.native)
	if err != nil {
		return nil, err
	}
	if !t.native.sameField(t.snapshot, current.snapshot) {
		_ = current.Close()
		return nil, ErrTargetChanged
	}
	return current, nil
}

func (d *DictationTarget) Insert(text string) error {
	if text == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.used {
		return ErrAlreadySubmitted
	}
	d.used = true
	if d.target == nil {
		if d.captureCancel != nil {
			d.captureCancel()
			d.captureCancel = nil
		}
		if err := validateText(text); err != nil {
			return err
		}
		return ErrNoInsertionTarget
	}
	// Target rechecks the app, field and selection immediately before writing.
	err := d.target.Insert(text)
	if errors.Is(err, errReplacementAccepted) {
		return nil
	}
	if errors.Is(err, ErrTargetChanged) {
		return ErrNoInsertionTarget
	}
	return err
}

func (d *DictationTarget) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.used = true
	if d.captureCancel != nil {
		d.captureCancel()
		d.captureCancel = nil
	}
	err := d.target.Close()
	d.target = nil
	return err
}
