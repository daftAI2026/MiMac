package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	brand "mimac.local/mimac/Assets/Brand"
	"mimac.local/mimac/internal/adapters/typeless"
)

// settingsState is app-owned so a completed, explicit read can be shown again
// if the settings window is closed and reopened.
type settingsState struct {
	brandMark            *ui.SVG
	typelessReadSequence uint64
	typelessReadBusy     bool
	typelessReadStatus   string
	typelessSnapshot     typeless.Snapshot
	typelessSnapshotRead bool
}

func (s *shell) settings(c *ui.Context) {
	if s.brandMark == nil {
		s.brandMark = ui.MustParseSVG([]byte(brand.SVG))
	}
	if c.Shortcut(ui.Cmd, ui.KeyW) || c.Shortcut(0, ui.KeyEscape) {
		if s.window != nil {
			s.window.Close()
		}
		return
	}

	t := c.Theme()
	bodySize := max(t.FontSize, 12)
	captionSize := max(t.FontSize-2, 10)
	titleSize := t.FontSize + 2

	ui.Column(c).Gap(t.Space(1.5)).Padding(t.Space(3)).Fill().Children(func() {
		ui.Row(c).Gap(t.Space(2.5)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, s.brandMark).FontSize(30).Label("MiMac")
			ui.Column(c).Gap(t.Space(0.25)).Children(func() {
				ui.Text(c, "MiMac").FontSize(titleSize).FontWeight(600).Role(ui.RoleHeading).Level(1)
				ui.Text(c, "开发预览").FontSize(captionSize).TextColor(t.TextMuted)
			})
		})

		ui.Divider(c)
		ui.Text(c, "设备与音频").FontSize(bodySize).FontWeight(600).Role(ui.RoleHeading).Level(2)
		settingsValueRow(c, t, bodySize, "遥控器", "小米 Pro 2 · 接入待启用")
		settingsValueRow(c, t, bodySize, "音频", "采集未启动")

		ui.Divider(c)
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Gap(t.Space(0.25)).Grow(1).Children(func() {
				ui.Text(c, "Typeless 配置").FontSize(bodySize).FontWeight(600).Role(ui.RoleHeading).Level(2)
				ui.Text(c, "选择设置文件，查看已有快捷键").FontSize(captionSize).TextColor(t.TextMuted)
			})
			buttonLabel := "选择设置文件…"
			if s.typelessReadBusy {
				buttonLabel = "处理中…"
			}
			button := ui.Button(c, buttonLabel).Disabled(s.typelessReadBusy).FontSize(bodySize)
			if button.Clicked() && !s.typelessReadBusy {
				s.startTypelessSettingsRead(s.window)
				c.Invalidate()
			}
		})
		ui.Text(c, s.typelessStatusText()).FontSize(captionSize).TextColor(t.TextMuted).Role(ui.RoleStatus)

		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Gap(t.Space(0.5)).Children(func() {
				bindings := s.typelessSnapshot.FeatureShortcutBindings
				ui.Text(c, "快捷键").FontSize(bodySize).FontWeight(600).Role(ui.RoleHeading).Level(2)
				ui.Text(c, "所选文件中的快捷键").FontSize(captionSize).TextColor(t.TextMuted)
				settingsActionRow(c, t, bodySize, "听写模式", s.typelessBinding(bindings.DictationMode))
				ui.Divider(c)
				settingsActionRow(c, t, bodySize, "Ask Anything", s.typelessBinding(bindings.AskAnythingMode))
				ui.Divider(c)
				settingsActionRow(c, t, bodySize, "翻译模式", s.typelessBinding(bindings.TranslationMode))
				ui.Divider(c)
				settingsActionRow(c, t, bodySize, "粘贴上次转写", s.typelessBinding(bindings.PasteLastTranscript))

				ui.Divider(c)
				settingsValueRow(c, t, bodySize, "麦克风", s.typelessMicrophoneText())
				ui.Text(c, "设备匹配将在音频接入后确认。").FontSize(captionSize).TextColor(t.TextMuted)
			})
		})
	})
}

func settingsValueRow(c *ui.Context, t *ui.Theme, size float32, label, value string) {
	ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).Width(126).Shrink(0).SingleLine().FontSize(size).TextColor(t.TextMuted)
		ui.Text(c, value).Grow(1).Shrink(1).FontSize(size)
	})
}

func settingsActionRow(c *ui.Context, t *ui.Theme, size float32, label, value string) {
	ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Start).Children(func() {
		ui.Text(c, label).Width(126).Shrink(0).SingleLine().FontSize(size).TextColor(t.TextMuted)
		ui.Text(c, value).Grow(1).Shrink(1).FontSize(size)
	})
}

func (s *shell) typelessStatusText() string {
	if s.typelessReadStatus != "" {
		return s.typelessReadStatus
	}
	return "选择 Typeless 的 app-settings.json 开始配置。"
}

func (s *shell) typelessBinding(binding typeless.ActionBinding) string {
	if !s.typelessSnapshotRead {
		return "尚未读取"
	}
	if binding.Status != typeless.StatusPresent {
		return settingsStatusLabel(binding.Status)
	}
	if len(binding.Values) == 0 {
		return "未配置快捷键"
	}

	const maxBindings = 4
	parts := make([]string, 0, min(len(binding.Values), maxBindings)+1)
	for _, value := range binding.Values[:min(len(binding.Values), maxBindings)] {
		parts = append(parts, settingsQuotedPreview(value, 44))
	}
	if len(binding.Values) > maxBindings {
		parts = append(parts, fmt.Sprintf("其余 %d 项", len(binding.Values)-maxBindings))
	}
	return settingsTruncate(strings.Join(parts, " · "), 128)
}

func (s *shell) typelessMicrophoneText() string {
	if !s.typelessSnapshotRead {
		return "尚未读取"
	}
	microphone := s.typelessSnapshot.SelectedMicrophone
	if microphone.Status != typeless.StatusPresent {
		return settingsStatusLabel(microphone.Status)
	}
	deviceID := microphone.DeviceID
	if deviceID.Status != typeless.StatusPresent {
		return settingsStatusLabel(deviceID.Status)
	}
	if deviceID.Value == "" {
		return "尚未指定"
	}
	return "已在 Typeless 中指定"
}

func settingsStatusLabel(status typeless.Status) string {
	switch status {
	case typeless.StatusPresent:
		return "已读取"
	case typeless.StatusMissing:
		return "字段缺失"
	case typeless.StatusNull:
		return "值为 null"
	case typeless.StatusUnknown:
		return "无法判断"
	case typeless.StatusMalformed:
		return "格式无效"
	case typeless.StatusUnsupported:
		return "不支持"
	case typeless.StatusError:
		return "读取失败"
	case typeless.StatusStale:
		return "文件读取期间发生变化"
	default:
		return "状态未知"
	}
}

func settingsQuotedPreview(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) > maxRunes {
		return strconv.Quote(string(runes[:maxRunes])) + "…"
	}
	return strconv.Quote(value)
}

func settingsTruncate(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func (s *shell) startTypelessSettingsRead(parent *mygo.Window) {
	s.typelessReadSequence++
	sequence := s.typelessReadSequence
	s.typelessReadBusy = true
	go s.readTypelessSettings(sequence, parent)
}

func (s *shell) readTypelessSettings(sequence uint64, parent *mygo.Window) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:      parent,
		Title:       "选择 Typeless 设置文件",
		ButtonLabel: "选择",
		Message:     "选择 Typeless 的 app-settings.json",
		Filters:     []mygo.FileFilter{{Name: "JSON 设置文件", Extensions: []string{"json"}}},
		Multiple:    false,
	})
	if err != nil {
		s.updateTypelessSettings(sequence, func() {
			s.typelessReadBusy = false
			s.typelessReadStatus = "文件选择器不可用"
			if s.typelessSnapshotRead {
				s.typelessReadStatus = "文件选择器不可用，仍显示上次读取结果"
			}
		})
		return
	}
	if len(paths) == 0 {
		// Dialog.Open returns no paths and no error on cancellation. Preserve
		// the prior app-level result and leave the status unchanged.
		s.updateTypelessSettings(sequence, func() { s.typelessReadBusy = false })
		return
	}

	shouldRead := false
	s.updateTypelessSettings(sequence, func() {
		s.typelessReadStatus = "正在读取设置…"
		shouldRead = true
	})
	if !shouldRead {
		return
	}

	snapshot, readErr := typeless.ReadFile(paths[0])
	s.updateTypelessSettings(sequence, func() {
		s.typelessReadBusy = false
		if readErr != nil {
			s.typelessSnapshot = typeless.Snapshot{}
			s.typelessSnapshotRead = false
			s.typelessReadStatus = typelessReadErrorLabel(readErr)
			return
		}
		s.typelessSnapshot = snapshot
		s.typelessSnapshotRead = true
		s.typelessReadStatus = "已读取所选文件，遥控触发尚未启用"
	})
}

func (s *shell) updateTypelessSettings(sequence uint64, update func()) {
	mygo.RunOnMain(func() {
		if s.closing || sequence != s.typelessReadSequence {
			return
		}
		update()
		if window := s.window; window != nil && !window.IsDestroyed() {
			window.Invalidate()
		}
	})
}

func typelessReadErrorLabel(err error) string {
	var readErr *typeless.ReadError
	if !errors.As(err, &readErr) {
		return "配置读取失败"
	}
	switch readErr.Code() {
	case typeless.ErrorTooLarge:
		return "文件超过 1 MiB 限制"
	case typeless.ErrorStale:
		return "文件读取期间发生变化，请重新选择"
	case typeless.ErrorMalformedJSON:
		return "JSON 格式无效"
	case typeless.ErrorUnsupportedRoot, typeless.ErrorUnsupportedFile:
		return "所选文件类型不受支持"
	default:
		return "无法读取所选配置文件"
	}
}
