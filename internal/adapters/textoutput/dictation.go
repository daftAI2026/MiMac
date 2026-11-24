package textoutput

import (
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
	mu     sync.Mutex
	target *Target
	used   bool
}

var editorProbe sync.Mutex

// BeginDictation records the foreground app at key-down. AX inspection runs
// off the input thread. Never queue a lookup to be made against a later focus.
func BeginDictation() *DictationTarget {
	d := &DictationTarget{}
	pid, err := frontmostProcessID()
	if err == nil && pid > 0 && editorProbe.TryLock() {
		go func() {
			defer editorProbe.Unlock()
			target, _ := Capture()
			d.attach(pid, target)
		}()
	}
	return d
}

func (d *DictationTarget) attach(pid int32, target *Target) {
	if target == nil {
		return
	}
	d.mu.Lock()
	if !d.used && target.snapshot.processID == pid {
		d.target = target
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	_ = target.Close()
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
	err := d.target.Close()
	d.target = nil
	return err
}
