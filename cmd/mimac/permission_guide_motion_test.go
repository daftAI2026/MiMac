package main

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/permissionguide"
)

func TestPermissionGuideFlightStaysInsideItsTransparentWindow(t *testing.T) {
	for _, panel := range []mygo.Rectangle{{X: 100, Y: 60, Width: 500, Height: 442}, {X: 1050, Y: 400, Width: 500, Height: 442}, {X: -1300, Y: -250, Width: 500, Height: 367}} {
		settings := permissionguide.Rect{X: float64(panel.X) - 650, Y: float64(panel.Y) + 40, Width: 600, Height: 700}
		area := mygo.Rectangle{X: panel.X - 1000, Y: panel.Y - 60, Width: 2000, Height: 1000}
		icon := ui.Rect{X: 202, Y: 36, W: 96, H: 96}
		g := permissionGuideFlightBounds(icon, panel, settings, area)
		start, end := g.point(0), g.point(1)
		if start != g.start || end != g.end {
			t.Fatalf("endpoints = %v / %v", start, end)
		}
		if got := end.X + float32(g.bounds.X); got != permissionGuideFlightDestination(settings).X {
			t.Fatalf("target x %v", got)
		}
		if got := end.Y + float32(g.bounds.Y); got != permissionGuideFlightDestination(settings).Y {
			t.Fatalf("target y %v", got)
		}
		for i := 0; i <= 100; i++ {
			p := g.point(float32(i) / 100)
			if p.X < 48 || p.Y < 48 || p.X > float32(g.bounds.Width)-48 || p.Y > float32(g.bounds.Height)-48 {
				t.Fatalf("clipped flight at %d: %v in %v", i, p, g.bounds)
			}
		}
		if g.control.Y > min(g.start.Y, g.end.Y) {
			t.Fatalf("curve bends down: %v", g)
		}
	}
}

func TestPermissionGuideFollowFinishesAtTheActualTarget(t *testing.T) {
	from := mygo.Rectangle{X: -700, Y: 300, Width: 500, Height: 442}
	to := mygo.Rectangle{X: 1016, Y: 96, Width: 500, Height: 442}
	start := permissionGuideFollowPosition(from, to, 0)
	end := permissionGuideFollowPosition(from, to, 1)
	if start != from || end != to {
		t.Fatalf("movement endpoints: %v -> %v", start, end)
	}
	previous := start.X
	for i := 1; i < 100; i++ {
		p := permissionGuideFollowPosition(from, to, float32(i)/100)
		if p.X < previous || p.X > to.X || p.Y < to.Y || p.Y > from.Y {
			t.Fatalf("overshot at %d: %v", i, p)
		}
		previous = p.X
	}
}

func TestPermissionGuideFlightSpringIsFiniteAndHasExactEndpoints(t *testing.T) {
	if permissionGuideFlightSpring(0) != 0 || permissionGuideFlightSpring(1) != 1 {
		t.Fatal("spring endpoint mismatch")
	}
	for i := 0; i <= 100; i++ {
		v := float64(permissionGuideFlightSpring(float32(i) / 100))
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatal("nonfinite spring")
		}
	}
}

func TestPermissionGuideIllustrationLeavesDesktopTransparentAndStopsScheduling(t *testing.T) {
	pixels := image.NewRGBA(image.Rect(0, 0, 80, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 80; x++ {
			pixels.SetRGBA(x, y, color.RGBA{A: 255})
		}
	}
	s := &shell{settingsState: settingsState{appIcon: ui.NewBitmap(pixels)}}
	view := &permissionGuideIllustration{active: true, run: 1, geometry: permissionGuideFlightGeometry{bounds: mygo.Rectangle{Width: 400, Height: 200}, start: ui.Point{X: 330, Y: 130}, control: ui.Point{X: 220, Y: 70}, end: ui.Point{X: 70, Y: 130}}}
	renderer := ui.NewTester(func(c *ui.Context) { s.drawPermissionGuideIllustration(c, view) }, 400, 200)
	renderer.Advance(400 * time.Millisecond)
	if got := renderer.Image().RGBAAt(0, 0).A; got != 0 {
		t.Fatalf("overlay paints desktop background alpha=%d", got)
	}
	visible := false
	for _, a := range renderer.Image().Pix {
		if a != 0 {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatal("active illustration rendered no icon")
	}
	view.active = false
	renderer.Frame()
	if renderer.FramePending() {
		t.Fatal("hidden illustration still schedules animation frames")
	}
	for _, a := range renderer.Image().Pix {
		if a != 0 {
			t.Fatal("hidden illustration left pixels behind")
		}
	}
}

func TestPermissionGuideDragPreviewContainsOnlyIconPixels(t *testing.T) {
	preview := permissionGuideDragPreview()
	if preview == nil {
		t.Fatal("missing native icon preview")
	}
	if preview.Bounds() != image.Rect(0, 0, 192, 192) {
		t.Fatal(preview.Bounds())
	}
	for y := 0; y < 192; y++ {
		for x := 0; x < 192; x++ {
			if (x < 20 || y < 20 || x >= 172 || y >= 172) && preview.RGBAAt(x, y).A != 0 {
				t.Fatalf("panel pixels in drag margin at %d,%d", x, y)
			}
		}
	}
	if preview.RGBAAt(96, 96).A == 0 {
		t.Fatal("icon is absent")
	}
}

func TestPermissionGuideDirectFollowDoesNotReplayOnPointerRelease(t *testing.T) {
	from := mygo.Rectangle{X: 100, Y: 100, Width: 500, Height: 367}
	target := mygo.Rectangle{X: 400, Y: 200, Width: 500, Height: 367}
	now := time.Unix(100, 0)
	s := &shell{permissionGuideState: permissionGuideState{followMoveFrom: from, followMoveStarted: now, followMoveTime: 250 * time.Millisecond}}
	halfway, moving := s.nextPermissionGuideFollowFrame(target, now.Add(125*time.Millisecond), false)
	if !moving || halfway.X <= from.X || halfway.X >= target.X {
		t.Fatalf("normal interpolation: %v %t", halfway, moving)
	}
	direct, moving := s.nextPermissionGuideFollowFrame(target, now.Add(130*time.Millisecond), true)
	if direct != target || moving {
		t.Fatalf("direct movement: %v %t", direct, moving)
	}
	released, moving := s.nextPermissionGuideFollowFrame(target, now.Add(150*time.Millisecond), false)
	if released != target || moving {
		t.Fatalf("release replayed old transition: %v %t", released, moving)
	}
	next := target
	next.X += 100
	s.followMoveFrom = target
	s.followMoveStarted = now.Add(time.Second)
	position, moving := s.nextPermissionGuideFollowFrame(next, now.Add(time.Second+125*time.Millisecond), false)
	if !moving || position.X <= target.X || position.X >= next.X {
		t.Fatalf("later idle move lost animation: %v %t", position, moving)
	}
}

func TestPermissionGuideFlightTargetsSettingsContentList(t *testing.T) {
	panel := mygo.Rectangle{X: 1000, Y: 50, Width: 500, Height: 342}
	settings := permissionguide.Rect{X: 100, Y: 50, Width: 740, Height: 625}
	g := permissionGuideFlightBounds(ui.Rect{X: 210, Y: 44, W: 80, H: 80}, panel, settings, mygo.Rectangle{Width: 1920, Height: 1080})
	end := g.point(1)
	if x, y := end.X+float32(g.bounds.X), end.Y+float32(g.bounds.Y); x != 586 || y != 244 {
		t.Fatalf("settings list target=(%g,%g), want(586,244)", x, y)
	}
}

func TestPermissionGuideIllustrationKeepsHandAboveTranslucentArtwork(t *testing.T) {
	icon := image.NewRGBA(image.Rect(0, 0, 80, 80))
	hand := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for i := 0; i < len(icon.Pix); i += 4 {
		icon.Pix[i], icon.Pix[i+3] = 255, 255
	}
	for i := 0; i < len(hand.Pix); i += 4 {
		hand.Pix[i+2], hand.Pix[i+3] = 255, 255
	}
	s := &shell{settingsState: settingsState{appIcon: ui.NewBitmap(icon)}}
	view := &permissionGuideIllustration{active: true, run: 1, cursor: ui.NewBitmap(hand), cursorWidth: 32, cursorHeight: 32, geometry: permissionGuideFlightGeometry{bounds: mygo.Rectangle{Width: 400, Height: 200}, start: ui.Point{X: 330, Y: 130}, control: ui.Point{X: 220, Y: 70}, end: ui.Point{X: 70, Y: 130}, iconSize: 96}}
	v := ui.NewTester(func(c *ui.Context) { s.drawPermissionGuideIllustration(c, view) }, 400, 200)
	v.Advance(400 * time.Millisecond)
	red, blue := false, false
	pixels := v.Image()
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			p := pixels.RGBAAt(x, y)
			if p.R > 100 && p.B == 0 && p.A > 120 && p.A < 160 {
				red = true
			}
			if p.B > 200 && p.R == 0 && p.A > 200 {
				blue = true
			}
		}
	}
	if !red || !blue {
		t.Fatalf("translucent artwork=%v, opaque system hand=%v", red, blue)
	}
	if pixels.RGBAAt(0, 0).A != 0 {
		t.Fatal("guidance paints the desktop")
	}
}

func TestPermissionGuideFlightUsesCompleteNativeTiming(t *testing.T) {
	for name, frames := range map[string][]ui.Keyframe{"progress": permissionGuideFlightProgress, "opacity": permissionGuideFlightOpacity, "scale": permissionGuideFlightScale} {
		if got := frames[len(frames)-1].At; got != 3650*time.Millisecond {
			t.Errorf("%s duration=%v, want 3.65s", name, got)
		}
	}
	var value float32
	renderer := ui.NewTester(func(c *ui.Context) { value = ui.Box(c).Keyframes("scale", permissionGuideFlightScale) }, 100, 100)
	previous := time.Duration(0)
	for _, sample := range []struct {
		at    time.Duration
		value float32
	}{
		{0 * time.Millisecond, 1.000000000000},
		{50 * time.Millisecond, 1.011056828217},
		{100 * time.Millisecond, 1.031048195663},
		{150 * time.Millisecond, 1.049414791976},
		{200 * time.Millisecond, 1.049358005607},
		{250 * time.Millisecond, 1.049193678577},
		{300 * time.Millisecond, 1.048929775116},
		{350 * time.Millisecond, 1.048573411193},
		{400 * time.Millisecond, 1.048130760264},
		{450 * time.Millisecond, 1.047607336025},
		{500 * time.Millisecond, 1.047007992416},
		{1000 * time.Millisecond, 1.037622124464},
		{2000 * time.Millisecond, 1.006019355182},
		{2150 * time.Millisecond, 1.000000000000},
		{3000 * time.Millisecond, 1.000000000000},
		{3650 * time.Millisecond, 1.000000000000},
	} {
		renderer.Advance(sample.at - previous)
		previous = sample.at
		if math.Abs(float64(value-sample.value)) > 0.00001 {
			t.Errorf("scale at %v=%g, native=%g", sample.at, value, sample.value)
		}
	}
}
