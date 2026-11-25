package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/adapters/applespeech"
	"mimac.local/mimac/internal/voicetools"
)

const (
	appleSpeechPreferencesFile = "apple-speech.json"
	defaultAppleSpeechLocale   = "zh-CN"
)

var errAppleSpeechLocalesPending = errors.New("Apple Speech locale inventory is not loaded")

type appleSpeechPreferences struct {
	Version int    `json:"version"`
	Locale  string `json:"locale"`
}

var appleSpeechPreferenceCache sync.Map // app-data path -> appleSpeechPreferences

var appleSpeechLocaleCache sync.Map // speech dylib path -> appleSpeechLocaleCacheEntry

type appleSpeechLocaleCacheEntry struct {
	locales []applespeech.Locale
	err     error
}

func validAppleSpeechPreferences(value appleSpeechPreferences) bool {
	if value.Version != 1 || value.Locale == "" || len(value.Locale) > 64 || !utf8.ValidString(value.Locale) ||
		strings.TrimSpace(value.Locale) != value.Locale || strings.ContainsAny(value.Locale, "\x00 \t\r\n") {
		return false
	}
	for _, part := range strings.Split(value.Locale, "-") {
		if part == "" || len(part) > 8 {
			return false
		}
		for _, r := range part {
			if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				return false
			}
		}
	}
	return true
}

func readAppleSpeechPreferences(path string) (appleSpeechPreferences, error) {
	data, err := voicetools.ReadRegularFile(context.Background(), path, 4096)
	if errors.Is(err, voicetools.ErrFileMissing) {
		return appleSpeechPreferences{Version: 1, Locale: defaultAppleSpeechLocale}, nil
	}
	if err != nil {
		return appleSpeechPreferences{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value appleSpeechPreferences
	if err := decoder.Decode(&value); err != nil {
		return appleSpeechPreferences{}, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || !validAppleSpeechPreferences(value) {
		return appleSpeechPreferences{}, errors.New("unsupported Apple Speech preferences")
	}
	return value, nil
}

func saveAppleSpeechPreferences(path string, value appleSpeechPreferences) error {
	if path == "" || !validAppleSpeechPreferences(value) {
		return errors.New("invalid Apple Speech preferences")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".apple-speech-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func currentAppleSpeechLocale() (string, error) {
	path, err := hostDataPath(appleSpeechPreferencesFile)
	if err != nil {
		return "", err
	}
	if cached, ok := appleSpeechPreferenceCache.Load(path); ok {
		return cached.(appleSpeechPreferences).Locale, nil
	}
	value, err := readAppleSpeechPreferences(path)
	if err != nil {
		return "", err
	}
	actual, _ := appleSpeechPreferenceCache.LoadOrStore(path, value)
	return actual.(appleSpeechPreferences).Locale, nil
}

// Route factories take one locale snapshot per route. Open Apple Speech
// sessions keep that locale for their full lifetime, even as preferences
// later change for a newly constructed route.
func selectedAppleSpeechRouteLocale() (string, error) {
	return currentAppleSpeechLocale()
}

func appleSpeechPreferencesPath() (string, error) {
	return hostDataPath(appleSpeechPreferencesFile)
}

// Apple model work calls this from the voice-tool worker. Settings rendering
// only reads its snapshot and never waits on Swift's asynchronous locale APIs.
func refreshAppleSpeechLocaleInventory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resources, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return err
	}
	library := filepath.Join(resources, "libMiMacSpeech.dylib")
	appleSpeechLocaleCache.Delete(library)
	locales, err := applespeech.SupportedLocales(library)
	entry := appleSpeechLocaleCacheEntry{locales: append([]applespeech.Locale(nil), locales...), err: err}
	appleSpeechLocaleCache.Store(library, entry)
	return err
}

func appleSpeechSupportedLocales() ([]applespeech.Locale, error) {
	resources, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return nil, err
	}
	library := filepath.Join(resources, "libMiMacSpeech.dylib")
	cached, ok := appleSpeechLocaleCache.Load(library)
	if !ok {
		return nil, errAppleSpeechLocalesPending
	}
	entry := cached.(appleSpeechLocaleCacheEntry)
	return append([]applespeech.Locale(nil), entry.locales...), entry.err
}

func appleSpeechLocaleDisplayName(identifier string) string {
	locales, err := appleSpeechSupportedLocales()
	if err != nil {
		return identifier
	}
	for _, locale := range locales {
		if locale.Identifier == identifier {
			return locale.DisplayName
		}
	}
	return identifier
}

func (s *shell) appleSpeechSettings(c *ui.Context, a appearance, tool *voiceTool) {
	sectionLine(c, a)
	locales, err := appleSpeechSupportedLocales()
	selected, prefErr := currentAppleSpeechLocale()
	if prefErr != nil {
		ui.Text(c, "Apple 语音语言设置读取失败，原文件已保留").FontSize(a.fonts.caption).TextColor(a.theme.Danger).Role(ui.RoleStatus)
		return
	}
	if errors.Is(err, errAppleSpeechLocalesPending) {
		ui.Text(c, "正在读取 Apple 支持的语音语言").FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Role(ui.RoleStatus)
		return
	}
	if err != nil {
		ui.Text(c, "无法读取此 Mac 支持的 Apple 语音语言").FontSize(a.fonts.caption).TextColor(a.theme.Danger).Role(ui.RoleStatus)
		if appButton(c, a, "重新获取语言", false).Clicked() {
			s.refreshVoiceTool(tool)
			c.Invalidate()
		}
		return
	}

	selectedName := selected
	selectedInstalled := false
	selectedSupported := false
	for _, locale := range locales {
		if locale.Identifier == selected {
			selectedName = locale.DisplayName
			selectedInstalled = locale.Installed
			selectedSupported = true
			break
		}
	}
	if !selectedSupported {
		selectedName += "（此 Mac 不再支持）"
	}
	ui.Row(c).FillWidth().MinWidth(0).MinHeight(settingsRowHeight).AlignItems(ui.Center).Gap(spaceLG).Padding(settingsRowPadding, 0).Children(func() {
		ui.Text(c, "识别语言").MinWidth(labelColumnWidth).Shrink(0).SingleLine().FontSize(a.fonts.body)
		label := selectedName
		if selectedSupported {
			if selectedInstalled {
				label += " · 已安装"
			} else {
				label += " · 需下载"
			}
		}
		button := appButton(c, a, label, tool.busy || s.appleSpeechSessionActive()).Grow(1).MinWidth(0).Label("选择 Apple 语音识别语言")
		if button.Clicked() {
			s.popupMenu(appleSpeechLocaleMenu(s, c, tool, locales, selected), button.Bounds())
		}
	})
}

func appleSpeechLocaleMenu(s *shell, c *ui.Context, tool *voiceTool, locales []applespeech.Locale, selected string) *mygo.Menu {
	items := make([]*mygo.MenuItem, 0, len(locales))
	for _, available := range locales {
		locale := available
		label := fmt.Sprintf("%s (%s)", locale.DisplayName, locale.Identifier)
		if locale.Installed {
			label += " · 已安装"
		} else {
			label += " · 需下载"
		}
		items = append(items, &mygo.MenuItem{
			Label:   label,
			Checked: locale.Identifier == selected,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				if s.setAppleSpeechLocale(tool, locale.Identifier) != nil {
					s.toolError = "无法切换 Apple 语音识别语言"
				}
				c.Invalidate()
			},
		})
	}
	return mygo.NewMenu(items)
}

func (s *shell) setAppleSpeechLocale(tool *voiceTool, identifier string) error {
	if s == nil || s.closing || tool == nil || tool.id != appleSpeechToolID {
		return errors.New("Apple Speech is busy")
	}
	current, err := currentAppleSpeechLocale()
	if err != nil {
		return err
	}
	if current == identifier {
		return nil
	}
	if tool.busy || s.appleSpeechSessionActive() || s.liveAudio.retiring {
		return errors.New("请结束当前语音会话后再切换语言")
	}
	locales, err := appleSpeechSupportedLocales()
	if err != nil {
		return err
	}
	var supported bool
	for _, locale := range locales {
		if locale.Identifier == identifier {
			supported = true
			break
		}
	}
	if !supported {
		return fmt.Errorf("locale %q is not supported by this Mac", identifier)
	}
	path, err := appleSpeechPreferencesPath()
	if err != nil {
		return err
	}
	value := appleSpeechPreferences{Version: 1, Locale: identifier}
	if err := saveAppleSpeechPreferences(path, value); err != nil {
		return err
	}
	appleSpeechPreferenceCache.Store(path, value)
	s.toolError = ""
	if s.liveAudio.connection != nil && s.liveAudio.toolID == appleSpeechToolID {
		s.retireLiveAudio()
	}
	s.refreshVoiceTool(tool)
	s.invalidateDeviceView()
	return nil
}

func (s *shell) appleSpeechSessionActive() bool {
	if s == nil || s.coordinator == nil || s.coordinator.core == nil {
		return false
	}
	snapshot := s.coordinator.core.Snapshot()
	return snapshot.HasSession || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 0
}

func appleSpeechStatusForLocale(tool *voiceTool, locale string) string {
	name := appleSpeechLocaleDisplayName(locale)
	if !tool.active {
		return "未启用"
	}
	if tool.busy {
		if tool.cancelRequested {
			return "正在取消；等待 Apple 模型操作结束"
		}
		if tool.preparingAssets {
			return "正在准备 " + name + " 模型"
		}
		return "正在检查 " + name + " 模型"
	}
	if tool.snapshot.Source == appleSpeechCleanupPendingSource {
		return "等待 Apple 模型操作结束，请重新检查"
	}
	if tool.lastAttemptCancelled {
		return "准备已取消"
	}
	switch tool.snapshot.State {
	case voicetools.Ready:
		return name + " 模型已就绪"
	case voicetools.ConfigMissing:
		return "尚未安装 " + name + " 模型"
	case voicetools.Unsupported:
		return "此 Mac 暂不支持 " + name
	default:
		return name + " 模型检查失败，请重试"
	}
}
