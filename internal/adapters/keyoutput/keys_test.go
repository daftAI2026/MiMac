package keyoutput

import (
	"errors"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/bridge"
)

func TestParseAndFormatShortcut(t *testing.T) {
	for _, text := range []string{
		"Up", "Down", "Left", "Right", "Return", "Escape", "Space",
		"Tab", "Backspace", "Delete", "Home", "End", "PageUp", "PageDown",
		"0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
		"A", "Z", "Command+C", "Command+V", "Command+1", "Control+Shift+Space", "F1", "F12", "F20",
	} {
		t.Run(text, func(t *testing.T) {
			keys, err := ParseShortcut(text)
			if err != nil {
				t.Fatal(err)
			}
			formatted, err := FormatShortcut(keys)
			if err != nil {
				t.Fatal(err)
			}
			if formatted != text {
				t.Fatalf("FormatShortcut() = %q, want %q", formatted, text)
			}
		})
	}
}

func TestParseShortcutRejectsUnsupportedAndMalformedKeys(t *testing.T) {
	for _, text := range []string{"", "Fn", "VolumeUp", "A+Command", "Control++C", "Command+Command+C", "Control+Unknown", "control+C", "F01"} {
		if _, err := ParseShortcut(text); !errors.Is(err, ErrInvalidShortcut) {
			t.Errorf("ParseShortcut(%q) error = %v, want ErrInvalidShortcut", text, err)
		}
	}
}

func TestPlanKeysMapsCommonVirtualKeys(t *testing.T) {
	want := map[bridge.Key]uint16{
		"Up": 0x7E, "Down": 0x7D, "Left": 0x7B, "Right": 0x7C,
		"Return": 0x24, "Escape": 0x35, "Space": 0x31,
		"Tab": 0x30, "Backspace": 0x33, "Delete": 0x75,
		"Home": 0x73, "PageUp": 0x74, "End": 0x77, "PageDown": 0x79,
		"0": 0x1D, "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15,
		"5": 0x17, "6": 0x16, "7": 0x1A, "8": 0x1C, "9": 0x19,
		"A": 0x00, "C": 0x08, "V": 0x09, "Z": 0x06,
		"Control": 0x3B, "Shift": 0x38, "Option": 0x3A, "Command": 0x37,
		"F1": 0x7A, "F2": 0x78, "F3": 0x63, "F4": 0x76, "F5": 0x60,
		"F6": 0x61, "F7": 0x62, "F8": 0x64, "F9": 0x65, "F10": 0x6D,
		"F11": 0x67, "F12": 0x6F, "F13": 0x69, "F14": 0x6B, "F15": 0x71,
		"F16": 0x6A, "F17": 0x40, "F18": 0x4F, "F19": 0x50, "F20": 0x5A,
	}
	for key, code := range want {
		strokes, held, err := PlanKeys(nil, []bridge.Effect{bridge.KeyDownEffect{Key: key}})
		if err != nil {
			t.Errorf("PlanKeys(%q): %v", key, err)
			continue
		}
		if len(strokes) != 1 || strokes[0].Code != code || !strokes[0].Down || !reflect.DeepEqual(held, []bridge.Key{key}) {
			t.Errorf("PlanKeys(%q) = strokes %#v held %v, want code %#x down", key, strokes, held, code)
		}
	}
}

func TestPlanKeysRejectsWholeBatchWithoutPartialOutput(t *testing.T) {
	tests := []struct {
		name    string
		effects []bridge.Effect
	}{
		{
			name: "unknown second key",
			effects: []bridge.Effect{
				bridge.KeyDownEffect{Key: "C"},
				bridge.KeyUpEffect{Key: "Fn"},
			},
		},
		{
			name: "non-key effect",
			effects: []bridge.Effect{
				bridge.KeyDownEffect{Key: "C"},
				bridge.BlockedEffect{},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			held := []bridge.Key{"Command"}
			strokes, next, err := PlanKeys(held, test.effects)
			if !errors.Is(err, ErrInvalidPlan) || strokes != nil || next != nil {
				t.Fatalf("PlanKeys() = %#v, %v, %v; want no partial result and ErrInvalidPlan", strokes, next, err)
			}
			if !reflect.DeepEqual(held, []bridge.Key{"Command"}) {
				t.Fatalf("PlanKeys mutated input held: %v", held)
			}
		})
	}
}

func TestPlanKeysSetsAllHeldModifierFlags(t *testing.T) {
	strokes, _, err := PlanKeys(nil, []bridge.Effect{
		bridge.KeyDownEffect{Key: "Control"},
		bridge.KeyDownEffect{Key: "Shift"},
		bridge.KeyDownEffect{Key: "Space"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{flagControl, flagControl | flagShift, flagControl | flagShift}
	if len(strokes) != len(want) {
		t.Fatalf("strokes = %#v", strokes)
	}
	for i := range want {
		if strokes[i].Flags != want[i] {
			t.Errorf("stroke %d flags = %#x, want %#x", i, strokes[i].Flags, want[i])
		}
	}
}

func TestRightModifiersKeepSideAndReleaseAggregateFlags(t *testing.T) {
	for _, tc := range []struct {
		key       bridge.Key
		left      bridge.Key
		code      uint16
		flags     uint64
		aggregate uint64
	}{
		{"RightOption", "Option", 61, 0x80040, 0x80000},
		{"RightCommand", "Command", 54, 0x100010, 0x100000},
	} {
		t.Run(string(tc.key), func(t *testing.T) {
			keys, err := ParseShortcut(string(tc.key))
			if err != nil {
				t.Fatal(err)
			}
			if value, err := FormatShortcut(keys); err != nil || value != string(tc.key) {
				t.Fatalf("roundtrip = %q, %v", value, err)
			}
			strokes, held, err := PlanKeys(nil, []bridge.Effect{
				bridge.KeyDownEffect{Key: tc.left}, bridge.KeyDownEffect{Key: tc.key},
				bridge.KeyUpEffect{Key: tc.key}, bridge.KeyUpEffect{Key: tc.left},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(held) != 0 || len(strokes) != 4 || strokes[1].Code != tc.code || strokes[2].Code != tc.code {
				t.Fatalf("wrong transitions: %+v, %v", strokes, held)
			}
			wantFlags := []uint64{tc.aggregate, tc.flags, tc.aggregate, 0}
			for i, want := range wantFlags {
				if strokes[i].Flags != want {
					t.Fatalf("step %d flags=%#x want=%#x", i, strokes[i].Flags, want)
				}
			}
		})
	}
}
