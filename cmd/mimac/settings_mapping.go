package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/statuscolor"
)

func (s *shell) mappingSettings(c *ui.Context, a appearance) {
	profile := s.activeProfile()
	activeVoiceTools := make([]*voiceTool, 0, len(s.tools))
	for _, tool := range s.tools {
		if tool.active {
			activeVoiceTools = append(activeVoiceTools, tool)
		}
	}
	// Keep one column width across selection changes and all row states.
	actionWidth := mappingChoiceWidth + mappingMenuWidth + spaceSM
	for _, tool := range activeVoiceTools {
		actionWidth = max(actionWidth, appMenuButtonWidth(c, a, tool.name)+mappingMenuWidth+spaceSM)
	}
	choiceWidth := actionWidth - mappingMenuWidth - spaceSM
	if s.selectedButton == "" {
		s.selectedButton = profile.Model.DefaultSelectedButtonID
	}
	headerHeight := mappingHeaderHeight(a.fonts.caption)
	rowHeight := mappingRowHeight(c, a)
	buttonHeight := rowHeight - 2*settingsRowPadding
	frameHeight := mappingReferenceFrameHeight(a.fonts.caption, rowHeight)
	ui.Row(c).Fill().MinWidth(0).AlignItems(ui.Start).Gap(windowInset).Children(func() {
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Height(frameHeight).MaxHeightPercent(100).MinHeight(0).Role(ui.RoleGroup).Label("遥控器预览").Gap(sectionGap).AlignItems(ui.Center).Children(func() {
			if len(s.deviceProfiles) > 1 {
				appMenuButton(c, a, profile.Name, false, s.systemSymbol("chevron.up.chevron.down", a.theme.Dark, accessoryIconSize), func(menu *ui.Menu) {
					for _, plugin := range s.deviceProfiles {
						if menu.Item(plugin.Profile.Name).Checked(plugin.Profile.ID == profile.ID).Chosen() {
							if err := s.selectDeviceProfile(plugin.Profile.ID); err != nil {
								s.mappingError = "请先松开按键并停用当前设备"
							}
						}
					}
				}).FillWidth().MinWidth(0)
			} else {
				ui.Text(c, profile.Name).FillWidth().TextAlign(ui.Center).FontSize(a.fonts.body).FontWeight(600)
			}
			ui.Column(c).Grow(1).Basis(0).FillWidth().MinHeight(0).AlignItems(ui.Center).Justify(ui.Center).Role(ui.RoleGroup).Label("遥控器展示区").Children(func() {
				s.remoteModel(c, a, profile)
			})
			footerMessage := ""
			if s.editButton == "" {
				footerMessage = s.mappingFooterError()
			}
			footerState := readyIndicatorFooterState{
				permission: s.deviceNeedsPermission || s.outputNeedsPermission,
				message:    footerMessage,
				action:     s.remoteButtonLabel(),
				busy:       s.remoteButtonDisabled(),
			}
			footer := ui.Column(c).Key("mapping-status-footer").FillWidth().Gap(spaceSM).Padding(0, 0, settingsRowPadding, 0).AlignItems(ui.Center)
			footer.Children(func() {
				if s.deviceNeedsPermission || s.outputNeedsPermission {
					if appButton(c, a, "打开系统设置", false).Clicked() {
						if s.deviceNeedsPermission {
							s.openInputPermissionSettings()
						} else {
							_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility")
						}
					}
				}
				if footerMessage != "" {
					ui.Text(c, footerMessage).FillWidth().TextAlign(ui.Center).FontSize(a.fonts.caption).TextColor(a.theme.Danger).Role(ui.RoleStatus)
				}
				s.mappingStatusRow(c, a, s.remoteLabel(), s.remoteTone(), s.remoteEnabled(), s.window, footer, footerState)
				disabled := s.remoteButtonDisabled()
				if appButton(c, a, s.remoteButtonLabel(), disabled).MinHeight(buttonHeight).Clicked() && !disabled {
					s.toggleRemote()
				}
			})
		})
		ui.Column(c).Grow(mappingListRatio).Basis(0).MinWidth(0).Height(frameHeight).MaxHeightPercent(100).MinHeight(0).Role(ui.RoleGroup).Label("按键设置").Gap(spaceSM).Children(func() {
			ui.Row(c).FillWidth().Height(headerHeight).Padding(0, settingsRowInset).AlignItems(ui.Center).Gap(spaceSM).Children(func() {
				ui.Text(c, "按键").FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Grow(1).MinWidth(0)
				ui.Text(c, "设备键值").FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Width(mappingDefaultWidth)
				ui.Text(c, "自定义").FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Width(actionWidth)
			})
			ui.Scroll(c).Grow(1).Basis(0).FillWidth().MinHeight(0).Children(func() {
				rows := ui.Column(c).Key("mapping-list/" + profile.ID).Gap(mappingRowGap).FillWidth().MinWidth(0)
				var selectedBounds ui.Rect
				rows.Children(func() {
					for _, button := range profile.Buttons {
						id := button.ID
						row := ui.Column(c).Key(profile.ID+"/row/"+id).Gap(spaceXS).MinHeight(rowHeight).Padding(settingsRowPadding, settingsRowInset).FillWidth().MinWidth(0).Radius(settingsGroupRadius)
						if s.selectedButton == id {
							selectedBounds = row.LayoutBounds()
						}
						if s.revealButton == id {
							row.ScrollIntoView()
							s.revealButton = ""
						}
						row.Children(func() {
							ui.Row(c).FillWidth().MinWidth(0).MinHeight(buttonHeight).AlignItems(ui.Center).Gap(spaceSM).Children(func() {
								voiceButton := id == s.voiceButtonID()
								selectLabel := "选择" + button.Label
								selectButton := ui.ButtonBase(c).Label(selectLabel).Justify(ui.Start).Grow(1).MinWidth(0).MinHeight(buttonHeight).Padding(0).FocusRing(false)
								if selectButton.FocusVisible() {
									selectButton.Border(lineWidth, a.theme.Focus)
								}
								selectButton.Children(func() { ui.Text(c, button.Label).FontSize(a.fonts.body) })
								if selectButton.Clicked() {
									s.selectedButton = id
									s.endShortcutRecording()
									c.Invalidate()
								}
								ui.Text(c, button.DefaultLabel).FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Width(mappingDefaultWidth)
								if voiceButton {
									s.mappingVoiceSelection(c, a, activeVoiceTools, choiceWidth, buttonHeight)
								} else if s.editButton == id {
									s.shortcutRecorder(c, a, choiceWidth, buttonHeight)
								} else {
									label := s.mappingLabel(profile.ID, id)
									field := appButton(c, a, label, s.mappingLoadFailed).Label(button.Label+"的快捷键").Width(choiceWidth).MinHeight(buttonHeight).Padding(controlInset, spaceSM)
									if field.Clicked() {
										s.beginShortcutRecording(id)
										c.Invalidate()
									}
								}
								if voiceButton {
									ui.Box(c).Width(mappingMenuWidth).MinHeight(buttonHeight).Shrink(0).PassThrough()
								} else {
									menuButton := styleAppButton(ui.ButtonBase(c), a, s.mappingLoadFailed).
										Label(button.Label + "的预设").Role(ui.RoleMenuButton).
										Width(mappingMenuWidth).MinHeight(buttonHeight).Padding(0).Center()
									icon := s.systemSymbol("ellipsis", a.theme.Dark, accessoryIconSize)
									menuButton.Children(func() {
										ui.Box(c).Width(accessoryIconSize).Height(accessoryIconSize).
											AlignItems(ui.Center).Justify(ui.Center).PassThrough().Children(func() {
											if icon != nil {
												image := ui.Image(c, icon).Width(accessoryIconSize).Height(accessoryIconSize).Fit(ui.Contain).Shrink(0)
												if s.mappingLoadFailed {
													image.Opacity(0.55)
												}
											} else {
												dotColor := a.theme.Text
												if s.mappingLoadFailed {
													dotColor = a.disabled
												}
												ui.Row(c).Center().Gap(2).PassThrough().Children(func() {
													for range 3 {
														ui.Box(c).Size(2, 2).Radius(1).Background(dotColor).PassThrough()
													}
												})
											}
										})
									})
									if menuButton.Clicked() {
										s.selectedButton = id
										s.popupMenu(s.mappingMenu(id), menuButton.Bounds())
									}
								}
							})
							if s.editButton == id && s.mappingError != "" {
								ui.Text(c, s.mappingError).FontSize(a.fonts.caption).TextColor(a.theme.Danger).Role(ui.RoleStatus)
							}
						})
					}
				})
				movingSelectionBackground(c, rows, selectedBounds, a.theme.Surface, settingsGroupRadius, "mapping-selection")
			})
		})
	})
}

func (s *shell) mappingVoiceSelection(c *ui.Context, a appearance, active []*voiceTool, width, height float32) {
	switch len(active) {
	case 0:
		button := appButton(c, a, "选择语音工具", false).Label("选择语音工具").Width(width).MinHeight(height).Padding(controlInset, spaceSM)
		if button.Clicked() {
			s.endShortcutRecording()
			s.showPage(pageVoice)
		}
	case 1:
		tool := active[0]
		if tool.id == s.currentVoiceTool {
			if tool.requiredInputSource == nil {
				ui.Text(c, tool.name).FontSize(a.fonts.body).Width(width).MinHeight(height)
				return
			}
			button := ui.ButtonBase(c).Label("重新选择：" + tool.name).
				Width(width).MinHeight(height).Padding(0).FocusRing(false).Justify(ui.Start).AlignItems(ui.Center)
			button.Children(func() { ui.Text(c, tool.name).FontSize(a.fonts.body) })
			if button.Clicked() && !s.toolLoadFailed {
				s.chooseMappingVoiceTool(c, tool)
			}
			return
		}
		button := appButton(c, a, "使用", s.toolLoadFailed).Label("使用："+tool.name).
			Width(width).MinHeight(height).Padding(controlInset, spaceSM)
		if button.Clicked() && !s.toolLoadFailed {
			s.chooseMappingVoiceTool(c, tool)
		}
	default:
		appMenuButton(c, a, s.voiceSelectionLabel(), s.toolLoadFailed, s.systemSymbol("chevron.up.chevron.down", a.theme.Dark, accessoryIconSize), func(menu *ui.Menu) {
			for _, tool := range active {
				item := menu.Item(tool.name).Checked(tool.id == s.currentVoiceTool)
				reselectCurrent := tool.id == s.currentVoiceTool && tool.requiredInputSource != nil
				if item.Chosen() && (tool.id != s.currentVoiceTool || reselectCurrent) && !s.toolLoadFailed {
					s.chooseMappingVoiceTool(c, tool)
				}
			}
		}).Label("选择语音工具").Width(width).MinHeight(height).Padding(controlInset, spaceSM)
	}
}

func (s *shell) chooseMappingVoiceTool(c *ui.Context, tool *voiceTool) {
	s.endShortcutRecording()
	if err := s.selectVoiceTool(tool); err != nil && s.toolError == "" {
		s.toolError = "无法切换语音工具"
	}
	c.Invalidate()
}

func (s *shell) mappingStatusRow(c *ui.Context, a appearance, label string, tone statusTone, enabled bool, window *mygo.Window, footer ui.Element, footerState readyIndicatorFooterState) {
	color := a.statusColor(tone)
	ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(spaceSM).Children(func() {
		if tone == toneWarning || tone == toneError {
			s.clearReadyIndicator()
			ui.Text(c, "!").FontSize(a.fonts.caption).TextColor(color)
		} else if enabled && tone == toneActive {
			activeColor := a.theme.Success
			if systemGreen, err := statuscolor.SystemGreen(a.theme.Dark); err == nil {
				activeColor = systemGreen
			}
			element := readyIndicatorSlot(c)
			if !s.syncReadyIndicator(c, element, footer, activeColor, label, footerState, a.fonts.caption, a.fonts.body, window, c.Preferences().ReduceMotion) {
				drawReadyIndicatorFallback(element, activeColor)
			}
		} else {
			s.clearReadyIndicator()
		}
		ui.Text(c, label).FontSize(a.fonts.caption).TextColor(color).Role(ui.RoleStatus)
	})
}

func (s *shell) beginShortcutRecording(id string) {
	if id == s.voiceButtonID() {
		return
	}
	if s.coordinator != nil {
		snapshot := s.coordinator.core.Snapshot()
		sinkHasHeldKeys := s.coordinator.sink != nil && len(s.coordinator.sink.Held()) != 0
		if len(snapshot.PressedButtons) != 0 {
			s.mappingError = "请先松开遥控器按键"
			return
		}
		if len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 0 || sinkHasHeldKeys {
			s.mappingError = "请先停用按键输出，再录入快捷键"
			return
		}
	}
	s.selectedButton, s.editButton, s.mappingError = id, id, ""
	s.recorderFocus.Focus()
	s.syncRecorderAccelerators()
	s.revealButton = id
}

func (s *shell) endShortcutRecording() {
	s.editButton = ""
	s.syncRecorderAccelerators()
}

func (s *shell) mappingMenu(id string) *mygo.Menu {
	if id == s.voiceButtonID() {
		return mygo.NewMenu([]*mygo.MenuItem{})
	}
	profileID := s.activeProfile().ID
	_, toolBound := s.profile.ToolActions[profileID][id]
	items := make([]*mygo.MenuItem, 0, 15)
	for _, option := range []struct{ label, value string }{
		{"使用设备默认", ""}, {"↑", "Up"}, {"↓", "Down"}, {"←", "Left"}, {"→", "Right"},
		{"确认 ↩", "Return"}, {"返回 ⎋", "Escape"}, {"空格", "Space"},
		{"复制 ⌘C", "Command+C"}, {"粘贴 ⌘V", "Command+V"},
	} {
		items = append(items, &mygo.MenuItem{Label: option.label, Checked: !toolBound && s.profile.Profiles[profileID][id] == option.value,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				if !s.closing && s.activeProfile().ID == profileID {
					s.saveMapping(id, option.value)
				}
			}})
	}
	items = append(items, mygo.Separator(), &mygo.MenuItem{Label: "录入快捷键…", Click: func(*mygo.MenuItem, *mygo.Window) {
		if !s.closing && s.activeProfile().ID == profileID {
			s.beginShortcutRecording(id)
			s.invalidateDeviceView()
		}
	}})
	items = append(items, mygo.Separator())
	items = append(items, s.toolShortcutMenuItems(profileID, id)...)
	return mygo.NewMenu(items)
}

func (s *shell) pluginSettings(c *ui.Context, a appearance) {
	pluginRow := func(name, status, action string, disabled bool, tool *voiceTool, run func()) {
		ui.Row(c).FillWidth().MinWidth(0).MinHeight(settingsRowHeight).AlignItems(ui.Center).Gap(spaceLG).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(spaceXS).Children(func() {
				ui.Row(c).MinWidth(0).AlignItems(ui.Center).Gap(spaceSM).Children(func() {
					if tool != nil {
						s.voiceToolArtwork(c, a, tool)
					}
					ui.Text(c, name).FontSize(a.fonts.body).FontWeight(600)
				})
				ui.Text(c, status).FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Role(ui.RoleStatus)
			})
			if action != "" {
				button := appButton(c, a, action, disabled).Label(action + "：" + name)
				if button.Clicked() && run != nil {
					run()
					c.Invalidate()
				}
			}
		})
	}

	pluginCard := func(name, status, action string, disabled bool, tool *voiceTool, run func()) func() {
		return func() {
			settingsGroup(c, a, func() {
				ui.Column(c).FillWidth().MinWidth(0).Padding(settingsRowInset, 0).Children(func() {
					pluginRow(name, status, action, disabled, tool, run)
				})
			}).Role(ui.RoleGroup).Label(name + "插件")
		}
	}
	pluginSection := func(title string, cards ...func()) {
		if len(cards) == 0 {
			return
		}
		ui.Column(c).FillWidth().MinWidth(0).Gap(spaceSM).Children(func() {
			ui.Text(c, title).FontSize(a.fonts.body).FontWeight(600).
				Padding(0, settingsRowInset).Role(ui.RoleHeading).Level(2)
			settingsColumns(c, cards...).Role(ui.RoleGroup).Label(title + "列表")
		})
	}

	coreStatus := "未启用"
	if s.plugins != nil && s.plugins.Active(corePlugin) {
		coreStatus = "运行中"
	}
	pluginSection("系统插件",
		pluginCard("按键核心", coreStatus, "按键映射", false, nil, func() { s.navigate(pageMappings) }),
	)

	device := s.activeDevicePlugin()
	if device.ModuleID != "" {
		pluginSection("设备", pluginCard(device.Profile.Name, s.remoteLabel(), s.remoteButtonLabel(), s.remoteButtonDisabled(), nil, func() { s.toggleRemote() }))
	}
	if s.toolError != "" {
		ui.Text(c, s.toolError).FontSize(a.fonts.caption).TextColor(a.theme.Danger).Role(ui.RoleStatus)
	}
	for _, kind := range []voiceToolKind{voiceInputMethod, voiceRecognition, voiceApplication} {
		cards := make([]func(), 0, len(s.tools))
		for _, tool := range s.tools {
			if tool.kind != kind {
				continue
			}
			action := "启用"
			if tool.active {
				action = "停用"
			}
			cards = append(cards, pluginCard(tool.name, voiceToolStatus(tool), action, s.toolLoadFailed, tool, func() { _ = s.setVoiceToolEnabled(tool, !tool.active) }))
		}
		pluginSection(string(kind), cards...)
	}
}
