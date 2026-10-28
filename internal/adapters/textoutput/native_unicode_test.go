//go:build darwin

package textoutput

import (
	"errors"
	"slices"
	"testing"
	"unicode/utf16"
	"unsafe"
)

func TestUnicodeEventPreparationRejectsIncompleteOutput(t *testing.T) {
	for _, tc := range []struct {
		name, text, failure string
		want                error
	}{
		{"complete", "Aa中é🙂👨‍👩‍👧‍👦", "", nil},
		{"denied", "text", "permission", ErrPostEventPermissionDenied},
		{"source_allocation", "text", "source", nil},
		{"down_allocation", "text", "down", nil},
		{"up_allocation", "text", "up", nil},
		{"truncated", "text", "truncate", ErrEventPayload},
		{"corrupt", "text", "corrupt", ErrEventPayload},
		{"newline", "a\nb", "", ErrUnsupportedText},
		{"paragraph", "a\u2029b", "", ErrUnsupportedText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created, released := []uintptr{}, []uintptr{}
			payloads := map[uintptr][]uint16{}
			flags := map[uintptr]uint64{}
			api := &nativeAPI{
				preflightPostEvent: func() bool { return tc.failure != "permission" },
				eventSourceCreate: func(state int32) uintptr {
					if state != -1 {
						t.Fatal("inherited physical modifier state")
					}
					if tc.failure == "source" {
						return 0
					}
					created = append(created, 1)
					return 1
				},
				eventCreate: func(source uintptr, code uint16, down bool) uintptr {
					if source != 1 || code != 49 {
						t.Fatal("unexpected key source or submit key")
					}
					if down && tc.failure == "down" || !down && tc.failure == "up" {
						return 0
					}
					event := uintptr(3)
					if down {
						event = 2
					}
					created = append(created, event)
					return event
				},
				eventSetFlags: func(event uintptr, value uint64) { flags[event] = value },
				eventSetUnicode: func(event uintptr, count uint64, p *uint16) {
					payloads[event] = slices.Clone(unsafe.Slice(p, int(count)))
				},
				eventGetUnicode: func(event uintptr, capacity uint64, count *uint64, p *uint16) {
					text := payloads[event]
					*count = uint64(len(text))
					copy(unsafe.Slice(p, int(capacity)), text)
					if tc.failure == "truncate" {
						*count--
					}
					if tc.failure == "corrupt" {
						*p = 'X'
					}
				},
				cfRelease: func(p uintptr) { released = append(released, p) },
			}
			events, err := prepareUnicodeEvents(api, tc.text)
			expectError := tc.failure != "" || tc.want != nil
			if (err != nil) != expectError || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v failure=%s", err, tc.want, tc.failure)
			}
			if err == nil {
				if events != [2]uintptr{2, 3} || !slices.Equal(payloads[2], utf16.Encode([]rune(tc.text))) || len(payloads[3]) != 0 || len(flags) != 2 || flags[2] != 0 || flags[3] != 0 {
					t.Fatal("not one complete unmodified text-down plus empty up")
				}
				for _, event := range events {
					api.cfRelease(event)
				}
			} else if events != [2]uintptr{} {
				t.Fatal("failed preparation exposed events")
			}
			slices.Sort(created)
			slices.Sort(released)
			if !slices.Equal(created, released) {
				t.Fatalf("created=%v released=%v", created, released)
			}
		})
	}
}
