package main

import (
	"bytes"
	"fmt"
	"image/png"
	"math"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	brand "mimac.local/mimac/Assets/Brand"
	"mimac.local/mimac/internal/voicetools"
)

const (
	onboardingTitleSize           float32 = 26
	onboardingWelcomeTitleSize    float32 = 34
	onboardingBodySize            float32 = 15
	onboardingCaptionSize         float32 = 13
	onboardingContentWidth        float32 = 560
	onboardingWelcomeIconSize     float32 = 160
	onboardingWelcomeIconSlotSize float32 = 192
	onboardingStepIconSize        float32 = 64
	onboardingActionSize          float32 = 16
	onboardingLineHeight          float32 = 20
	onboardingMarkOffsetY         float32 = 4
	onboardingWelcomeActionsGap   float32 = 22
	onboardingInsetX              float32 = 20
	onboardingHeadingGap          float32 = 12
	onboardingContentGap          float32 = 20
	onboardingActionsTop          float32 = 24
	onboardingPermissionTileWidth float32 = 240
	onboardingPermissionIconSize  float32 = 28
)

const onboardingWelcomeDescription = "花一点时间，连接遥控器、选择语音工具，并设置所需权限。"

// Balance the paragraph using the host's own text measurements. Keep its
// original height while finding the narrowest width, without storing a second
// text-layout cache or inserting presentation-specific line breaks.
func onboardingBalancedTextWidth(c *ui.Context, text string, size float32, weight int, available float32) float32 {
	span := ui.Span{Text: text, Size: size, Weight: weight}
	upper, height := c.MeasureText(available, span)
	if upper <= 0 {
		return available
	}
	lower := float32(0)
	for upper-lower > 1 {
		middle := (lower + upper) / 2
		_, candidateHeight := c.MeasureText(middle, span)
		if candidateHeight <= height+.5 {
			upper = middle
		} else {
			lower = middle
		}
	}
	return float32(math.Ceil(float64(upper)))
}

func (s *shell) onboardingView(c *ui.Context) {
	a := appAppearance(c.Theme().Dark, s.preferencesState.value.TextSize)
	a.fonts = fontSizes{caption: onboardingCaptionSize, body: onboardingBodySize, title: onboardingTitleSize}
	c.SetTheme(&a.theme)
	if s.onboarding.step == onboardingStepWelcome {
		if s.brandMark == nil {
			s.brandMark = ui.MustParseSVG([]byte(brand.SVG))
		}
		s.onboardingWelcomeView(c, a)
		return
	}
	if s.onboarding.step == onboardingStepCompletion {
		if s.onboarding.completionIcon == nil && s.brandMark == nil {
			s.brandMark = ui.MustParseSVG([]byte(brand.SVG))
		}
		s.onboardingCompletionView(c, a)
		return
	}
	if s.appIcon == nil {
		if img, err := png.Decode(bytes.NewReader(brand.AboutIconPNG)); err == nil {
			s.appIcon = ui.NewBitmap(img)
		}
	}
	windowWidth, _ := c.Size()
	availableWidth := min(windowWidth-2*onboardingInsetX, onboardingContentWidth)
	ui.Column(c).Fill().Background(a.theme.Background).Padding(20, onboardingInsetX, 24, onboardingInsetX).Children(func() {
		ui.Box(c).FillWidth().Height(28).Shrink(0).DragWindow()
		ui.Column(c).FillWidth().Grow(1).Basis(0).MinHeight(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Column(c).Width(availableWidth).MinWidth(0).Children(func() {
				s.onboardingPageHeader(c, a, availableWidth, onboardingStepHeading(s.onboarding.step), onboardingStepSubheading(s.onboarding.step))
				ui.Box(c).Height(onboardingContentGap).Shrink(0)
				switch s.onboarding.step {
				case onboardingStepVoice:
					s.onboardingVoiceStep(c, a)
				case onboardingStepPermissions:
					s.onboardingPermissionsStep(c, a)
				default:
					s.onboardingDeviceStep(c, a)
				}
				if s.onboarding.errorText != "" {
					ui.Text(c, s.onboarding.errorText).FillWidth().FontSize(onboardingCaptionSize).TextAlign(ui.Center).TextColor(a.theme.Danger).Role(ui.RoleStatus)
				}
				ui.Box(c).Height(onboardingActionsTop).Shrink(0)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(spaceSM).Children(func() {
					if s.onboarding.step > onboardingStepWelcome {
						if s.onboardingBackButton(c, a).Clicked() {
							s.moveOnboarding(-1)
						}
					}
					label := "继续"
					if s.onboardingPrimaryButton(c, a, label, true).Clicked() {
						s.moveOnboarding(1)
					}
				})
			})
		})
		ui.Text(c, onboardingStepPosition(s.onboarding.step)).FillWidth().FontSize(onboardingCaptionSize).TextAlign(ui.Center).TextColor(a.theme.TextMuted)
	})
}

func onboardingTextHeader(c *ui.Context, a appearance, width, titleSize float32, heading, description string) {
	headingWidth := onboardingBalancedTextWidth(c, heading, titleSize, 700, width)
	bodyWidth := onboardingBalancedTextWidth(c, description, onboardingBodySize, 400, width)
	ui.Column(c).Width(width).AlignItems(ui.Center).Gap(onboardingHeadingGap).Children(func() {
		ui.Text(c, heading).Width(headingWidth).FontSize(titleSize).FontWeight(700).LetterSpacing(0).TextAlign(ui.Center).Role(ui.RoleHeading).Level(1)
		ui.Text(c, description).Width(bodyWidth).FontSize(onboardingBodySize).FontWeight(400).FixedLineHeight(onboardingLineHeight).LetterSpacing(0).TextAlign(ui.Center).TextColor(a.theme.TextMuted)
	})
}

func (s *shell) onboardingPageHeader(c *ui.Context, a appearance, width float32, heading, description string) {
	titleSize := onboardingTitleSize
	if s.onboarding.step == onboardingStepWelcome {
		titleSize = onboardingWelcomeTitleSize
	}
	ui.Column(c).Width(width).AlignItems(ui.Center).Children(func() {
		if s.onboarding.step == onboardingStepWelcome {
			ui.Column(c).Size(onboardingWelcomeIconSlotSize, onboardingWelcomeIconSlotSize).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				// The mark occupies 224 units of its 256-unit source viewBox.
				// Keep its visible width independent of the surrounding artwork slot.
				ui.Icon(c, s.brandMark).Size(onboardingWelcomeIconSize, onboardingWelcomeIconSize).
					Scale(256.0/224.0, 256.0/224.0).Translate(0, onboardingMarkOffsetY).TextColor(a.theme.Text).Label(mygo.App.Name() + " Logo")
			})
			ui.Box(c).Height(sectionGap).Shrink(0)
		} else {
			if s.appIcon != nil {
				ui.Image(c, s.appIcon).Size(onboardingStepIconSize, onboardingStepIconSize).Fit(ui.Contain).Label(mygo.App.Name() + " Logo").
					DrawOver(func(p *ui.Painter, r ui.Rect) {
						p.Stroke(r, a.theme.Border, r.W/4, lineWidth)
					})
			}
			ui.Box(c).Height(onboardingHeadingGap).Shrink(0)
		}
		onboardingTextHeader(c, a, width, titleSize, heading, description)
	})
}

// Welcome is an introduction, outside the three numbered setup steps.
func (s *shell) onboardingWelcomeView(c *ui.Context, a appearance) {
	windowWidth, _ := c.Size()
	availableWidth := min(windowWidth-2*onboardingInsetX, onboardingContentWidth)
	ui.Column(c).Fill().Background(a.theme.Background).Padding(20, onboardingInsetX, 24, onboardingInsetX).Children(func() {
		ui.Box(c).FillWidth().Height(28).Shrink(0).DragWindow()
		ui.Column(c).FillWidth().Grow(1).Basis(0).MinHeight(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Column(c).Width(availableWidth).AlignItems(ui.Center).Children(func() {
				s.onboardingPageHeader(c, a, availableWidth, onboardingStepHeading(onboardingStepWelcome), onboardingWelcomeDescription)
				ui.Box(c).Height(onboardingActionsTop).Shrink(0)
				if s.onboardingPrimaryButton(c, a, "继续", true).Clicked() {
					s.moveOnboarding(1)
				}
				ui.Box(c).Height(onboardingWelcomeActionsGap).Shrink(0)
				if ui.ButtonBase(c).Key("onboarding-skip").Label("稍后设置").Padding(0, spaceSM).Cursor(ui.CursorPointer).Children(func() {
					ui.Text(c, "稍后设置").FontSize(onboardingCaptionSize).FontWeight(400).LetterSpacing(0).TextColor(a.theme.TextMuted)
				}).Clicked() {
					s.finishOnboarding()
				}
				if s.onboarding.errorText != "" {
					ui.Text(c, s.onboarding.errorText).FillWidth().FontSize(onboardingCaptionSize).TextAlign(ui.Center).TextColor(a.theme.Danger).Role(ui.RoleStatus)
				}
			})
		})
	})
}

func (s *shell) onboardingBackButton(c *ui.Context, a appearance) ui.Element {
	button := styleAppButton(ui.ButtonBase(c).Key("onboarding-previous").Label("上一步"), a, false).
		Width(navigationHeight).Height(navigationHeight).Padding(0).
		Radius(navigationHeight / 2).Justify(ui.Center).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	icon := s.symbolBitmap(symbolKey{name: "arrow.left", dark: a.theme.Dark, size: settingsIconSize, tint: a.theme.Text})
	button.Children(func() {
		if icon != nil {
			ui.Image(c, icon).Width(settingsIconSize).Height(settingsIconSize).Fit(ui.Contain)
		}
	})
	return button
}

func (s *shell) onboardingPrimaryButton(c *ui.Context, a appearance, label string, showArrow bool) ui.Element {
	button := ui.ButtonBase(c).Key("onboarding-next").Label(label).FocusRing(false).
		Cursor(ui.CursorPointer).FontWeight(600).Height(navigationHeight).Padding(0, spaceMD).
		Radius(navigationHeight/2).Background(a.theme.Text).
		Border(lineWidth, a.theme.Text).TextColor(a.theme.Background)
	if button.Pressed() {
		button.Background(a.theme.Text.Alpha(.8))
	} else if button.Hovered() {
		button.Background(a.theme.Text.Alpha(.9))
	}
	if button.FocusVisible() {
		button.BorderColor(a.theme.Background)
	}
	var icon *ui.Bitmap
	if showArrow {
		icon = s.symbolBitmap(symbolKey{name: "arrow.right", dark: a.theme.Dark, size: settingsIconSize, tint: a.theme.Background})
	}
	button.Children(func() {
		// Native symbol side bearings make the painted text/arrow group slightly
		// left-heavy despite its centered layout box. Correct only this group.
		content := ui.Row(c).AlignItems(ui.Center).Gap(spaceSM)
		if showArrow {
			content.Translate(1, 0)
		}
		content.Children(func() {
			ui.Text(c, label).SingleLine().FontSize(onboardingActionSize).TextColor(a.theme.Background)
			if showArrow {
				if icon != nil {
					ui.Image(c, icon).Width(settingsIconSize).Height(settingsIconSize).Fit(ui.Contain)
				} else {
					ui.Box(c).Width(settingsIconSize).Height(settingsIconSize).Shrink(0)
				}
			}
		})
	})
	return button
}

func onboardingStepHeading(step onboardingStep) string {
	switch step {
	case onboardingStepWelcome:
		return "欢迎使用 " + mygo.App.Name()
	case onboardingStepVoice:
		return "准备语音工具"
	case onboardingStepPermissions:
		return "系统权限"
	case onboardingStepCompletion:
		return "设置完成"
	default:
		return "连接遥控器"
	}
}
func onboardingStepSubheading(step onboardingStep) string {
	switch step {
	case onboardingStepVoice:
		return "选择已有输入法，或使用本机语音识别。"
	case onboardingStepPermissions:
		return "允许 MiMac 读取遥控按键并执行您的映射。"
	case onboardingStepCompletion:
		return "几项常用按键，来自您的当前配置。"
	default:
		return "在蓝牙设置中连接遥控器，已连接的设备无需重新配对。"
	}
}
func onboardingStepPosition(step onboardingStep) string {
	switch step {
	case onboardingStepWelcome, onboardingStepCompletion:
		return ""
	case onboardingStepVoice:
		return "2 / 3"
	case onboardingStepPermissions:
		return "3 / 3"
	default:
		return "1 / 3"
	}
}

func (s *shell) onboardingDeviceStep(c *ui.Context, a appearance) {
	profile := s.activeProfile()
	ui.Column(c).FillWidth().Gap(spaceMD).Children(func() {
		onboardingGroup(c, a, func() {
			ui.Row(c).FillWidth().MinHeight(64).Padding(spaceSM, spaceMD).AlignItems(ui.Center).Gap(spaceMD).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(spaceXS).Children(func() {
					ui.Text(c, profile.Name).FontSize(onboardingBodySize).FontWeight(600)
					status := s.deviceConnectionLabel()
					if s.deviceReadBusy {
						status = "正在检测"
					}
					ui.Text(c, status).FontSize(onboardingCaptionSize).TextColor(a.theme.TextMuted).Role(ui.RoleStatus)
				})
				if appButton(c, a, "蓝牙设置…", false).Clicked() {
					s.performSetupAction(setupBluetoothConnection)
				}
				if s.canRetryDeviceDetection() && appButton(c, a, "重试", false).Clicked() {
					s.detectDevice()
				}
			})
			sectionLine(c, a)
			guide := profile.ConnectionGuide
			if guide.FirstPairing == "" {
				guide.FirstPairing = "让遥控器进入配对模式，在蓝牙设置中选择它。"
			}
			if guide.Reconnect == "" {
				guide.Reconnect = "唤醒遥控器，在蓝牙设置中确认已连接。"
			}
			ui.Row(c).FillWidth().Gap(0).Children(func() {
				s.onboardingInfoCard(c, a, "首次连接", guide.FirstPairing)
				ui.Box(c).Width(lineWidth).FillHeight().Shrink(0).Background(a.separator)
				s.onboardingInfoCard(c, a, "已经配对", guide.Reconnect)
			})
		})
	})
}
func (s *shell) onboardingInfoCard(c *ui.Context, a appearance, title, description string) {
	ui.Column(c).Grow(1).Basis(0).MinWidth(0).Padding(spaceMD).Gap(spaceSM).Children(func() {
		ui.Text(c, title).FontSize(onboardingBodySize).FontWeight(600)
		ui.Text(c, description).FillWidth().FontSize(onboardingCaptionSize).TextColor(a.theme.TextMuted)
	})
}
func (s *shell) onboardingVoiceStep(c *ui.Context, a appearance) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(spaceSM).Children(func() {
		if s.toolLoadFailed {
			ui.Text(c, "无法读取工具设置，请在插件中重试。").FontSize(onboardingBodySize).TextColor(a.theme.TextMuted).Role(ui.RoleStatus)
		} else {
			for _, tool := range s.tools {
				if s.voicePluginVisible(tool) && tool.releaseReady {
					s.onboardingVoiceToolRow(c, a, tool)
				}
			}
		}
		if s.toolLoadFailed {
			s.onboardingPluginSettingsButton(c, a)
		}
	})
}
func (s *shell) onboardingVoiceToolRow(c *ui.Context, a appearance, tool *voiceTool) {
	selected := tool.id == s.currentVoiceTool
	status, canSelect, installState := s.onboardingVoiceToolStatus(tool)
	onboardingGroup(c, a, func() {
		ui.Row(c).Key("onboarding-tool/"+tool.id).FillWidth().MinWidth(0).MinHeight(58).Padding(spaceSM, spaceMD).AlignItems(ui.Center).Gap(spaceMD).Children(func() {
			s.voiceToolArtwork(c, a, tool)
			ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(spaceXS).Children(func() {
				ui.Text(c, tool.name).FontSize(onboardingBodySize).FontWeight(600)
				ui.Text(c, status).FillWidth().FontSize(onboardingCaptionSize).TextColor(a.theme.TextMuted).Role(ui.RoleStatus)
			})
			if s.onboardingVoiceToolNeedsRetry(tool) {
				if appButton(c, a, "重试", false).Clicked() {
					if tool.active {
						s.refreshVoiceTool(tool)
					} else {
						s.refreshOnboardingVoiceLocations()
					}
				}
				return
			}
			if s.onboardingVoiceToolNeedsSettings(tool) {
				s.onboardingPluginSettingsButton(c, a)
				return
			}
			if tool.prepareAssets != nil && tool.active {
				if tool.preparingAssets && tool.busy {
					label := "取消准备"
					if tool.cancelRequested {
						label = "正在取消"
					}
					if appButton(c, a, label, tool.cancelRequested).Clicked() && !tool.cancelRequested {
						tool.requestCancel()
						s.invalidateOnboardingWindow()
					}
				} else if !tool.busy && tool.snapshot.State == voicetools.ConfigMissing {
					if appButton(c, a, "下载中文模型", false).Clicked() {
						s.refreshVoiceToolAssetsUsing(tool, tool.prepareAssets)
						s.invalidateOnboardingWindow()
					}
				}
			}
			if !tool.active && installState == voicetools.NotFound && tool.installURL != "" {
				if appButton(c, a, "下载安装…", false).Clicked() {
					if err := mygo.Shell.OpenExternal(tool.installURL); err != nil {
						s.onboarding.errorText = "无法打开下载页面，请重试。"
						s.invalidateOnboardingWindow()
					}
				}
			} else {
				label := "启用并使用"
				if tool.active {
					label = "使用"
				}
				if selected {
					label = "当前工具"
				}
				disabled := !canSelect || tool.busy || s.toolLoadFailed || selected
				if appButton(c, a, label, disabled).Clicked() && !disabled {
					s.chooseOnboardingVoiceTool(tool)
				}
			}
		})
	})
}
func (s *shell) onboardingVoiceToolStatus(tool *voiceTool) (string, bool, voicetools.State) {
	if tool.prepareAssets != nil && tool.locateApplication == nil {
		if !tool.active {
			return "本机识别，可按需下载模型", true, voicetools.Ready
		}
		return voiceToolStatus(tool), !tool.busy && tool.snapshot.State == voicetools.Ready, voicetools.Ready
	}
	if tool.active {
		return voiceToolStatus(tool), !tool.busy && tool.snapshot.State == voicetools.Ready, tool.snapshot.State
	}
	state, checked := s.onboarding.toolLocations[tool.id]
	if !checked {
		return "正在检测…", false, ""
	}
	if s.onboarding.toolLocationErrors[tool.id] {
		return "检测失败，请重试", false, state
	}
	switch state {
	case voicetools.Ready:
		return "已安装", true, state
	case voicetools.NotFound:
		return "未安装", false, state
	case voicetools.Ambiguous:
		return "存在多个副本，请在插件中选择", false, state
	case voicetools.Unsupported:
		return "暂不支持此版本", false, state
	default:
		return "无法读取应用信息", false, state
	}
}
func (s *shell) chooseOnboardingVoiceTool(tool *voiceTool) {
	if s.closing || s.onboarding.window == nil || s.onboarding.window.IsDestroyed() || s.toolLoadFailed || !slicesContainsVoiceTool(s.tools, tool) || !s.voicePluginVisible(tool) || !tool.releaseReady {
		return
	}
	s.onboarding.errorText = ""
	if !tool.active {
		if tool.locateApplication != nil && s.onboarding.toolLocations[tool.id] != voicetools.Ready {
			return
		}
		if err := s.setVoiceToolEnabled(tool, true); err != nil {
			s.onboarding.errorText = "无法启用工具，请重试。"
			s.invalidateOnboardingWindow()
			return
		}
	}
	if err := s.selectVoiceTool(tool); err != nil {
		s.onboarding.errorText = s.toolError
		if s.onboarding.errorText == "" {
			s.onboarding.errorText = "无法选择工具，请重试。"
		}
	}
	s.invalidateOnboardingWindow()
}
func slicesContainsVoiceTool(tools []*voiceTool, target *voiceTool) bool {
	for _, tool := range tools {
		if tool == target {
			return true
		}
	}
	return false
}
func (s *shell) onboardingPermissionsStep(c *ui.Context, a appearance) {
	summary := summarizeOnboardingPermissions(s.onboarding.permissionSnapshot.rows)
	if summary.total == 0 {
		ui.Text(c, "当前无需额外权限").FillWidth().FontSize(onboardingBodySize).TextAlign(ui.Center).TextColor(a.theme.TextMuted)
		return
	}
	ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
		ui.Column(c).Width(onboardingPermissionTileWidth).MinWidth(0).Role(ui.RoleGroup).Label("系统权限摘要").Children(func() {
			onboardingGroup(c, a, func() {
				ui.Column(c).FillWidth().Padding(24, 10).AlignItems(ui.Center).Children(func() {
					s.onboardingPermissionsIcon(c, a, summary.allGranted())
					ui.Box(c).Height(spaceSM).Shrink(0)
					ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(2).Children(func() {
						ui.Text(c, "系统权限").FillWidth().FontSize(onboardingBodySize).FontWeight(600).TextAlign(ui.Center)
						ui.Box(c).FillWidth().Height(24).Center().Children(func() {
							ui.Row(c).Height(18).Padding(0, 7).AlignItems(ui.Center).Radius(9).Background(a.theme.Surface).Border(lineWidth, a.theme.Border).Children(func() {
								ui.Text(c, summary.label()).SingleLine().FontSize(12).FontWeight(600).TextColor(a.theme.TextMuted).Role(ui.RoleStatus)
							})
						})
					})
					ui.Box(c).Height(spaceSM).Shrink(0)
					description := "授予系统权限，获得流畅的 " + mygo.App.Name() + " 使用体验。"
					width := onboardingBalancedTextWidth(c, description, onboardingCaptionSize, 400, onboardingPermissionTileWidth-40)
					ui.Text(c, description).Width(width).FontSize(onboardingCaptionSize).TextAlign(ui.Center).TextColor(a.theme.TextMuted)
					ui.Box(c).Height(24).Shrink(0)
					ui.Box(c).FillWidth().Height(controlHeight).Center().Children(func() {
						if summary.allGranted() {
							s.permissionGuideGrantedBadge(c, a)
						} else {
							button := styleAppButton(ui.ButtonBase(c).Label("授权…"), a, false).Radius(controlHeight / 2)
							button.Children(func() { ui.Text(c, "授权…").FontSize(onboardingCaptionSize) })
							if button.Clicked() {
								s.showOnboardingPermissionGuide()
							}
						}
					})
				})
			})
		})
	})
}

type onboardingPermissionSummary struct {
	granted int
	total   int
}

func summarizeOnboardingPermissions(rows []permissionGuideRow) onboardingPermissionSummary {
	summary := onboardingPermissionSummary{total: len(rows)}
	for _, row := range rows {
		if row.access == permissionGuideGranted {
			summary.granted++
		}
	}
	return summary
}

func (s onboardingPermissionSummary) allGranted() bool {
	return s.total > 0 && s.granted == s.total
}

func (s onboardingPermissionSummary) label() string {
	if s.total == 0 {
		return ""
	}
	if s.allGranted() {
		return "全部已授权"
	}
	if s.granted == 0 {
		return "尚未授权"
	}
	return fmt.Sprintf("已授权 %d/%d", s.granted, s.total)
}

func (s *shell) invalidateOnboardingWindow() {
	if s.onboarding.window != nil && !s.onboarding.window.IsDestroyed() {
		s.onboarding.window.Invalidate()
	}
}

func (s *shell) onboardingPluginSettingsButton(c *ui.Context, a appearance) {
	if appButton(c, a, "插件设置…", false).Clicked() {
		s.openOnboardingSettingsPage(pagePlugins)
	}
}

func (s *shell) onboardingVoiceToolNeedsSettings(tool *voiceTool) bool {
	if tool.busy {
		return false
	}
	if !tool.active {
		return s.onboarding.toolLocations[tool.id] == voicetools.Ambiguous
	}
	return tool.snapshot.State == voicetools.Ambiguous || (tool.snapshot.State == voicetools.ConfigMissing && tool.prepareAssets == nil)
}
