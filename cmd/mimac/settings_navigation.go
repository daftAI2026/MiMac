package main

import (
	"image"
	"image/draw"

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
	row := ui.ButtonBase(c).Key("navigation/"+string(page)).Label(string(page)).FocusRing(false).Justify(ui.Start).AlignItems(ui.Center).Gap(sidebarLabelGap).
		Padding(spaceXS, spaceMD, spaceXS, sidebarItemInset).MinHeight(navigationHeight).FillWidth().Radius(controlRadius)
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
			ui.Box(c).Width(settingsIconSize).Height(settingsIconSize).Shrink(0).Justify(ui.Center).AlignItems(ui.Start).Children(func() {
				ui.Image(c, icon).Width(sidebarIconSize).Height(sidebarIconSize).Fit(ui.Contain).Shrink(0)
			})
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
	names := map[settingsPage]string{pageDevice: "rectangle.connected.to.line.below", pageMappings: "arrow.triangle.branch", pageVoice: "mic", pagePlugins: "puzzlepiece.extension", pageGeneral: "gearshape", pageAbout: "info.circle"}
	return s.symbolBitmap(symbolKey{name: names[page], dark: dark, size: sidebarIconSize, leading: true})
}

type symbolKey struct {
	name    string
	size    float32
	dark    bool
	leading bool
}

func (s *shell) systemSymbol(name string, dark bool, size float32) *ui.Bitmap {
	return s.symbolBitmap(symbolKey{name: name, dark: dark, size: size})
}

func (s *shell) symbolBitmap(key symbolKey) *ui.Bitmap {
	if s.navigationSymbols == nil {
		s.navigationSymbols = make(map[symbolKey]*ui.Bitmap)
	}
	if bitmap, exists := s.navigationSymbols[key]; exists {
		return bitmap
	}
	var bitmap *ui.Bitmap
	if pixels, err := symbols.Image(key.name, key.dark, float64(key.size)); err == nil {
		if key.leading {
			pixels = leadingSymbol(pixels)
		}
		bitmap = ui.NewBitmap(pixels)
	}
	s.navigationSymbols[key] = bitmap
	return bitmap
}

// Keep the native glyph's size and vertical position, but align its visible
// left edge inside the shared sidebar column. This runs only when cached.
func leadingSymbol(src image.Image) image.Image {
	bounds := src.Bounds()
	left := bounds.Max.X
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < left; x++ {
			if _, _, _, alpha := src.At(x, y).RGBA(); alpha != 0 {
				left = x
				break
			}
		}
	}
	if left == bounds.Min.X || left == bounds.Max.X {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), src, image.Pt(left, bounds.Min.Y), draw.Src)
	return dst
}
