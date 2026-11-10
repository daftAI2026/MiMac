package main

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	brand "mimac.local/mimac/Assets/Brand"
)

// Settings state survives disposal of the settings window.
type settingsState struct {
	selectedButton string
	revealButton   string

	page                  settingsPage
	backPages             []settingsPage
	forwardPages          []settingsPage
	profile               mappingProfile
	mappingPath           string
	mappingError          string
	mappingLoadFailed     bool
	menuOpen              bool
	editButton            string
	recorderAccelerators  map[*mygo.MenuItem]string
	recorderFocus         ui.Handle
	outputStatus          string
	outputNeedsPermission bool
	outputNeedsRestart    bool

	deviceReadBusy        bool
	deviceNeedsConnection bool
	deviceReadSequence    uint64
	deviceStatus          string
	deviceDetail          string
	deviceNeedsPermission bool
	deviceNeedsRestart    bool
	deviceLastPress       string
	deviceTone            statusTone
	brandMark             *ui.SVG
	modelAssets           map[string]modelArtwork
	navigationSymbols     map[symbolKey]*ui.Bitmap
	appIcon               *ui.Bitmap
	socialIcons           map[string]*ui.SVG
	voiceToolIcons        map[string]*voiceToolIcon
	aboutError            string
}

type settingsPage string

const (
	pageGeneral  settingsPage = "通用"
	pageAbout    settingsPage = "关于"
	pageMappings settingsPage = "按键映射"
	pageDevice   settingsPage = "设备"
	pageVoice    settingsPage = "语音工具"
	pagePlugins  settingsPage = "插件"
)

func (s *shell) settings(c *ui.Context) {
	s.syncRecorderAccelerators()
	if s.brandMark == nil {
		s.brandMark = ui.MustParseSVG([]byte(brand.SVG))
	}
	if s.editButton == "" && (c.Shortcut(ui.Cmd, ui.KeyW) || c.Shortcut(0, ui.KeyEscape)) {
		if s.window != nil {
			s.window.Close()
		}
		return
	}
	a := appAppearance(c.Theme().Dark, s.preferencesState.value.TextSize)
	c.SetTheme(&a.theme)
	if s.page == "" {
		s.page = pageMappings
	}
	if s.page != pageMappings {
		s.clearReadyIndicator()
	}
	ui.Row(c).Fill().Children(func() {
		ui.Column(c).Width(sidebarWidth).Shrink(0).FillHeight().Padding(0, spaceMD, sectionGap, spaceMD).Background(a.theme.Surface).Children(func() {
			ui.Box(c).Height(max(c.TitleBar().Height, sidebarHeaderHeight)).FillWidth().Shrink(0).DragWindow()
			s.navigationItems(c, a)
			ui.Box(c).Grow(1)
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(sidebarLabelGap).Padding(0, spaceMD, 0, sidebarItemInset).Children(func() {
				ui.Icon(c, s.brandMark).FontSize(settingsIconSize).TextColor(a.theme.Text)
				ui.Text(c, mygo.App.Name()).FontSize(a.fonts.body).TextColor(a.theme.Text)
			})
		})
		ui.Box(c).Width(lineWidth).FillHeight().Shrink(0).Background(a.separator)
		ui.Column(c).Grow(1).Basis(0).FillHeight().MinWidth(0).Children(func() {
			ui.Box(c).Height(contentHeaderHeight).FillWidth().Shrink(0).DragWindow()
			ui.Box(c).Grow(1).Basis(0).FillWidth().MinHeight(0).MinWidth(0).Padding(settingsPaneTop, settingsPaneInset, settingsPaneInset, settingsPaneInset).Children(func() {
				if s.page == pageGeneral {
					s.generalSettings(c, a)
					return
				}
				if s.page == pageAbout {
					s.aboutSettings(c, a)
					return
				}
				if s.page == pageMappings {
					s.mappingSettings(c, a)
					return
				}
				ui.Scroll(c).Fill().Children(func() {
					ui.Column(c).FillWidth().MinWidth(0).Gap(sectionGap).Children(func() {
						switch s.page {
						case pageDevice:
							s.deviceSettings(c, a)
						case pageVoice:
							s.voiceSettings(c, a)
						case pagePlugins:
							s.pluginSettings(c, a)
						}
					})
				})
			})
		})
	})
}

func (s *shell) deviceSettings(c *ui.Context, a appearance) {
	settingsGroup(c, a, func() {
		ui.Row(c).AlignItems(ui.Center).FillWidth().MinHeight(settingsRowHeight).Padding(settingsRowPadding, 0).Children(func() {
			ui.Text(c, s.activeProfile().Name).FontSize(a.fonts.body).FontWeight(600).Grow(1)
			if appButton(c, a, "按键映射", false).Clicked() {
				s.navigate(pageMappings)
			}
		})
		sectionLine(c, a)
		settingsValueRow(c, &a.theme, a.fonts.body, "连接", s.deviceConnectionLabel())
		sectionLine(c, a)
		ui.Row(c).FillWidth().MinWidth(0).MinHeight(settingsRowHeight).AlignItems(ui.Center).Justify(ui.End).Gap(spaceSM).Children(func() {
			if appButton(c, a, "系统权限…", false).Clicked() {
				s.showPermissionGuide()
			}
			if appButton(c, a, "蓝牙设置", false).Clicked() {
				s.performSetupAction(setupBluetoothConnection)
			}
			disabled := s.deviceReadBusy || s.monitor != nil
			if appButton(c, a, "重新检测", disabled).Clicked() && !disabled {
				s.detectDevice()
			}
		})
		if s.deviceDetail != "" {
			ui.Text(c, s.deviceDetail).FontSize(a.fonts.caption).TextColor(a.theme.TextMuted).Padding(spaceSM, 0)
		}
		if action := s.nextSetupAction(); action != setupNone {
			ui.Row(c).FillWidth().Justify(ui.End).Gap(spaceSM).Padding(spaceSM, 0).Children(func() {
				if action != setupBluetoothConnection && appButton(c, a, action.label(), false).Clicked() {
					s.performSetupAction(action)
				}
				if action == setupBluetoothPermission || action == setupBluetoothConnection {
					_, _, blocked := s.audioRetryBlocked()
					if appButton(c, a, "重新连接音频", blocked).Clicked() && !blocked {
						s.performSetupAction(setupRetryAudio)
					}
				}
			})
		}
		if s.deviceNeedsPermission {
			ui.Text(c, "授权后，在按键映射中启用遥控器。").FontSize(a.fonts.caption).TextColor(a.theme.TextMuted)
		}
	})
	if s.deviceNeedsConnection {
		guide := s.activeProfile().ConnectionGuide
		if guide.FirstPairing == "" {
			guide.FirstPairing = "按照设备说明进入配对模式，然后在蓝牙设置中选择设备连接。"
		}
		if guide.Reconnect == "" {
			guide.Reconnect = "唤醒设备，然后在蓝牙设置中连接。"
		}
		settingsGroup(c, a, func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(spaceSM).Children(func() {
				ui.Text(c, "首次连接").FontSize(a.fonts.body).FontWeight(600)
				ui.Text(c, guide.FirstPairing).FontSize(a.fonts.body).TextColor(a.theme.TextMuted)
				sectionLine(c, a)
				ui.Text(c, "已经配对").FontSize(a.fonts.body).FontWeight(600)
				ui.Text(c, guide.Reconnect).FontSize(a.fonts.body).TextColor(a.theme.TextMuted)
			})
		})
	}
}

// Connection is a projection of discovery or the existing live device owner,
// independently of whether the user has enabled mapped output.
func (s *shell) deviceConnectionLabel() string {
	if s.monitor != nil && s.coordinator != nil && s.coordinator.core.Snapshot().Connected {
		return "已连接"
	}
	if s.deviceReadBusy {
		return "正在检测"
	}
	switch s.deviceStatus {
	case "", "未启用", "已停止监听", "正在监听":
		return "尚未检测"
	case "等待支持的遥控器":
		return "未连接"
	default:
		return s.deviceStatus
	}
}

func (s *shell) detectDevice() {
	discover := s.activeDevicePlugin().Discover
	if discover == nil {
		return
	}
	s.deviceReadSequence++
	sequence := s.deviceReadSequence
	s.deviceReadBusy = true
	s.deviceStatus = "正在检测"
	s.deviceTone = toneNeutral
	go func() {
		result, err := discover()
		mygo.RunOnMain(func() {
			if s.closing || sequence != s.deviceReadSequence {
				return
			}
			s.deviceReadBusy = false
			s.deviceDetail = ""
			s.deviceTone = toneNeutral
			s.deviceNeedsConnection = err == nil && result.Count == 0
			switch {
			case err != nil:
				s.deviceStatus = "检测失败"
				s.deviceTone = toneError
			case result.Count == 0:
				s.deviceStatus = "未连接"
			case result.Count > 1:
				s.deviceStatus = fmt.Sprintf("发现 %d 个匹配项", result.Count)
				s.deviceTone = toneWarning
			default:
				s.deviceStatus = "已连接"
				if !result.Supported {
					s.deviceStatus = "按键格式暂不支持"
					s.deviceTone = toneWarning
				}
			}
			s.invalidateDeviceView()
		})
	}()
}

func settingsValueRow(c *ui.Context, t *ui.Theme, size float32, label, value string) {
	ui.Row(c).Gap(spaceLG).AlignItems(ui.Center).FillWidth().MinWidth(0).MinHeight(settingsRowHeight).Padding(settingsRowPadding, 0).Children(func() {
		ui.Text(c, label).MinWidth(labelColumnWidth).Shrink(0).SingleLine().FontSize(size)
		ui.Text(c, value).Grow(1).Shrink(1).MinWidth(0).FontSize(size).TextColor(t.TextMuted).TextAlign(ui.End)
	})
}

func settingsQuotedPreview(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) > maxRunes {
		return shortcutPreview(string(runes[:maxRunes])) + "…"
	}
	return shortcutPreview(value)
}

func shortcutPreview(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}
