package main

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	brand "mimac.local/mimac/Assets/Brand"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/platform/permissionguide"
	"mimac.local/mimac/internal/platform/statuscolor"
	"mimac.local/mimac/internal/platform/symbols"
)

const (
	permissionGuideWidth                          = 500
	permissionGuideContentInset                   = 20
	permissionGuideChromeHeight                   = 214 + permissionGuideContentInset
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
	permissionGuideReadbackInterval               = 100 * time.Millisecond
	permissionGuideReadbackDuration               = time.Minute
	permissionGuideFollowInterval                 = 33 * time.Millisecond
	permissionGuideSettingsLookupInterval         = 100 * time.Millisecond
	permissionGuideSettingsSearchDuration         = 6 * time.Second
	permissionGuideWindowGap                      = 16
)

type permissionGuideState struct {
	guideWindow                  *mygo.Window
	snapshot                     permissionGuideSnapshot
	phase                        permissionGuidePhase
	currentID                    permissionGuideID
	settingsOpenedFor            permissionGuideID
	resumeID                     permissionGuideID
	repairAttempt                *permissionGuideRepairAttempt
	dragRegistrationPreparation  *permissionGuideDragRegistrationPreparation
	generation                   uint64
	pollTimer                    *time.Timer
	pollGeneration               uint64
	followTimer                  *time.Timer
	followGeneration             uint64
	readbackUntil                time.Time
	permissionGuideNativeRequest bool
	followActive                 bool
	followManual                 bool
	followTarget                 mygo.Rectangle
	followStartBounds            mygo.Rectangle
	followMoveStarted            time.Time
	followMoveFrom               mygo.Rectangle
	followMoveTime               time.Duration
	followMoveInitial            bool
	settingsSearchUntil          time.Time
	settingsCandidate            permissionguide.WindowSnapshot
	settingsExplicitJoinPending  bool
	followWindowID               uint32
	followMoving                 bool
	iconDragPreview              *image.RGBA
	iconBounds                   ui.Rect
	iconDragging                 bool
	iconWasHovered               bool
	hasDraggedIcon               bool
	reduceMotion                 bool
	illustration                 *permissionGuideIllustration
	dropGuide                    *permissionGuideDrop
	grantedStyles                [2]permissionGuideGrantedStyle
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

// showPermissionGuide presents the one native permission panel. The
// snapshot queries active capability owners and read-only OS authorization APIs.
func (s *shell) showPermissionGuide() {
	s.onboarding.permissionGuideOpened = false
	s.showPermissionGuideFor(0)
}

func (s *shell) showPermissionGuideFor(preferred permissionGuideID) {
	if s.closing {
		return
	}
	if s.guideWindow != nil && !s.guideWindow.IsDestroyed() {
		if !s.guideWindow.IsVisible() {
			s.settingsOpenedFor = 0
			s.settingsExplicitJoinPending = false
			s.publishPermissionGuideSnapshot(s.readPermissionGuideSnapshot())
			s.currentID = preferred
			s.guideWindow.SetSize(permissionGuideWidth, permissionGuideWindowHeight(s.snapshot))
			s.positionPermissionGuideForPresentation(s.guideWindow, preferred)
			s.showPermissionGuideWindow(s.guideWindow, s.generation)
		} else {
			s.guideWindow.ShowInactive()
			if access, exists := permissionGuideAccessForID(s.snapshot, preferred); exists && permissionGuideCanRoute(preferred, access) {
				s.settingsOpenedFor = 0
				s.currentID = preferred
				if permissionguide.SystemSettingsRunning() {
					s.openPermissionGuideSettings(preferred, true)
				}
			}
		}
		s.refreshPermissionGuide()
		return
	}
	s.generation++
	s.preparePermissionGuideGrantedStyles()
	generation := s.generation
	s.iconBounds = ui.Rect{}
	s.iconDragging, s.hasDraggedIcon, s.iconWasHovered = false, false, false
	s.publishPermissionGuideSnapshot(s.readPermissionGuideSnapshot())
	s.phase = permissionGuideWaiting
	s.currentID = preferred
	s.settingsOpenedFor = 0
	window := mygo.NewWindow(mygo.WindowOptions{
		Title: mygo.App.Name() + " 系统权限",
		Width: permissionGuideWidth, Height: permissionGuideWindowHeight(s.snapshot), Panel: true, PanelPointerOnly: true,
		DisableResize: true, DisableMinimize: true, DisableMaximize: true, DisableFullScreen: true,
		TitleBarStyle: mygo.TitleBarHidden, HideWindowButtons: true, Hidden: true,
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
			s.cancelDragRegistrationPreparation()
			s.stopPermissionGuidePolling()
			s.stopPermissionGuideFollowing()
			if !s.onboardingPermissionSurfaceVisible() {
				s.stopPermissionGuideReadback()
			}
			s.settingsExplicitJoinPending = false
			s.closePermissionGuideDrop()
		}
	})
	window.OnMove(func() {
		if s.guideWindow == window && (s.followActive || s.followTimer != nil) {
			bounds := window.Bounds()
			if bounds.X != s.followStartBounds.X || bounds.Y != s.followStartBounds.Y {
				s.followManual = true
				s.settingsExplicitJoinPending = false
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
			s.settingsExplicitJoinPending = false
			s.resumeID = 0
			s.cancelDragRegistrationPreparation()
			s.repairAttempt = nil
			s.onboarding.permissionGuideOpened = false
			s.onboarding.permissionActionTaken = false
			s.generation++
		}
	})
	// MyGo already created the native window. OnReadyToShow is for web
	// content and will not present this hidden native panel.
	s.positionPermissionGuideForPresentation(window, preferred)
	s.showPermissionGuideWindow(window, generation)
}

// A direct General action can already see its destination. Place the hidden
// guide before showing it so it never covers that Settings window initially.
func (s *shell) positionPermissionGuideForPresentation(window *mygo.Window, preferred permissionGuideID) {
	settings, found := permissionguide.VisibleSystemSettingsWindow(0)
	onboarding := s.onboarding.permissionGuideOpened && preferred == 0
	if found && permissionGuideStartsAttached(s.snapshot, preferred, s.onboarding.permissionGuideOpened) {
		display := permissionGuideSettingsDisplay(settings.Frame)
		if target, ok := permissionGuideCompanionBounds(settings.Frame, display.WorkArea, window.Bounds()); ok {
			window.SetPosition(target.X, target.Y)
			return
		}
	}
	window.Center()
	if onboarding || !found {
		bounds := window.Bounds()
		area := mygo.Screen.DisplayMatching(bounds).WorkArea
		window.SetPosition(permissionGuideStandaloneX(area, bounds), bounds.Y)
	}
}

func permissionGuideStandaloneX(area, panel mygo.Rectangle) int {
	if area.Width < panel.Width || panel.Width <= 0 {
		return panel.X
	}
	return min(area.X+area.Width/2, area.X+area.Width-panel.Width)
}

func permissionGuideStartsAttached(snapshot permissionGuideSnapshot, preferred permissionGuideID, onboarding bool) bool {
	access, exists := permissionGuideAccessForID(snapshot, preferred)
	return snapshot.actionsEnabled && !onboarding && exists && permissionGuideCanRoute(preferred, access)
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
	window.ShowInactive()
	if !window.IsVisible() {
		return
	}
	freshOnboarding := s.onboarding.permissionGuideOpened && s.currentID == 0
	if s.advancePermissionGuide() {
		window.Invalidate()
	}
	s.prepareDragRegistration(s.currentID)
	// First-use presentation observes an existing window on its current page.
	// Explicit permission actions and relaunch can navigate to a pending list.
	if len(s.snapshot.rows) > 0 && s.phase == permissionGuideWaiting && !s.followManual {
		if freshOnboarding {
			if permissionguide.SystemSettingsRunning() {
				if _, exists := permissionguide.SystemSettingsWindowSnapshot(); exists {
					permissionguide.RevealExistingSystemSettings()
				}
				s.schedulePermissionGuideFollowing(window, generation)
			}
			return
		}
		runPermissionGuidePresentation(s.snapshot, s.currentID, permissionguide.SystemSettingsRunning(),
			func(id permissionGuideID) { s.openPermissionGuideSettings(id, true) },
			func() { s.schedulePermissionGuideFollowing(window, generation) })
	}
}

func (s *shell) advancePermissionGuide() bool {
	if s.closing || s.guideWindow == nil || s.guideWindow.IsDestroyed() || !s.guideWindow.IsVisible() {
		return false
	}
	nextID := nextPermissionGuideTarget(s.snapshot, s.settingsOpenedFor, s.currentID)
	if nextID == 0 {
		changed := s.phase != permissionGuideComplete || s.currentID != 0 || s.settingsOpenedFor != 0
		if !changed {
			return false
		}
		s.cancelDragRegistrationPreparation()
		s.phase = permissionGuideComplete
		s.currentID = 0
		s.settingsOpenedFor = 0
		s.settingsExplicitJoinPending = false
		s.stopPermissionGuideFollowing()
		return true
	}
	changed := s.phase != permissionGuideWaiting || s.currentID != nextID
	s.phase = permissionGuideWaiting
	if s.currentID != nextID {
		s.cancelDragRegistrationPreparation()
		s.currentID = nextID
		s.settingsOpenedFor = 0
		s.settingsExplicitJoinPending = false
		s.followManual = false
		s.stopPermissionGuideFollowing()
	}
	return changed
}

// Prefer the explicitly opened page only while the corresponding capability
// is still missing. A granted row must not hold the sidecar's target.
func nextPermissionGuideTarget(snapshot permissionGuideSnapshot, opened, current permissionGuideID) permissionGuideID {
	for _, id := range []permissionGuideID{opened, current} {
		if access, exists := permissionGuideAccessForID(snapshot, id); exists && access != permissionGuideGranted {
			return id
		}
	}
	for _, row := range snapshot.rows {
		if row.access != permissionGuideGranted {
			return row.id
		}
	}
	return 0
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
	token := s.pollGeneration
	interval := s.permissionGuidePollIntervalAt(time.Now())
	if s.permissionGuideInteractionActive() {
		interval = permissionGuidePollInterval
	}
	s.pollTimer = time.AfterFunc(interval, func() {
		mygo.RunOnMain(func() {
			if !permissionGuideTimerCurrent(token, s.pollGeneration) || s.guideWindow != window || s.generation != generation || s.closing || !window.IsVisible() {
				return
			}
			s.pollTimer = nil
			if s.permissionGuideInteractionActive() {
				s.schedulePermissionGuidePoll(window, generation)
				return
			}
			s.refreshPermissionGuideSnapshot()
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
	var settings permissionguide.WindowSnapshot
	var found bool
	if s.followActive {
		settings, found = permissionguide.SystemSettingsWindowSnapshot()
	} else {
		// A covered window has geometry even before the user can see it.
		settings, found = permissionguide.VisibleSystemSettingsWindow(0)
	}
	now := time.Now()
	ready, expired := s.permissionGuideSettingsJoinReady(settings, found, now)
	if !ready {
		s.hidePermissionGuideIllustration()
		if expired {
			s.stopPermissionGuideFollowing()
			s.stopPermissionGuideReadback()
		} else {
			s.schedulePermissionGuideFollowTick(window, generation, s.permissionGuideSettingsFollowInterval())
		}
		return
	}
	initial := !s.followActive
	settingsBounds := settings.Frame
	display := permissionGuideSettingsDisplay(settingsBounds)
	target, ok := permissionGuideCompanionBounds(settingsBounds, display.WorkArea, window.Bounds())
	if !ok {
		s.followActive = false
		s.schedulePermissionGuideFollowTick(window, generation, s.permissionGuideSettingsFollowInterval())
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
	s.settingsExplicitJoinPending = false
	if initial {
		s.armPermissionGuideReadback(false)
		s.schedulePermissionGuidePoll(window, generation)
	}
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

func (s *permissionGuideState) permissionGuideSettingsFollowInterval() time.Duration {
	if s.settingsExplicitJoinPending && s.settingsOpenedFor != 0 && s.settingsSearchUntil.IsZero() && s.settingsCandidate.ID == 0 {
		return permissionGuidePollInterval
	}
	return permissionGuideSettingsLookupInterval
}

func (s *shell) schedulePermissionGuideFollowTick(window *mygo.Window, generation uint64, interval time.Duration) {
	if window == nil || window.IsDestroyed() || !window.IsVisible() || s.guideWindow != window || s.generation != generation || s.closing || s.followManual {
		return
	}
	if s.followTimer != nil {
		s.followTimer.Stop()
	}
	s.followGeneration++
	token := s.followGeneration
	s.followTimer = time.AfterFunc(interval, func() {
		mygo.RunOnMain(func() {
			if !permissionGuideTimerCurrent(token, s.followGeneration) {
				return
			}
			s.followTimer = nil
			s.followPermissionGuideSettings(window, generation)
		})
	})
}

func permissionGuideSettingsDisplay(settings permissionguide.Rect) mygo.Display {
	return mygo.Screen.DisplayMatching(mygo.Rectangle{
		X: int(math.Round(settings.X)), Y: int(math.Round(settings.Y)),
		Width: int(math.Round(settings.Width)), Height: int(math.Round(settings.Height)),
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
	s.pollGeneration++
	if s.pollTimer != nil {
		s.pollTimer.Stop()
		s.pollTimer = nil
	}
}

func (s *shell) stopPermissionGuideFollowing() {
	s.followGeneration++
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
	s.stopPermissionGuideReadback()
	s.stopPermissionGuideFollowing()
}

// refreshPermissionGuide derives display state from read-only permission APIs
// and the active device/audio owners. It does not acquire a device or start
// audio, even in a packaged Dev app.
func (s *shell) refreshPermissionGuide() {
	if s.refreshPermissionGuideSnapshot() {
		s.schedulePermissionGuideReadback()
	}
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
	return permissionGuideCapabilityIDs(hasSelectedDevice, hasKeyOutput, plugin.UsesBluetoothAudio)
}

func (s *shell) permissionGuideAppBundlePath() string {
	if !mygo.App.IsPackaged() {
		return ""
	}
	bundleID, ok := permissionguide.MainBundleIdentifier()
	if !ok {
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
	if !permissionGuideBundleIdentityAllowed(bundleID, mygo.App.Name(), bundlePath, true) {
		return ""
	}
	return bundlePath
}

func permissionGuideCapabilityIDs(hasSelectedDevice, hasKeyOutput, usesBluetoothAudio bool) []permissionGuideID {
	ids := make([]permissionGuideID, 0, 3)
	if hasSelectedDevice {
		ids = append(ids, permissionGuideInput)
	}
	if hasKeyOutput {
		ids = append(ids, permissionGuidePostEvent)
	}
	if usesBluetoothAudio {
		ids = append(ids, permissionGuideBluetooth)
	}
	return ids
}

func permissionGuideBundleIdentityAllowed(bundleID, appName, bundlePath string, packaged bool) bool {
	if !packaged {
		return false
	}
	if (bundleID == "dev.mimac.app" && appName == "MiMac") || (bundleID == "dev.mimac.app.dev" && appName == developmentAppName) {
		return true
	}
	if !strings.HasPrefix(bundleID, "dev.mimac.verification.") || !strings.HasPrefix(appName, "MiMac ") ||
		appName == developmentAppName || strings.ContainsRune(appName, '/') {
		return false
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(filepath.Join(cache, "MiMac"))
	if err != nil {
		return false
	}
	return strings.HasPrefix(bundlePath, root+string(os.PathSeparator))
}

func permissionGuideRows(snapshot permissionGuideSnapshot) []permissionGuideRow {
	return append([]permissionGuideRow(nil), snapshot.rows...)
}

func permissionGuideRowFor(id permissionGuideID) permissionGuideRow {
	switch id {
	case permissionGuideInput:
		return permissionGuideRow{id: id, icon: deviceIconSymbol, title: "输入监控", description: "读取所选设备的按键"}
	case permissionGuidePostEvent:
		return permissionGuideRow{id: id, icon: "hand.tap", title: permissionguide.AccessibilityPermissionTitle(), description: "向当前应用发送设定的按键"}
	case permissionGuideBluetooth:
		return permissionGuideRow{id: id, icon: symbols.BluetoothTemplate, title: "蓝牙", description: "接收遥控器麦克风音频，用于语音输入"}
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
	repairTitle, repairDetail := s.permissionGuideRepairCopy(s.snapshot)
	repairHeight := permissionGuideRepairMeasuredHeight(c, repairTitle, repairDetail)
	if s.guideWindow != nil && len(rows) > 0 {
		height := permissionGuideChromeHeight + int(math.Ceil(float64(rowsHeight+repairHeight)))
		if s.guideWindow.Bounds().Height != height {
			s.guideWindow.SetSize(permissionGuideWidth, height)
		}
	}
	ui.Column(c).Fill().Background(a.theme.Surface).Padding(8, 0, permissionGuideContentInset, 0).Children(func() {
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
		ui.Column(c).FillWidth().Padding(0, permissionGuideContentInset).Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
				s.permissionGuideAppIcon(c)
				ui.Box(c).FillWidth().Height(12).Shrink(0)
				ui.Text(c, "管理权限").Height(30).FontSize(permissionGuideTitle).FontWeight(700).TextAlign(ui.Center)
			})
			ui.Box(c).FillWidth().Height(24).Shrink(0)
			if len(rows) == 0 {
				ui.Text(c, "当前配置不需要额外系统权限，您可以关闭此面板").FillWidth().Height(44).TextAlign(ui.Center).FontSize(permissionGuideCaption).TextColor(a.theme.TextMuted)
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
			if repairTitle != "" {
				ui.Column(c).FillWidth().MinWidth(0).Height(repairHeight).Shrink(0).
					Padding(permissionGuideRepairTop, permissionGuideRepairInset, 0, permissionGuideRepairInset).
					Gap(permissionGuideRepairTextGap).Children(func() {
					ui.Text(c, repairTitle).FillWidth().FontSize(permissionGuideCaption).FontWeight(600).TextColor(a.theme.Text)
					ui.Text(c, repairDetail).FillWidth().MinWidth(0).FontSize(permissionGuideCaption).TextColor(a.theme.TextMuted)
				})
			}
		})
	})
}

func permissionGuideMeasuredRowHeight(c *ui.Context, row permissionGuideRow) float32 {
	width := float32(permissionGuideWidth-2*permissionGuideContentInset) - 2*lineWidth - 24 - permissionGuideIconBox - 24 - permissionGuideControlWidth
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
		s.successBadge(c, a, "已授权")
		return
	}
	if !s.snapshot.actionsEnabled || row.access == permissionGuideRestricted {
		permissionGuideBadge(c, a, "", permissionGuideAccessLabel(row.access))
		return
	}
	label := permissionGuideActionLabel(row)
	button := styleAppButton(ui.ButtonBase(c).Label(label), a, false).
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
	if permissionGuideAllowsAppDrag(id) && (access == permissionGuideNotDetermined || access == permissionGuideNotGranted || access == permissionGuideDenied) {
		s.resumeID = id
	}
	runPermissionGuideGrant(id, access, func(id permissionGuideID) {
		switch id {
		case permissionGuideBluetooth:
			generation := s.generation
			s.armPermissionGuideReadback(true)
			_ = permissionguide.RequestBluetoothAccessWithCallback(func() {
				if s.closing || s.guideWindow == nil || s.guideWindow.IsDestroyed() || s.generation != generation {
					return
				}
				s.refreshPermissionGuide()
			})
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
	// The row routes to Settings without reordering the visible guide.
	permissionguide.YieldToSystemSettings()
	if s.currentID != id {
		s.cancelDragRegistrationPreparation()
	}
	s.currentID = id
	s.phase = permissionGuideWaiting
	s.settingsOpenedFor = id
	s.settingsExplicitJoinPending = true
	s.followManual = false
	switch id {
	case permissionGuideInput:
		s.openInputPermissionSettings()
	case permissionGuidePostEvent:
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility")
	case permissionGuideBluetooth:
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_Bluetooth")
	}
	if !s.followActive {
		s.schedulePermissionGuideFollowing(s.guideWindow, s.generation)
	}
	s.prepareDragRegistration(id)
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

// The initial join uses a bounded fast search, then a slower search only for an
// explicitly opened route. Once a late window appears, the second stability
// observation uses the fast cadence. Ordinary following resumes after joining.
func (s *permissionGuideState) permissionGuideSettingsJoinReady(settings permissionguide.WindowSnapshot, found bool, now time.Time) (ready, expired bool) {
	if s.followActive && found && settings.ID == s.followWindowID {
		return true, false
	}
	if s.followActive {
		s.followActive = false
		s.settingsSearchUntil = now.Add(permissionGuideSettingsSearchDuration)
		s.settingsCandidate = permissionguide.WindowSnapshot{}
	}
	if s.settingsSearchUntil.IsZero() {
		if !s.settingsExplicitJoinPending || s.settingsOpenedFor == 0 {
			s.settingsOpenedFor = 0
			return false, true
		}
	} else if !now.Before(s.settingsSearchUntil) {
		s.settingsSearchUntil = time.Time{}
		s.settingsCandidate = permissionguide.WindowSnapshot{}
		if !s.settingsExplicitJoinPending || s.settingsOpenedFor == 0 {
			s.settingsOpenedFor = 0
			return false, true
		}
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
func permissionGuideCanRoute(id permissionGuideID, access permissionGuideAccess) bool {
	return permissionGuideAllowsAppDrag(id) && access != permissionGuideGranted && access != permissionGuideRestricted
}

func runPermissionGuidePresentation(snapshot permissionGuideSnapshot, preferred permissionGuideID, settingsRunning bool, open func(permissionGuideID), join func()) {
	if preferred == permissionGuideBluetooth {
		// Bluetooth has its own explicit request in the companion card.
		// Opening it from General should not jump to another permissions list.
		return
	}
	if settingsRunning && snapshot.actionsEnabled {
		if access, exists := permissionGuideAccessForID(snapshot, preferred); exists && permissionGuideCanRoute(preferred, access) {
			open(preferred)
			return
		}
		for _, row := range snapshot.rows {
			if permissionGuideCanRoute(row.id, row.access) {
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

func (s *shell) successBadge(c *ui.Context, a appearance, label string) {
	index := 0
	if a.theme.Dark {
		index = 1
	}
	style := s.grantedStyles[index]
	if style.color.A == 0 {
		style.color = a.theme.Success
		style.fill = style.color.Alpha(.08)
	}
	drawSuccessBadge(c, style.icon, style.color, style.fill, label)
}

func drawSuccessBadge(c *ui.Context, icon *ui.Bitmap, foreground, fill ui.Color, label string) {
	ui.Row(c).Height(24).Shrink(0).Padding(0, 9).Gap(5).AlignItems(ui.Center).Radius(12).Background(fill).Children(func() {
		if icon != nil {
			ui.Image(c, icon).Width(permissionGuideCaption).Height(permissionGuideCaption).Fit(ui.Contain)
		} else {
			ui.Text(c, "✓").Role(ui.RoleNone).FontSize(permissionGuideCaption).TextColor(foreground)
		}
		ui.Text(c, label).SingleLine().FontSize(permissionGuideCaption).TextColor(foreground)
	})
}
