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
	var current *voiceTool
	for _, tool := range s.tools {
		if !tool.active {
			continue
		}
		enabled = append(enabled, tool)
		if tool.id == s.currentVoiceTool {
			current = tool
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

	settingsColumns(c, func() {
		for _, tool := range enabled {
			s.voiceToolChoice(c, a, tool, tool == current)
		}
	}, func() {
		if current == nil {
			settingsGroup(c, a, func() {
				ui.Column(c).FillWidth().Padding(settingsRowInset, 0).Children(func() {
					ui.Text(c, "选择语音工具").FontSize(a.fonts.body).Role(ui.RoleStatus)
				})
			})
			return
		}
		s.voiceToolDetails(c, a, current)
	})
}

func (s *shell) voiceToolChoice(c *ui.Context, a appearance, tool *voiceTool, selected bool) {
	settingsGroup(c, a, func() {
		ui.Column(c).FillWidth().MinWidth(0).Padding(settingsRowInset, 0).Gap(spaceSM).Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).MinHeight(controlHeight).AlignItems(ui.Center).Gap(spaceSM).Children(func() {
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
				button := appButton(c, a, "重新读取", tool.busy).Label("重新读取" + tool.name + "配置")
				if button.Clicked() && !tool.busy {
					s.refreshVoiceTool(tool)
				}
			})
		})
	}).Role(ui.RoleGroup).Label(tool.name + "选项")
}

func (s *shell) voiceToolDetails(c *ui.Context, a appearance, tool *voiceTool) {
	settingsGroup(c, a, func() {
		ui.Column(c).FillWidth().MinWidth(0).Padding(settingsRowInset, 0).Children(func() {
			ui.Text(c, tool.name).FontSize(a.fonts.body).FontWeight(600).Role(ui.RoleHeading).Level(2)
			if tool.busy || tool.snapshot.State != voicetools.Ready {
				ui.Text(c, voiceToolStatus(tool)).FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Role(ui.RoleStatus).Padding(spaceSM, 0, 0, 0)
				return
			}
			if len(tool.snapshot.Actions) == 0 {
				ui.Text(c, "未发现可显示的快捷键").FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Role(ui.RoleStatus).Padding(spaceSM, 0, 0, 0)
				return
			}
			for _, action := range tool.snapshot.Actions {
				sectionLine(c, a)
				settingsActionRow(c, &a.theme, a.fonts.body, action.Name, toolActionLabel(action))
			}
		})
	}).Role(ui.RoleGroup).Label("当前工具配置")
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
