package main

import (
	"image"
	"image/draw"

	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/symbols"
)

func (s *shell) navigationItems(c *ui.Context, a appearance) {
	list := ui.Column(c).Key("settings-navigation-list").Gap(spaceXS).FillWidth()
	var selected ui.Rect
	list.Children(func() {
		for _, entry := range sidebarEntries {
			bounds := s.navigationItem(c, a, entry.page)
			if entry.page == s.page {
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
		ui.Box(c).Width(settingsIconSize).Height(settingsIconSize).Shrink(0).Justify(ui.Center).AlignItems(ui.Start).Children(func() {
			if icon := s.navigationSymbol(page, a.theme.Dark); icon != nil {
				ui.Image(c, icon).Width(sidebarIconSize).Height(sidebarIconSize).Fit(ui.Contain).Shrink(0)
			}
		})
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

var sidebarEntries = []struct {
	page   settingsPage
	symbol string
}{
	{pageDevice, "externaldrive"},
	{pageMappings, "point.topleft.down.curvedto.point.bottomright.up"},
	{pageVoice, "mic"},
	{pagePlugins, "puzzlepiece.extension"},
	{pageGeneral, "gearshape"},
	{pageAbout, "info.circle"},
}

func (s *shell) navigationSymbol(page settingsPage, dark bool) *ui.Bitmap {
	var name string
	for _, entry := range sidebarEntries {
		if entry.page == page {
			name = entry.symbol
		}
	}
	key := symbolKey{name: name, dark: dark, size: sidebarIconSize, sidebar: true}
	if bitmap, exists := s.navigationSymbols[key]; exists {
		return bitmap
	}
	if s.navigationSymbols == nil {
		s.navigationSymbols = make(map[symbolKey]*ui.Bitmap)
	}
	names := make([]string, len(sidebarEntries))
	for i, entry := range sidebarEntries {
		names[i] = entry.symbol
	}
	// A single native batch gives the set one scale and one stroke weight.
	pixels, err := symbols.Images(names, dark, float64(sidebarIconSize), float64(sidebarSymbolWeight))
	var aligned []image.Image
	if err == nil {
		aligned = centeredSidebarSymbols(pixels)
	}
	for i, entry := range sidebarEntries {
		entryKey := symbolKey{name: entry.symbol, dark: dark, size: sidebarIconSize, sidebar: true}
		var bitmap *ui.Bitmap
		if i < len(aligned) {
			bitmap = ui.NewBitmap(aligned[i])
		}
		s.navigationSymbols[entryKey] = bitmap
	}
	return s.navigationSymbols[key]
}

type symbolKey struct {
	name    string
	size    float32
	dark    bool
	sidebar bool
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
		bitmap = ui.NewBitmap(pixels)
	}
	s.navigationSymbols[key] = bitmap
	return bitmap
}

// Anchor the widest painted icon at the column's left edge and center every
// other glyph on it. Whole-pixel translations preserve the native raster.
// This runs only once for each cached appearance, never on a draw frame.
func centeredSidebarSymbols(sources []image.Image) []image.Image {
	bounds := make([]image.Rectangle, len(sources))
	maxWidth := 0
	for i, src := range sources {
		r := src.Bounds()
		ink := image.Rectangle{Min: r.Max, Max: r.Min}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if _, _, _, a := src.At(x, y).RGBA(); a != 0 {
					ink.Min.X = min(ink.Min.X, x)
					ink.Min.Y = min(ink.Min.Y, y)
					ink.Max.X = max(ink.Max.X, x+1)
					ink.Max.Y = max(ink.Max.Y, y+1)
				}
			}
		}
		if ink.Empty() {
			ink = image.Rectangle{}
		}
		bounds[i] = ink
		maxWidth = max(maxWidth, ink.Dx())
	}
	out := make([]image.Image, len(sources))
	for i, src := range sources {
		r := src.Bounds()
		dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		if !bounds[i].Empty() {
			left := (maxWidth - bounds[i].Dx()) / 2
			draw.Draw(dst, dst.Bounds(), src, image.Pt(bounds[i].Min.X-left, r.Min.Y), draw.Src)
		}
		out[i] = dst
	}
	return out
}
