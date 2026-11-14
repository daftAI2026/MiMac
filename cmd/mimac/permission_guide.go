package main

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	brand "mimac.local/mimac/Assets/Brand"
	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/platform/permissionguide"
	"mimac.local/mimac/internal/platform/statuscolor"
)

const (
	permissionGuideWidth                          = 500
	permissionGuideChromeHeight                   = 249
	permissionGuideTitle                  float32 = 26
	permissionGuideRowTitle               float32 = 14
	permissionGuideCaption                float32 = 12
	permissionGuideLinkIconSize           float32 = 13
	permissionGuideLinkIconWeight                 = .3
	permissionGuideIconSize               float32 = 80
	permissionGuideIconSlot               float32 = 96
	permissionGuideIconBox                float32 = 35
	permissionGuideControlWidth           float32 = 76
	permissionGuidePollInterval                   = 700 * time.Millisecond
	permissionGuideFollowInterval                 = 33 * time.Millisecond
	permissionGuideSettingsLookupInterval         = 100 * time.Millisecond
	permissionGuideSettingsSearchDuration         = 6 * time.Second
	permissionGuideWindowGap                      = 16
)

type permissionGuideState struct {
	guideWindow         *mygo.Window
	snapshot            permissionGuideSnapshot
	phase               permissionGuidePhase
	currentID           permissionGuideID
	settingsOpenedFor   permissionGuideID
	generation          uint64
	pollTimer           *time.Timer
	followTimer         *time.Timer
	followActive        bool
	followManual        bool
	followTarget        mygo.Rectangle
	followStartBounds   mygo.Rectangle
	followMoveStarted   time.Time
	followMoveFrom      mygo.Rectangle
	followMoveTime      time.Duration
	followMoveInitial   bool
	settingsSearchUntil time.Time
	settingsCandidate   permissionguide.WindowSnapshot
	followWindowID      uint32
	followMoving        bool
	iconDragPreview     *image.RGBA
	iconBounds          ui.Rect
	iconDragging        bool
	hasDraggedIcon      bool
	reduceMotion        bool
	illustration        *permissionGuideIllustration
	dropGuide           *permissionGuideDrop
	grantedStyles       [2]permissionGuideGrantedStyle
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
	s.preparePermissionGuideGrantedStyles()
	generation := s.generation
	s.iconBounds = ui.Rect{}
	s.iconDragging, s.hasDraggedIcon = false, false
	s.snapshot = s.readPermissionGuideSnapshot()
	s.phase = permissionGuideWaiting
	s.currentID = 0
	s.settingsOpenedFor = 0
	window := mygo.NewWindow(mygo.WindowOptions{
		Title: mygo.App.Name() + " 系统权限",
		Width: permissionGuideWidth, Height: permissionGuideWindowHeight(s.snapshot), Panel: true,
		DisableResize: true, DisableMinimize: true, DisableMaximize: true, DisableFullScreen: true,
		TitleBarStyle: mygo.TitleBarHidden, HideWindowButtons: true, Hidden: true,
		Content: ui.View(s.permissionGuideView),
	})
	s.guideWindow = window
	permissionguide.ConfigurePermissionPanel(window)
	window.SetMovable(true)
	window.OnShow(func() {
		if s.guideWindow == window && s.generation == generation {
			s.schedulePermissionGuidePoll(window, generation)
		}
	})
	window.OnHide(func() {
		if s.guideWindow == window && s.generation == generation {
			s.stopPermissionGuidePolling()
			s.stopPermissionGuideFollowing()
			s.closePermissionGuideDrop()
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
			if bounds.X != s.followStartBounds.X || bounds.Y != s.followStartBounds.Y {
				s.followManual = true
				s.stopPermissionGuideFollowing()
			}
		}
	})
	window.OnClosed(func() {
		if s.guideWindow == window {
			s.cancelPermissionGuideTimers()
			s.closePermissionGuideIllustration()
			permissionguide.CancelBluetoothRequest()
			s.guideWindow = nil
			s.iconDragPreview = nil
			s.grantedStyles = [2]permissionGuideGrantedStyle{}
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
	window.Show()
	if !window.IsVisible() {
		return
	}
	s.advancePermissionGuide()
	// An existing Settings session is routed to the missing list before joining.
	// A closed Settings app remains closed until an explicit row action.
	if len(s.snapshot.rows) > 0 && s.phase == permissionGuideWaiting && !s.followManual {
		runPermissionGuidePresentation(s.snapshot, permissionguide.SystemSettingsRunning(),
			func(id permissionGuideID) { s.openPermissionGuideSettings(id, true) },
			func() { s.schedulePermissionGuideFollowing(window, generation) })
	}
}

func (s *shell) advancePermissionGuide() {
	if s.closing || s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.guideWindow.IsVisible() {
		return
	}
	var nextID permissionGuideID
	s.phase = permissionGuideComplete
	for _, row := range s.snapshot.rows {
		if row.access != permissionGuideGranted {
			s.phase = permissionGuideWaiting
			break
		}
	}
	if _, exists := permissionGuideAccessForID(s.snapshot, s.settingsOpenedFor); exists {
		nextID = s.settingsOpenedFor
	} else if access, exists := permissionGuideAccessForID(s.snapshot, s.currentID); exists && access != permissionGuideGranted {
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
		s.stopPermissionGuideFollowing()
		s.guideWindow.Invalidate()
		return
	}
	if s.currentID != nextID {
		s.currentID = nextID
		s.settingsOpenedFor = 0
		s.followManual = false
		s.stopPermissionGuideFollowing()
	}
}

func permissionGuideAccessForID(snapshot permissionGuideSnapshot, id permissionGuideID) (permissionGuideAccess, bool) {
	for _, row := range snapshot.rows {
		if row.id == id {
			return row.access, true
		}
	}
	return permissionGuideUnknown, false
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
	s.settingsSearchUntil = time.Now().Add(permissionGuideSettingsSearchDuration)
	s.schedulePermissionGuideFollowTick(window, generation, permissionGuideSettingsLookupInterval)
}

func (s *shell) followPermissionGuideSettings(window *mygo.Window, generation uint64) {
	if window == nil || window.IsDestroyed() || !window.IsVisible() || s.guideWindow != window || s.generation != generation || s.closing || s.followManual {
		s.stopPermissionGuideFollowing()
		return
	}
	settings, found := permissionguide.SystemSettingsWindowSnapshot()
	now := time.Now()
	ready, expired := s.permissionGuideSettingsJoinReady(settings, found, now)
	if !ready {
		s.hidePermissionGuideIllustration()
		if expired {
			s.stopPermissionGuideFollowing()
		} else {
			s.schedulePermissionGuideFollowTick(window, generation, permissionGuideSettingsLookupInterval)
		}
		return
	}
	initial := !s.followActive
	settingsBounds := settings.Frame
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
	current := window.Bounds()
	if !s.followActive || target != s.followTarget {
		s.followMoveFrom = current
		s.followMoveStarted = time.Now()
		s.followMoveTime = 250 * time.Millisecond
		s.followMoveInitial = initial
		if initial {
			s.followMoveTime = 350 * time.Millisecond
		}
		s.followTarget = target
		s.hidePermissionGuideIllustration()
	}
	s.followActive = true
	s.followWindowID = settings.ID
	pointerDown := permissionguide.PrimaryMouseButtonDown()
	position, moving := s.nextPermissionGuideFollowFrame(target, time.Now(), s.reduceMotion || pointerDown)
	s.followMoving = moving
	s.followStartBounds = position
	if current.X != position.X || current.Y != position.Y {
		window.SetPosition(position.X, position.Y)
	}
	if pointerDown || s.followMoving {
		s.hidePermissionGuideIllustration()
	} else {
		s.syncPermissionGuideIllustration(settingsBounds, display.WorkArea)
	}
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
	leftSpace := settings.X - float64(workArea.X)
	rightSpace := float64(workRight) - (settings.X + settings.Width)
	x := rightX
	if rightSpace < float64(current.Width+permissionGuideWindowGap) && leftSpace > rightSpace {
		x = leftX
	}
	x = max(workArea.X, min(x, workRight-current.Width))
	y := int(math.Round(settings.Y))
	if y < workArea.Y {
		y = workArea.Y
	}
	if y+current.Height > workBottom {
		y = workBottom - current.Height
	}
	return mygo.Rectangle{X: x, Y: y, Width: current.Width, Height: current.Height}, true
}

func (s *shell) stopPermissionGuidePolling() {
	if s.pollTimer != nil {
		s.pollTimer.Stop()
		s.pollTimer = nil
	}
}

func (s *shell) stopPermissionGuideFollowing() {
	if s.followTimer != nil {
		s.followTimer.Stop()
		s.followTimer = nil
	}
	s.followActive = false
	s.settingsSearchUntil = time.Time{}
	s.settingsCandidate = permissionguide.WindowSnapshot{}
	s.followWindowID = 0
	s.followMoving = false
	s.hidePermissionGuideIllustration()
}

func (s *shell) cancelPermissionGuideTimers() {
	s.closePermissionGuideDrop()
	s.stopPermissionGuidePolling()
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
	if len(nextSnapshot.rows) != len(s.snapshot.rows) {
		s.guideWindow.SetSize(permissionGuideWidth, permissionGuideWindowHeight(nextSnapshot))
	}
	s.snapshot = nextSnapshot
	s.updateOnboardingPermissions(nextSnapshot)
	s.advancePermissionGuide()
	s.guideWindow.Invalidate()
}

func permissionGuideWindowHeight(snapshot permissionGuideSnapshot) int {
	if len(snapshot.rows) == 0 {
		return permissionGuideChromeHeight + 44
	}
	return permissionGuideChromeHeight + len(snapshot.rows)*75
}

func (s *shell) readPermissionGuideSnapshot() permissionGuideSnapshot {
	rows := make([]permissionGuideRow, 0, 3)
	for _, id := range s.requiredPermissionGuideIDs() {
		row := permissionGuideRowFor(id)
		switch id {
		case permissionGuideInput:
			row.access = permissionGuideInputAccess(xiaomi.ListenAccess())
		case permissionGuidePostEvent:
			row.access = permissionGuideAccessibilityAccess(permissionguide.ReadAccessibilityAccess())
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
	hasKeyOutput := hasSelectedDevice || (s.coordinator != nil && s.coordinator.hasExecutableMapping())
	hasBluetoothVoiceRoute := s.audioRequested() && s.selectedAudioRouteTool() != nil
	return permissionGuideCapabilityIDs(hasSelectedDevice, hasKeyOutput, hasBluetoothVoiceRoute)
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

func permissionGuideCapabilityIDs(hasSelectedDevice, hasKeyOutput, hasBluetoothVoiceRoute bool) []permissionGuideID {
	ids := make([]permissionGuideID, 0, 3)
	if hasSelectedDevice {
		ids = append(ids, permissionGuideInput)
	}
	if hasKeyOutput {
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
		return permissionGuideRow{id: id, icon: deviceIconSymbol, title: "输入监控", description: "读取所选设备的按键。"}
	case permissionGuidePostEvent:
		return permissionGuideRow{id: id, icon: "hand.tap", title: permissionguide.AccessibilityPermissionTitle(), description: "向当前应用发送设定的按键。"}
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

func permissionGuideAccessibilityAccess(trusted bool, err error) permissionGuideAccess {
	switch {
	case err != nil:
		return permissionGuideUnknown
	case trusted:
		return permissionGuideGranted
	default:
		return permissionGuideNotGranted
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
	if s.iconDragPreview == nil {
		s.iconDragPreview = permissionGuideDragPreview()
	}
	s.reduceMotion = c.Preferences().ReduceMotion
	if s.reduceMotion {
		s.hidePermissionGuideIllustration()
	}
	rows := permissionGuideRows(s.snapshot)
	var rowHeights [3]float32
	rowsHeight := float32(0)
	for index, row := range rows {
		rowHeights[index] = permissionGuideMeasuredRowHeight(c, row)
		rowsHeight += rowHeights[index]
	}
	if len(rows) > 0 {
		rowsHeight += float32(len(rows)+1) * lineWidth // Card border and row separators.
	}
	if s.guideWindow != nil && len(rows) > 0 {
		height := permissionGuideChromeHeight + int(math.Ceil(float64(rowsHeight)))
		if s.guideWindow.Bounds().Height != height {
			s.guideWindow.SetSize(permissionGuideWidth, height)
		}
	}
	ui.Column(c).Fill().Background(a.theme.Surface).Padding(8, 0, 35, 0).Children(func() {
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
		ui.Box(c).FillWidth().Height(24).Shrink(0)
		ui.Column(c).FillWidth().Padding(0, 20).Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
				s.permissionGuideAppIcon(c)
				ui.Box(c).FillWidth().Height(12).Shrink(0)
				ui.Text(c, "管理权限").Height(30).FontSize(permissionGuideTitle).FontWeight(700).TextAlign(ui.Center)
			})
			ui.Box(c).FillWidth().Height(24).Shrink(0)
			if len(rows) == 0 {
				ui.Text(c, "当前配置不需要额外系统权限。您可以关闭此面板。").FillWidth().Height(44).TextAlign(ui.Center).FontSize(permissionGuideCaption).TextColor(a.theme.TextMuted)
			} else {
				ui.Column(c).Key("permission-guide-card").FillWidth().MinWidth(0).Height(rowsHeight).Border(lineWidth, a.theme.Border).Radius(12).Background(a.theme.Background).Children(func() {
					for index, row := range rows {
						if index > 0 {
							ui.Box(c).FillWidth().Padding(0, 11).Children(func() { sectionLine(c, a) })
						}
						s.permissionGuideRow(c, a, row, rowHeights[index])
					}
				})
			}
		})
	})
}

func permissionGuideMeasuredRowHeight(c *ui.Context, row permissionGuideRow) float32 {
	width := float32(permissionGuideWidth) - 40 - 2*lineWidth - 24 - permissionGuideIconBox - 24 - permissionGuideControlWidth
	_, title := c.MeasureText(width, ui.Span{Text: row.title, Size: permissionGuideRowTitle, Weight: 600})
	_, description := c.MeasureText(width, ui.Span{Text: row.description, Size: permissionGuideCaption})
	return max(permissionGuideIconBox, max(18, title)+4+description) + 24
}

func (s *shell) permissionGuideRow(c *ui.Context, a appearance, row permissionGuideRow, height float32) {
	ui.Row(c).Key("permission-guide/" + permissionGuideIDString(row.id)).FillWidth().MinWidth(0).
		Height(height).Shrink(0).Padding(12).Gap(12).AlignItems(ui.Start).Children(func() {
		ui.Box(c).Width(permissionGuideIconBox).Height(permissionGuideIconBox).Shrink(0).Radius(9).Background(a.theme.Surface).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			if icon := s.systemSymbol(row.icon, a.theme.Dark, 18); icon != nil {
				ui.Image(c, icon).Width(18).Height(18).Fit(ui.Contain).Label(row.title)
			}
		})
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(4).Children(func() {
			s.permissionGuideOpenSettingsButton(c, a, row)
			ui.Text(c, row.description).FillWidth().MinWidth(0).FontSize(permissionGuideCaption).TextColor(a.theme.TextMuted)
		})
		ui.Row(c).Width(permissionGuideControlWidth).Height(permissionGuideIconBox).Shrink(0).AlignItems(ui.Center).Justify(ui.End).Children(func() {
			s.permissionGuideAccessControl(c, a, row)
		})
	})
}

func (s *shell) permissionGuideOpenSettingsButton(c *ui.Context, a appearance, row permissionGuideRow) {
	button := ui.ButtonBase(c).Label("在系统设置中管理" + row.title).Height(18).Padding(0).Justify(ui.Start).FocusRing(false).Cursor(ui.CursorPointer).Disabled(!s.snapshot.actionsEnabled)
	button.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
			ui.Text(c, row.title).SingleLine().FontSize(permissionGuideRowTitle).FontWeight(600)
			if icon := s.symbolBitmap(symbolKey{name: "arrow.up.right.circle", dark: a.theme.Dark, size: permissionGuideLinkIconSize, weight: permissionGuideLinkIconWeight}); icon != nil {
				ui.Image(c, icon).Width(permissionGuideLinkIconSize).Height(permissionGuideLinkIconSize).Fit(ui.Contain).Opacity(.55)
			}
		})
	})
	if button.Clicked() && s.snapshot.actionsEnabled {
		s.openPermissionGuideSettings(row.id, true)
	}
}

func (s *shell) permissionGuideAccessControl(c *ui.Context, a appearance, row permissionGuideRow) {
	if row.access == permissionGuideGranted {
		s.permissionGuideGrantedBadge(c, a)
		return
	}
	if !s.snapshot.actionsEnabled || row.access == permissionGuideRestricted {
		permissionGuideBadge(c, a, "", permissionGuideAccessLabel(row.access))
		return
	}
	label := permissionGuideActionLabel(row)
	button := styleAppButton(ui.ButtonBase(c).Label(label), a, !s.snapshot.actionsEnabled).
		MinWidth(64).Height(24).Padding(0, 10).Radius(12)
	button.Children(func() { ui.Text(c, label).SingleLine().FontSize(permissionGuideCaption) })
	if button.Clicked() && s.snapshot.actionsEnabled {
		s.performPermissionGuideAction(row.id)
	}
}

func permissionGuideActionLabel(row permissionGuideRow) string {
	if row.access == permissionGuideUnknown {
		return "打开设置"
	}
	return "授权…"
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
	ui.Row(c).Height(26).Shrink(0).AlignItems(ui.Center).Gap(5).Children(func() {
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
	runPermissionGuideGrant(id, access, func(id permissionGuideID) {
		switch id {
		case permissionGuideInput:
			_ = xiaomi.RequestListenAccess()
		case permissionGuidePostEvent:
			_ = keyoutput.RequestAccess()
		case permissionGuideBluetooth:
			_ = permissionguide.RequestBluetoothAccess()
		}
	}, func() permissionGuideAccess {
		s.refreshPermissionGuide()
		fresh, _ := permissionGuideAccessForID(s.snapshot, id)
		return fresh
	}, func(id permissionGuideID) { s.openPermissionGuideSettings(id, true) })
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

// List-based permissions are managed by the system settings list. Opening it
// does not request access; the user grants access there, including by app drag.
func runPermissionGuideGrant(id permissionGuideID, access permissionGuideAccess, request func(permissionGuideID), refresh func() permissionGuideAccess, open func(permissionGuideID)) {
	if access == permissionGuideGranted || access == permissionGuideRestricted {
		return
	}
	if id == permissionGuideBluetooth && access == permissionGuideNotDetermined {
		request(id)
		refresh()
		return
	}
	open(id)
}

// The initial join is bounded and waits for two identical native observations.
// Ordinary following resumes only after that first stable window is known.
func (s *permissionGuideState) permissionGuideSettingsJoinReady(settings permissionguide.WindowSnapshot, found bool, now time.Time) (ready, expired bool) {
	if s.followActive && found && settings.ID == s.followWindowID {
		return true, false
	}
	if s.followActive {
		s.followActive = false
		s.settingsSearchUntil = now.Add(permissionGuideSettingsSearchDuration)
		s.settingsCandidate = permissionguide.WindowSnapshot{}
	}
	if s.settingsSearchUntil.IsZero() || !now.Before(s.settingsSearchUntil) {
		s.settingsOpenedFor = 0
		return false, true
	}
	if !found {
		s.settingsCandidate = permissionguide.WindowSnapshot{}
		return false, false
	}
	if settings == s.settingsCandidate {
		return true, false
	}
	s.settingsCandidate = settings
	return false, false
}

// Presentation may attach to an existing settings session. It must not launch
// one implicitly or issue native permission requests.
func runPermissionGuidePresentation(snapshot permissionGuideSnapshot, settingsRunning bool, open func(permissionGuideID), join func()) {
	if settingsRunning && snapshot.actionsEnabled {
		for _, row := range snapshot.rows {
			if permissionGuideAllowsAppDrag(row.id) && row.access != permissionGuideGranted && row.access != permissionGuideRestricted {
				open(row.id)
				return
			}
		}
	}
	join()
}

type permissionGuideGrantedStyle struct {
	badgeColor ui.Color
	color      ui.Color
	fill       ui.Color
	icon       *ui.Bitmap
}

func (s *shell) preparePermissionGuideGrantedStyles() {
	badgeColor := appAppearance(false, s.preferencesState.value.TextSize).theme.Success
	if green, err := statuscolor.SystemGreen(false); err == nil {
		badgeColor = green
	}
	for i, dark := range []bool{false, true} {
		foreground := appAppearance(dark, s.preferencesState.value.TextSize).theme.Success
		if green, err := statuscolor.SystemGreen(dark); err == nil {
			foreground = green
		}
		fill := foreground.Alpha(.08)
		foreground = permissionGuideGrantedTextColor(foreground, fill.Over(appAppearance(dark, s.preferencesState.value.TextSize).theme.Background), appAppearance(dark, s.preferencesState.value.TextSize).theme.Text)
		icon := s.symbolBitmap(symbolKey{name: "checkmark.circle", dark: dark, size: permissionGuideCaption, tint: foreground})
		s.grantedStyles[i] = permissionGuideGrantedStyle{badgeColor: badgeColor, color: foreground, fill: fill, icon: icon}
	}
}

// Graphical completion marks use one original system green in every
// appearance. Readable text colors remain a separate presentation role.
func (s *shell) permissionGuideCompletionBadgeColor() ui.Color {
	if color := s.grantedStyles[0].badgeColor; color.A != 0 {
		return color
	}
	if color := s.grantedStyles[0].color; color.A != 0 {
		return color
	}
	return appAppearance(false, s.preferencesState.value.TextSize).theme.Success
}

func (s *shell) permissionGuideGrantedBadge(c *ui.Context, a appearance) {
	index := 0
	if a.theme.Dark {
		index = 1
	}
	style := s.grantedStyles[index]
	if style.color.A == 0 {
		style.color = a.theme.Success
		style.fill = style.color.Alpha(.08)
	}
	drawPermissionGuideGrantedBadge(c, style.icon, style.color, style.fill)
}

func drawPermissionGuideGrantedBadge(c *ui.Context, icon *ui.Bitmap, foreground, fill ui.Color) {
	ui.Row(c).Height(24).Shrink(0).Padding(0, 9).Gap(5).AlignItems(ui.Center).Radius(12).Background(fill).Children(func() {
		if icon != nil {
			ui.Image(c, icon).Width(permissionGuideCaption).Height(permissionGuideCaption).Fit(ui.Contain)
		} else {
			ui.Text(c, "✓").Role(ui.RoleNone).FontSize(permissionGuideCaption).TextColor(foreground)
		}
		ui.Text(c, "已授权").SingleLine().FontSize(permissionGuideCaption).TextColor(foreground)
	})
}
