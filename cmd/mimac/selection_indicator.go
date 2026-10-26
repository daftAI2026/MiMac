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

func selectionOutlineRun(profileID, selectedID string) uint64 {
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

const (
	decorativeMotionFrameInterval = time.Second / 30
	selectionOutlineStep          = 150 * time.Millisecond
	selectionOutlineDuration      = 4 * selectionOutlineStep
	selectionOutlineRest          = 6500 * time.Millisecond
)

type selectionOutlineState struct {
	run     uint64
	started time.Time
	next    time.Time
}

// selectionOutlineLocal ties the pulse cadence to the selected button. The
// base outline remains static when motion is reduced or the window is hidden.
func selectionOutlineLocal(element ui.Element, selected bool, run uint64, window *mygo.Window, reduceMotion bool) *selectionOutlineState {
	state := ui.Local(element, "selected-circle-outline", func() selectionOutlineState { return selectionOutlineState{} })
	if state == nil {
		return nil
	}
	if !selected || reduceMotion || !motionWindowVisible(window) {
		*state = selectionOutlineState{}
		return state
	}
	if state.run != run {
		*state = selectionOutlineState{run: run}
	}
	return state
}

// addSelectionOutline paints the original paired strokes with one opacity
// curve. Painter.After schedules only the next 30 Hz pulse frame or rest end.
func addSelectionOutline(c *ui.Context, radius float32, state *selectionOutlineState, window *mygo.Window, reduceMotion bool) {
	if state == nil {
		return
	}
	ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).PassThrough().Draw(func(p *ui.Painter, bounds ui.Rect) {
		if reduceMotion || !motionWindowVisible(window) {
			*state = selectionOutlineState{}
			paintSelectionOutline(p, bounds, radius, 0.72)
			return
		}

		now := p.Now()
		if state.started.IsZero() {
			if state.next.IsZero() {
				state.started = now
			} else if wait := state.next.Sub(now); wait > 0 {
				paintSelectionOutline(p, bounds, radius, 0.72)
				p.After(wait)
				return
			} else {
				state.next = time.Time{}
				state.started = now
			}
		}

		elapsed := now.Sub(state.started)
		if elapsed >= selectionOutlineDuration {
			state.started = time.Time{}
			state.next = now.Add(selectionOutlineRest)
			paintSelectionOutline(p, bounds, radius, 0.72)
			p.After(selectionOutlineRest)
			return
		}

		paintSelectionOutline(p, bounds, radius, selectionOutlineOpacity(elapsed))
		p.After(decorativeMotionFrameInterval)
	})
}

func selectionOutlineOpacity(elapsed time.Duration) float32 {
	const (
		base = float32(0.72)
		dim  = float32(0.48)
	)
	step := selectionOutlineStep
	switch {
	case elapsed < step:
		return selectionOutlineTween(base, 1, elapsed, step)
	case elapsed < 2*step:
		return selectionOutlineTween(1, dim, elapsed-step, step)
	case elapsed < 3*step:
		return selectionOutlineTween(dim, 1, elapsed-2*step, step)
	case elapsed < selectionOutlineDuration:
		return selectionOutlineTween(1, base, elapsed-3*step, step)
	default:
		return base
	}
}

func selectionOutlineTween(from, to float32, elapsed, duration time.Duration) float32 {
	progress := ui.EaseInOut(float32(elapsed) / float32(duration))
	return from + (to-from)*progress
}

func paintSelectionOutline(p *ui.Painter, bounds ui.Rect, radius, opacity float32) {
	p.Stroke(bounds, ui.Hex("#000000").Alpha(opacity), radius, 1)
	inner := ui.Rect{X: bounds.X + 1, Y: bounds.Y + 1, W: bounds.W - 2, H: bounds.H - 2}
	if inner.W > 0 && inner.H > 0 {
		p.Stroke(inner, modelHighlight.Alpha(opacity), max(0, radius-1), 1)
	}
}

func motionWindowVisible(window *mygo.Window) bool {
	return window != nil && window.IsVisible() && !window.IsMinimized()
}
