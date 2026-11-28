package main

import (
	"fmt"
	"strings"

	"mimac.local/mimac/internal/voicetools"
)

// voiceToolLiveTextSetting keeps provider support separate from the global
// ShowLiveText preference. The returned status is intentionally empty for a
// normally supported provider; Hex and unsupported providers need a caveat.
func voiceToolLiveTextSetting(tool *voiceTool) (supported bool, status string) {
	if tool == nil {
		return false, "选择支持实时字幕的语音工具。"
	}
	switch tool.liveText {
	case voiceToolLiveTextSupported:
		return true, ""
	case voiceToolLiveTextNeedsModel:
		if !tool.active {
			return true, "Hex 需要启用本地服务并加载支持实时识别的听写模型。"
		}
		if tool.busy {
			return true, "Hex 正在检查本地服务；还需加载支持实时识别的听写模型。"
		}
		if tool.snapshot.State != voicetools.Ready {
			return true, "Hex 尚未连接本地服务；连接后需加载支持实时识别的听写模型。"
		}
		return true, "Hex 需加载支持实时识别的听写模型。"
	case voiceToolLiveTextMarkedRange:
		if voiceToolUsesNativeMarkedText(tool) {
			return true, "支持的输入框会显示临时听写文字；整句仍由输入法提交。"
		}
		return false, "当前语音工具未提供可读取的预编辑文字。"
	default:
		name := strings.TrimSpace(tool.name)
		if name == "" {
			name = "当前语音工具"
		}
		return false, fmt.Sprintf("%s 未向 MiMac 提供实时文字", name)
	}
}

func voiceToolUsesNativeMarkedText(tool *voiceTool) bool {
	if tool == nil || tool.liveText != voiceToolLiveTextMarkedRange {
		return false
	}
	switch tool.id {
	case "voice-doubao", "voice-wetype":
		return true
	default:
		return false
	}
}
