package main

import (
	"image"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/permissionguide"
)

const (
	permissionGuideDropRadius = 12
	permissionGuideRimGap     = 1.5
)

// One guide owns its display resources. Drag phases only show or hide them.
type permissionGuideDrop struct {
	active        bool
	run           uint64
	targetID      uint32
	targetContent permissionguide.Rect
	timer         *time.Timer
	panels        []*permissionGuideDropPanel
}

func (d *permissionGuideDrop) acceptsRelease(point image.Point) bool {
	r := d.targetContent
	x, y := float64(point.X), float64(point.Y)
	return d.active && d.targetID != 0 && x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height
}

type permissionGuideDropPanel struct {
	window  *mygo.Window
	bounds  mygo.Rectangle
	hole    ui.Rect
	inside  bool
	closing bool
}

func (s *shell) startPermissionGuideDrop() {
	if s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.guideWindow.IsVisible() || !permissionGuideAllowsAppDrag(s.currentID) {
		return
	}
	d := s.dropGuide
	if d == nil {
		d = &permissionGuideDrop{}
		s.dropGuide = d
	}
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.active = true
	d.run++
	d.targetID = 0
	s.iconDragging = true
	s.hidePermissionGuideIllustration()
	s.updatePermissionGuideDrop(d)
}

func (s *shell) updatePermissionGuideDrop(d *permissionGuideDrop) {
	if s.dropGuide != d || !d.active {
		return
	}
	if s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.guideWindow.IsVisible() || s.closing {
		s.closePermissionGuideDrop()
		return
	}
	target, found := permissionguide.SystemSettingsDropTarget(d.targetID)
	if !found {
		s.stopPermissionGuideDrop()
		return
	}
	first := d.targetID == 0
	d.targetID = target.ID
	s.hasDraggedIcon = true
	displays := mygo.Screen.Displays()
	sameScreens := len(displays) == len(d.panels)
	if sameScreens {
		for i, display := range displays {
			if display.Bounds != d.panels[i].bounds {
				sameScreens = false
				break
			}
		}
	}
	if !sameScreens {
		for _, p := range d.panels {
			p.close()
		}
		d.panels = nil
	}
	hole := permissionGuideSettingsContent(target.Frame)
	d.targetContent = hole
	pointer := mygo.Screen.CursorScreenPoint()
	inside := d.acceptsRelease(image.Pt(pointer.X, pointer.Y))
	for i, display := range displays {
		local := ui.Rect{X: float32(hole.X) - float32(display.Bounds.X), Y: float32(hole.Y) - float32(display.Bounds.Y), W: float32(hole.Width), H: float32(hole.Height)}
		if i >= len(d.panels) {
			p := &permissionGuideDropPanel{bounds: display.Bounds, hole: local, inside: inside}
			d.panels = append(d.panels, p)
			p.window = mygo.NewWindow(mygo.WindowOptions{Title: mygo.App.Name() + " 权限拖放区域", Width: display.Bounds.Width, Height: display.Bounds.Height,
				UseContentSize: true, Hidden: true, Transparent: true, DisableResize: true, DisableShadow: true, Panel: true, Frameless: true,
				Content: ui.View(func(c *ui.Context) { drawPermissionGuideDrop(c, p.hole, p.inside) })})
			p.window.SetBounds(display.Bounds)
			p.window.SetIgnoreMouseEvents(true)
			p.show()
		} else {
			p := d.panels[i]
			if p.hole != local || p.inside != inside {
				p.hole, p.inside = local, inside
				p.window.Invalidate()
			}
			if first {
				p.show()
			}
		}
	}
	run := d.run
	d.timer = time.AfterFunc(permissionGuideFollowInterval, func() {
		mygo.RunOnMain(func() {
			if s.dropGuide == d && d.active && d.run == run {
				s.updatePermissionGuideDrop(d)
			}
		})
	})
}

func (p *permissionGuideDropPanel) show() {
	if p.closing || p.window.IsDestroyed() {
		return
	}
	permissionguide.ConfigureDropOverlay(p.window)
	p.window.SetBounds(p.bounds)
	p.window.ShowInactive()
	permissionguide.FadeDropOverlay(p.window, true, nil)
}

func (s *shell) stopPermissionGuideDrop() {
	d := s.dropGuide
	if d == nil || !d.active {
		return
	}
	d.active = false
	d.run++
	d.targetID = 0
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	run := d.run
	for _, p := range d.panels {
		if !p.window.IsDestroyed() {
			permissionguide.FadeDropOverlay(p.window, false, func() {
				if s.dropGuide == d && d.run == run && !d.active && !p.window.IsDestroyed() {
					p.window.Hide()
				}
			})
		}
	}
}

func (s *shell) closePermissionGuideDrop() {
	d := s.dropGuide
	if d == nil {
		return
	}
	s.dropGuide = nil
	d.active = false
	d.run++
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	for _, p := range d.panels {
		p.close()
	}
	d.panels = nil
}

func (p *permissionGuideDropPanel) close() {
	p.closing = true
	if p.window != nil && !p.window.IsDestroyed() {
		p.window.Close()
	}
}

func drawPermissionGuideDrop(c *ui.Context, hole ui.Rect, inside bool) {
	c.Root().Background(ui.Transparent)
	mask := ui.Box(c).Fill().Background(ui.Transparent)
	targetOpacity := float32(.46)
	if inside {
		targetOpacity = .9
	}
	opacity := mask.AnimateTo("drop-rim", targetOpacity, ui.Animation{Duration: 180 * time.Millisecond, NonMotion: true})
	mask.Draw(func(p *ui.Painter, r ui.Rect) {
		path := permissionGuideDropPath(r, hole)
		p.FillPath(&path, ui.RGB(0, 0, 0).Alpha(.5))
		if hole.W > 0 && hole.H > 0 {
			// Keep the line in the dimmed area so it never covers the system's drop feedback.
			const outset = permissionGuideRimGap + lineWidth
			rim := ui.Rect{X: hole.X - outset, Y: hole.Y - outset, W: hole.W + 2*outset, H: hole.H + 2*outset}
			p.Stroke(rim, ui.RGB(255, 255, 255).Alpha(opacity), permissionGuideDropCorner(hole)+outset, lineWidth)
		}
	})
}

func permissionGuideDropCorner(hole ui.Rect) float32 {
	return min(permissionGuideDropRadius, hole.W/2, hole.H/2)
}

// Opposite windings leave one transparent rounded hole in a non-zero fill.
// Keeping the hole untrimmed also handles targets spanning multiple displays.
func permissionGuideDropPath(outer, hole ui.Rect) ui.Path {
	var p ui.Path
	x, y, w, h := outer.X, outer.Y, outer.W, outer.H
	p.MoveTo(x, y).LineTo(x+w, y).LineTo(x+w, y+h).LineTo(x, y+h).Close()
	if hole.W <= 0 || hole.H <= 0 {
		return p
	}
	x, y, w, h = hole.X, hole.Y, hole.W, hole.H
	radius := permissionGuideDropCorner(hole)
	k := radius * float32(.5522847498307936)
	p.MoveTo(x+radius, y).
		CubeTo(x+radius-k, y, x, y+radius-k, x, y+radius).
		LineTo(x, y+h-radius).CubeTo(x, y+h-radius+k, x+radius-k, y+h, x+radius, y+h).
		LineTo(x+w-radius, y+h).CubeTo(x+w-radius+k, y+h, x+w, y+h-radius+k, x+w, y+h-radius).
		LineTo(x+w, y+radius).CubeTo(x+w, y+radius-k, x+w-radius+k, y, x+w-radius, y).
		LineTo(x+radius, y).Close()
	return p
}
