package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	textCaption          float32 = 12
	textBody             float32 = 13
	textTitle            float32 = 16
	lineWidth            float32 = 0.5
	modelSelectionLine   float32 = 1
	windowInset          float32 = 24
	settingsPaneInset    float32 = 20
	settingsPaneTop      float32 = 20
	settingsRowInset     float32 = 10
	settingsRowHeight    float32 = 38
	settingsRowPadding   float32 = 7
	settingsGroupRadius  float32 = 12
	settingsIconSize     float32 = 20
	accessoryIconSize    float32 = 12
	toolbarInset         float32 = 8
	scrollGutter         float32 = 12
	spaceXS              float32 = 4
	spaceSM              float32 = 8
	spaceMD              float32 = 12
	spaceLG              float32 = 16
	sectionGap           float32 = 20
	controlInset         float32 = 3
	controlHeight        float32 = 24
	controlRadius        float32 = 5
	sidebarWidth         float32 = 184
	sidebarHeaderHeight  float32 = 64
	contentHeaderHeight  float32 = 52
	navigationHeight     float32 = 36
	navigationWidth      float32 = 76
	labelColumnWidth     float32 = 104
	mappingChoiceWidth   float32 = 128
	mappingDefaultWidth  float32 = 80
	mappingMenuWidth     float32 = 24
	mappingListRatio     float32 = 1.6
	mappingRowGap        float32 = 2
	mappingReferenceRows         = 13
	modelMaxWidth        float32 = 120
	modelMaxHeight       float32 = 400
	windowWidth                  = 900
	windowHeight                 = 660
	windowMinWidth               = windowWidth
	windowMinHeight              = windowHeight
	trafficLightInset            = 20
)

func mappingReferenceFrameHeight(captionSize, rowHeight float32) float32 {
	headerHeight := mappingHeaderHeight(captionSize)
	rowsHeight := float32(mappingReferenceRows)*rowHeight + float32(mappingReferenceRows-1)*mappingRowGap
	return headerHeight + spaceSM + rowsHeight
}

func mappingRowHeight(c *ui.Context, a appearance) float32 {
	_, height := c.MeasureText(0, ui.Span{Text: "语音 Ag", Font: a.theme.Font, Size: a.fonts.body})
	buttonHeight := max(controlHeight, float32(math.Ceil(float64(height+2*controlInset+2*lineWidth))))
	return max(settingsRowHeight, buttonHeight+2*settingsRowPadding)
}

func mappingHeaderHeight(captionSize float32) float32 {
	return captionSize + spaceXS
}

// Selection feedback is visual only. MyGo retargets interrupted transitions,
// stops requesting frames when settled, and honors macOS Reduce Motion.
var selectionMotion = ui.Animation{Duration: 250 * time.Millisecond, Ease: ui.EaseInOut}

// Read once when the host starts. Native and MyGo content share this baseline.
var systemTextSize = textBody

// Device artwork keeps its own lighting in both app appearances.
var modelHighlight = ui.Hex("#ffffff")

type statusTone uint8

const (
	toneNeutral statusTone = iota
	toneActive
	toneWarning
	toneError
)

// The palette extends MiMac's existing neutral light/dark appearance. Roles,
// rather than individual views, own colors. State colors accompany text.
type appearance struct {
	fonts     fontSizes
	theme     ui.Theme
	separator ui.Color
	disabled  ui.Color
}

type fontSizes struct{ caption, body, title float32 }

func textStyles(size int) fontSizes {
	base := max(12, systemTextSize)
	switch size {
	case -1:
		return fontSizes{max(12, base-1), max(12, base-1), base + 2}
	case 1:
		return fontSizes{base + 1, base + 2, base + 5}
	default:
		return fontSizes{max(12, base-1), base, base + 3}
	}
}

func appAppearance(dark bool, textSize int) appearance {
	t := *ui.LightTheme()
	a := appearance{separator: ui.Hex("#e4e4e7"), disabled: ui.Hex("#71717a")}
	t.Background, t.Text, t.TextMuted = ui.Hex("#ffffff"), ui.Hex("#18181b"), ui.Hex("#71717a")
	t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#f4f4f5"), ui.Hex("#e9e9ec"), ui.Hex("#dddde1")
	t.Border = ui.Hex("#d9d9de")
	t.Accent, t.AccentHover, t.AccentPressed = t.Text, ui.Hex("#3f3f46"), ui.Hex("#09090b")
	t.AccentText = ui.Hex("#ffffff")
	t.Success, t.Warning, t.Danger = t.Text, t.Text, t.Text
	t.Selection, t.Focus = t.SurfacePressed, t.Text
	t.Inverse, t.InverseText, t.Scrollbar = t.Text, t.Background, ui.Hex("#a1a1aa")
	if dark {
		t.Dark = true
		t.Background, t.Text, t.TextMuted = ui.Hex("#18181b"), ui.Hex("#f4f4f5"), ui.Hex("#a1a1aa")
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#27272a"), ui.Hex("#323236"), ui.Hex("#3c3c41")
		t.Border = ui.Hex("#52525b")
		t.Accent, t.AccentHover, t.AccentPressed = t.Text, ui.Hex("#e4e4e7"), ui.Hex("#d4d4d8")
		t.AccentText = t.Background
		t.Success, t.Warning, t.Danger = t.Text, t.Text, t.Text
		t.Selection, t.Focus = t.SurfacePressed, t.Text
		t.Inverse, t.InverseText, t.Scrollbar = t.Text, t.Background, ui.Hex("#71717a")
		a.separator, a.disabled = ui.Hex("#3f3f46"), ui.Hex("#a1a1aa")
	}
	a.fonts = textStyles(textSize)
	t.FontSize, t.Radius, t.Spacing = a.fonts.body, controlRadius, spaceXS
	a.theme = t
	return a
}

func (a appearance) statusColor(tone statusTone) ui.Color {
	switch tone {
	case toneActive:
		return a.theme.Success
	case toneWarning:
		return a.theme.Warning
	case toneError:
		return a.theme.Danger
	default:
		return a.theme.TextMuted
	}
}

// ButtonBase keeps keyboard/AX behavior without the framework's wider focus
// stroke. Focus, hover and press use immediate color changes, with no motion.
func appButton(c *ui.Context, a appearance, label string, disabled bool) ui.Element {
	b := styleAppButton(ui.ButtonBase(c), a, disabled)
	b.Children(func() { ui.Text(c, label).SingleLine().FontSize(a.fonts.body) })
	return b
}

func appMenuButton(c *ui.Context, a appearance, label string, disabled bool, indicator *ui.Bitmap, build func(*ui.Menu)) ui.Element {
	b := styleAppButton(ui.ButtonBase(c), a, disabled).Padding(controlInset, spaceSM).Justify(ui.Center)
	if indicator != nil {
		b.MinWidth(appMenuButtonWidth(c, a, label))
	}
	b.Children(func() {
		text := ui.Text(c, label).SingleLine().MinWidth(0).Shrink(1).FillWidth().
			TextAlign(ui.Center).FontSize(a.fonts.body)
		if indicator != nil {
			// Reserve the same space on both sides so the label stays centered
			// while the native indicator keeps its own hit-independent slot.
			text.Margin(0, accessoryIconSize+spaceSM)
		}
	})
	if indicator != nil {
		b.Children(func() {
			ui.Box(c).Absolute().Right(spaceSM).Top(0).Bottom(0).Width(accessoryIconSize).
				AlignItems(ui.Center).Justify(ui.Center).PassThrough().Children(func() {
				ui.Image(c, indicator).Width(accessoryIconSize).Height(accessoryIconSize).Fit(ui.Contain)
			})
		})
	}
	return b.Menu(build)
}

// Measure through MyGo's cached text layout, with the same font as the label.
// The symmetric reserve keeps the label centered and the indicator separated.
func appMenuButtonWidth(c *ui.Context, a appearance, label string) float32 {
	width, _ := c.MeasureText(0, ui.Span{Text: label, Font: a.theme.Font, Size: a.fonts.body})
	width += 2 * (spaceSM + accessoryIconSize + spaceSM + lineWidth)
	return float32(math.Ceil(float64(width/spaceXS))) * spaceXS
}

func styleAppButton(b ui.Element, a appearance, disabled bool) ui.Element {
	t := a.theme
	b.Disabled(disabled).FocusRing(false).
		Padding(controlInset, spaceMD).MinHeight(controlHeight).Radius(t.Radius).
		Background(t.Surface).Border(lineWidth, t.Border)
	if !disabled {
		if b.Pressed() {
			b.Background(t.SurfacePressed)
		} else if b.Hovered() {
			b.Background(t.SurfaceHover)
		}
		if b.FocusVisible() {
			b.BorderColor(t.Focus)
		}
	}
	textColor := t.Text
	if disabled {
		textColor = a.disabled
	}
	return b.TextColor(textColor)
}

func sectionLine(c *ui.Context, a appearance) {
	ui.Box(c).Height(lineWidth).FillWidth().Background(a.separator).Shrink(0)
}

func settingsGroup(c *ui.Context, a appearance, content func()) ui.Element {
	return ui.Column(c).FillWidth().MinWidth(0).Padding(0, settingsRowInset).
		Background(a.theme.Surface).Radius(settingsGroupRadius).Children(content)
}
