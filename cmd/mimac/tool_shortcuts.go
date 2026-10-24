package main

import (
	"fmt"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/voicetools"
)

const toolShortcutMenuNote = "绑定工具动作"
const maxToolShortcutCandidates = 8

func (s *shell) toolShortcutMenuItems(profileID, buttonID string) []*mygo.MenuItem {
	items := []*mygo.MenuItem{}
	for _, tool := range s.tools {
		if tool.active && s.currentVoiceTool == tool.id {
			items = append(items, s.toolShortcutMenuItem(tool, profileID, buttonID))
		}
	}
	items = append(items, &mygo.MenuItem{Label: "语音工具设置…", Click: func(*mygo.MenuItem, *mygo.Window) {
		if !s.closing {
			s.navigate(pageVoice)
		}
	}})
	return items
}

func (s *shell) toolShortcutMenuItem(tool *voiceTool, profileID, buttonID string) *mygo.MenuItem {
	sequence := tool.sequence
	items := []*mygo.MenuItem{{Label: toolShortcutMenuNote, Disabled: true}, mygo.Separator()}
	if tool.busy || tool.snapshot.State != voicetools.Ready {
		items = append(items, &mygo.MenuItem{Label: voiceToolStatus(tool), Disabled: true})
	} else {
		for _, action := range tool.snapshot.Actions {
			if action.Disabled {
				items = append(items, &mygo.MenuItem{Label: action.Name + "：已关闭快捷键", Disabled: true})
				continue
			}
			if action.Scope != "global" {
				label := "作用范围待验证"
				if action.Scope == "app" {
					label = "仅限应用内使用"
				}
				items = append(items, &mygo.MenuItem{Label: action.Name + "：" + label, Disabled: true})
				continue
			}
			if len(action.Candidates) == 0 {
				items = append(items, &mygo.MenuItem{Label: action.Name + "：未设置快捷键", Disabled: true})
				continue
			}
			for _, candidate := range action.Candidates[:min(len(action.Candidates), maxToolShortcutCandidates)] {
				if !canBindToolAction(action, candidate) || !toolCandidateResolves(tool, action.ID, candidate.Raw) {
					items = append(items, &mygo.MenuItem{Label: action.Name + "：暂不支持此快捷键", Disabled: true})
					continue
				}
				actionID, raw, shortcut := action.ID, candidate.Raw, candidate.Shortcut
				items = append(items, &mygo.MenuItem{
					Label: fmt.Sprintf("%s  %s", action.Name, shortcutLabel(shortcut)), Disabled: s.mappingLoadFailed,
					Click: func(*mygo.MenuItem, *mygo.Window) {
						if s.toolCandidateIsCurrent(tool, profileID, buttonID, sequence, actionID, raw, shortcut) {
							s.saveToolAction(buttonID, voicetools.ActionRef{ToolID: tool.id, ActionID: actionID, CandidateRaw: raw})
						}
					},
				})
			}
			if remaining := len(action.Candidates) - maxToolShortcutCandidates; remaining > 0 {
				items = append(items, &mygo.MenuItem{Label: fmt.Sprintf("其余 %d 个快捷键", remaining), Disabled: true})
			}
		}
		if len(tool.snapshot.Actions) == 0 {
			items = append(items, &mygo.MenuItem{Label: "未设置快捷键", Disabled: true})
		}
	}
	return &mygo.MenuItem{Label: "从 " + tool.name + " 选用", Submenu: items}
}

func (s *shell) toolCandidateIsCurrent(tool *voiceTool, profileID, buttonID string, sequence uint64, actionID, raw, shortcut string) bool {
	if s.closing || s.mappingLoadFailed || s.activeProfile().ID != profileID || s.currentVoiceTool != tool.id || !tool.active || tool.busy || tool.sequence != sequence || tool.snapshot.State != voicetools.Ready {
		return false
	}
	if _, exists := s.activeProfile().Button(buttonID); !exists {
		return false
	}
	for _, action := range tool.snapshot.Actions {
		if action.ID != actionID || action.Disabled {
			continue
		}
		for _, candidate := range action.Candidates {
			if canBindToolAction(action, candidate) && candidate.Raw == raw && candidate.Shortcut == shortcut && toolCandidateResolves(tool, actionID, raw) {
				return true
			}
		}
		return false
	}
	return false
}

// Candidate syntax alone is insufficient: a tool must declare its command.
func canBindToolAction(action voicetools.Action, candidate voicetools.Candidate) bool {
	if action.Disabled || action.Scope != "global" || !candidate.Supported || action.Control == nil {
		return false
	}
	if _, err := keyoutput.ParseShortcut(candidate.Shortcut); err != nil {
		return false
	}
	_, err := voicetools.ResolveAction(voicetools.ActionRef{ToolID: "candidate", ActionID: action.ID, CandidateRaw: candidate.Raw}, 1,
		voicetools.Snapshot{State: voicetools.Ready, Actions: []voicetools.Action{action}})
	return err == nil
}

func (s *shell) saveToolAction(buttonID string, ref voicetools.ActionRef) {
	if s.mappingLoadFailed || s.closing {
		return
	}
	if _, err := s.resolveToolAction(ref); err != nil {
		s.mappingError = "工具动作当前不可用"
		return
	}
	next, err := s.profile.withToolAction(s.activeProfile(), buttonID, ref)
	if err != nil {
		s.mappingError = "工具动作不受支持"
		return
	}
	if s.coordinator != nil && s.coordinator.report().Pressed != 0 {
		s.mappingError = "请先松开遥控器按键"
		return
	}
	if err = saveProfile(s.mappingPath, next); err != nil {
		s.mappingError = "保存失败，仍使用原映射"
		return
	}
	s.profile = next
	if s.coordinator != nil {
		s.coordinator.mappings, _ = next.keys(s.activeProfile())
		s.coordinator.actions = next.actionRefs(s.activeProfile())
		s.coordinator.resolveAction = s.resolveToolAction
	}
	s.mappingError = ""
	s.endShortcutRecording()
	s.refreshRemoteOutput(false)
	s.invalidateDeviceView()
}

func (s *shell) mappingLabel(profileID, buttonID string) string {
	if ref, ok := s.profile.ToolActions[profileID][buttonID]; ok {
		for _, tool := range s.tools {
			if tool.id != ref.ToolID {
				continue
			}
			for _, action := range tool.snapshot.Actions {
				if action.ID == ref.ActionID {
					label := tool.name + " / " + action.Name
					if _, err := s.resolveToolAction(ref); err != nil {
						label += "（不可用）"
					}
					return label
				}
			}
			return tool.name + "（不可用）"
		}
		return "工具动作（不可用）"
	}
	if shortcut := s.profile.Profiles[profileID][buttonID]; shortcut != "" {
		return shortcutLabel(shortcut)
	}
	return "使用默认"
}

func toolCandidateResolves(tool *voiceTool, actionID, raw string) bool {
	_, err := voicetools.ResolveAction(voicetools.ActionRef{ToolID: tool.id, ActionID: actionID, CandidateRaw: raw}, tool.sequence, tool.snapshot)
	return err == nil
}
