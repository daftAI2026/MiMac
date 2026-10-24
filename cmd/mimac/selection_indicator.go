package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

func movingSelectionBackground(c *ui.Context, container ui.Element, selected ui.Rect, color ui.Color, radius float32, key string) {
	if selected.H <= 0 {
		c.AnimationFrame()
		return
	}
	parent := container.LayoutBounds()
	if parent.H <= 0 {
		c.AnimationFrame()
		return
	}
	y := container.AnimateTo(key+"/y", selected.Y-parent.Y, selectionMotion)
	height := container.AnimateTo(key+"/height", selected.H, selectionMotion)
	container.Draw(func(p *ui.Painter, bounds ui.Rect) {
		if height > 0 && bounds.W > 0 {
			p.Fill(ui.Rect{X: bounds.X, Y: bounds.Y + y, W: bounds.W, H: height}, color, radius)
		}
	})
}

func selectionPulseRun(profileID, selectedID string) uint64 {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	hash := offset
	for _, value := range []string{profileID, selectedID} {
		for i := 0; i < len(value); i++ {
			hash ^= uint64(value[i])
			hash *= prime
		}
		hash ^= 0xff
		hash *= prime
	}
	if hash == 0 {
		return 1
	}
	return hash
}

func selectionOutlinePulse(element ui.Element, run uint64) float32 {
	timeline := element.Timeline("selection-outline", ui.TimelineOptions{Run: run})
	const (
		base = float32(0.72)
		dim  = float32(0.48)
	)
	first := timeline.Tween(ui.Tween{From: base, To: 1, Duration: 150 * time.Millisecond, Ease: ui.EaseInOut})
	second := timeline.Tween(ui.Tween{From: 1, To: dim, Duration: 150 * time.Millisecond, Ease: ui.EaseInOut})
	third := timeline.Tween(ui.Tween{From: dim, To: 1, Duration: 150 * time.Millisecond, Ease: ui.EaseInOut})
	fourth := timeline.Tween(ui.Tween{From: 1, To: base, Duration: 150 * time.Millisecond, Ease: ui.EaseInOut})
	switch at := timeline.Time(); {
	case at < 150*time.Millisecond:
		return first
	case at < 300*time.Millisecond:
		return second
	case at < 450*time.Millisecond:
		return third
	default:
		return fourth
	}
}

func addSelectionOutline(c *ui.Context, radius, opacity float32) {
	outerColor := ui.Hex("#000000").Alpha(opacity)
	innerColor := modelHighlight.Alpha(opacity)
	ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Radius(radius).Border(1, outerColor).PassThrough()
	ui.Box(c).Absolute().Left(1).Top(1).Right(1).Bottom(1).Radius(max(0, radius-1)).Border(1, innerColor).PassThrough()
}
