package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/chatgpt"
	"mimac.local/mimac/internal/adapters/doubao"
	"mimac.local/mimac/internal/adapters/shandian"
	"mimac.local/mimac/internal/adapters/typeless"
	"mimac.local/mimac/internal/adapters/wetype"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/pluginrt"
	"mimac.local/mimac/internal/voicetools"
)

type voiceTool struct {
	id, name            string
	kind                voiceToolKind
	discover            func(context.Context, voicetools.Locator) voicetools.Snapshot
	requiredInputSource func(string) (string, bool)
	active, busy        bool
	sequence            uint64
	cancel              context.CancelFunc
	snapshot            voicetools.Snapshot
}

// Kind describes the user-facing capability, independent of how the adapter
// executes. Both kinds use the same plugin lifecycle and discovery contract.
type voiceToolKind string

const (
	voiceInputMethod voiceToolKind = "语音输入法"
	voiceApplication voiceToolKind = "应用语音"
)

type voiceToolsState struct {
	tools               []*voiceTool
	currentVoiceTool    string
	toolLocator         voicetools.Locator
	toolPreferences     toolPreferences
	toolPreferencesPath string
	toolLoadFailed      bool
	toolError           string
	voiceStartBlocked   string
}

func builtInVoiceTools() []*voiceTool {
	return []*voiceTool{
		{id: "voice-doubao", name: "豆包输入法", kind: voiceInputMethod, discover: doubao.Discover, requiredInputSource: doubao.RequiredInputSourceID},
		{id: "voice-typeless", name: "Typeless", kind: voiceInputMethod, discover: typeless.Discover},
		{id: "voice-wetype", name: "微信输入法", kind: voiceInputMethod, discover: wetype.Discover},
		{id: "voice-shandian", name: "闪电说", kind: voiceInputMethod, discover: shandian.Discover},
		{id: "voice-chatgpt", name: "ChatGPT", kind: voiceApplication, discover: chatgpt.Discover},
	}
}

// Discovery is one cancellable read per activation or explicit refresh. MyGo
// serializes completions; there is no watcher, polling loop, or target process.
func (t *voiceTool) refresh(locator voicetools.Locator, dispatch func(func()), changed func()) {
	if !t.active {
		return
	}
	if t.cancel != nil {
		t.cancel()
	}
	t.sequence++
	sequence := t.sequence
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.busy = cancel, true
	t.snapshot = voicetools.Snapshot{}
	go func() {
		result := t.discover(ctx, locator)
		cancel()
		dispatch(func() {
			if !t.active || t.sequence != sequence {
				return
			}
			t.busy, t.cancel, t.snapshot = false, nil, result
			changed()
		})
	}()
}

func (t *voiceTool) stop() {
	t.sequence++
	if t.cancel != nil {
		t.cancel()
	}
	t.active, t.busy, t.cancel = false, false, nil
	t.snapshot = voicetools.Snapshot{}
}

func (s *shell) voiceToolManifests() []pluginrt.Manifest {
	s.tools = builtInVoiceTools()
	manifests := make([]pluginrt.Manifest, 0, len(s.tools))
	for _, tool := range s.tools {
		manifests = append(manifests, pluginrt.Manifest{ID: tool.id, Start: func() (pluginrt.StopFunc, error) {
			tool.active = true
			s.refreshVoiceTool(tool)
			return func() error {
				tool.stop()
				// Discovery owns no key source. Output failure stays in the output
				// plugin's recovery barrier and does not resurrect a stopped reader.
				if err := s.cancelToolActions(tool.id); err != nil {
					s.outputStatus = "输出已暂停，请松开按键后恢复"
				}
				return nil
			}, nil
		}})
	}
	return manifests
}

func (s *shell) refreshVoiceTool(tool *voiceTool) {
	if s.closing {
		return
	}
	tool.refresh(s.toolLocator, mygo.RunOnMain, func() {
		if !s.closing {
			s.refreshRemoteOutput(false)
			s.invalidateDeviceView()
		}
	})
}

func (s *shell) loadVoiceTools() {
	if s.coordinator != nil {
		s.coordinator.resolveAction = s.resolveToolAction
		s.coordinator.preflightAction = s.preflightToolAction
	}
	locator, err := voicetools.DefaultLocator()
	if err != nil {
		s.toolLoadFailed, s.toolError = true, "无法定位语音工具"
		return
	}
	s.toolLocator = locator
	base, err := os.UserConfigDir()
	if err != nil {
		s.toolLoadFailed, s.toolError = true, "插件设置位置不可用"
		return
	}
	s.toolPreferencesPath = filepath.Join(base, "MiMac", "tools.json")
	s.toolPreferences, err = readToolPreferences(s.toolPreferencesPath)
	if err != nil {
		s.toolLoadFailed, s.toolError = true, "插件设置读取失败，原文件已保留"
		return
	}
	s.currentVoiceTool = s.toolPreferences.Current
	for _, tool := range s.tools {
		if slices.Contains(s.toolPreferences.Enabled, tool.id) {
			if err := s.plugins.Enable(tool.id); err != nil {
				s.toolError = "插件启动失败"
			}
		}
	}
}

func (s *shell) setVoiceToolEnabled(tool *voiceTool, enabled bool) error {
	defer s.refreshLiveAudio()
	if s.closing || s.toolLoadFailed || s.plugins == nil || !slices.Contains(s.tools, tool) {
		return errors.New("voice tool activation unavailable")
	}
	if tool.active == enabled {
		return nil
	}
	next := toolPreferences{Version: 1, Enabled: slices.Clone(s.toolPreferences.Enabled), Current: s.currentVoiceTool}
	next.Enabled = slices.DeleteFunc(next.Enabled, func(id string) bool { return id == tool.id })
	if enabled {
		next.Enabled = append(next.Enabled, tool.id)
	} else if next.Current == tool.id {
		next.Current = ""
	}
	if err := saveToolPreferences(s.toolPreferencesPath, next); err != nil {
		s.toolError = "保存失败，插件未更改"
		return err
	}
	s.toolPreferences = next
	s.currentVoiceTool = next.Current
	var err error
	if enabled {
		err = s.plugins.Enable(tool.id)
	} else {
		err = s.plugins.Disable(tool.id)
	}
	if err != nil {
		s.toolError = "插件状态更改失败"
		return err
	}
	s.toolError = ""
	s.invalidateDeviceView()
	return nil
}

// Adapter availability and the selected voice target are separate choices.
// Only one loaded adapter may supply the active tool's mapping presets.
func (s *shell) selectVoiceTool(tool *voiceTool) error {
	defer s.refreshLiveAudio()
	if s.closing || s.toolLoadFailed || !slices.Contains(s.tools, tool) || !tool.active {
		return errors.New("voice tool selection unavailable")
	}
	if s.currentVoiceTool == tool.id {
		return nil
	}
	next := s.toolPreferences
	next.Current = tool.id
	if err := saveToolPreferences(s.toolPreferencesPath, next); err != nil {
		s.toolError = "保存失败，当前工具未更改"
		return err
	}
	oldTool := s.currentVoiceTool
	s.toolPreferences, s.currentVoiceTool = next, tool.id
	if err := s.cancelToolActions(oldTool); err != nil {
		s.toolError = "原工具按键未能释放"
		return err
	}
	s.toolError = ""
	s.refreshRemoteOutput(false)
	s.invalidateDeviceView()
	return nil
}

func voiceToolStatus(tool *voiceTool) string {
	if !tool.active {
		return "未启用"
	}
	if tool.busy {
		return "正在读取配置"
	}
	switch tool.snapshot.State {
	case voicetools.Ready:
		if strings.HasPrefix(tool.snapshot.Source, "defaults:") {
			return "使用应用默认快捷键"
		}
		return "已读取配置"
	case voicetools.NotFound:
		return "未找到应用"
	case voicetools.NotRunning:
		return "应用未运行"
	case voicetools.ConfigMissing:
		return "未找到快捷键配置"
	case voicetools.Ambiguous:
		return "发现多个配置来源"
	case voicetools.Unsupported:
		return "暂不支持此配置"
	default:
		return "读取失败"
	}
}

func (s *shell) resolveToolAction(ref voicetools.ActionRef) (bridge.ActionPlan, error) {
	if s.closing || s.currentVoiceTool != ref.ToolID {
		return bridge.ActionPlan{}, errors.New("voice target unavailable")
	}
	for _, tool := range s.tools {
		if tool.id == ref.ToolID && tool.active && !tool.busy {
			return voicetools.ResolveAction(ref, tool.sequence, tool.snapshot)
		}
	}
	return bridge.ActionPlan{}, errors.New("voice target unavailable")
}

func (s *shell) cancelToolActions(toolID string) error {
	if s.coordinator == nil || toolID == "" {
		return nil
	}
	return s.coordinator.step(bridge.CancelToolActions(toolID))
}
