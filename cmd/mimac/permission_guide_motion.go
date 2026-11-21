package main

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
	brand "mimac.local/mimac/Assets/Brand"
	"mimac.local/mimac/internal/platform/permissionguide"
)

var permissionGuideIconMotion = ui.Animation{Spring: ui.SpringOptions{Duration: 300 * time.Millisecond, Bounce: .25}}
var permissionGuideJoinEase = ui.CubicBezier(.42, 0, .58, 1)
var permissionGuideFollowEase = ui.CubicBezier(0, 0, .58, 1)

func permissionGuideFollowPosition(from, to mygo.Rectangle, progress float32) mygo.Rectangle {
	progress = max(0, min(1, progress))
	t := float64(permissionGuideFollowEase(progress))
	return mygo.Rectangle{X: from.X + int(math.Round(float64(to.X-from.X)*t)), Y: from.Y + int(math.Round(float64(to.Y-from.Y)*t)), Width: to.Width, Height: to.Height}
}

// Direct movement completes the current transition so releasing the pointer
// cannot replay an older interpolated position.
func (s *shell) nextPermissionGuideFollowFrame(target mygo.Rectangle, now time.Time, direct bool) (mygo.Rectangle, bool) {
	if direct {
		s.followMoveFrom = target
		s.followMoveStarted = now.Add(-s.followMoveTime)
	}
	progress := min(1, float32(now.Sub(s.followMoveStarted))/float32(s.followMoveTime))
	if s.followMoveInitial {
		t := float64(permissionGuideJoinEase(max(0, min(1, progress))))
		return mygo.Rectangle{X: s.followMoveFrom.X + int(math.Round(float64(target.X-s.followMoveFrom.X)*t)), Y: s.followMoveFrom.Y + int(math.Round(float64(target.Y-s.followMoveFrom.Y)*t)), Width: target.Width, Height: target.Height}, progress < 1
	}
	return permissionGuideFollowPosition(s.followMoveFrom, target, progress), progress < 1
}

func (s *shell) permissionGuideAppIcon(c *ui.Context) {
	icon := ui.Box(c).Key("permission-guide-app-icon").Width(permissionGuideIconSlot).Height(permissionGuideIconSlot).Shrink(0).
		Label(mygo.App.Name() + " 应用图标").AlignItems(ui.Center).Justify(ui.Center).Cursor(ui.CursorGrab)
	if s.snapshot.appBundlePath != "" {
		icon.Tooltip("拖入系统设置的权限列表")
		if data, err := transfer.FileData(s.snapshot.appBundlePath); err == nil {
			window, generation := s.guideWindow, s.generation
			var draggedPermission, previousResumeID permissionGuideID
			icon.DragDataFrom(func() transfer.Data {
				draggedPermission = s.currentID
				previousResumeID = s.resumeID
				s.prepareDragRegistration(draggedPermission)
				return permissionGuideFileOffer(draggedPermission, data, func() {
					if draggedPermission == permissionGuidePostEvent {
						if s.guideWindow == window && s.generation == generation && permissionguide.SettingsReadyForControlDrag(s.settingsOpenedFor == draggedPermission) {
							s.repairDragRegistrationBeforeDelivery(draggedPermission)
						}
						return
					}
					d := s.dropGuide
					if s.guideWindow != window || s.generation != generation || s.currentID != draggedPermission ||
						!permissionGuideAllowsAppDrag(draggedPermission) || d == nil || !d.active {
						return
					}
					p := mygo.Screen.CursorScreenPoint()
					point := image.Pt(p.X, p.Y)
					if d.acceptsRelease(point) && permissionguide.SettingsReceivesReleasedDrop(d.targetID, p.X, p.Y,
						draggedPermission == permissionGuideInput, s.settingsOpenedFor == draggedPermission) {
						s.repairDragRegistrationBeforeDelivery(draggedPermission)
					}
				})
			}, transfer.DragOptions{Started: func(image.Point) {
				if s.guideWindow == window && s.generation == generation {
					s.startPermissionGuideDrop()
					if d := s.dropGuide; d != nil && d.active && d.targetID != 0 {
						s.resumeID = draggedPermission
					}
				}
			}, Operations: transfer.Copy, Preview: s.iconDragPreview, Hotspot: image.Pt(96, 96), PreviewScale: 2, Done: func(result transfer.Result) {
				if s.guideWindow != window || s.generation != generation {
					return
				}
				d := s.dropGuide
				submitted := d != nil && permissionGuideAllowsAppDrag(draggedPermission) && result.Err == nil &&
					!result.Canceled && result.Operation == transfer.Copy && d.acceptsRelease(result.Point)
				if submitted {
					s.resumeID = draggedPermission
				} else {
					s.resumeID = previousResumeID
				}
				s.stopPermissionGuideDrop()
				s.iconDragging = false
				s.hidePermissionGuideIllustration()
				if submitted {
					permissionguide.YieldToSystemSettings()
				}
				s.refreshPermissionGuide()
			}})
		}
	}
	hovered, pressed, dragging := icon.Hovered(), icon.Pressed(), icon.Dragging()
	if hovered && !s.iconWasHovered && !dragging {
		s.prepareDragRegistration(s.currentID)
	}
	s.iconWasHovered = hovered
	s.iconDragging = dragging
	if dragging {
		s.hidePermissionGuideIllustration()
	}
	if pressed || dragging {
		icon.Cursor(ui.CursorGrabbing)
	}
	scale, alpha, radius, y := float32(1), float32(.15), float32(4), float32(2)
	if hovered {
		scale, alpha, radius, y = 1.06, .35, 10, 5
	}
	if pressed || dragging {
		scale = .96
	}
	if dragging {
		alpha = 0
	}
	scale = icon.AnimateTo("scale", scale, permissionGuideIconMotion)
	alpha = icon.AnimateTo("shadow-alpha", alpha, permissionGuideIconMotion)
	radius = icon.AnimateTo("shadow-radius", radius, permissionGuideIconMotion)
	y = icon.AnimateTo("shadow-y", y, permissionGuideIconMotion)
	icon.Children(func() {
		if s.appIcon != nil {
			ui.Image(c, s.appIcon).Width(permissionGuideIconSize).Height(permissionGuideIconSize).Fit(ui.Contain).
				Radius(16).Scale(scale, scale).Shadow(0, y, radius, 0, ui.RGB(0, 0, 0).Alpha(alpha))
		}
	})
	if bounds := icon.LayoutBounds(); bounds.W > 0 && bounds.H > 0 {
		s.iconBounds = bounds
	} else {
		c.After(time.Millisecond)
	}
}

// MyGo calls the drag source only after the drag threshold. Device Control
// may read the file before AppKit reports Started. Input Monitoring can defer
// its release check until the receiver reads either URL representation.
func permissionGuideFileOffer(id permissionGuideID, file transfer.Data, beforeDelivery func()) transfer.Data {
	if id == permissionGuidePostEvent {
		beforeDelivery()
		return file
	}
	deliver := sync.OnceFunc(beforeDelivery)
	representation := func(format transfer.Format) transfer.Representation {
		return transfer.Lazy(format, func() ([]byte, error) {
			deliver()
			return file.Read(format)
		})
	}
	return transfer.New(transfer.NewItem(representation(transfer.FileList), representation(transfer.URIList)))
}

// Supply only the icon pixels. The default native drag preview captures the
// composited element rectangle, which includes the panel behind the icon.
func permissionGuideDragPreview() *image.RGBA {
	source, err := png.Decode(bytes.NewReader(brand.AboutIconPNG))
	if err != nil {
		return nil
	}
	preview := image.NewRGBA(image.Rect(0, 0, 192, 192))
	// Match the pressed icon size, retaining a transparent margin and hotspot.
	draw.CatmullRom.Scale(preview, image.Rect(20, 20, 172, 172), source, source.Bounds(), draw.Src, nil)
	return preview
}

type permissionGuideFlightGeometry struct {
	bounds              mygo.Rectangle
	start, control, end ui.Point
	iconSize            float32
}

func permissionGuideFlightBounds(icon ui.Rect, panel mygo.Rectangle, settings permissionguide.Rect, area mygo.Rectangle) permissionGuideFlightGeometry {
	start := ui.Point{X: float32(panel.X) + icon.X + icon.W/2, Y: float32(panel.Y) + icon.Y + icon.H/2}
	end := permissionGuideFlightDestination(settings)
	lift := float32(math.Hypot(float64(end.X-start.X), float64(end.Y-start.Y))) / 4
	top := min(start.Y, end.Y)
	padding := icon.W/2 + 16
	control := ui.Point{X: (start.X + end.X) / 2, Y: top - min(lift, max(0, top-float32(area.Y)-padding))}
	left, right := min(start.X, end.X, control.X)-padding, max(start.X, end.X, control.X)+padding
	upper, lower := min(start.Y, end.Y, control.Y)-padding, max(start.Y, end.Y, control.Y)+padding
	bounds := mygo.Rectangle{X: int(math.Floor(float64(left))), Y: int(math.Floor(float64(upper))), Width: int(math.Ceil(float64(right))) - int(math.Floor(float64(left))), Height: int(math.Ceil(float64(lower))) - int(math.Floor(float64(upper)))}
	local := func(p ui.Point) ui.Point { return ui.Point{X: p.X - float32(bounds.X), Y: p.Y - float32(bounds.Y)} }
	return permissionGuideFlightGeometry{bounds: bounds, start: local(start), control: local(control), end: local(end), iconSize: icon.W}
}

func (g permissionGuideFlightGeometry) point(t float32) ui.Point {
	t = max(0, min(1, t))
	u := 1 - t
	return ui.Point{X: u*u*g.start.X + 2*u*t*g.control.X + t*t*g.end.X, Y: u*u*g.start.Y + 2*u*t*g.control.Y + t*t*g.end.Y}
}

type permissionGuideIllustration struct {
	window                    *mygo.Window
	geometry                  permissionGuideFlightGeometry
	active                    bool
	run                       uint64
	cursor                    *ui.Bitmap
	cursorWidth, cursorHeight float32
}

func (s *shell) syncPermissionGuideIllustration(settings permissionguide.Rect, area mygo.Rectangle) {
	access, exists := permissionGuideAccessForID(s.snapshot, s.currentID)
	if !exists || access == permissionGuideGranted || access == permissionGuideUnknown || access == permissionGuideRestricted ||
		!s.snapshot.actionsEnabled || !permissionGuideAllowsAppDrag(s.currentID) || s.reduceMotion || s.hasDraggedIcon || s.iconDragging || s.followManual || s.iconBounds.W <= 0 {
		s.hidePermissionGuideIllustration()
		return
	}
	// The source rectangle includes the icon's transparent margin. Preserve it
	// for the group size and flight bounds, rather than measuring painted pixels.
	geometry := permissionGuideFlightBounds(s.iconBounds, s.guideWindow.Bounds(), settings, area)
	if s.illustration == nil {
		view := &permissionGuideIllustration{}
		if graphic, err := permissionguide.ClosedHandCursorImage(); err == nil {
			view.cursor = ui.NewBitmap(graphic.Pixels)
			view.cursorWidth, view.cursorHeight = graphic.Width, graphic.Height
		}
		s.illustration = view
		view.window = mygo.NewWindow(mygo.WindowOptions{Title: mygo.App.Name() + " 权限拖放引导", Width: geometry.bounds.Width, Height: geometry.bounds.Height,
			UseContentSize: true, Hidden: true, Transparent: true, DisableResize: true, DisableShadow: true, Panel: true, Frameless: true, Parent: s.guideWindow,
			Content: ui.View(func(c *ui.Context) { s.drawPermissionGuideIllustration(c, view) })})
		view.window.SetIgnoreMouseEvents(true)
	}
	view := s.illustration
	previousBounds := view.geometry.bounds
	changed, show := view.updateGeometry(geometry)
	if !changed {
		return
	}
	if previousBounds != geometry.bounds {
		view.window.SetBounds(geometry.bounds)
	}
	if show {
		view.window.ShowInactive()
	}
	view.window.Invalidate()
}

// Refreshing a live anchor must not restart a partially shown flight or
// re-order its window in front of System Settings again.
func (view *permissionGuideIllustration) updateGeometry(geometry permissionGuideFlightGeometry) (changed, show bool) {
	if view.active && view.geometry == geometry {
		return false, false
	}
	view.geometry = geometry
	if view.active {
		return true, false
	}
	view.active = true
	view.run++
	return true, true
}

func (s *shell) hidePermissionGuideIllustration() {
	if view := s.illustration; view != nil && view.active {
		view.active = false
		if !view.window.IsDestroyed() {
			view.window.Hide()
		}
	}
}

func (s *shell) closePermissionGuideIllustration() {
	if view := s.illustration; view != nil {
		s.illustration = nil
		view.active = false
		if !view.window.IsDestroyed() {
			view.window.Close()
		}
	}
}

var permissionGuideFlightOpacity = []ui.Keyframe{
	{Value: 0}, {At: 200 * time.Millisecond, Value: 1}, {At: 2150 * time.Millisecond, Value: 1},
	{At: 2450 * time.Millisecond, Value: 0}, {At: 3650 * time.Millisecond, Value: 0},
}
var permissionGuideFlightProgress = []ui.Keyframe{
	{Value: 0}, {At: 150 * time.Millisecond, Value: 0}, {At: 2150 * time.Millisecond, Value: 1},
	{At: 2450 * time.Millisecond, Value: 1}, {At: 3650 * time.Millisecond, Value: 0},
}
var permissionGuideFlightScale = []ui.Keyframe{
	{Value: 1}, {At: 150 * time.Millisecond, Value: float32(1 + .08*permissionGuideSpringResponse(.3)), Ease: permissionGuideFlightSpring},
	{At: 2150 * time.Millisecond, Value: 1, Ease: ui.CubicBezier(.42, 0, 1, 1)}, {At: 3650 * time.Millisecond, Value: 1},
}

func permissionGuideFlightSpring(t float32) float32 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	// The 150 ms segment samples a spring with a 500 ms response. Normalize
	// its actual endpoint so the next segment continues without snapping.
	return float32(permissionGuideSpringResponse(float64(t)*.3) / permissionGuideSpringResponse(.3))
}

func permissionGuideSpringResponse(t float64) float64 {
	x := t * 2 * math.Pi
	z := .85
	d := math.Sqrt(1 - z*z)
	return 1 - math.Exp(-z*x)*(math.Cos(d*x)+z/d*math.Sin(d*x))
}

func (s *shell) drawPermissionGuideIllustration(c *ui.Context, view *permissionGuideIllustration) {
	c.Root().Background(ui.Transparent)
	if !view.active || s.appIcon == nil || c.Preferences().ReduceMotion {
		return
	}
	// A newly visible flight starts at the first keyframe. Varying Run on
	// a retained element would resume from its last sampled progress.
	root := ui.Box(c).Key(view.run).Fill().Background(ui.Transparent)
	options := ui.KeyframeOptions{Repeat: ui.RepeatForever}
	opacity := root.Keyframes("opacity", permissionGuideFlightOpacity, options)
	progress := root.Keyframes("progress", permissionGuideFlightProgress, options)
	scale := root.Keyframes("scale", permissionGuideFlightScale, options)
	point := view.geometry.point(progress)
	size := view.geometry.iconSize
	if size <= 0 {
		size = permissionGuideIconSlot
	}
	root.Children(func() {
		ui.Box(c).Absolute().Left(point.X-size/2).Top(point.Y-size/2).Width(size).Height(size).Scale(scale, scale).Opacity(opacity).Children(func() {
			artworkSize := size * permissionGuideIconSize / permissionGuideIconSlot
			ui.Image(c, s.appIcon).Absolute().Left((size - artworkSize) / 2).Top((size - artworkSize) / 2).Width(artworkSize).Height(artworkSize).Fit(ui.FillBox).Opacity(.55)
			if view.cursor != nil {
				ui.Image(c, view.cursor).Absolute().Left((size - view.cursorWidth) / 2).Top((size - view.cursorHeight) / 2).Width(view.cursorWidth).Height(view.cursorHeight).Fit(ui.Contain)
			}
		})
	})
}

// System Settings reserves a sidebar and toolbar before the permission list.
// Small windows fall back to their full frame if the list area is empty.
func permissionGuideFlightDestination(settings permissionguide.Rect) ui.Point {
	content := permissionGuideSettingsContent(settings)
	return ui.Point{X: float32(content.X + content.Width/2), Y: float32(content.Y + min(content.Height/2, 130))}
}

// Shared by the illustration destination and the live drop spotlight.
func permissionGuideSettingsContent(settings permissionguide.Rect) permissionguide.Rect {
	if settings.Width > 256 && settings.Height > 76 {
		return permissionguide.Rect{X: settings.X + 244, Y: settings.Y + 64, Width: settings.Width - 256, Height: settings.Height - 76}
	}
	return settings
}
