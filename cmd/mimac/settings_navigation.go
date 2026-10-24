package main

import (
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/symbols"
)

func (s *shell) navigationItems(c *ui.Context, a appearance) {
	pages := []settingsPage{pageDevice, pageMappings, pageVoice, pagePlugins, pageGeneral, pageAbout}
	list := ui.Column(c).Key("settings-navigation-list").Gap(spaceXS).FillWidth()
	var selected ui.Rect
	list.Children(func() {
		for _, page := range pages {
			bounds := s.navigationItem(c, a, page)
			if page == s.page {
				selected = bounds
			}
		}
	})
	movingSelectionBackground(c, list, selected, a.theme.SurfacePressed, controlRadius, "navigation-selection")
}

func (s *shell) navigationItem(c *ui.Context, a appearance, page settingsPage) ui.Rect {
	row := ui.ButtonBase(c).Key("navigation/"+string(page)).Label(string(page)).FocusRing(false).Justify(ui.Start).AlignItems(ui.Center).Gap(spaceSM).
		Padding(spaceXS, spaceMD).MinHeight(navigationHeight).FillWidth().Radius(controlRadius)
	if s.page != page && row.Hovered() {
		row.Background(a.theme.SurfaceHover)
	}
	if row.Pressed() {
		row.Background(a.theme.SurfacePressed)
	}
	if row.FocusVisible() {
		row.Border(lineWidth, a.theme.Focus)
	}
	row.Children(func() {
		if icon := s.navigationSymbol(page, a.theme.Dark); icon != nil {
			ui.Image(c, icon).Width(settingsIconSize).Height(settingsIconSize).Fit(ui.Contain).Shrink(0)
		}
		weight := 400
		if s.page == page {
			weight = 600
		}
		ui.Text(c, string(page)).FontSize(a.fonts.body).FontWeight(weight)
	})
	if row.Clicked() {
		s.navigate(page)
		c.Invalidate()
	}
	return row.LayoutBounds()
}

func (s *shell) navigationSymbol(page settingsPage, dark bool) *ui.Bitmap {
	names := map[settingsPage]string{pageDevice: "appletvremote.gen4", pageMappings: "keyboard", pageVoice: "waveform", pagePlugins: "puzzlepiece.extension", pageGeneral: "gearshape", pageAbout: "info.circle"}
	return s.systemSymbol(names[page], dark)
}

func (s *shell) systemSymbol(name string, dark bool) *ui.Bitmap {
	key := name
	if dark {
		key += "-dark"
	}
	if s.navigationSymbols == nil {
		s.navigationSymbols = make(map[string]*ui.Bitmap)
	}
	if bitmap, exists := s.navigationSymbols[key]; exists {
		return bitmap
	}
	var bitmap *ui.Bitmap
	if pixels, err := symbols.Image(name, dark); err == nil {
		bitmap = ui.NewBitmap(pixels)
	}
	s.navigationSymbols[key] = bitmap
	return bitmap
}
