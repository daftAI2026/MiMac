package main

import (
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/readyindicator"
)

const (
	readyDotDiameter    = float32(8)
	readyRippleDiameter = float32(20)
	readyLayoutWait     = time.Millisecond
)

type readyIndicatorStamp struct {
	contentWidth, contentHeight int
	footer, bounds              ui.Rect
	label                       string
	footerState                 readyIndicatorFooterState
	caption, body               float32
	rgba                        uint32
	animate                     bool
}

type readyIndicatorFooterState struct {
	permission bool
	message    string
	action     string
	busy       bool
}

type readyIndicatorState struct {
	control     *readyindicator.Control
	active      bool
	failed      bool
	stamp       readyIndicatorStamp
	hasStamp    bool
	measureNext bool
	measureAt   time.Time
	applied     readyIndicatorStamp
	hasApplied  bool
}

// readyIndicatorSlot reserves the pulse's maximum width in MyGo's layout.
func readyIndicatorSlot(c *ui.Context) ui.Element {
	return ui.Box(c).Key("mapping-ready-indicator").Width(readyRippleDiameter).Height(readyDotDiameter).Shrink(0)
}

// drawReadyIndicatorFallback is static and side-effect free. It supplies the
// dot while native setup is pending or unavailable.
func drawReadyIndicatorFallback(element ui.Element, color ui.Color) {
	element.Draw(func(p *ui.Painter, bounds ui.Rect) {
		x := bounds.X + (bounds.W-readyDotDiameter)/2
		y := bounds.Y + (bounds.H-readyDotDiameter)/2
		p.Fill(ui.Rect{X: x, Y: y, W: readyDotDiameter, H: readyDotDiameter}, color, readyDotDiameter/2)
	})
}

func (s *shell) syncReadyIndicator(
	c *ui.Context,
	element, footer ui.Element,
	color ui.Color,
	label string,
	footerState readyIndicatorFooterState,
	caption, body float32,
	window *mygo.Window,
	reduceMotion bool,
) bool {
	if s.closing || s.page != pageMappings || window == nil {
		s.clearReadyIndicator()
		return false
	}

	state := &s.readyIndicator
	if !state.active {
		state.active = true
		state.failed = false
		state.hasStamp = false
		state.hasApplied = false
		state.measureNext = false
	}
	if state.failed {
		return false
	}

	contentWidth, contentHeight := window.ContentSize()
	colorValue := readyColorRGBA(color)
	current := readyIndicatorStamp{
		contentWidth:  contentWidth,
		contentHeight: contentHeight,
		footer:        footer.LayoutBounds(),
		bounds:        element.LayoutBounds(),
		label:         label,
		footerState:   footerState,
		caption:       caption,
		body:          body,
		rgba:          colorValue,
		animate:       !reduceMotion,
	}

	if state.measureNext {
		// MyGo may rebuild a frame several times with the same Context time.
		// Wait until the next frame, when LayoutBounds contains committed geometry.
		if currentFrame := c.Now(); currentFrame.Equal(state.measureAt) {
			return state.control != nil
		}
		state.measureNext = false
		state.stamp = current
		state.hasStamp = true
	} else if !state.hasStamp || current != state.stamp {
		state.stamp = current
		state.hasStamp = true
		state.measureNext = true
		state.measureAt = c.Now()
		c.After(readyLayoutWait)
		// An attached view keeps the dot visible until its next layout update;
		// do not paint a second dot at the new position during that handoff.
		return state.control != nil
	}

	if !validReadyBounds(current.bounds) {
		return false
	}

	if state.control == nil {
		control, err := readyindicator.New(window.NativeHandle())
		if err != nil {
			state.failed = true
			log.Printf("ready indicator setup failed; using static fallback: %v", err)
			return false
		}
		state.control = control
	}

	if !state.hasApplied || current != state.applied {
		bounds := current.bounds
		err := state.control.Update(readyindicator.Rect{
			X: float64(bounds.X), Y: float64(bounds.Y),
			W: float64(bounds.W), H: float64(bounds.H),
		}, colorValue, current.animate)
		if err != nil {
			state.control.Close()
			state.control = nil
			state.failed = true
			log.Printf("ready indicator update failed; using static fallback: %v", err)
			return false
		}
		state.applied = current
		state.hasApplied = true
	}
	return true
}

func validReadyBounds(bounds ui.Rect) bool {
	return bounds.W >= readyRippleDiameter && bounds.H >= readyDotDiameter
}

func readyColorRGBA(color ui.Color) uint32 {
	return uint32(color.R)<<24 | uint32(color.G)<<16 | uint32(color.B)<<8 | uint32(color.A)
}

func (s *shell) clearReadyIndicator() {
	state := &s.readyIndicator
	if state.control != nil {
		state.control.Close()
	}
	*state = readyIndicatorState{}
}
