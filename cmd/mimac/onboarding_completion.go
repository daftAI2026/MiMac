package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/voicetools"
)

const (
	onboardingCompletionIconSlot    float32 = 128
	onboardingCompletionWidth       float32 = 400
	onboardingCompletionBadge       float32 = onboardingCompletionIconSlot * .3125
	onboardingCompletionBadgeOffset float32 = onboardingCompletionIconSlot*.9859375 - onboardingCompletionBadge
)

type onboardingCompletionHint struct {
	label, value string
}

// These are read-only projections of the plugin's presentation metadata and
// the existing mappings. They never create bindings or synthesize defaults.
func (s *shell) onboardingCompletionHints() []onboardingCompletionHint {
	profile := s.activeProfile()
	var hints [3]onboardingCompletionHint
	n := 0
	for _, symbol := range [...]string{"voice", "confirm", "volume-up"} {
		for _, button := range profile.Buttons {
			if button.Symbol != symbol {
				continue
			}
			value := s.mappingLabel(profile.ID, button.ID)
			if value == "使用默认" {
				value = button.DefaultLabel
			}
			if button.ID == s.voiceButtonID() {
				value = "未选择语音工具"
				for _, tool := range s.tools {
					if tool.id == s.currentVoiceTool && tool.active {
						value = tool.name
						if tool.busy || tool.snapshot.State != voicetools.Ready {
							value += "（未就绪）"
						}
						break
					}
				}
			}
			if s.mappingLoadFailed {
				value = "无法读取设置"
			}
			hints[n] = onboardingCompletionHint{label: button.Label, value: value}
			n++
			break
		}
	}
	return hints[:n]
}

func (s *shell) onboardingCompletionView(c *ui.Context, a appearance) {
	windowWidth, _ := c.Size()
	width := min(windowWidth-2*onboardingInsetX, onboardingContentWidth)
	ui.Column(c).Fill().Background(a.theme.Background).Padding(20, onboardingInsetX, 24, onboardingInsetX).Children(func() {
		ui.Box(c).FillWidth().Height(28).Shrink(0).DragWindow()
		ui.Column(c).FillWidth().Grow(1).Basis(0).MinHeight(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Column(c).Width(width).AlignItems(ui.Center).Children(func() {
				s.onboardingCompletionArtwork(c, a)
				ui.Box(c).Height(onboardingHeadingGap).Shrink(0)
				onboardingTextHeader(c, a, width, onboardingTitleSize, onboardingStepHeading(onboardingStepCompletion), onboardingStepSubheading(onboardingStepCompletion))
				ui.Box(c).Height(onboardingContentGap).Shrink(0)
				if hints := s.onboardingCompletionHints(); len(hints) > 0 {
					ui.Column(c).Width(min(width, onboardingCompletionWidth)).Children(func() {
						settingsGroup(c, a, func() {
							for index, hint := range hints {
								if index > 0 {
									sectionLine(c, a)
								}
								ui.Row(c).FillWidth().MinWidth(0).MinHeight(44).Padding(spaceSM, spaceMD).AlignItems(ui.Center).Gap(spaceMD).Children(func() {
									ui.Text(c, hint.label).FontSize(onboardingBodySize).Shrink(0)
									ui.Text(c, hint.value).Grow(1).Basis(0).MinWidth(0).TextAlign(ui.End).FontSize(onboardingCaptionSize).TextColor(a.theme.TextMuted)
								})
							}
						})
					})
				}
				if s.onboarding.errorText != "" {
					ui.Text(c, s.onboarding.errorText).FillWidth().FontSize(onboardingCaptionSize).TextAlign(ui.Center).TextColor(a.theme.Danger).Role(ui.RoleStatus)
				}
				ui.Box(c).Height(onboardingActionsTop).Shrink(0)
				if s.onboardingPrimaryButton(c, a, "开始使用 "+mygo.App.Name(), false).Clicked() {
					s.finishOnboarding()
				}
				ui.Box(c).Height(onboardingWelcomeActionsGap).Shrink(0)
				if ui.ButtonBase(c).Key("onboarding-completion-back").Label("返回系统权限").Padding(0, spaceSM).Cursor(ui.CursorPointer).Children(func() {
					ui.Text(c, "返回系统权限").FontSize(onboardingCaptionSize).TextColor(a.theme.TextMuted)
				}).Clicked() {
					s.moveOnboarding(-1)
				}
			})
		})
	})
}

func (s *shell) onboardingCompletionArtwork(c *ui.Context, a appearance) {
	artwork := ui.Box(c).Key("onboarding-completion-artwork").Size(onboardingCompletionIconSlot, onboardingCompletionIconSlot)
	entrance, badge := onboardingCompletionMotion(c, artwork)
	artwork.Scale(1+.4*(1-entrance), 1+.4*(1-entrance)).Rotate(-12 * (1 - entrance)).Opacity(max(0, min(1, entrance)))
	artwork.Children(func() {
		if s.onboarding.completionIcon != nil {
			ui.Image(c, s.onboarding.completionIcon).Size(onboardingCompletionIconSlot, onboardingCompletionIconSlot).Fit(ui.Contain).Label(mygo.App.Name() + " Logo")
		} else if s.brandMark != nil {
			ui.Box(c).Fill().Center().Children(func() {
				ui.Icon(c, s.brandMark).Size(104, 104).TextColor(a.theme.Text).Label(mygo.App.Name() + " Logo")
			})
		}
		index := 0
		if a.theme.Dark {
			index = 1
		}
		color := s.grantedStyles[index].color
		if color.A == 0 {
			color = a.theme.Success
		}
		ui.Box(c).Absolute().Left(onboardingCompletionBadgeOffset).Top(onboardingCompletionBadgeOffset).Size(onboardingCompletionBadge, onboardingCompletionBadge).Radius(onboardingCompletionBadge/2).Background(color).Center().Scale(badge, badge).Opacity(max(0, min(1, badge))).Children(func() {
			key := symbolKey{name: "checkmark", dark: a.theme.Dark, size: 28, tint: ui.RGB(255, 255, 255)}
			if icon := s.navigationSymbols[key]; icon != nil {
				ui.Image(c, icon).Size(28, 28).Fit(ui.Contain).Role(ui.RoleNone)
			} else {
				ui.Text(c, "✓").FontSize(28).TextColor(ui.RGB(255, 255, 255)).Role(ui.RoleNone)
			}
		})
	})
}

// Resolve the large artwork through the current verified bundle. The smaller
// about-page bitmap is intentionally not enlarged for this surface.
func (s *shell) prepareOnboardingCompletionArtwork() {
	if s.onboarding.completionIcon != nil {
		return
	}
	bundle := s.permissionGuideAppBundlePath()
	if bundle == "" {
		return
	}
	data, err := mygo.Shell.FileIcon(bundle, int(onboardingCompletionIconSlot*2))
	if err != nil {
		return
	}
	if bitmap, err := ui.DecodeBitmap(data); err == nil {
		s.onboarding.completionIcon = bitmap
	}
}
