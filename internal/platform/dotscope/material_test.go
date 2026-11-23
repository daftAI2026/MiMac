package dotscope

import (
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestRendererDrawsAndUpdatesSyntheticDots(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	snapshot := Snapshot{
		Pose:    Pose{Width: 112, Height: PlateHeight, Opacity: 1, Scale: 1, Grid: 1, Live: 1},
		Columns: 1,
		Rows:    1,
		Pitch:   2.6,
		Dots:    [][2]float32{{0, 0}},
	}
	view := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Box(c).Fill().Draw(func(p *ui.Painter, bounds ui.Rect) {
			renderer.Draw(p, bounds, snapshot, true)
		})
	}, 144, 64)

	center := func() color.RGBA { return view.Image().RGBAAt(72, 32) }
	before := center()
	if before.A < 200 {
		t.Fatalf("idle plate center alpha = %d, want a visible plate", before.A)
	}
	if corner := view.Image().RGBAAt(2, 2); corner.A != 0 {
		t.Fatalf("outside plate alpha = %d, want transparent", corner.A)
	}

	snapshot.Dots[0] = [2]float32{1, 1}
	view.Frame()
	after := center()
	if after.R <= before.R+30 || after.G <= before.G+30 || after.B <= before.B+30 {
		t.Fatalf("lit dot pixel = %+v, idle pixel = %+v; expected a bright warm core", after, before)
	}
	if after.A < before.A {
		t.Fatalf("lit dot reduced alpha: idle %+v, lit %+v", before, after)
	}
}

func TestRendererUsesAppearancePalette(t *testing.T) {
	dark := materialPalette(true, 0, 0)
	light := materialPalette(false, 0, 0)
	if dark.plate == light.plate || dark.base == light.base {
		t.Fatalf("appearance palettes are not distinct: dark=%+v light=%+v", dark, light)
	}
	processing := materialPalette(true, 1, 0)
	failure := materialPalette(true, 0, 1)
	if processing.base == dark.base || failure.base == dark.base {
		t.Fatalf("state palette did not respond: idle=%+v processing=%+v failure=%+v", dark.base, processing.base, failure.base)
	}
}
