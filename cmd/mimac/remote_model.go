package main

import (
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/devices"
)

// These are generic control symbols. A device plugin supplies their names and
// geometry; this renderer contains no device IDs, HID usages or model layout.
var controlSymbols = map[string]*ui.SVG{}

func controlSymbol(name string) *ui.SVG {
	if icon, ok := controlSymbols[name]; ok {
		return icon
	}
	paths := map[string]string{
		"power":    `<path d="M12 3v8M7 5.8a8 8 0 1 0 10 0"/>`,
		"voice":    `<rect x="9" y="3" width="6" height="12" rx="3"/><path d="M6 10v3a6 6 0 0 0 12 0v-3M12 19v3M9 22h6"/>`,
		"arrow-up": `<path d="m7 14 5-5 5 5"/>`, "arrow-down": `<path d="m7 10 5 5 5-5"/>`,
		"arrow-left": `<path d="m14 7-5 5 5 5"/>`, "arrow-right": `<path d="m10 7 5 5-5 5"/>`,
		"confirm": `<circle cx="12" cy="12" r="3"/>`, "back": `<path d="m14 6-6 6 6 6"/>`,
		"home":      `<path d="m3 11 9-8 9 8v10H3zm6 10v-7h6v7"/>`,
		"menu":      `<path d="M4 6h16M4 12h16M4 18h16"/>`,
		"volume-up": `<path d="M5 12h14M12 5v14"/>`, "volume-down": `<path d="M5 12h14"/>`,
		"tv": `<rect x="3" y="5" width="18" height="13" rx="2"/><path d="M8 21h8M12 18v3"/>`,
	}
	path, ok := paths[name]
	if !ok {
		return nil
	}
	icon := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round">` + path + `</svg>`))
	controlSymbols[name] = icon
	return icon
}

func (s *shell) remoteModel(c *ui.Context, a appearance, profile devices.Profile) {
	model := profile.Model
	if model.Width <= 0 || model.Height <= 0 {
		return
	}
	scale := min(modelMaxWidth/model.Width, modelMaxHeight/model.Height)
	var artwork *ui.Bitmap
	if provider := s.activeDevicePlugin().Artwork; provider != nil {
		if s.modelBitmaps == nil {
			s.modelBitmaps = make(map[string]*ui.Bitmap)
		}
		var cached bool
		artwork, cached = s.modelBitmaps[profile.ID]
		if !cached {
			pixels, err := provider()
			if err == nil {
				artwork = ui.NewBitmap(pixels)
			} else {
				s.mappingError = "遥控器外观无法加载"
			}
			s.modelBitmaps[profile.ID] = artwork
		}
	}
	pressed := make(map[string]bool)
	if s.coordinator != nil {
		for _, press := range s.coordinator.core.Snapshot().PressedButtons {
			pressed[press.PhysicalButtonID()] = true
		}
	}
	body := ui.Box(c).Key(profile.ID + "/remote-model").Role(ui.RoleGroup).Label("遥控器模型").Width(model.Width * scale).Height(model.Height * scale).Shrink(0)
	if artwork == nil {
		body.Radius(model.CornerRadius*scale).Background(a.theme.Surface).Border(lineWidth, a.theme.Border)
	}
	ringPulse := selectionOutlinePulse(body, selectionPulseRun(profile.ID, s.selectedButton))
	reduceMotion := c.Preferences().ReduceMotion
	body.Children(func() {
		if artwork != nil {
			ui.Image(c, artwork).Grayscale().Absolute().Left(0).Top(0).Width(model.Width * scale).Height(model.Height * scale).Fit(ui.Contain)
		}
		for _, surface := range model.Surfaces {
			r := surface.Bounds
			ui.Box(c).Absolute().Left(r.Min.X * scale).Top(r.Min.Y * scale).Width((r.Max.X - r.Min.X) * scale).Height((r.Max.Y - r.Min.Y) * scale).
				Radius(surface.CornerRadius * scale).Background(a.theme.Text)
		}
		for _, button := range profile.Buttons {
			r := button.Bounds
			item := ui.ButtonBase(c).Key(profile.ID + "/model/" + button.ID).Label(button.Label).Absolute().Left(r.Min.X * scale).Top(r.Min.Y * scale).
				Width((r.Max.X - r.Min.X) * scale).Height((r.Max.Y - r.Min.Y) * scale).Padding(0).Radius(button.CornerRadius * scale).FocusRing(false)
			selectedButton := s.selectedButton == button.ID
			sweepRun := selectionPulseRun(profile.ID, button.ID)
			sweep := selectionSweepLocal(item, selectedButton, sweepRun, s.window, reduceMotion)
			if artwork != nil {
				item.Background(ui.Transparent).Border(1, ui.Transparent)
			} else {
				item.Background(a.theme.Text).Border(lineWidth, a.theme.Border)
			}
			selected := float32(0)
			if selectedButton {
				selected = 1
			}
			color := a.theme.Background
			if pressed[button.ID] || item.Pressed() {
				if artwork != nil {
					item.BorderColor(modelHighlight.Alpha(0.85))
				} else {
					item.Background(a.theme.Accent)
					color = a.theme.AccentText
				}
			} else if item.Hovered() && s.selectedButton != button.ID {
				if artwork != nil {
					item.BorderColor(modelHighlight.Alpha(0.55))
				} else {
					item.Background(a.theme.TextMuted)
				}
			}
			if item.FocusVisible() {
				if artwork != nil {
					item.BorderColor(modelHighlight)
				} else {
					item.BorderColor(a.theme.Focus)
				}
			}
			if artwork == nil || selected == 1 {
				item.Children(func() {
					if selected == 1 {
						addSelectionOutline(c, button.CornerRadius*scale, ringPulse)
						addSelectionSweep(c, button.CornerRadius*scale, sweep, s.window, reduceMotion)
					}
					if artwork == nil {
						if icon := controlSymbol(button.Symbol); icon != nil {
							ui.Icon(c, icon).FontSize(a.fonts.caption).TextColor(color)
						} else {
							ui.Text(c, button.Symbol).FontSize(a.fonts.caption).TextColor(color)
						}
					}
				})
			}
			if item.Clicked() {
				s.selectedButton, s.revealButton = button.ID, button.ID
				s.endShortcutRecording()
				c.Invalidate()
			}
		}
	})
}
