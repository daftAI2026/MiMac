package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/adapters/keyoutput"
)

// AppKit normally consumes menu key equivalents before a focused view sees
// them. Keep the native menus, but lend their accelerators to this recorder
// while it is open. Restore the exact originals on cancel, save or close.
func (s *shell) syncRecorderAccelerators() {
	if s.editButton == "" {
		for item, accelerator := range s.recorderAccelerators {
			item.SetAccelerator(accelerator)
		}
		s.recorderAccelerators = nil
		return
	}
	if s.recorderAccelerators != nil || mygo.App.Menu() == nil {
		return
	}
	s.recorderAccelerators = make(map[*mygo.MenuItem]string)
	var pause func([]*mygo.MenuItem)
	pause = func(items []*mygo.MenuItem) {
		for _, item := range items {
			if item.Accelerator != "" {
				s.recorderAccelerators[item] = item.Accelerator
				item.SetAccelerator("")
			}
			pause(item.Submenu)
		}
	}
	pause(mygo.App.Menu().Items())
}

func shortcutLabel(value string) string {
	if value == "" {
		return "不设置"
	}
	labels := map[string]string{"Control": "⌃", "Option": "⌥", "Shift": "⇧", "Command": "⌘",
		"RightOption": "右⌥", "rightoption": "右⌥", "RightCommand": "右⌘", "Return": "↩", "Escape": "⎋",
		"Space": "空格", "Up": "↑", "Down": "↓", "Left": "←", "Right": "→"}
	parts := strings.Split(value, "+")
	for i, part := range parts {
		if label, ok := labels[part]; ok {
			parts[i] = label
		}
	}
	return strings.Join(parts, "")
}

// Recording belongs to the focused MyGo control. It never installs a global
// keyboard listener, and physical keys match the sink's ANSI-key semantics.
func recordedShortcut(event ui.InputEvent) (string, bool) {
	if event.Kind != ui.InputKeyDown || event.Repeat {
		return "", false
	}
	key := event.PhysicalKey
	if key == ui.KeyUnknown {
		key = event.Key
	}
	name := map[ui.Key]string{ui.KeyEnter: "Return", ui.KeyEscape: "Escape", ui.KeySpace: "Space", ui.KeyUp: "Up", ui.KeyDown: "Down", ui.KeyLeft: "Left", ui.KeyRight: "Right", ui.KeyTab: "Tab", ui.KeyBackspace: "Backspace", ui.KeyDelete: "Delete", ui.KeyHome: "Home", ui.KeyEnd: "End", ui.KeyPageUp: "PageUp", ui.KeyPageDown: "PageDown"}[key]
	if key >= ui.KeyA && key <= ui.KeyZ {
		name = string(rune('A' + key - ui.KeyA))
	}
	if key >= ui.KeyF1 && key <= ui.KeyF12 {
		name = fmt.Sprintf("F%d", key-ui.KeyF1+1)
	}
	if key >= ui.Key0 && key <= ui.Key9 {
		name = string(rune('0' + key - ui.Key0))
	}
	if name == "" {
		return "", false
	}
	parts := make([]string, 0, 5)
	for _, modifier := range []struct {
		mask ui.Modifiers
		name string
	}{{ui.Ctrl, "Control"}, {ui.Alt, "Option"}, {ui.Shift, "Shift"}, {ui.Super, "Command"}} {
		if event.Mods&modifier.mask != 0 {
			parts = append(parts, modifier.name)
		}
	}
	value := strings.Join(append(parts, name), "+")
	_, err := keyoutput.ParseShortcut(value)
	return value, err == nil
}

func (s *shell) shortcutRecorder(c *ui.Context, a appearance) {
	field := ui.Box(c).Bind(&s.recorderFocus).Focusable().FocusRing(false).Role(ui.RoleButton).Label("录入快捷键").
		Width(mappingChoiceWidth).Padding(controlInset, spaceSM).MinHeight(controlHeight).Justify(ui.Center).AlignItems(ui.Center).
		Radius(controlRadius).Border(lineWidth, a.theme.Focus).Background(a.theme.Background)
	field.HandleInput(func(event ui.InputEvent) bool {
		if event.Kind != ui.InputKeyDown {
			return event.Kind == ui.InputKeyUp || event.Kind == ui.InputText
		}
		if event.Repeat {
			return true
		}
		key := event.PhysicalKey
		if key == ui.KeyUnknown {
			key = event.Key
		}
		if key == ui.KeyEscape && event.Mods == 0 {
			s.mappingError = ""
			s.endShortcutRecording()
		} else if value, ok := recordedShortcut(event); ok {
			// AppKit can consume Command key-up before a focused view receives it.
			// A complete chord is available on key-down; commit that single event.
			s.saveMapping(s.editButton, value)
		} else if key != ui.KeyUnknown {
			s.mappingError = "暂不支持此按键"
		}
		return true
	}).Children(func() { ui.Text(c, "按下快捷键").SingleLine().FontSize(a.fonts.body) })
}
