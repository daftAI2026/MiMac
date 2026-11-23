package main

import (
	"errors"
	"path/filepath"
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
	window                    *mygo.Window
	read                      func() voiceOverlayFrame
	model                     *dotscope.Model
	renderer                  *dotscope.Renderer
	token                     bridge.SessionToken
	epoch, previousPaint      time.Time
	started, submitted, ended bool
	visible, closed, reduce   bool
}

func newVoiceDotView(read func() voiceOverlayFrame) (voiceOverlayView, error) {
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return nil, err
	}
	return openVoiceDotView(read, filepath.Join(dir, dotscope.LibraryName))
}

func openVoiceDotView(read func() voiceOverlayFrame, library string) (voiceOverlayView, error) {
	renderer, err := dotscope.NewRenderer()
	if err != nil {
		return nil, err
	}
	model, err := dotscope.Open(library)
	if err != nil {
		return nil, err
	}
	v := &voiceDotView{read: read, model: model, renderer: renderer}
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
	now := time.Now()
	if !v.visible || v.token != frame.Token {
		v.selectToken(frame.Token, now)
		display := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
		if display.WorkArea.Width > 0 {
			v.window.SetBounds(mygo.Rectangle{
				X: display.Bounds.X + (display.Bounds.Width-voiceDotWidth)/2, Y: display.WorkArea.Y,
				Width: voiceDotWidth, Height: voiceDotHeight,
			})
		}
	}
	if frame.Phase == bridge.PhaseDraining {
		v.submit(now)
	}
	if !v.visible {
		v.visible = true
		v.window.ShowInactive()
	}
	v.window.Invalidate()
	return nil
}

// The complete Core token scopes this projection, including connection generation.
// The native model owns animation timing and outstanding presentation jobs.
func (v *voiceDotView) selectToken(token bridge.SessionToken, now time.Time) {
	if token == v.token {
		return
	}
	if v.started && !v.ended {
		v.retire(now)
	}
	v.token = token
	v.started, v.submitted, v.ended = false, false, false
}

func (v *voiceDotView) seconds(now time.Time) float64 {
	if v.epoch.IsZero() {
		v.epoch = now
	}
	return now.Sub(v.epoch).Seconds()
}

func (v *voiceDotView) start(now time.Time) {
	if !v.started {
		v.model.Start(v.seconds(now), v.reduce)
		v.started = true
	}
}

func (v *voiceDotView) submit(now time.Time) {
	if !v.submitted && !v.ended {
		v.start(now)
		v.model.Submit(v.token.ID(), v.seconds(now))
		v.submitted = true
	}
}

func (v *voiceDotView) job() uint64 {
	if v.submitted {
		return v.token.ID()
	}
	return 0 // No transcription job was submitted for this capture.
}

func (v *voiceDotView) retire(now time.Time) {
	v.model.Cancel(v.job(), v.seconds(now))
	v.ended = true
}

// Receipt can arrive before the next paint. Submit then finish within the same
// callback so a fast text delivery still uses the model's processing dwell.
func (v *voiceDotView) finish(token bridge.SessionToken, failed bool, now time.Time) {
	if v.closed || !v.visible || v.token != token || v.ended {
		return
	}
	v.start(now)
	if failed {
		v.model.Fail(v.job(), v.seconds(now))
	} else {
		v.submit(now)
		v.model.Done(v.job(), v.seconds(now))
	}
	v.ended = true
}

func (v *voiceDotView) advance(now time.Time, frame voiceOverlayFrame, reduce bool) bool {
	v.reduce = reduce
	dt := voiceDotPeriod
	if !v.previousPaint.IsZero() {
		dt = now.Sub(v.previousPaint)
	}
	v.previousPaint = now
	if frame.Visible {
		v.selectToken(frame.Token, now)
		if !v.ended {
			v.start(now)
			switch frame.Phase {
			case bridge.PhaseCapturing:
				v.model.Meter(frame.CenteredRMS)
			case bridge.PhaseDraining:
				v.submit(now)
			}
		}
	} else if !v.ended && v.started {
		// External input methods do not acknowledge a transcript. Fade away
		// when their audio owner retires, using the model's neutral exit.
		v.retire(now)
	}
	return v.model.Step(v.seconds(now), float32(dt.Seconds()), reduce)
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
			v.token = bridge.SessionToken{}
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
		v.model.Close()
	}
	return nil
}
