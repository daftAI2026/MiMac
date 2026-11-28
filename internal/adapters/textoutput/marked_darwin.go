//go:build darwin

package textoutput

import (
	"context"
	"errors"
	"math"
	"time"
	"unicode/utf16"
	"unsafe"
)

// WatchMarkedText observes only the composition that begins in the original
// field. The input method owns editing and commit. A slow AX server cannot
// block audio startup. Cancel the context to retire this session's observer.
func WatchMarkedText(ctx context.Context, onDraft func(string)) {
	if ctx == nil || ctx.Err() != nil || onDraft == nil {
		return
	}
	if _, err := loadNativeAPI(); err != nil {
		return
	}
	pid, err := frontmostProcessID()
	if err != nil {
		return
	}
	go watchMarkedField(ctx, pid, onDraft)
}

func watchMarkedField(ctx context.Context, pid int32, onDraft func(string)) {
	api, err := loadNativeAPI()
	if err != nil || ctx.Err() != nil || api.isProcessTrusted() == 0 || secureEventInputEnabled(api) {
		return
	}
	element, owner, err := focusedElement(api, pid)
	if err != nil {
		return
	}
	defer api.cfRelease(element)
	if owner != pid || api.setMessagingTimeout(element, 0.25) != axErrorSuccess || validateEditableElement(api, element) != nil {
		return
	}
	selection, err := selectedRange(api, element)
	if err != nil {
		return
	}
	anchor, ok := readNativeRange(api, selection)
	api.cfRelease(selection)
	if !ok || anchor.location < 0 {
		return
	}
	rangeAttribute, err := api.makeString("AXTextInputMarkedRange")
	if err != nil {
		return
	}
	defer api.cfRelease(rangeAttribute)
	stringAttribute, err := api.makeString("AXStringForRange")
	if err != nil {
		return
	}
	defer api.cfRelease(stringAttribute)
	field := markedTextField{api: api, frontmostPID: frontmostProcessID, element: element, pid: pid, anchor: anchor.location, rangeAttribute: rangeAttribute, stringAttribute: stringAttribute}
	// Ignore a pre-existing composition. Only a subsequent clean press can
	// attribute a new marked range to this tool and this editor.
	initial, err := field.markedRange()
	if err != nil || initial.length > 0 {
		return
	}
	last := ""
	defer func() {
		if last != "" && ctx.Err() == nil {
			onDraft("")
		}
	}()
	timer := time.NewTicker(80 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			text, stable, err := field.draft()
			if err != nil || ctx.Err() != nil {
				return
			}
			if stable && last != text {
				last = text
				onDraft(text)
			}
		}
	}
}

type markedTextField struct {
	frontmostPID                             func() (int32, error)
	api                                      *nativeAPI
	element, rangeAttribute, stringAttribute uintptr
	pid                                      int32
	anchor                                   int64
}

func readNativeRange(api *nativeAPI, value uintptr) (nativeRange, bool) {
	var r nativeRange
	if value == 0 || api.cfGetTypeID(value) != api.axValueGetTypeID() || api.axValueGetType(value) != axValueCFRange {
		return r, false
	}
	ok := api.axValueGetValue(value, axValueCFRange, unsafe.Pointer(&r)) != 0
	return r, ok
}

func (f markedTextField) markedRange() (nativeRange, error) {
	var value uintptr
	code := f.api.copyAttributeValue(f.element, f.rangeAttribute, &value)
	if value != 0 {
		defer f.api.cfRelease(value)
	}
	if code == axErrorNoValue {
		return nativeRange{}, nil
	}
	if code != axErrorSuccess {
		return nativeRange{}, nativeAXError("read marked range", code)
	}
	r, ok := readNativeRange(f.api, value)
	if !ok || r.length < 0 || (r.length > 0 && (r.location < 0 || r.location > math.MaxInt64-r.length)) {
		return nativeRange{}, ErrInvalidTarget
	}
	return r, nil
}

func (f markedTextField) stillFocused() bool {
	front, err := f.frontmostPID()
	if err != nil || front != f.pid || secureEventInputEnabled(f.api) {
		return false
	}
	current, pid, err := focusedElement(f.api, front)
	if err != nil {
		return false
	}
	defer f.api.cfRelease(current)
	return pid == f.pid && f.api.cfEqual(current, f.element) != 0 && f.api.secureState(current) == nil
}

func (f markedTextField) draft() (string, bool, error) {
	if !f.stillFocused() {
		return "", false, ErrTargetChanged
	}
	r, err := f.markedRange()
	if err != nil || r.length == 0 {
		return "", true, err
	}
	if r.location != f.anchor {
		return "", false, ErrTargetChanged
	}
	// Only the bounded tail of the IME-provided range is needed for display.
	// Never read AXValue, the selection's contents, or text around the caret.
	window := r
	const maxUnits = 256
	if window.length > maxUnits {
		window.location += window.length - maxUnits
		window.length = maxUnits
	}
	arg := f.api.axValueCreate(axValueCFRange, unsafe.Pointer(&window))
	if arg == 0 {
		return "", false, ErrInvalidTarget
	}
	defer f.api.cfRelease(arg)
	var value uintptr
	code := f.api.copyParameterizedValue(f.element, f.stringAttribute, arg, &value)
	if value != 0 {
		defer f.api.cfRelease(value)
	}
	after, err := f.markedRange()
	if err != nil || !f.stillFocused() {
		return "", false, errors.Join(ErrTargetChanged, err)
	}
	if after != r {
		return "", false, nil // The IME revised its in-flight draft. Read it next tick.
	}
	if code != axErrorSuccess || value == 0 || f.api.cfGetTypeID(value) != f.api.cfStringGetTypeID() || f.api.cfStringGetLength(value) != window.length {
		return "", false, ErrInvalidTarget
	}
	var units [maxUnits]uint16
	f.api.cfStringGetCharacters(value, nativeRange{length: window.length}, &units[0])
	tail := units[:window.length]
	if window.location != r.location && len(tail) > 0 && tail[0] >= 0xDC00 && tail[0] <= 0xDFFF {
		tail = tail[1:] // The tail begins in the second half of a UTF-16 pair.
	}
	return string(utf16.Decode(tail)), true, nil
}
