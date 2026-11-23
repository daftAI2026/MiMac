package main

import (
	"errors"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/dotscope"
)

// The free-floating plate sits 6 pt below the menu bar. The rest of this
// click-through panel leaves room for its shadow, including during the exit.
const (
	voiceDotWidth  = 176
	voiceDotHeight = 64
	voiceDotGap    = 6
	voiceDotPeriod = time.Second / 60
)

type voiceDotView struct {
	window                      *mygo.Window
	read                        func() voiceOverlayFrame
	model                       *dotscope.Model
	renderer                    *dotscope.Renderer
	token                       bridge.SessionToken
	stage                       dotscope.Stage
	previousPaint, workingSince time.Time
	doneAt, terminalUntil       time.Time
	visible, closed, restart    bool
}

func newVoiceDotView(read func() voiceOverlayFrame) (voiceOverlayView, error) {
	renderer, err := dotscope.NewRenderer()
	if err != nil {
		return nil, err
	}
	v := &voiceDotView{read: read, model: dotscope.New(), renderer: renderer}
	v.window = mygo.NewWindow(mygo.WindowOptions{
		Title: "点阵语音条", Width: voiceDotWidth, Height: voiceDotHeight,
		UseContentSize: true, Panel: true, PanelPointerOnly: true, IgnoreMouseEvents: true,
		Frameless: true, Transparent: true, Hidden: true, DisableResize: true, DisableShadow: true,
		Content: ui.View(v.draw),
	})
	return v, nil
}

func (v *voiceDotView) Update(frame voiceOverlayFrame) error {
	if v.closed || v.window.IsDestroyed() {
		return errors.New("voice dots: view is closed")
	}
	if !frame.Visible {
		return v.Hide()
	}
	if !v.visible || v.token != frame.Token {
		v.begin(frame.Token)
		display := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
		if display.WorkArea.Width > 0 {
			v.window.SetBounds(mygo.Rectangle{
				X: display.Bounds.X + (display.Bounds.Width-voiceDotWidth)/2, Y: display.WorkArea.Y,
				Width: voiceDotWidth, Height: voiceDotHeight,
			})
		}
	}
	if !v.visible {
		v.visible = true
		v.window.ShowInactive()
	}
	v.window.Invalidate()
	return nil
}

func (v *voiceDotView) begin(token bridge.SessionToken) {
	v.token = token
	v.restart = true
	v.stage = dotscope.StageRecording
	v.workingSince, v.doneAt, v.terminalUntil = time.Time{}, time.Time{}, time.Time{}
}

// Only the matching audio owner's explicit completion can color the terminal
// frame. An external tool recording audio offers no text delivery receipt.
func (v *voiceDotView) finish(token bridge.SessionToken, failed bool, now time.Time) {
	if v.closed || !v.visible || v.token != token {
		return
	}
	if failed {
		v.stage = dotscope.StageFailed
		v.doneAt = time.Time{}
		v.terminalUntil = now.Add(600 * time.Millisecond)
		return
	}
	v.stage = dotscope.StageProcessing
	v.doneAt = now
	if !v.workingSince.IsZero() && now.Before(v.workingSince.Add(200*time.Millisecond)) {
		v.doneAt = v.workingSince.Add(200 * time.Millisecond)
	}
	v.terminalUntil = v.doneAt.Add(100 * time.Millisecond)
}

func (v *voiceDotView) advance(now time.Time, frame voiceOverlayFrame, reduce bool) bool {
	dt := voiceDotPeriod
	if !v.previousPaint.IsZero() {
		dt = min(max(now.Sub(v.previousPaint), time.Second/240), time.Second/20)
	}
	v.previousPaint = now
	if frame.Visible && frame.Token != v.token {
		v.begin(frame.Token)
	}
	if v.restart {
		v.model.Restart(reduce)
		v.restart = false
	}
	if frame.Visible {
		v.stage = dotscope.StageRecording
		if frame.Phase == bridge.PhaseDraining {
			v.stage = dotscope.StageProcessing
			if v.workingSince.IsZero() {
				v.workingSince = now
			}
		}
	} else if !v.doneAt.IsZero() && !now.Before(v.doneAt) {
		v.stage = dotscope.StageDone
	}
	visible := frame.Visible || now.Before(v.terminalUntil)
	loudness := float32(-120)
	if frame.Phase == bridge.PhaseCapturing && frame.Visible {
		loudness = frame.DB
	}
	v.model.Advance(float32(dt.Seconds()), dotscope.Frame{Stage: v.stage, Visible: visible, LoudnessDB: loudness, ReduceMotion: reduce})
	return visible || !v.model.Settled()
}

func (v *voiceDotView) draw(c *ui.Context) {
	dark, reduce := c.Theme().Dark, c.Preferences().ReduceMotion
	c.Root().Background(ui.Transparent).Draw(func(p *ui.Painter, bounds ui.Rect) {
		if v.closed || !v.visible {
			return
		}
		if !v.advance(time.Now(), v.read(), reduce) {
			v.visible = false
			v.previousPaint = time.Time{}
			v.window.Hide()
			return
		}
		bounds.Y += voiceDotGap + dotscope.PlateHeight/2 - bounds.H/2
		v.renderer.Draw(p, bounds, v.model.Snapshot(), dark)
		// Ask the display clock only while the plate is moving or listening.
		// The software fallback and reduced motion use fewer redraws.
		if reduce || !p.Accelerated() {
			p.AnimationFrameRate(20)
		} else {
			p.AnimationFrameRate(60)
		}
	})
}

func (v *voiceDotView) Hide() error {
	// The last scheduled paint reads the empty session, fades, then unmaps
	// the panel. Repeated idle notifications do not wake a hidden renderer.
	return nil
}

func (v *voiceDotView) Close() error {
	if !v.closed {
		v.closed, v.visible = true, false
		if !v.window.IsDestroyed() {
			v.window.Hide()
			v.window.Close()
		}
	}
	return nil
}
