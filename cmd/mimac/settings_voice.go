package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/voicetools"
)

func (s *shell) voiceSettings(c *ui.Context, a appearance) {
	if s.toolError != "" {
		ui.Text(c, s.toolError).FontSize(a.fonts.caption).TextColor(a.theme.Danger).Role(ui.RoleStatus)
	}

	enabled := make([]*voiceTool, 0, len(s.tools))
	for _, tool := range s.tools {
		if tool.active {
			enabled = append(enabled, tool)
		}
	}

	if len(enabled) == 0 {
		settingsGroup(c, a, func() {
			ui.Column(c).FillWidth().MinWidth(0).Padding(settingsRowInset, 0).Gap(spaceSM).Children(func() {
				ui.Text(c, "尚未启用语音工具").FontSize(a.fonts.body).Role(ui.RoleStatus)
				button := appButton(c, a, "管理插件", false).Label("管理语音工具插件")
				if button.Clicked() {
					s.navigate(pagePlugins)
					c.Invalidate()
				}
			})
		})
		return
	}

	for _, kind := range []voiceToolKind{voiceInputMethod, voiceRecognition, voiceApplication} {
		cards := make([]func(), 0, len(enabled))
		for _, tool := range enabled {
			if tool.kind != kind {
				continue
			}
			tool := tool
			selected := tool.id == s.currentVoiceTool
			cards = append(cards, func() { s.voiceToolChoice(c, a, tool, selected) })
		}
		if len(cards) == 0 {
			continue
		}

		ui.Column(c).FillWidth().MinWidth(0).Gap(spaceSM).Children(func() {
			ui.Text(c, string(kind)).FontSize(a.fonts.body).FontWeight(600).
				Padding(0, settingsRowInset).Role(ui.RoleHeading).Level(2)
			settingsColumns(c, cards...).Role(ui.RoleGroup).Label(string(kind) + "列表")
		})
	}
}

func (s *shell) voiceToolChoice(c *ui.Context, a appearance, tool *voiceTool, selected bool) {
	settingsGroup(c, a, func() {
		ui.Column(c).FillWidth().MinWidth(0).Padding(settingsRowInset, 0).Gap(spaceSM).Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).MinHeight(controlHeight).AlignItems(ui.Center).Gap(spaceSM).Children(func() {
				s.voiceToolArtwork(c, a, tool)
				ui.Text(c, tool.name).FontSize(a.fonts.body).FontWeight(600).Grow(1).MinWidth(0)
				label := "使用"
				if selected {
					label = "当前工具"
				}
				button := appButton(c, a, label, selected || s.toolLoadFailed).Label(label + "：" + tool.name)
				if button.Clicked() && !selected && !s.toolLoadFailed {
					if err := s.selectVoiceTool(tool); err != nil && s.toolError == "" {
						s.toolError = "无法切换语音工具"
					}
					c.Invalidate()
				}
			})
			ui.Row(c).FillWidth().MinWidth(0).MinHeight(controlHeight).AlignItems(ui.Center).Gap(spaceSM).Children(func() {
				ui.Text(c, voiceToolStatus(tool)).FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Role(ui.RoleStatus).Grow(1).MinWidth(0)
				label := "重新读取"
				if tool.prepareAssets != nil {
					label = "重新检查"
					if tool.snapshot.State == voicetools.ConfigMissing {
						label = "下载中文模型"
					}
				}
				button := appButton(c, a, label, tool.busy).Label(label + tool.name)
				if button.Clicked() && !tool.busy {
					if tool.prepareAssets != nil && tool.snapshot.State == voicetools.ConfigMissing {
						s.refreshVoiceToolUsing(tool, tool.prepareAssets)
					} else {
						s.refreshVoiceTool(tool)
					}
				}
			})
			if selected {
				s.voiceToolDetails(c, a, tool)
			}
		})
	}).Role(ui.RoleGroup).Label(tool.name + "选项")
}

func (s *shell) voiceToolDetails(c *ui.Context, a appearance, tool *voiceTool) {
	if tool.busy || tool.snapshot.State != voicetools.Ready {
		return
	}
	if tool.id == appleSpeechToolID {
		ui.Column(c).FillWidth().MinWidth(0).Children(func() {
			sectionLine(c, a)
			settingsValueRow(c, &a.theme, a.fonts.body, "识别语言", "简体中文")
		})
		return
	}
	if len(tool.snapshot.Actions) == 0 {
		sectionLine(c, a)
		ui.Text(c, "未发现可显示的快捷键").FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Role(ui.RoleStatus)
		return
	}
	ui.Column(c).FillWidth().MinWidth(0).Children(func() {
		for _, action := range tool.snapshot.Actions {
			sectionLine(c, a)
			settingsValueRow(c, &a.theme, a.fonts.body, action.Name, toolActionLabel(action))
		}
	})
}

func toolActionLabel(action voicetools.Action) string {
	if action.Disabled {
		return "已关闭快捷键"
	}
	if len(action.Candidates) == 0 {
		return "未设置快捷键"
	}
	values := make([]string, 0, min(4, len(action.Candidates)))
	for _, candidate := range action.Candidates[:min(4, len(action.Candidates))] {
		if candidate.Shortcut != "" {
			label := shortcutLabel(candidate.Shortcut)
			if !candidate.Supported {
				label += "（待验证）"
			}
			values = append(values, label)
		} else {
			values = append(values, "暂不支持此快捷键")
		}
	}
	return strings.Join(values, "\n")
}
