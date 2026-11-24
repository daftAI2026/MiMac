package main

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/dotscope"
)

const (
	voiceCaptionWidth       = 560
	voiceCaptionHeight      = 44
	voiceCaptionPillHeight  = 32
	voiceCaptionPillPadding = 14
	voiceCaptionPillRadius  = 16
	voiceCaptionGap         = 4
	voiceCaptionMargin      = 12
	voiceCaptionFade        = 150 * time.Millisecond
)

type voiceCaptionView interface {
	Update(stable, tentative string, bounds mygo.Rectangle, opacity float64) error
	Hide() error
	Close() error
}

type mygoVoiceCaptionView struct {
	window            *mygo.Window
	stable, tentative string
	visible, closed   bool
	opacity           float64
	bounds            mygo.Rectangle
}

func newVoiceCaptionView() (voiceCaptionView, error) {
	v := &mygoVoiceCaptionView{}
	v.window = mygo.NewWindow(mygo.WindowOptions{
		Title: "MiMac 实时识别文字", Width: voiceCaptionWidth, Height: voiceCaptionHeight,
		UseContentSize: true, Panel: true, PanelPointerOnly: true, IgnoreMouseEvents: true,
		Frameless: true, Transparent: true, Hidden: true, DisableResize: true, DisableShadow: true,
		Content: ui.View(v.draw),
	})
	v.window.OnClose(func(event *mygo.CloseEvent) {
		v.visible = false
		v.closed = true
	})
	v.window.OnClosed(func() { v.visible = false })
	return v, nil
}

func voiceCaptionBoundsForHUD(dot, screen mygo.Rectangle, bottom bool) mygo.Rectangle {
	width := voiceCaptionWidth
	if screen.Width > 0 {
		width = min(width, max(0, screen.Width-2*voiceCaptionMargin))
	}
	x := dot.X + (dot.Width-width)/2
	if screen.Width > 0 {
		x = max(screen.X+voiceCaptionMargin, min(x, screen.X+screen.Width-width-voiceCaptionMargin))
	}
	// The shader centers its 26 pt plate inside the frozen dot panel; the
	// visible top is offset by voiceDotPlateTopOffset. Upstream hangs the
	// caption on the screen-edge side of the plate with a 4 pt gap.
	envelopeInset := (voiceCaptionHeight - voiceCaptionPillHeight) / 2
	plateTop := dot.Y + voiceDotPlateTopOffset(bottom)
	y := plateTop + int(dotscope.PlateHeight) + voiceCaptionGap - envelopeInset
	if bottom {
		y = plateTop - voiceCaptionGap - voiceCaptionPillHeight - envelopeInset
	}
	return mygo.Rectangle{X: x, Y: y, Width: width, Height: voiceCaptionHeight}
}

func (v *mygoVoiceCaptionView) Update(stable, tentative string, bounds mygo.Rectangle, opacity float64) error {
	if v == nil || v.closed || v.window == nil || v.window.IsDestroyed() {
		return errors.New("voice caption: view is closed")
	}
	if strings.TrimSpace(stable) == "" && strings.TrimSpace(tentative) == "" || opacity <= 0 {
		return v.Hide()
	}
	if opacity > 1 {
		opacity = 1
	}
	if !v.visible || v.bounds != bounds {
		v.bounds = bounds
		v.window.SetBounds(bounds)
	}
	if v.stable != stable || v.tentative != tentative {
		v.stable, v.tentative = stable, tentative
		v.window.Invalidate()
	}
	if v.opacity != opacity {
		v.opacity = opacity
		v.window.SetOpacity(opacity)
	}
	if !v.visible {
		v.visible = true
		v.window.ShowInactive()
	}
	return nil
}

func (v *mygoVoiceCaptionView) draw(c *ui.Context) {
	if v.closed || !v.visible || strings.TrimSpace(v.stable) == "" && strings.TrimSpace(v.tentative) == "" {
		return
	}
	theme := c.Theme()
	ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Height(voiceCaptionPillHeight).MaxWidth(voiceCaptionWidth-8).
			Padding(0, voiceCaptionPillPadding).Radius(voiceCaptionPillRadius).
			Background(theme.Surface).Border(0.5, theme.Border).
			Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.16)).Children(func() {
			rtl := voiceCaptionIsRTL(v.stable, v.tentative)
			line := ui.Row(c).MinWidth(0).Shrink(1)
			if rtl {
				line = line.Direction(ui.RightToLeft)
			}
			line.Children(func() {
				if rtl {
					if strings.TrimSpace(v.tentative) != "" {
						tentative := strings.TrimSpace(v.tentative)
						words := ui.Text(c, tentative).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0).
							TextColor(theme.TextMuted).FontSize(13)
						if strings.TrimSpace(v.stable) != "" && hasLeadingSpace(v.tentative) {
							words = words.Margin(0, 4, 0, 0)
						}
						words.Role(ui.RoleStatus)
					}
					if strings.TrimSpace(v.stable) != "" {
						ui.Text(c, strings.TrimSpace(v.stable)).SingleLine().TruncateStart().Shrink(1).MinWidth(0).
							TextColor(theme.Text).FontSize(13).Role(ui.RoleStatus)
					}
					return
				}
				if strings.TrimSpace(v.stable) != "" {
					ui.Text(c, strings.TrimSpace(v.stable)).SingleLine().TruncateStart().Shrink(1).MinWidth(0).
						TextColor(theme.Text).FontSize(13).Role(ui.RoleStatus)
				}
				if strings.TrimSpace(v.tentative) != "" {
					ui.Text(c, v.tentative).SingleLine().PreserveSpaces().Ellipsis("…").Shrink(1).MinWidth(0).
						TextColor(theme.TextMuted).FontSize(13).Role(ui.RoleStatus)
				}
			})
		})
	})
}

func voiceCaptionIsRTL(stable, tentative string) bool {
	for _, value := range []string{stable, tentative} {
		for _, r := range value {
			if !unicode.IsLetter(r) {
				continue
			}
			return r >= '\u0590' && r <= '\u08ff' || r >= '\ufb1d' && r <= '\ufdff' || r >= '\ufe70' && r <= '\ufeff'
		}
	}
	return false
}

func hasLeadingSpace(text string) bool {
	if text == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text)
	return unicode.IsSpace(r)
}

func (v *mygoVoiceCaptionView) Hide() error {
	if v == nil || v.closed {
		return nil
	}
	v.visible = false
	v.stable, v.tentative = "", ""
	v.opacity = 0
	if v.window != nil && !v.window.IsDestroyed() {
		v.window.Hide()
	}
	return nil
}

func (v *mygoVoiceCaptionView) Close() error {
	if v == nil || v.closed {
		return nil
	}
	v.visible = false
	v.closed = true
	if v.window != nil && !v.window.IsDestroyed() {
		v.window.Hide()
		v.window.Close()
	}
	return nil
}
