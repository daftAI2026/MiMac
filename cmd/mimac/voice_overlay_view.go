package main

import (
	"errors"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/siriglass"
)

const voiceOverlaySlot = 160

// The native material's shadow extends beyond its layout slot. Reserve a
// transparent tail; it is not a second surface or a caption container.
const voiceOverlayHeight = voiceOverlaySlot + int(2*windowInset)
const voiceOverlayFrameInterval = time.Second / 30

// MyGo owns the nonactivating panel and frame scheduling. The native adapter
// owns only the material view inside it, never microphone or session state.
type mygoVoiceOverlay struct {
	window          *mygo.Window
	renderer        *siriglass.Renderer
	read            func() voiceOverlayFrame
	failed          func(error)
	frame           voiceOverlayFrame
	trace           *voiceOverlayTrace
	visible, closed bool
}

func newVoiceOverlayView(read func() voiceOverlayFrame, failed func(error)) (voiceOverlayView, error) {
	view := &mygoVoiceOverlay{read: read, failed: failed, frame: voiceOverlayFrame{DB: -60}}
	view.window = mygo.NewWindow(mygo.WindowOptions{
		Title: "MiMac 语音状态", Width: voiceOverlaySlot, Height: voiceOverlayHeight,
		UseContentSize: true, Hidden: true, DisableResize: true, DisableShadow: true,
		Transparent: true, Panel: true, Frameless: true,
		Content: ui.View(view.draw),
	})
	view.window.SetIgnoreMouseEvents(true)
	renderer, err := siriglass.Open(view.window.NativeHandle())
	view.renderer = renderer
	if err != nil {
		return view, err
	}
	view.window.OnClose(func(event *mygo.CloseEvent) {
		view.visible = false
		if view.closed {
			return
		}
		if err := view.renderer.Close(); err != nil {
			event.PreventDefault()
			view.window.Hide()
			view.failed(err)
			return
		}
		view.closed = true
	})
	view.window.OnClosed(func() { view.visible = false })
	return view, nil
}

func (v *mygoVoiceOverlay) Update(frame voiceOverlayFrame) error {
	if err := v.render(frame); err != nil {
		return err
	}
	if frame.Visible {
		v.window.Invalidate()
	}
	return nil
}

func voiceOverlayBounds(area mygo.Rectangle) mygo.Rectangle {
	width, height := voiceOverlaySlot, voiceOverlayHeight
	return mygo.Rectangle{X: area.X + max(0, (area.Width-width)/2), Y: area.Y + max(0, area.Height-height-int(windowInset)), Width: width, Height: height}
}

func (v *mygoVoiceOverlay) render(frame voiceOverlayFrame) error {
	if v.closed || v.window == nil || v.window.IsDestroyed() || v.renderer == nil {
		return errors.New("voice overlay: view is closed")
	}
	if !frame.Visible {
		return v.Hide()
	}
	err := v.renderer.UpdateMeters(true, frame.DB, siriglass.MetersFromSpectrum(frame.Spectrum))
	v.trace.render(frame, err)
	if err != nil {
		return err
	}
	if !v.visible || v.frame.Token != frame.Token {
		area := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint()).WorkArea
		if area.Width > 0 && area.Height > 0 {
			v.window.SetBounds(voiceOverlayBounds(area))
		}
	}
	if !v.visible {
		v.window.ShowInactive()
	}
	if v.frame.Label != frame.Label {
		v.window.SetTitle(frame.Label)
	}
	v.visible = true
	v.frame = frame
	return nil
}

func (v *mygoVoiceOverlay) draw(c *ui.Context) {
	// Transparent NSWindow does not clear MyGo's theme-backed content root.
	c.Root().Background(ui.Transparent)
	if v.closed || !v.visible {
		return
	}
	if err := v.render(v.read()); err != nil {
		v.failed(err)
		return
	}
	if !v.visible {
		return
	}
	c.After(voiceOverlayFrameInterval)
}

func (v *mygoVoiceOverlay) Hide() error {
	wasVisible, token := v.visible, v.frame.Token
	v.visible = false
	v.frame = voiceOverlayFrame{DB: -60}
	if v.window != nil && !v.window.IsDestroyed() {
		v.window.Hide()
	}
	if !v.closed && v.renderer != nil {
		err := v.renderer.Update(false, -60)
		if wasVisible {
			v.trace.hidden(token, err)
		}
		return err
	}
	return nil
}

func (v *mygoVoiceOverlay) Close() error {
	if v.closed {
		return nil
	}
	v.visible = false
	if v.window != nil && !v.window.IsDestroyed() {
		v.window.Hide()
	}
	if v.renderer != nil {
		if err := v.renderer.Close(); err != nil {
			return err
		}
	}
	v.closed = true
	if v.window != nil && !v.window.IsDestroyed() {
		v.window.Close()
	}
	return nil
}
