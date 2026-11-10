package main

import (
	"bytes"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	brand "mimac.local/mimac/Assets/Brand"
	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/platform/permissionguide"
)

const (
	permissionGuideWidth                          = 500
	permissionGuideTitle                  float32 = 26
	permissionGuideRowTitle               float32 = 14
	permissionGuideCaption                float32 = 12
	permissionGuideIconSize               float32 = 80
	permissionGuideIconBox                float32 = 35
	permissionGuideRowHeight              float32 = 72
	permissionGuidePollInterval                   = 700 * time.Millisecond
	permissionGuideFollowInterval                 = 33 * time.Millisecond
	permissionGuideSettingsLookupInterval         = 500 * time.Millisecond
	permissionGuideEntranceDuration               = 250 * time.Millisecond
	permissionGuideWindowGap                      = 12
)

type permissionGuideState struct {
	guideWindow       *mygo.Window
	snapshot          permissionGuideSnapshot
	dragPrompt        bool
	phase             permissionGuidePhase
	currentID         permissionGuideID
	settingsOpenedFor permissionGuideID
	generation        uint64
	pollTimer         *time.Timer
	animationTimer    *time.Timer
	followTimer       *time.Timer
	followActive      bool
	followManual      bool
	followTarget      mygo.Rectangle
	followStartBounds mygo.Rectangle
	animationStarted  time.Time
}

type permissionGuidePhase uint8

const (
	permissionGuideIdle permissionGuidePhase = iota
	permissionGuideWaiting
	permissionGuideComplete
)

type permissionGuideID uint8

const (
	permissionGuideInput permissionGuideID = iota + 1
	permissionGuidePostEvent
	permissionGuideBluetooth
)

type permissionGuideAccess uint8

const (
	permissionGuideUnknown permissionGuideAccess = iota
	permissionGuideNotDetermined
	permissionGuideNotGranted
	permissionGuideDenied
	permissionGuideRestricted
	permissionGuideGranted
)

type permissionGuideSnapshot struct {
	rows           []permissionGuideRow
	actionsEnabled bool
	appBundlePath  string
}

type permissionGuideRow struct {
	id          permissionGuideID
	icon        string
	title       string
	description string
	access      permissionGuideAccess
}

// showPermissionGuide opens or focuses the one native permission panel. The
// snapshot queries active capability owners and read-only OS authorization APIs.
func (s *shell) showPermissionGuide() {
	if s.closing {
		return
	}
	if s.guideWindow != nil && !s.guideWindow.IsDestroyed() {
		if !s.guideWindow.IsVisible() {
			s.settingsOpenedFor = 0
			s.snapshot = s.readPermissionGuideSnapshot()
			s.guideWindow.SetSize(permissionGuideWidth, permissionGuideWindowHeight(s.snapshot))
			s.showPermissionGuideWindow(s.guideWindow, s.generation)
		}
		s.refreshPermissionGuide()
		s.guideWindow.Focus()
		return
	}
	s.generation++
	generation := s.generation
	s.dragPrompt = false
	s.snapshot = s.readPermissionGuideSnapshot()
	s.phase = permissionGuideWaiting
	s.currentID = 0
	s.settingsOpenedFor = 0
	window := mygo.NewWindow(mygo.WindowOptions{
		Title: mygo.App.Name() + " 系统权限",
		Width: permissionGuideWidth, Height: permissionGuideWindowHeight(s.snapshot),
		DisableResize: true, DisableMinimize: true, DisableMaximize: true, DisableFullScreen: true,
		TitleBarStyle: mygo.TitleBarHidden, HideWindowButtons: true, Panel: true, Hidden: true,
		Content: ui.View(s.permissionGuideView),
	})
	s.guideWindow = window
	window.SetMovable(true)
	window.OnShow(func() {
		if s.guideWindow == window && s.generation == generation {
			s.schedulePermissionGuidePoll(window, generation)
		}
	})
	window.OnHide(func() {
		if s.guideWindow == window && s.generation == generation {
			s.stopPermissionGuidePolling()
			s.stopPermissionGuideAnimation()
			s.stopPermissionGuideFollowing()
		}
	})
	window.OnFocus(func() {
		if s.guideWindow == window && s.generation == generation {
			s.refreshPermissionGuide()
		}
	})
	window.OnMove(func() {
		if s.guideWindow == window && (s.followActive || s.followTimer != nil) {
			bounds := window.Bounds()
			if s.followActive && (bounds.X != s.followTarget.X || bounds.Y != s.followTarget.Y) {
				s.followManual = true
				s.stopPermissionGuideFollowing()
			} else if !s.followActive && (bounds.X != s.followStartBounds.X || bounds.Y != s.followStartBounds.Y) {
				s.followManual = true
				s.stopPermissionGuideFollowing()
			}
		}
	})
	window.OnClosed(func() {
		if s.guideWindow == window {
			s.cancelPermissionGuideTimers()
			permissionguide.CancelBluetoothRequest()
			s.guideWindow = nil
			s.dragPrompt = false
			s.phase = permissionGuideIdle
			s.currentID = 0
			s.settingsOpenedFor = 0
			s.generation++
		}
	})
	window.OnReadyToShow(func() {
		if s.guideWindow == window && s.generation == generation {
			s.showPermissionGuideWindow(window, generation)
		}
	})
}

// closePermissionGuide releases the panel through MyGo's normal window close
// path so a later explicit request can create a fresh panel.
func (s *shell) closePermissionGuide() {
	if s.guideWindow == nil || s.guideWindow.IsDestroyed() {
		return
	}
	s.cancelPermissionGuideTimers()
	permissionguide.CancelBluetoothRequest()
	s.guideWindow.Close()
}

func (s *shell) showPermissionGuideWindow(window *mygo.Window, generation uint64) {
	if window == nil || window.IsDestroyed() || s.guideWindow != window || s.generation != generation {
		return
	}
	s.stopPermissionGuideAnimation()
	window.SetOpacity(0)
	window.Show()
	if !window.IsVisible() {
		return
	}
	s.animationStarted = time.Now()
	s.animatePermissionGuideWindow(window, generation)
	s.advancePermissionGuide()
}

func (s *shell) animatePermissionGuideWindow(window *mygo.Window, generation uint64) {
	if window == nil || window.IsDestroyed() || s.guideWindow != window || s.generation != generation {
		return
	}
	progress := float64(time.Since(s.animationStarted)) / float64(permissionGuideEntranceDuration)
	progress = math.Max(0, math.Min(1, progress))
	eased := 1 - math.Pow(1-progress, 3)
	window.SetOpacity(eased)
	if progress >= 1 {
		s.animationTimer = nil
		return
	}
	s.animationTimer = time.AfterFunc(16*time.Millisecond, func() {
		mygo.RunOnMain(func() { s.animatePermissionGuideWindow(window, generation) })
	})
}

func (s *shell) advancePermissionGuide() {
	if s.closing || s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.guideWindow.IsVisible() {
		return
	}
	var nextID permissionGuideID
	if access, exists := permissionGuideAccessForID(s.snapshot, s.currentID); exists && access != permissionGuideGranted {
		nextID = s.currentID
	} else {
		for _, row := range s.snapshot.rows {
			if row.access != permissionGuideGranted {
				nextID = row.id
				break
			}
		}
	}
	if nextID == 0 {
		s.phase = permissionGuideComplete
		s.currentID = 0
		s.settingsOpenedFor = 0
		s.dragPrompt = false
		s.stopPermissionGuideFollowing()
		s.guideWindow.Invalidate()
		return
	}
	if s.currentID != nextID {
		s.currentID = nextID
		s.settingsOpenedFor = 0
		s.dragPrompt = false
		s.followManual = false
		s.stopPermissionGuideFollowing()
	}
	s.phase = permissionGuideWaiting
	access, _ := permissionGuideAccessForID(s.snapshot, nextID)
	if !s.snapshot.actionsEnabled || !permissionGuideAutoOpenSettings(access) || s.settingsOpenedFor == nextID {
		return
	}
	s.openPermissionGuideSettings(nextID, false)
}

func permissionGuideAccessForID(snapshot permissionGuideSnapshot, id permissionGuideID) (permissionGuideAccess, bool) {
	for _, row := range snapshot.rows {
		if row.id == id {
			return row.access, true
		}
	}
	return permissionGuideUnknown, false
}

func permissionGuideAutoOpenSettings(access permissionGuideAccess) bool {
	switch access {
	case permissionGuideNotDetermined, permissionGuideNotGranted, permissionGuideDenied:
		return true
	default:
		return false
	}
}

func (s *shell) schedulePermissionGuidePoll(window *mygo.Window, generation uint64) {
	if window == nil || window.IsDestroyed() || !window.IsVisible() || s.guideWindow != window || s.generation != generation || s.closing {
		return
	}
	s.stopPermissionGuidePolling()
	s.pollTimer = time.AfterFunc(permissionGuidePollInterval, func() {
		mygo.RunOnMain(func() {
			if s.guideWindow != window || s.generation != generation || s.closing || !window.IsVisible() {
				return
			}
			s.refreshPermissionGuide()
			s.schedulePermissionGuidePoll(window, generation)
		})
	})
}

func (s *shell) schedulePermissionGuideFollowing(window *mygo.Window, generation uint64) {
	s.stopPermissionGuideFollowing()
	if window == nil || window.IsDestroyed() || !window.IsVisible() || s.guideWindow != window || s.generation != generation {
		return
	}
	s.followActive = false
	s.followStartBounds = window.Bounds()
	s.schedulePermissionGuideFollowTick(window, generation, permissionGuideSettingsLookupInterval)
}

func (s *shell) followPermissionGuideSettings(window *mygo.Window, generation uint64) {
	if window == nil || window.IsDestroyed() || !window.IsVisible() || s.guideWindow != window || s.generation != generation || s.closing || s.followManual {
		s.stopPermissionGuideFollowing()
		return
	}
	settingsBounds, found := permissionguide.SystemSettingsWindowBounds()
	if !found {
		s.followActive = false
		s.schedulePermissionGuideFollowTick(window, generation, permissionGuideSettingsLookupInterval)
		return
	}
	display := mygo.Screen.DisplayMatching(mygo.Rectangle{
		X: int(math.Round(settingsBounds.X)), Y: int(math.Round(settingsBounds.Y)),
		Width: int(math.Round(settingsBounds.Width)), Height: int(math.Round(settingsBounds.Height)),
	})
	target, ok := permissionGuideCompanionBounds(settingsBounds, display.WorkArea, window.Bounds())
	if !ok {
		s.followActive = false
		s.schedulePermissionGuideFollowTick(window, generation, permissionGuideSettingsLookupInterval)
		return
	}
	s.followActive = true
	s.followTarget = target
	current := window.Bounds()
	if current.X != target.X || current.Y != target.Y {
		window.SetPosition(target.X, target.Y)
	}
	s.followStartBounds = target
	s.schedulePermissionGuideFollowTick(window, generation, permissionGuideFollowInterval)
}

func (s *shell) schedulePermissionGuideFollowTick(window *mygo.Window, generation uint64, interval time.Duration) {
	if window == nil || window.IsDestroyed() || !window.IsVisible() || s.guideWindow != window || s.generation != generation || s.closing || s.followManual {
		return
	}
	if s.followTimer != nil {
		s.followTimer.Stop()
	}
	s.followTimer = time.AfterFunc(interval, func() {
		mygo.RunOnMain(func() { s.followPermissionGuideSettings(window, generation) })
	})
}

func permissionGuideCompanionBounds(settings permissionguide.Rect, workArea, current mygo.Rectangle) (mygo.Rectangle, bool) {
	if settings.Width <= 0 || settings.Height <= 0 || workArea.Width <= 0 || workArea.Height <= 0 || current.Width <= 0 || current.Height <= 0 {
		return mygo.Rectangle{}, false
	}
	leftX := int(math.Floor(settings.X)) - permissionGuideWindowGap - current.Width
	rightX := int(math.Ceil(settings.X+settings.Width)) + permissionGuideWindowGap
	workRight := workArea.X + workArea.Width
	workBottom := workArea.Y + workArea.Height
	if current.Width > workArea.Width || current.Height > workArea.Height {
		return mygo.Rectangle{}, false
	}
	leftFits := leftX >= workArea.X && leftX+current.Width <= workRight
	rightFits := rightX >= workArea.X && rightX+current.Width <= workRight
	if !leftFits && !rightFits {
		return mygo.Rectangle{}, false
	}
	x := leftX
	if !leftFits || (rightFits && absInt(rightX-current.X) < absInt(leftX-current.X)) {
		x = rightX
	}
	y := current.Y
	if y < workArea.Y {
		y = workArea.Y
	}
	if y+current.Height > workBottom {
		y = workBottom - current.Height
	}
	return mygo.Rectangle{X: x, Y: y, Width: current.Width, Height: current.Height}, true
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func (s *shell) stopPermissionGuidePolling() {
	if s.pollTimer != nil {
		s.pollTimer.Stop()
		s.pollTimer = nil
	}
}

func (s *shell) stopPermissionGuideAnimation() {
	if s.animationTimer != nil {
		s.animationTimer.Stop()
		s.animationTimer = nil
	}
}

func (s *shell) stopPermissionGuideFollowing() {
	if s.followTimer != nil {
		s.followTimer.Stop()
		s.followTimer = nil
	}
	s.followActive = false
}

func (s *shell) cancelPermissionGuideTimers() {
	s.stopPermissionGuidePolling()
	s.stopPermissionGuideAnimation()
	s.stopPermissionGuideFollowing()
}

// refreshPermissionGuide derives display state from read-only permission APIs
// and the active device/audio owners. It does not acquire a device or start
// audio, even in a packaged Dev app.
func (s *shell) refreshPermissionGuide() {
	if s.closing || s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.guideWindow.IsVisible() {
		return
	}
	nextSnapshot := s.readPermissionGuideSnapshot()
	if permissionGuideWindowHeight(nextSnapshot) != permissionGuideWindowHeight(s.snapshot) {
		s.guideWindow.SetSize(permissionGuideWidth, permissionGuideWindowHeight(nextSnapshot))
	}
	s.snapshot = nextSnapshot
	s.advancePermissionGuide()
	s.guideWindow.Invalidate()
}

func permissionGuideWindowHeight(snapshot permissionGuideSnapshot) int {
	return 223 + len(snapshot.rows)*73
}

func (s *shell) readPermissionGuideSnapshot() permissionGuideSnapshot {
	rows := make([]permissionGuideRow, 0, 3)
	for _, id := range s.requiredPermissionGuideIDs() {
		row := permissionGuideRowFor(id)
		switch id {
		case permissionGuideInput:
			row.access = permissionGuideInputAccess(xiaomi.ListenAccess())
		case permissionGuidePostEvent:
			row.access = permissionGuideOutputAccess(keyoutput.ReadPermissionState())
		case permissionGuideBluetooth:
			bluetoothAccess := permissionguide.ReadBluetoothAccess()
			if bluetoothAccess != permissionguide.BluetoothAccessNotDetermined && bluetoothAccess != permissionguide.BluetoothAccessUnknown {
				// Release the passive manager after a terminal authorization
				// result; query failures do not prove a decision was made.
				permissionguide.CancelBluetoothRequest()
			}
			row.access = permissionGuideBluetoothAccess(bluetoothAccess)
		}
		rows = append(rows, row)
	}
	bundlePath := s.permissionGuideAppBundlePath()
	return permissionGuideSnapshot{
		rows:           rows,
		actionsEnabled: bundlePath != "",
		appBundlePath:  bundlePath,
	}
}

func (s *shell) requiredPermissionGuideIDs() []permissionGuideID {
	plugin := s.activeDevicePlugin()
	hasSelectedDevice := plugin.ModuleID != "" && plugin.Profile.ID != ""
	hasOutputMapping := s.coordinator != nil && s.coordinator.hasExecutableMapping()
	hasBluetoothVoiceRoute := s.audioRequested() && s.selectedAudioRouteTool() != nil
	return permissionGuideCapabilityIDs(hasSelectedDevice, hasOutputMapping, hasBluetoothVoiceRoute)
}

func (s *shell) permissionGuideAppBundlePath() string {
	if !mygo.App.IsPackaged() {
		return ""
	}
	bundleID, ok := permissionguide.MainBundleIdentifier()
	if !ok || !permissionGuideBundleIdentityAllowed(bundleID, mygo.App.Name(), true) {
		return ""
	}
	executable, err := mygo.App.Path(mygo.PathExe)
	if err != nil {
		return ""
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return ""
	}
	macOSDir := filepath.Dir(executable)
	contentsDir := filepath.Dir(macOSDir)
	bundlePath := filepath.Dir(contentsDir)
	if filepath.Base(macOSDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" || filepath.Ext(bundlePath) != ".app" {
		return ""
	}
	bundleInfo, err := os.Stat(bundlePath)
	if err != nil || !bundleInfo.IsDir() {
		return ""
	}
	infoPlist, err := os.Stat(filepath.Join(contentsDir, "Info.plist"))
	if err != nil || !infoPlist.Mode().IsRegular() {
		return ""
	}
	return bundlePath
}

func permissionGuideCapabilityIDs(hasSelectedDevice, hasOutputMapping, hasBluetoothVoiceRoute bool) []permissionGuideID {
	ids := make([]permissionGuideID, 0, 3)
	if hasSelectedDevice {
		ids = append(ids, permissionGuideInput)
	}
	if hasOutputMapping {
		ids = append(ids, permissionGuidePostEvent)
	}
	if hasBluetoothVoiceRoute {
		ids = append(ids, permissionGuideBluetooth)
	}
	return ids
}

func permissionGuideBundleIdentityAllowed(bundleID, appName string, packaged bool) bool {
	return packaged && ((bundleID == "dev.mimac.app" && appName == "MiMac") || (bundleID == "dev.mimac.app.dev" && appName == developmentAppName))
}

func permissionGuideRows(snapshot permissionGuideSnapshot) []permissionGuideRow {
	return append([]permissionGuideRow(nil), snapshot.rows...)
}

func permissionGuideRowFor(id permissionGuideID) permissionGuideRow {
	switch id {
	case permissionGuideInput:
		return permissionGuideRow{id: id, icon: "keyboard", title: "输入监控", description: "读取已选设备的按键并执行您设置的映射。"}
	case permissionGuidePostEvent:
		return permissionGuideRow{id: id, icon: "hand.tap", title: "辅助功能", description: "允许此应用向前台应用发送您配置的按键。"}
	case permissionGuideBluetooth:
		return permissionGuideRow{id: id, icon: "wave.3.right", title: "蓝牙", description: "让已选语音工具使用遥控器音频。"}
	default:
		return permissionGuideRow{id: id, title: "系统权限"}
	}
}

func permissionGuideInputAccess(state xiaomi.ListenAccessState) permissionGuideAccess {
	switch state {
	case xiaomi.ListenAccessNotDetermined:
		return permissionGuideNotDetermined
	case xiaomi.ListenAccessGranted:
		return permissionGuideGranted
	case xiaomi.ListenAccessDenied:
		return permissionGuideDenied
	default:
		return permissionGuideUnknown
	}
}

func permissionGuideOutputAccess(state keyoutput.PermissionState) permissionGuideAccess {
	switch state {
	case keyoutput.PermissionStateGranted:
		return permissionGuideGranted
	case keyoutput.PermissionStateNotGranted:
		return permissionGuideNotGranted
	default:
		return permissionGuideUnknown
	}
}

func permissionGuideBluetoothAccess(state permissionguide.BluetoothAccess) permissionGuideAccess {
	switch state {
	case permissionguide.BluetoothAccessNotDetermined:
		return permissionGuideNotDetermined
	case permissionguide.BluetoothAccessRestricted:
		return permissionGuideRestricted
	case permissionguide.BluetoothAccessDenied:
		return permissionGuideDenied
	case permissionguide.BluetoothAccessAuthorized:
		return permissionGuideGranted
	default:
		return permissionGuideUnknown
	}
}

func (s *shell) permissionGuideView(c *ui.Context) {
	a := appAppearance(c.Theme().Dark, s.preferencesState.value.TextSize)
	c.SetTheme(&a.theme)
	if s.appIcon == nil {
		if img, err := png.Decode(bytes.NewReader(brand.AboutIconPNG)); err == nil {
			s.appIcon = ui.NewBitmap(img)
		}
	}
	ui.Column(c).Fill().Background(a.theme.Surface).Padding(8, 0, 20, 0).Children(func() {
		ui.Row(c).FillWidth().Height(20).Padding(0, 8).AlignItems(ui.Center).Children(func() {
			close := ui.ButtonBase(c).Label("关闭系统权限面板").Width(20).Height(20).Padding(0).
				Background(a.theme.Border).Radius(10).AlignItems(ui.Center).Justify(ui.Center).FocusRing(false)
			close.Children(func() {
				// The shared monochrome symbol palette would paint the filled
				// circle and its cross the same color. Keep the native cross
				// separate from the neutral control background.
				if icon := s.systemSymbol("xmark", true, 11); icon != nil {
					ui.Image(c, icon).Width(11).Height(11).Fit(ui.Contain)
				}
			})
			if close.Clicked() {
				s.closePermissionGuide()
			}
			ui.Box(c).Grow(1).FillHeight().DragWindow()
		})
		ui.Box(c).FillWidth().Height(17).Shrink(0)
		ui.Column(c).FillWidth().Padding(0, 20).Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
				if s.appIcon != nil {
					icon := ui.Image(c, s.appIcon).Width(permissionGuideIconSize).Height(permissionGuideIconSize).Fit(ui.Contain).Label(mygo.App.Name() + " 应用图标")
					if s.snapshot.appBundlePath != "" {
						if data, err := transfer.FileData(s.snapshot.appBundlePath); err == nil {
							icon.DragData(data, transfer.DragOptions{Operations: transfer.Copy})
						}
					}
				}
				ui.Box(c).FillWidth().Height(19).Shrink(0)
				ui.Text(c, "管理权限").FontSize(permissionGuideTitle).FontWeight(700).TextAlign(ui.Center)
			})
			rows := permissionGuideRows(s.snapshot)
			if s.dragPrompt && s.snapshot.appBundlePath != "" {
				ui.Text(c, "将上方 "+mygo.App.Name()+" 图标拖入对应的权限列表。").FillWidth().Height(18).TextAlign(ui.Center).FontSize(permissionGuideCaption).TextColor(a.theme.TextMuted)
			} else if s.phase == permissionGuideComplete && len(rows) > 0 {
				ui.Text(c, "所需权限已授权，您可以关闭此面板。").FillWidth().Height(18).TextAlign(ui.Center).FontSize(permissionGuideCaption).TextColor(a.theme.TextMuted)
			} else {
				ui.Box(c).FillWidth().Height(18).Shrink(0)
			}
			if len(rows) == 0 {
				ui.Text(c, "当前配置不需要额外系统权限。您可以关闭此面板。").FillWidth().Height(44).TextAlign(ui.Center).FontSize(permissionGuideCaption).TextColor(a.theme.TextMuted)
			} else {
				ui.Column(c).Key("permission-guide-card").FillWidth().MinWidth(0).Border(lineWidth, a.theme.Border).Radius(12).Background(a.theme.Background).Children(func() {
					for index, row := range rows {
						if index > 0 {
							ui.Box(c).FillWidth().Padding(0, 11).Children(func() { sectionLine(c, a) })
						}
						s.permissionGuideRow(c, a, row)
					}
				})
			}
		})
	})
}

func (s *shell) permissionGuideRow(c *ui.Context, a appearance, row permissionGuideRow) {
	ui.Row(c).Key("permission-guide/"+permissionGuideIDString(row.id)).FillWidth().MinWidth(0).
		Height(permissionGuideRowHeight).Padding(0, 11).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Width(permissionGuideIconBox).Height(permissionGuideIconBox).Shrink(0).Radius(9).Background(a.theme.Surface).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			if icon := s.systemSymbol(row.icon, a.theme.Dark, 18); icon != nil {
				ui.Image(c, icon).Width(18).Height(18).Fit(ui.Contain).Label(row.title)
			}
		})
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(4).Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Text(c, row.title).SingleLine().FontSize(permissionGuideRowTitle).FontWeight(600).Shrink(1)
				s.permissionGuideOpenSettingsButton(c, a, row)
			})
			ui.Text(c, row.description).FillWidth().MinWidth(0).FontSize(permissionGuideCaption).TextColor(a.theme.TextMuted)
		})
		s.permissionGuideAccessControl(c, a, row)
	})
}

func (s *shell) permissionGuideOpenSettingsButton(c *ui.Context, a appearance, row permissionGuideRow) {
	button := ui.ButtonBase(c).Label("在系统设置中管理" + row.title).Width(18).Height(18).Shrink(0).Padding(0).FocusRing(false).Disabled(!s.snapshot.actionsEnabled)
	button.Children(func() {
		if icon := s.systemSymbol("arrow.up.right.circle", a.theme.Dark, 16); icon != nil {
			ui.Image(c, icon).Width(16).Height(16).Fit(ui.Contain)
		}
	})
	if button.Clicked() && s.snapshot.actionsEnabled {
		s.openPermissionGuideSettings(row.id, true)
	}
}

func (s *shell) permissionGuideAccessControl(c *ui.Context, a appearance, row permissionGuideRow) {
	if row.access == permissionGuideGranted {
		permissionGuideBadge(c, a, "✓", "已授权")
		return
	}
	if !s.snapshot.actionsEnabled || row.access == permissionGuideRestricted {
		permissionGuideBadge(c, a, "", permissionGuideAccessLabel(row.access))
		return
	}
	label := permissionGuideActionLabel(row)
	button := styleAppButton(ui.ButtonBase(c).Label(label), a, !s.snapshot.actionsEnabled).
		MinWidth(64).Height(28).Padding(0, 8).Radius(14)
	button.Children(func() { ui.Text(c, label).SingleLine().FontSize(permissionGuideCaption) })
	if button.Clicked() && s.snapshot.actionsEnabled {
		s.performPermissionGuideAction(row.id)
	}
}

func permissionGuideActionLabel(row permissionGuideRow) string {
	switch row.id {
	case permissionGuideInput:
		switch row.access {
		case permissionGuideNotDetermined:
			return "请求授权"
		case permissionGuideDenied:
			return "打开设置"
		case permissionGuideUnknown:
			return "重试"
		}
	case permissionGuidePostEvent:
		switch row.access {
		case permissionGuideUnknown:
			return "重试"
		case permissionGuideDenied:
			return "打开设置"
		case permissionGuideNotGranted:
			return "请求授权"
		default:
			return "打开设置"
		}
	case permissionGuideBluetooth:
		switch row.access {
		case permissionGuideNotDetermined:
			return "请求授权"
		case permissionGuideDenied:
			return "打开设置"
		case permissionGuideUnknown:
			return "重试"
		}
	}
	return "打开设置"
}

func permissionGuideAccessLabel(access permissionGuideAccess) string {
	switch access {
	case permissionGuideGranted:
		return "已授权"
	case permissionGuideNotDetermined, permissionGuideNotGranted:
		return "未授权"
	case permissionGuideDenied:
		return "已拒绝"
	case permissionGuideRestricted:
		return "受系统限制"
	default:
		return "无法读取"
	}
}

func permissionGuideBadge(c *ui.Context, a appearance, symbol, label string) {
	ui.Row(c).Height(26).Shrink(0).AlignItems(ui.Center).Gap(4).Padding(0, 9).Radius(13).Background(a.theme.Surface).Children(func() {
		if symbol != "" {
			ui.Text(c, symbol).FontSize(permissionGuideCaption).FontWeight(600)
		}
		ui.Text(c, label).SingleLine().FontSize(permissionGuideCaption).TextColor(a.theme.Text)
	})
}

func (s *shell) performPermissionGuideAction(id permissionGuideID) {
	if s.closing || s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.snapshot.actionsEnabled {
		return
	}
	access, exists := permissionGuideAccessForID(s.snapshot, id)
	if !exists {
		return
	}
	if access == permissionGuideUnknown {
		s.refreshPermissionGuide()
		return
	}
	if access == permissionGuideDenied {
		s.openPermissionGuideSettings(id, true)
		return
	}
	switch id {
	case permissionGuideInput:
		if access == permissionGuideNotDetermined {
			_ = xiaomi.RequestListenAccess()
		}
	case permissionGuidePostEvent:
		if access == permissionGuideNotGranted {
			_ = keyoutput.RequestAccess()
		}
	case permissionGuideBluetooth:
		if access == permissionGuideNotDetermined {
			_ = permissionguide.RequestBluetoothAccess()
		}
	}
	s.refreshPermissionGuide()
	if nextAccess, exists := permissionGuideAccessForID(s.snapshot, id); exists && nextAccess != permissionGuideGranted && s.settingsOpenedFor != id {
		s.openPermissionGuideSettings(id, false)
	}
}

func (s *shell) openPermissionGuideSettings(id permissionGuideID, force bool) {
	if s.closing || s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.snapshot.actionsEnabled {
		return
	}
	if !force && s.settingsOpenedFor == id {
		return
	}
	if _, exists := permissionGuideAccessForID(s.snapshot, id); !exists {
		return
	}
	s.currentID = id
	s.phase = permissionGuideWaiting
	s.settingsOpenedFor = id
	s.followManual = false
	s.stopPermissionGuideFollowing()
	switch id {
	case permissionGuideInput:
		s.openInputPermissionSettings()
	case permissionGuidePostEvent:
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility")
	case permissionGuideBluetooth:
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_Bluetooth")
	}
	s.dragPrompt = permissionGuideAllowsAppDrag(id) && s.snapshot.appBundlePath != ""
	s.guideWindow.Focus()
	s.schedulePermissionGuideFollowing(s.guideWindow, s.generation)
	s.guideWindow.Invalidate()
}

func permissionGuideAllowsAppDrag(id permissionGuideID) bool {
	return id == permissionGuideInput || id == permissionGuidePostEvent
}

func permissionGuideIDString(id permissionGuideID) string {
	switch id {
	case permissionGuideInput:
		return "input-monitoring"
	case permissionGuidePostEvent:
		return "post-event"
	case permissionGuideBluetooth:
		return "bluetooth-audio"
	default:
		return "unknown"
	}
}
