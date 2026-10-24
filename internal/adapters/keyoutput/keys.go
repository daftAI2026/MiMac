// Package keyoutput translates Core key effects into a small, explicit set of
// macOS virtual-key strokes. It never interprets text or guesses a layout.
package keyoutput

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"mimac.local/mimac/internal/bridge"
)

var (
	ErrInvalidShortcut = errors.New("keyoutput: invalid shortcut")
	ErrInvalidPlan     = errors.New("keyoutput: invalid key effect batch")
)

// Stroke is one planned keyboard transition. Flags is the modifier state
// after the transition, matching the held-key ledger used by Sink.
type Stroke struct {
	Key   bridge.Key
	Code  uint16
	Down  bool
	Flags uint64
}

type keySpec struct {
	code uint16
	flag uint64
}

const (
	flagShift   uint64 = 1 << 17
	flagControl uint64 = 1 << 18
	flagOption  uint64 = 1 << 19
	flagCommand uint64 = 1 << 20
)

var letterCodes = map[byte]uint16{
	'A': 0x00, 'B': 0x0B, 'C': 0x08, 'D': 0x02, 'E': 0x0E,
	'F': 0x03, 'G': 0x05, 'H': 0x04, 'I': 0x22, 'J': 0x26,
	'K': 0x28, 'L': 0x25, 'M': 0x2E, 'N': 0x2D, 'O': 0x1F,
	'P': 0x23, 'Q': 0x0C, 'R': 0x0F, 'S': 0x01, 'T': 0x11,
	'U': 0x20, 'V': 0x09, 'W': 0x0D, 'X': 0x07, 'Y': 0x10,
	'Z': 0x06,
}

// digitCodes are ANSI-row physical key codes, not layout-independent text.
var digitCodes = map[byte]uint16{
	'0': 0x1D, '1': 0x12, '2': 0x13, '3': 0x14, '4': 0x15,
	'5': 0x17, '6': 0x16, '7': 0x1A, '8': 0x1C, '9': 0x19,
}

var functionCodes = map[int]uint16{
	1: 0x7A, 2: 0x78, 3: 0x63, 4: 0x76, 5: 0x60,
	6: 0x61, 7: 0x62, 8: 0x64, 9: 0x65, 10: 0x6D,
	11: 0x67, 12: 0x6F, 13: 0x69, 14: 0x6B, 15: 0x71,
	16: 0x6A, 17: 0x40, 18: 0x4F, 19: 0x50, 20: 0x5A,
}

func specFor(key bridge.Key) (keySpec, error) {
	switch key {
	case "Control":
		return keySpec{code: 0x3B, flag: flagControl}, nil // kVK_Control
	case "Shift":
		return keySpec{code: 0x38, flag: flagShift}, nil // kVK_Shift
	case "Option":
		return keySpec{code: 0x3A, flag: flagOption}, nil // kVK_Option
	case "Command":
		return keySpec{code: 0x37, flag: flagCommand}, nil // kVK_Command
	case "RightOption":
		return keySpec{code: 0x3D, flag: flagOption | 0x40}, nil // kVK_RightOption, NX_DEVICERALTKEYMASK
	case "RightCommand":
		return keySpec{code: 0x36, flag: flagCommand | 0x10}, nil // kVK_RightCommand, NX_DEVICERCMDKEYMASK
	case "Return":
		return keySpec{code: 0x24}, nil // kVK_Return
	case "Escape":
		return keySpec{code: 0x35}, nil // kVK_Escape
	case "Space":
		return keySpec{code: 0x31}, nil // kVK_Space
	case "Tab":
		return keySpec{code: 0x30}, nil // kVK_Tab
	case "Backspace":
		return keySpec{code: 0x33}, nil // kVK_Delete
	case "Delete":
		return keySpec{code: 0x75}, nil // kVK_ForwardDelete
	case "Home":
		return keySpec{code: 0x73}, nil // kVK_Home
	case "PageUp":
		return keySpec{code: 0x74}, nil // kVK_PageUp
	case "End":
		return keySpec{code: 0x77}, nil // kVK_End
	case "PageDown":
		return keySpec{code: 0x79}, nil // kVK_PageDown
	case "Left":
		return keySpec{code: 0x7B}, nil // kVK_LeftArrow
	case "Right":
		return keySpec{code: 0x7C}, nil // kVK_RightArrow
	case "Down":
		return keySpec{code: 0x7D}, nil // kVK_DownArrow
	case "Up":
		return keySpec{code: 0x7E}, nil // kVK_UpArrow
	}
	name := string(key)
	if len(name) == 1 {
		if code, ok := letterCodes[name[0]]; ok {
			return keySpec{code: code}, nil
		}
		if code, ok := digitCodes[name[0]]; ok {
			return keySpec{code: code}, nil
		}
	}
	if len(name) >= 2 && name[0] == 'F' {
		if n, err := strconv.Atoi(name[1:]); err == nil {
			if code, ok := functionCodes[n]; ok && strconv.Itoa(n) == name[1:] {
				return keySpec{code: code}, nil
			}
		}
	}
	return keySpec{}, fmt.Errorf("unsupported key %q", key)
}

// ParseShortcut accepts canonical key names separated by '+'. Modifiers must
// precede non-modifiers; it accepts one non-modifier, or modifier-only holds.
// Letters map to ANSI physical-key codes, not layout-independent text input.
func ParseShortcut(text string) ([]bridge.Key, error) {
	if text == "" || strings.TrimSpace(text) != text {
		return nil, fmt.Errorf("%w: empty or padded shortcut", ErrInvalidShortcut)
	}
	parts := strings.Split(text, "+")
	keys := make([]bridge.Key, 0, len(parts))
	seen := make(map[bridge.Key]struct{}, len(parts))
	mainSeen := false
	for i, part := range parts {
		if part == "" || strings.TrimSpace(part) != part {
			return nil, fmt.Errorf("%w: empty or padded key", ErrInvalidShortcut)
		}
		key := bridge.Key(part)
		spec, err := specFor(key)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidShortcut, err)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w: repeated key %q", ErrInvalidShortcut, key)
		}
		seen[key] = struct{}{}
		if spec.flag == 0 {
			if mainSeen || i != len(parts)-1 {
				return nil, fmt.Errorf("%w: modifiers must precede one main key", ErrInvalidShortcut)
			}
			mainSeen = true
		} else if mainSeen {
			return nil, fmt.Errorf("%w: modifier follows main key", ErrInvalidShortcut)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// FormatShortcut validates a shortcut and returns its canonical '+'-joined
// spelling, preserving its declared key order.
func FormatShortcut(keys []bridge.Key) (string, error) {
	if len(keys) == 0 {
		return "", fmt.Errorf("%w: no keys", ErrInvalidShortcut)
	}
	seen := make(map[bridge.Key]struct{}, len(keys))
	mainSeen := false
	parts := make([]string, 0, len(keys))
	for i, key := range keys {
		spec, err := specFor(key)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidShortcut, err)
		}
		if _, exists := seen[key]; exists {
			return "", fmt.Errorf("%w: repeated key %q", ErrInvalidShortcut, key)
		}
		seen[key] = struct{}{}
		if spec.flag == 0 {
			if mainSeen || i != len(keys)-1 {
				return "", fmt.Errorf("%w: modifiers must precede one main key", ErrInvalidShortcut)
			}
			mainSeen = true
		} else if mainSeen {
			return "", fmt.Errorf("%w: modifier follows main key", ErrInvalidShortcut)
		}
		parts = append(parts, string(key))
	}
	return strings.Join(parts, "+"), nil
}

// PlanKeys validates the complete effect batch before a native sink allocates
// or posts events. On error it returns no partial plan and leaves held intact.
func PlanKeys(held []bridge.Key, effects []bridge.Effect) (strokes []Stroke, nextHeld []bridge.Key, err error) {
	nextHeld = append([]bridge.Key(nil), held...)
	for i, key := range nextHeld {
		if _, err := specFor(key); err != nil {
			return nil, nil, fmt.Errorf("%w: invalid held key: %v", ErrInvalidPlan, err)
		}
		if containsKey(nextHeld[:i], key) {
			return nil, nil, fmt.Errorf("%w: duplicate held key %q", ErrInvalidPlan, key)
		}
	}

	strokes = make([]Stroke, 0, len(effects))
	down := func(key bridge.Key) error {
		spec, err := specFor(key)
		if err != nil {
			return err
		}
		if containsKey(nextHeld, key) {
			return fmt.Errorf("key %q is already held", key)
		}
		nextHeld = append(nextHeld, key)
		strokes = append(strokes, Stroke{Key: key, Code: spec.code, Down: true, Flags: flagsFor(nextHeld)})
		return nil
	}
	up := func(key bridge.Key) error {
		spec, err := specFor(key)
		if err != nil {
			return err
		}
		index := indexOfKey(nextHeld, key)
		if index < 0 {
			return fmt.Errorf("key %q is not held", key)
		}
		nextHeld = append(nextHeld[:index], nextHeld[index+1:]...)
		strokes = append(strokes, Stroke{Key: key, Code: spec.code, Down: false, Flags: flagsFor(nextHeld)})
		return nil
	}

	for i, effect := range effects {
		var effectErr error
		switch e := effect.(type) {
		case bridge.KeyDownEffect:
			effectErr = down(e.Key)
		case *bridge.KeyDownEffect:
			if e == nil {
				effectErr = errors.New("nil KeyDownEffect")
			} else {
				effectErr = down(e.Key)
			}
		case bridge.KeyUpEffect:
			effectErr = up(e.Key)
		case *bridge.KeyUpEffect:
			if e == nil {
				effectErr = errors.New("nil KeyUpEffect")
			} else {
				effectErr = up(e.Key)
			}
		case bridge.TargetTapEffect:
			effectErr = tapKeys(e.Keys(), len(nextHeld), down, up)
		case *bridge.TargetTapEffect:
			if e == nil {
				effectErr = errors.New("nil TargetTapEffect")
			} else {
				effectErr = tapKeys(e.Keys(), len(nextHeld), down, up)
			}
		default:
			effectErr = fmt.Errorf("unsupported effect type %T", effect)
		}
		if effectErr != nil {
			return nil, nil, fmt.Errorf("effect %d: %w", i, errors.Join(ErrInvalidPlan, effectErr))
		}
	}
	return strokes, nextHeld, nil
}

func tapKeys(keys []bridge.Key, heldCount int, down, up func(bridge.Key) error) error {
	if len(keys) == 0 {
		return errors.New("target tap has no keys")
	}
	if heldCount != 0 {
		return errors.New("target tap conflicts with held keys")
	}
	seen := make(map[bridge.Key]struct{}, len(keys))
	for _, key := range keys {
		if _, err := specFor(key); err != nil {
			return err
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("target tap repeats key %q", key)
		}
		seen[key] = struct{}{}
	}
	for _, key := range keys {
		if err := down(key); err != nil {
			return err
		}
	}
	for i := len(keys) - 1; i >= 0; i-- {
		if err := up(keys[i]); err != nil {
			return err
		}
	}
	return nil
}

func flagsFor(held []bridge.Key) uint64 {
	var flags uint64
	for _, key := range held {
		spec, _ := specFor(key)
		flags |= spec.flag
	}
	return flags
}

func containsKey(keys []bridge.Key, key bridge.Key) bool { return indexOfKey(keys, key) >= 0 }

func indexOfKey(keys []bridge.Key, key bridge.Key) int {
	for i, candidate := range keys {
		if candidate == key {
			return i
		}
	}
	return -1
}
