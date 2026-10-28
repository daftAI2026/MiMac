//go:build darwin

package textoutput

import (
	"errors"
	"runtime"
	"slices"
	"unicode"
	"unicode/utf16"
)

func requestPostEventAccess() bool {
	api, err := loadNativeAPI()
	return err == nil && (api.preflightPostEvent() || api.requestPostEvent())
}

func insertUnicodeText(api *nativeAPI, state snapshot, text string) error {
	events, err := prepareUnicodeEvents(api, text)
	if err != nil {
		return err
	}
	defer func() {
		for _, event := range events {
			api.cfRelease(event)
		}
	}()
	if !api.preflightPostEvent() {
		return ErrPostEventPermissionDenied
	}
	if err := confirmSnapshot(api, state); err != nil {
		return err
	}
	// Exactly one key-down carries the entire transcript. Splitting into
	// characters lets an input handler move the remaining text to another field.
	// The matching key-up carries no text. No Return or submit action is sent.
	api.eventPostToPID(state.processID, events[0])
	api.eventPostToPID(state.processID, events[1])
	return nil
}

// Prepare the complete pair before checking focus, so allocation failure or
// native payload truncation cannot leave partial text or a held key behind.
func prepareUnicodeEvents(api *nativeAPI, text string) ([2]uintptr, error) {
	var events [2]uintptr
	if err := validateText(text); err != nil {
		return events, err
	}
	if text == "" {
		return events, ErrInvalidTarget
	}
	for _, r := range text {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return events, ErrUnsupportedText
		}
	}
	if !api.preflightPostEvent() {
		return events, ErrPostEventPermissionDenied
	}
	source := api.eventSourceCreate(-1) // private state, not physical modifiers
	if source == 0 {
		return events, errors.New("textoutput: create event source failed")
	}
	defer api.cfRelease(source)
	keep := false
	defer func() {
		if !keep {
			for _, event := range events {
				if event != 0 {
					api.cfRelease(event)
				}
			}
		}
	}()
	for i := range events {
		events[i] = api.eventCreate(source, 49, i == 0) // Space as in Chromium's injector
		if events[i] == 0 {
			return [2]uintptr{}, errors.New("textoutput: create text event failed")
		}
		api.eventSetFlags(events[i], 0)
	}
	units := utf16.Encode([]rune(text))
	api.eventSetUnicode(events[0], uint64(len(units)), &units[0])
	runtime.KeepAlive(units)
	api.eventSetUnicode(events[1], 0, nil)
	// Apple publishes no maximum payload size. Verify the retained contents,
	// rather than assuming either a historical 20-unit limit or no truncation.
	copied := make([]uint16, len(units))
	var count uint64
	api.eventGetUnicode(events[0], uint64(len(copied)), &count, &copied[0])
	runtime.KeepAlive(copied)
	if count != uint64(len(units)) || !slices.Equal(copied, units) {
		return [2]uintptr{}, ErrEventPayload
	}
	keep = true
	return events, nil
}
