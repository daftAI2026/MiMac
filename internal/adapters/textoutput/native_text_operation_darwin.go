//go:build darwin

package textoutput

import (
	"errors"
	"math"
	"unicode/utf16"
	"unsafe"
)

// Some editable elements advertise AXSelectedText as writable but do not
// implement its setter (including WebKit). Discover a parameterized operation
// once at capture, without inspecting text or guessing from the application.
func operationMarker(api *nativeAPI, element uintptr) (uintptr, error) {
	var names uintptr
	code := api.copyParameterizedNames(element, &names)
	if names != 0 {
		defer api.cfRelease(names)
	}
	if code == axErrorAttrUnsupported || code == axErrorNoValue || code == axErrorNotImplemented || code == axErrorParamUnsupported {
		return 0, nil
	}
	if code != axErrorSuccess {
		return 0, nativeAXError("list parameterized attributes", code)
	}
	if names == 0 {
		return 0, nil
	}
	if api.cfGetTypeID(names) != api.cfArrayGetTypeID() {
		return 0, ErrNotEditable
	}
	attribute, err := api.makeString("AXTextOperation")
	if err != nil {
		return 0, err
	}
	defer api.cfRelease(attribute)
	for i, count := int64(0), api.cfArrayGetCount(names); i < count; i++ {
		if api.cfEqual(api.cfArrayGetValueAtIndex(names, i), attribute) != 0 {
			return api.optionalAttribute(element, "AXSelectedTextMarkerRange")
		}
	}
	return 0, nil
}

type nativeRange struct{ location, length int64 }

func insertTextOperation(api *nativeAPI, state snapshot, text string) error {
	attribute, err := api.makeString("AXTextOperation")
	if err != nil {
		return err
	}
	defer api.cfRelease(attribute)
	parameters, err := textOperationParameters(api, state.marker, text)
	if err != nil {
		return err
	}
	defer api.cfRelease(parameters)
	var selected nativeRange
	if api.axValueGetValue(state.selection, axValueCFRange, unsafe.Pointer(&selected)) == 0 || selected.location < 0 || selected.length < 0 {
		return ErrInvalidTarget
	}
	// ReplacePreserveCase retains a non-empty replacement selection. Prepare a
	// guarded caret collapse so the next dictation appends instead of replacing
	// the previous one. Empty selections already leave the caret at the end.
	var replacement, caret, selectionAttribute uintptr
	if selected.length > 0 {
		length := int64(0)
		for _, r := range text {
			length += int64(utf16.RuneLen(r))
		}
		if selected.location > math.MaxInt64-length {
			return ErrInvalidTarget
		}
		replaced := nativeRange{selected.location, length}
		collapsed := nativeRange{selected.location + length, 0}
		replacement = api.axValueCreate(axValueCFRange, unsafe.Pointer(&replaced))
		if replacement == 0 {
			return ErrInvalidTarget
		}
		defer api.cfRelease(replacement)
		caret = api.axValueCreate(axValueCFRange, unsafe.Pointer(&collapsed))
		if caret == 0 {
			return ErrInvalidTarget
		}
		defer api.cfRelease(caret)
		selectionAttribute, err = api.makeString("AXSelectedTextRange")
		if err != nil {
			return err
		}
		defer api.cfRelease(selectionAttribute)
	}
	if err := confirmSnapshot(api, state); err != nil {
		return err
	}
	var result uintptr
	code := api.copyParameterizedValue(state.element, attribute, parameters, &result)
	if result != 0 {
		defer api.cfRelease(result)
	}
	if code != axErrorSuccess {
		return nativeAXError("replace selected text", code)
	}
	// An empty result means that no range was handled. Never read returned
	// strings and never retry through another path after this request.
	if result == 0 || api.cfGetTypeID(result) != api.cfArrayGetTypeID() || api.cfArrayGetCount(result) != 1 {
		return errors.New("textoutput: text operation did not acknowledge one replacement")
	}
	if replacement != 0 {
		expected := snapshot{processID: state.processID, element: state.element, selection: replacement}
		if err := confirmSnapshot(api, expected); err != nil {
			return err
		}
		if code := api.setAttributeValue(state.element, selectionAttribute, caret); code != axErrorSuccess {
			return nativeAXError("collapse replacement selection", code)
		}
	}
	return nil
}

func textOperationParameters(api *nativeAPI, marker uintptr, text string) (uintptr, error) {
	parameters := api.cfDictionaryCreateMutable(0, 4, api.dictionaryKeyCallbacks, api.dictionaryValueCallbacks)
	if parameters == 0 {
		return 0, ErrInvalidTarget
	}
	keep := false
	defer func() {
		if !keep {
			api.cfRelease(parameters)
		}
	}()
	markers := api.cfArrayCreateMutable(0, 1, api.arrayCallbacks)
	if markers == 0 {
		return 0, ErrInvalidTarget
	}
	defer api.cfRelease(markers)
	api.cfArrayAppendValue(markers, marker)
	value, err := api.makeUTF8String(text)
	if err != nil {
		return 0, err
	}
	defer api.cfRelease(value)
	operation, err := api.makeString("TextOperationReplacePreserveCase")
	if err != nil {
		return 0, err
	}
	defer api.cfRelease(operation)
	for _, entry := range []struct {
		name  string
		value uintptr
	}{
		{"AXTextOperationType", operation},
		{"AXTextOperationMarkerRanges", markers},
		{"AXTextOperationReplacementString", value},
		{"AXTextOperationSmartReplace", api.cfBooleanFalse},
	} {
		key, err := api.makeString(entry.name)
		if err != nil {
			return 0, err
		}
		api.cfDictionarySetValue(parameters, key, entry.value)
		api.cfRelease(key)
	}
	keep = true
	return parameters, nil
}
