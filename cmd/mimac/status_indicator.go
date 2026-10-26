package main

import (
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	readyHaloDuration = 1800 * time.Millisecond
	readyHaloRest     = 900 * time.Millisecond
)

type readyHaloState struct {
	next    time.Time
	started time.Time
}

// readyHalo draws a quiet ripple around the ready dot. Painter.After wakes
// only when another ripple is due; AnimationFrame is requested only while a
// ripple is visible.
func readyHalo(dot ui.Element, color ui.Color, window *mygo.Window, reduceMotion bool) {
	state := ui.Local(dot, "remote-ready-halo", func() readyHaloState { return readyHaloState{} })
	if state == nil {
		return
	}
	if reduceMotion || !motionWindowVisible(window) {
		*state = readyHaloState{}
	}
	dot.Draw(func(p *ui.Painter, bounds ui.Rect) {
		if reduceMotion || !motionWindowVisible(window) {
			*state = readyHaloState{}
			return
		}
		now := p.Now()
		if state.started.IsZero() && state.next.IsZero() {
			state.started = now
		}
		if state.started.IsZero() {
			if state.next.IsZero() {
				state.next = now.Add(readyHaloRest)
			}
			if wait := state.next.Sub(now); wait > 0 {
				p.After(wait)
				return
			}
			state.next = time.Time{}
			state.started = now
		}
		elapsed := now.Sub(state.started)
		if elapsed >= readyHaloDuration {
			state.started = time.Time{}
			state.next = now.Add(readyHaloRest)
			p.After(readyHaloRest)
			return
		}
		progress := ui.EaseInOut(float32(elapsed) / float32(readyHaloDuration))
		radius := float32(4) + 3.5*progress
		alpha := 0.20 * (1 - progress)
		centerX, centerY := bounds.X+bounds.W/2, bounds.Y+bounds.H/2
		p.Stroke(ui.Rect{X: centerX - radius, Y: centerY - radius, W: 2 * radius, H: 2 * radius}, color.Alpha(alpha), radius, 0.5)
		p.AnimationFrame()
	})
}
