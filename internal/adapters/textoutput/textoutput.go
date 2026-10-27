// Package textoutput inserts finalized recognition text into the editable
// accessibility element that was focused when Capture was called.
package textoutput

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

const MaxTextBytes = 64 * 1024

var (
	ErrUnsupported       = errors.New("textoutput: unsupported platform")
	ErrUnsupportedTarget = errors.New("textoutput: target lacks a supported text replacement operation")
	ErrPermissionDenied  = errors.New("textoutput: accessibility permission is unavailable")
	ErrNoFrontmostApp    = errors.New("textoutput: no frontmost application")
	ErrNoFocusedElement  = errors.New("textoutput: no focused accessibility element")
	ErrNotEditable       = errors.New("textoutput: focused element is not editable")
	ErrSecureInput       = errors.New("textoutput: secure input is active")
	ErrTargetChanged     = errors.New("textoutput: captured target or selection changed")
	ErrInvalidTarget     = errors.New("textoutput: invalid accessibility target")
	ErrInvalidUTF8       = errors.New("textoutput: text is not valid UTF-8")
	ErrNUL               = errors.New("textoutput: text contains NUL")
	ErrTooLarge          = errors.New("textoutput: text exceeds 64 KiB")
	ErrClosed            = errors.New("textoutput: target is closed")
	ErrAlreadySubmitted  = errors.New("textoutput: target has already been submitted")
)

// snapshot holds only target identity and selection metadata. Native
// implementations must not read the field's text or interact with a clipboard.
type snapshot struct {
	processID int32
	element   uintptr
	selection uintptr
	marker    uintptr // optional opaque range for parameterized text operations
}

// native keeps operating-system calls isolated from the one-shot lifecycle.
// insert must revalidate process, focused element, and selection immediately
// before making exactly one text replacement request. A native adapter may
// collapse the replacement selection afterward, but must never retry the text.
type native interface {
	capture() (snapshot, error)
	insert(snapshot, string) error
	close(snapshot) error
}

// Target freezes one foreground process, focused AX element, and selected
// range. It permits at most one non-empty insertion attempt.
type Target struct {
	mu        sync.Mutex
	native    native
	snapshot  snapshot
	closed    bool
	submitted bool
}

// Capture records the current frontmost process, focused editable AX element,
// and selection without reading the element's text. The caller should invoke
// this when the input action begins and retain the returned Target until the
// final transcript is ready.
func Capture() (*Target, error) {
	return captureWith(defaultNative())
}

// HasAccess reports whether macOS currently trusts this process for
// Accessibility access. It never prompts the user.
func HasAccess() bool { return hasAccessibilityAccess() }

// RequestAccess asks macOS to present its Accessibility permission prompt.
// Call it only after an explicit user action. The prompt is asynchronous, so
// the returned value reports current access and may remain false until the
// user grants permission; call HasAccess again afterward.
func RequestAccess() bool { return requestAccessibilityAccess() }

func captureWith(n native) (*Target, error) {
	if n == nil {
		return nil, ErrUnsupported
	}
	captured, err := n.capture()
	if err != nil {
		return nil, err
	}
	if captured.processID <= 0 || captured.element == 0 || captured.selection == 0 {
		_ = n.close(captured)
		return nil, ErrInvalidTarget
	}
	return &Target{native: n, snapshot: captured}, nil
}

// Insert sends one final, non-empty UTF-8 transcript to the captured element.
// Empty recognition results are accepted as no-ops and do not consume the
// one-shot insertion. A successful return means the native AX request was
// accepted; it does not prove that the target application visibly committed it.
// An error may also occur after text was accepted (for example, if focus changed
// before collapsing the replacement selection). Never retry the transcript.
func (t *Target) Insert(text string) error {
	if t == nil {
		return ErrClosed
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrClosed
	}
	if t.submitted {
		return ErrAlreadySubmitted
	}
	if err := validateText(text); err != nil {
		return err
	}
	if text == "" {
		return nil
	}

	// Consume the attempt even when validation or the native request fails. A
	// delayed retry must never write into a later field or repeat partial output.
	t.submitted = true
	if err := t.native.insert(t.snapshot, text); err != nil {
		return fmt.Errorf("textoutput: insert final text: %w", err)
	}
	return nil
}

func validateText(text string) error {
	if !utf8.ValidString(text) {
		return ErrInvalidUTF8
	}
	if strings.IndexByte(text, 0) >= 0 {
		return ErrNUL
	}
	if len(text) > MaxTextBytes {
		return ErrTooLarge
	}
	return nil
}

// Close releases the retained AX element and selection value. It is safe to
// call repeatedly and never submits text.
func (t *Target) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	err := t.native.close(t.snapshot)
	t.snapshot = snapshot{}
	return err
}
