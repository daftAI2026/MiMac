package main

import (
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/permissionguide"
)

// This owns presentation resources for one native drag, not authorization state.
type permissionGuideDrop struct {
	active bool
	timer  *time.Timer
	panels []*permissionGuideDropPanel
}

type permissionGuideDropPanel struct {
	window  *mygo.Window
	bounds  mygo.Rectangle
	hole    ui.Rect
	ready   bool
	closing bool
}

func (s *shell) startPermissionGuideDrop() {
	s.stopPermissionGuideDrop()
	if s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.guideWindow.IsVisible() || !permissionGuideAllowsAppDrag(s.currentID) {
		return
	}
	d := &permissionGuideDrop{active: true}
	s.dropGuide = d
	s.iconDragging, s.hasDraggedIcon = true, true
	s.hidePermissionGuideIllustration()
	s.updatePermissionGuideDrop(d)
}

func (s *shell) updatePermissionGuideDrop(d *permissionGuideDrop) {
	if s.dropGuide != d || !d.active {
		return
	}
	if s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.guideWindow.IsVisible() || s.closing {
		s.stopPermissionGuideDrop()
		return
	}
	target, found := permissionguide.SystemSettingsWindowSnapshot()
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
	if !found || !sameScreens {
		for _, p := range d.panels {
			p.close()
		}
		d.panels = nil
	}
	if found {
		hole := permissionGuideSettingsContent(target.Frame)
		for i, display := range displays {
			local := ui.Rect{X: float32(hole.X) - float32(display.Bounds.X), Y: float32(hole.Y) - float32(display.Bounds.Y), W: float32(hole.Width), H: float32(hole.Height)}
			if i >= len(d.panels) {
				p := &permissionGuideDropPanel{bounds: display.Bounds, hole: local}
				d.panels = append(d.panels, p)
				p.window = mygo.NewWindow(mygo.WindowOptions{Title: mygo.App.Name() + " 权限拖放区域", Width: display.Bounds.Width, Height: display.Bounds.Height,
					UseContentSize: true, Hidden: true, Transparent: true, DisableResize: true, DisableShadow: true, Panel: true, Frameless: true,
					Content: ui.View(func(c *ui.Context) { drawPermissionGuideDrop(c, p.hole) })})
				p.window.SetBounds(display.Bounds)
				p.window.SetIgnoreMouseEvents(true)
				p.window.OnReadyToShow(func() {
					p.ready = true
					if p.closing || s.dropGuide != d || !d.active {
						p.window.Close()
						return
					}
					permissionguide.ConfigureDropOverlay(p.window)
					p.window.ShowInactive()
					permissionguide.FadeDropOverlay(p.window, true, nil)
				})
			} else if p := d.panels[i]; p.hole != local {
				p.hole = local
				p.window.Invalidate()
			}
		}
	}
	d.timer = time.AfterFunc(permissionGuideFollowInterval, func() { mygo.RunOnMain(func() { s.updatePermissionGuideDrop(d) }) })
}

func (s *shell) stopPermissionGuideDrop() {
	d := s.dropGuide
	if d == nil {
		return
	}
	s.dropGuide = nil
	d.active = false
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
	if p.closing {
		return
	}
	p.closing = true
	if p.window == nil || p.window.IsDestroyed() {
		return
	}
	if !p.ready {
		p.window.Close()
		return
	}
	permissionguide.FadeDropOverlay(p.window, false, func() {
		if !p.window.IsDestroyed() {
			p.window.Close()
		}
	})
}

func drawPermissionGuideDrop(c *ui.Context, hole ui.Rect) {
	c.Root().Background(ui.Transparent)
	ui.Box(c).Fill().Background(ui.Transparent).Draw(func(p *ui.Painter, r ui.Rect) {
		path := permissionGuideDropPath(r, hole)
		p.FillPath(&path, ui.RGB(0, 0, 0).Alpha(.5))
	})
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
	radius := min(float32(12), w/2, h/2)
	k := radius * float32(.5522847498307936)
	p.MoveTo(x+radius, y).
		CubeTo(x+radius-k, y, x, y+radius-k, x, y+radius).
		LineTo(x, y+h-radius).CubeTo(x, y+h-radius+k, x+radius-k, y+h, x+radius, y+h).
		LineTo(x+w-radius, y+h).CubeTo(x+w-radius+k, y+h, x+w, y+h-radius+k, x+w, y+h-radius).
		LineTo(x+w, y+radius).CubeTo(x+w, y+radius-k, x+w-radius+k, y, x+w-radius, y).
		LineTo(x+radius, y).Close()
	return p
}
