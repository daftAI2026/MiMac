package main

import (
	"bytes"
	"image/png"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	brand "mimac.local/mimac/Assets/Brand"
	"mimac.local/mimac/internal/voicetools"
)

const (
	onboardingTitleSize    float32 = 26
	onboardingBodySize     float32 = 14
	onboardingCaptionSize  float32 = 12
	onboardingContentWidth float32 = 600
)

func (s *shell) onboardingView(c *ui.Context) {
	a := appAppearance(c.Theme().Dark, s.preferencesState.value.TextSize)
	a.fonts = fontSizes{caption: onboardingCaptionSize, body: onboardingBodySize, title: onboardingTitleSize}
	c.SetTheme(&a.theme)
	if s.appIcon == nil {
		if img, err := png.Decode(bytes.NewReader(brand.AboutIconPNG)); err == nil {
			s.appIcon = ui.NewBitmap(img)
		}
	}
	ui.Column(c).Fill().Background(a.theme.Background).Padding(20, 40, 24, 40).Children(func() {
		ui.Box(c).FillWidth().Height(28).Shrink(0).DragWindow()
		ui.Column(c).FillWidth().Grow(1).Basis(0).MinHeight(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Column(c).Width(onboardingContentWidth).MinWidth(0).Gap(sectionGap).Children(func() {
				ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(spaceSM).Children(func() {
					if s.appIcon != nil {
						ui.Image(c, s.appIcon).Size(64, 64).Fit(ui.Contain)
					}
					ui.Text(c, onboardingStepHeading(s.onboarding.step)).FontSize(onboardingTitleSize).FontWeight(700).TextAlign(ui.Center).Role(ui.RoleHeading).Level(1)
					ui.Text(c, onboardingStepSubheading(s.onboarding.step)).FillWidth().FontSize(onboardingBodySize).TextAlign(ui.Center).TextColor(a.theme.TextMuted)
				})
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
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(spaceSM).Children(func() {
					if s.onboarding.step > onboardingStepDevice {
						if appButton(c, a, "上一步", false).Clicked() {
							s.moveOnboarding(-1)
						}
					}
					label := "继续"
					if s.onboarding.step == onboardingStepPermissions {
						label = "开始使用"
					}
					if appButton(c, a, label, false).Clicked() {
						if s.onboarding.step == onboardingStepPermissions {
							s.finishOnboarding()
						} else {
							s.moveOnboarding(1)
						}
					}
				})
			})
		})
		ui.Text(c, onboardingStepPosition(s.onboarding.step)).FillWidth().FontSize(onboardingCaptionSize).TextAlign(ui.Center).TextColor(a.theme.TextMuted)
	})
}

func onboardingStepHeading(step onboardingStep) string {
	switch step {
	case onboardingStepVoice:
		return "准备语音工具"
	case onboardingStepPermissions:
		return "系统权限"
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
	default:
		return "在蓝牙设置中连接遥控器，已连接的设备无需重新配对。"
	}
}
func onboardingStepPosition(step onboardingStep) string {
	switch step {
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
		settingsGroup(c, a, func() {
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
				disabled := s.deviceReadBusy || s.monitor != nil
				if appButton(c, a, "重新检测", disabled).Clicked() && !disabled {
					s.detectDevice()
				}
			})
		})
		guide := profile.ConnectionGuide
		if guide.FirstPairing == "" {
			guide.FirstPairing = "让遥控器进入配对模式，在蓝牙设置中选择它。"
		}
		if guide.Reconnect == "" {
			guide.Reconnect = "唤醒遥控器，在蓝牙设置中确认已连接。"
		}
		ui.Row(c).FillWidth().Gap(spaceMD).AlignItems(ui.Start).Children(func() {
			s.onboardingInfoCard(c, a, "首次连接", guide.FirstPairing)
			s.onboardingInfoCard(c, a, "已经配对", guide.Reconnect)
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
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(spaceSM).Children(func() {
			if appButton(c, a, "重新检测", false).Clicked() {
				s.retryOnboardingVoiceChecks()
			}
			if appButton(c, a, "插件设置…", false).Clicked() {
				s.openOnboardingSettingsPage(pagePlugins)
			}
		})
	})
}
func (s *shell) onboardingVoiceToolRow(c *ui.Context, a appearance, tool *voiceTool) {
	selected := tool.id == s.currentVoiceTool
	status, canSelect, installState := s.onboardingVoiceToolStatus(tool)
	settingsGroup(c, a, func() {
		ui.Row(c).Key("onboarding-tool/"+tool.id).FillWidth().MinWidth(0).MinHeight(58).Padding(spaceSM, spaceMD).AlignItems(ui.Center).Gap(spaceMD).Children(func() {
			s.voiceToolArtwork(c, a, tool)
			ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(spaceXS).Children(func() {
				ui.Text(c, tool.name).FontSize(onboardingBodySize).FontWeight(600)
				ui.Text(c, status).FillWidth().FontSize(onboardingCaptionSize).TextColor(a.theme.TextMuted).Role(ui.RoleStatus)
			})
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
			s.onboarding.errorText = "无法启用工具，请在插件中重试。"
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
	snapshot := s.onboarding.permissionSnapshot
	ui.Column(c).FillWidth().Gap(spaceMD).Children(func() {
		if len(snapshot.rows) == 0 {
			ui.Text(c, "当前无需额外权限").FillWidth().FontSize(onboardingBodySize).TextAlign(ui.Center).TextColor(a.theme.TextMuted)
		} else {
			settingsGroup(c, a, func() {
				ui.Column(c).FillWidth().Padding(0, spaceMD).Children(func() {
					for index, row := range snapshot.rows {
						if index > 0 {
							ui.Box(c).Key("onboarding-permission-line/" + permissionGuideIDString(row.id)).FillWidth().Height(lineWidth).Background(a.separator).Shrink(0)
						}
						ui.Row(c).Key("onboarding-permission/" + permissionGuideIDString(row.id)).FillWidth().MinHeight(60).AlignItems(ui.Center).Gap(spaceMD).Children(func() {
							ui.Box(c).Size(24, 24).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
								if icon := s.systemSymbol(row.icon, a.theme.Dark, 20); icon != nil {
									ui.Image(c, icon).Size(20, 20).Fit(ui.Contain)
								}
							})
							ui.Text(c, row.title).FontSize(onboardingBodySize).FontWeight(600).Grow(1)
							ui.Text(c, permissionGuideAccessLabel(row.access)).FontSize(onboardingCaptionSize).TextColor(a.theme.TextMuted).Role(ui.RoleStatus)
						})
					}
				})
			})
		}
		ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
			if appButton(c, a, "授权…", false).Clicked() {
				s.showOnboardingPermissionGuide()
			}
		})
	})
}
func (s *shell) invalidateOnboardingWindow() {
	if s.onboarding.window != nil && !s.onboarding.window.IsDestroyed() {
		s.onboarding.window.Invalidate()
	}
}
