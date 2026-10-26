package main

import (
	"time"

	"github.com/egoist/mygo"
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

const (
	// Decorative motion uses elapsed time for its curve, but does not need
	// to redraw the entire MyGo surface at the display's maximum rate.
	decorativeMotionFrameInterval = time.Second / 30
	selectionSweepDelay           = 3600 * time.Millisecond
	selectionSweepDuration        = 600 * time.Millisecond
	selectionSweepRest            = 6500 * time.Millisecond
)

type selectionSweepState struct {
	run      uint64
	next     time.Time
	started  time.Time
	sweeping bool
}

// selectionSweepLocal keeps cadence with the button element, so it disappears
// with the model view. MyGo's Painter.After schedules one repaint at the next
// sweep; it does not keep requesting animation frames while the sweep rests.
func selectionSweepLocal(element ui.Element, selected bool, run uint64, window *mygo.Window, reduceMotion bool) *selectionSweepState {
	state := ui.Local(element, "selected-circle-sweep", func() selectionSweepState { return selectionSweepState{} })
	if state == nil {
		return nil
	}
	if !selected || reduceMotion || !motionWindowVisible(window) {
		*state = selectionSweepState{}
		return state
	}
	if state.run != run {
		*state = selectionSweepState{run: run}
	}
	return state
}

func addSelectionSweep(c *ui.Context, radius float32, state *selectionSweepState, window *mygo.Window, reduceMotion bool) {
	if state == nil {
		return
	}
	ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).PassThrough().Draw(func(p *ui.Painter, bounds ui.Rect) {
		if reduceMotion || !motionWindowVisible(window) {
			*state = selectionSweepState{}
			return
		}
		now := p.Now()
		if state.next.IsZero() && !state.sweeping {
			state.next = now.Add(selectionSweepDelay)
		}
		if !state.sweeping {
			if wait := state.next.Sub(now); wait > 0 {
				p.After(wait)
				return
			}
			state.started = now
			state.sweeping = true
		}
		elapsed := now.Sub(state.started)
		if elapsed >= selectionSweepDuration {
			state.sweeping = false
			state.started = time.Time{}
			state.next = now.Add(selectionSweepRest)
			p.After(selectionSweepRest)
			return
		}
		progress := ui.EaseInOut(float32(elapsed) / float32(selectionSweepDuration))
		paintSelectionSweep(p, bounds, radius, progress)
		p.After(decorativeMotionFrameInterval)
	})
}

func paintSelectionSweep(p *ui.Painter, bounds ui.Rect, radius, progress float32) {
	const inset float32 = 2.5
	inner := ui.Rect{X: bounds.X + inset, Y: bounds.Y + inset, W: bounds.W - 2*inset, H: bounds.H - 2*inset}
	if inner.W <= 0 || inner.H <= 0 {
		return
	}
	stripeWidth := max(float32(3), min(inner.W*0.22, float32(8)))
	start := inner.X - stripeWidth*2
	end := inner.X + inner.W + stripeWidth*2
	cx := start + (end-start)*progress
	cy := inner.Y + inner.H/2
	beam := ui.Rect{X: cx - stripeWidth/2, Y: cy - inner.H*0.9, W: stripeWidth, H: inner.H * 1.8}
	beamRadius := stripeWidth / 2
	light := ui.Hex("#f5f5f5")
	gradient := ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{
		{Color: light.Alpha(0), At: 0},
		{Color: light.Alpha(0.08), At: 0.28},
		{Color: light.Alpha(0.17), At: 0.5},
		{Color: light.Alpha(0.08), At: 0.72},
		{Color: light.Alpha(0), At: 1},
	}}
	p.Clip(inner, max(0, radius-inset), func() {
		p.Transform(ui.Rotation(-18), cx, cy, func() {
			p.FillGradient(beam, gradient, beamRadius)
		})
	})
}

func motionWindowVisible(window *mygo.Window) bool {
	return window != nil && window.IsVisible() && !window.IsMinimized()
}
