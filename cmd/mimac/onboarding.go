package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/voicetools"
)

const (
	onboardingRecordVersion = 1
	onboardingRecordFile    = "onboarding.json"
)

type onboardingRecord struct {
	Version   int  `json:"version"`
	Started   bool `json:"started"`
	Completed bool `json:"completed"`
	Step      int  `json:"step,omitempty"`
}

func validOnboardingRecord(record onboardingRecord) bool {
	return record.Version == onboardingRecordVersion && record.Step >= int(onboardingStepWelcome) && record.Step < int(onboardingStepCount) && (!record.Completed || record.Started)
}

func readOnboardingRecord(path string) (onboardingRecord, bool, error) {
	if path == "" {
		return onboardingRecord{}, false, errors.New("onboarding: empty record path")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return onboardingRecord{}, false, nil
	}
	if err != nil {
		return onboardingRecord{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 4096 {
		return onboardingRecord{}, true, errors.New("onboarding: unsupported record file")
	}
	file, err := os.Open(path)
	if err != nil {
		return onboardingRecord{}, true, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	var record onboardingRecord
	if err := decoder.Decode(&record); err != nil {
		return onboardingRecord{}, true, err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || !validOnboardingRecord(record) {
		return onboardingRecord{}, true, errors.New("onboarding: invalid record")
	}
	return record, true, nil
}

func writeOnboardingRecord(path string, record onboardingRecord) error {
	if path == "" || !validOnboardingRecord(record) {
		return errors.New("onboarding: invalid record")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".onboarding-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
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

type onboardingStep int8

// Saved setup steps retain their original values for existing progress records.
const onboardingStepWelcome onboardingStep = -1

const (
	onboardingStepDevice onboardingStep = iota
	onboardingStepVoice
	onboardingStepPermissions
	onboardingStepCompletion
	onboardingStepCount
)

func onboardingStepAt(step int) onboardingStep {
	return onboardingStep(max(int(onboardingStepWelcome), min(step, int(onboardingStepCount)-1)))
}

func onboardingAdjacentStep(step onboardingStep, delta int) onboardingStep {
	return onboardingStepAt(int(step) + delta)
}

func shouldPresentOnboarding(record onboardingRecord, exists, legacyConfigurationExists bool, readErr error) bool {
	if exists && readErr == nil {
		return record.Started && !record.Completed
	}
	return !legacyConfigurationExists
}

func onboardingRecordForOpen(record onboardingRecord, exists, automaticFirstRun bool) (onboardingRecord, bool) {
	if exists || !automaticFirstRun {
		return record, false
	}
	return onboardingRecord{Version: onboardingRecordVersion, Started: true, Step: int(onboardingStepWelcome)}, true
}

func onboardingInitialStep(record onboardingRecord, automaticFirstRun bool) onboardingStep {
	if automaticFirstRun && validOnboardingRecord(record) && record.Started && !record.Completed {
		return onboardingStepAt(record.Step)
	}
	return onboardingStepWelcome
}

func onboardingRecordForStep(record onboardingRecord, step onboardingStep, automaticFirstRun bool) (onboardingRecord, bool) {
	if !automaticFirstRun || !validOnboardingRecord(record) || !record.Started || record.Completed || step < onboardingStepWelcome || step >= onboardingStepCount || record.Step == int(step) {
		return record, false
	}
	record.Step = int(step)
	return record, true
}

type onboardingState struct {
	window                 *mygo.Window
	step                   onboardingStep
	record                 onboardingRecord
	recordPath             string
	persistProgress        bool
	errorText              string
	permissionSnapshot     permissionGuideSnapshot
	permissionGuideOpened  bool
	toolLocations          map[string]voicetools.State
	toolLocationErrors     map[string]bool
	toolLocationGeneration uint64
	toolLocationCancel     context.CancelFunc
	showOnboardingOnLaunch bool
}

func (s *shell) shouldPresentInitialOnboarding() bool {
	path, err := hostDataPath(onboardingRecordFile)
	if err != nil {
		return s.shouldPresentFirstRunSettings()
	}
	record, exists, readErr := readOnboardingRecord(path)
	return shouldPresentOnboarding(record, exists, !s.shouldPresentFirstRunSettings(), readErr)
}

func (s *shell) showInitialOnboarding() { s.openOnboarding(true) }

// showOnboarding is an explicit menu action. It can restart a completed guide
// without changing any device, plugin, mapping, or voice-tool owner.
func (s *shell) showOnboarding() { s.openOnboarding(false) }

func (s *shell) openOnboarding(automaticFirstRun bool) {
	if s.closing {
		return
	}
	if s.onboarding.window != nil && !s.onboarding.window.IsDestroyed() {
		s.onboarding.window.Show()
		s.onboarding.window.Focus()
		return
	}
	s.onboarding.step = onboardingStepWelcome
	s.onboarding.persistProgress = automaticFirstRun
	s.onboarding.errorText = ""
	s.onboarding.permissionGuideOpened = false
	s.onboarding.toolLocations = nil
	s.onboarding.toolLocationErrors = nil
	s.onboarding.recordPath, _ = hostDataPath(onboardingRecordFile)
	if s.onboarding.recordPath == "" {
		s.onboarding.errorText = "无法读取首次设置进度；本次仍可继续。"
		s.onboarding.record = onboardingRecord{Version: onboardingRecordVersion, Started: true, Step: int(onboardingStepWelcome)}
	} else {
		record, exists, err := readOnboardingRecord(s.onboarding.recordPath)
		if err != nil {
			s.onboarding.errorText = "无法读取首次设置进度；本次仍可继续，完成时会尝试重新保存。"
			s.onboarding.record = onboardingRecord{Version: onboardingRecordVersion, Started: true, Step: int(onboardingStepWelcome)}
		} else {
			s.onboarding.record, _ = onboardingRecordForOpen(record, exists, automaticFirstRun)
			if _, needsWrite := onboardingRecordForOpen(record, exists, automaticFirstRun); needsWrite {
				if writeErr := writeOnboardingRecord(s.onboarding.recordPath, s.onboarding.record); writeErr != nil {
					s.onboarding.errorText = "无法保存首次设置进度；本次仍可继续。"
				}
			}
		}
	}
	s.onboarding.step = onboardingInitialStep(s.onboarding.record, automaticFirstRun)
	if s.onboarding.step == onboardingStepPermissions {
		s.onboarding.permissionSnapshot = s.readPermissionGuideSnapshot()
	}
	s.prepareOnboardingPresentation()
	window := mygo.NewWindow(mygo.WindowOptions{
		Title: mygo.App.Name() + " 首次设置", Width: windowWidth, Height: windowHeight,
		MinWidth: windowMinWidth, MinHeight: windowMinHeight,
		TitleBarStyle: mygo.TitleBarHidden, TrafficLightPosition: &mygo.Point{X: trafficLightInset, Y: trafficLightInset},
		Content: ui.View(s.onboardingView),
	})
	s.onboarding.window = window
	window.OnFocus(func() {
		if !s.closing && s.onboarding.window == window {
			s.prepareOnboardingPresentation()
			s.refreshOnboardingChecks()
			s.refreshOnboardingProjection()
		}
	})
	window.OnClosed(func() {
		if s.onboarding.window != window {
			return
		}
		s.stopOnboardingVoiceLocations()
		s.onboarding.window = nil
		if s.onboarding.permissionGuideOpened {
			s.onboarding.permissionGuideOpened = false
			s.closePermissionGuide()
		}
		if s.window == nil && (s.guideWindow == nil || s.guideWindow.IsDestroyed()) {
			s.releaseSettingsAssets()
		}
	})
	s.refreshOnboardingChecks()
}

func (s *shell) prepareOnboardingPresentation() {
	s.preparePermissionGuideGrantedStyles()
	for _, dark := range []bool{false, true} {
		a := appAppearance(dark, s.preferencesState.value.TextSize)
		s.symbolBitmap(symbolKey{name: "arrow.left", dark: dark, size: settingsIconSize, tint: a.theme.Text})
		s.symbolBitmap(symbolKey{name: "arrow.right", dark: dark, size: settingsIconSize, tint: a.theme.Background})
		s.symbolBitmap(symbolKey{name: "checkmark", dark: dark, size: 24, tint: ui.RGB(255, 255, 255)})
	}
}

func (s *shell) closeOnboarding() {
	if s.onboarding.window == nil || s.onboarding.window.IsDestroyed() {
		return
	}
	s.onboarding.window.Close()
}

func (s *shell) moveOnboarding(delta int) {
	if s.onboarding.window == nil || s.onboarding.window.IsDestroyed() {
		return
	}
	step := onboardingAdjacentStep(s.onboarding.step, delta)
	if step == s.onboarding.step {
		return
	}
	if s.onboarding.step == onboardingStepVoice {
		s.stopOnboardingVoiceLocations()
	}
	if step == onboardingStepCompletion && s.onboarding.permissionGuideOpened {
		s.onboarding.permissionGuideOpened = false
		s.closePermissionGuide()
	}
	s.onboarding.step = step
	s.onboarding.errorText = ""
	if next, needsWrite := onboardingRecordForStep(s.onboarding.record, step, s.onboarding.persistProgress); needsWrite {
		if err := writeOnboardingRecord(s.onboarding.recordPath, next); err != nil {
			s.onboarding.errorText = "无法保存设置进度；本次仍可继续。"
		} else {
			s.onboarding.record = next
		}
	}
	s.refreshOnboardingProjection()
	s.refreshOnboardingChecks()
	s.onboarding.window.Invalidate()
}

func samePermissionGuideSnapshot(a, b permissionGuideSnapshot) bool {
	if a.actionsEnabled != b.actionsEnabled || a.appBundlePath != b.appBundlePath || len(a.rows) != len(b.rows) {
		return false
	}
	for i := range a.rows {
		if a.rows[i] != b.rows[i] {
			return false
		}
	}
	return true
}

func (s *shell) updateOnboardingPermissions(next permissionGuideSnapshot) bool {
	if samePermissionGuideSnapshot(s.onboarding.permissionSnapshot, next) {
		return false
	}
	s.onboarding.permissionSnapshot = next
	if s.onboarding.window != nil && !s.onboarding.window.IsDestroyed() && s.onboarding.step == onboardingStepPermissions {
		s.onboarding.window.Invalidate()
	}
	return true
}

func (s *shell) refreshOnboardingProjection() {
	if s.onboarding.window == nil || s.onboarding.window.IsDestroyed() {
		return
	}
	if s.onboarding.step == onboardingStepPermissions {
		s.onboarding.permissionSnapshot = s.readPermissionGuideSnapshot()
	}
	s.onboarding.window.Invalidate()
}

func (s *shell) refreshOnboardingAfterActivation() {
	if s.onboarding.window == nil || s.onboarding.window.IsDestroyed() || s.closing {
		return
	}
	s.refreshSetupAfterActivation()
	if s.onboarding.step == onboardingStepDevice && s.monitor == nil && !s.deviceReadBusy {
		s.detectDevice()
	}
	s.refreshOnboardingProjection()
	s.onboarding.window.Show()
	s.onboarding.window.Focus()
}

func (s *shell) showOnboardingPermissionGuide() {
	if s.onboarding.window == nil || s.onboarding.window.IsDestroyed() {
		return
	}
	s.onboarding.permissionGuideOpened = true
	s.showPermissionGuide()
	s.updateOnboardingPermissions(s.snapshot)
	s.onboarding.window.Invalidate()
}

func (s *shell) finishOnboarding() {
	if s.onboarding.window == nil || s.onboarding.window.IsDestroyed() {
		return
	}
	record := onboardingRecord{Version: onboardingRecordVersion, Started: true, Completed: true}
	if err := writeOnboardingRecord(s.onboarding.recordPath, record); err != nil {
		s.report.OnboardingError = err.Error()
	} else {
		s.onboarding.record = record
	}
	s.stopOnboardingVoiceLocations()
	s.page = pageMappings
	s.closeOnboarding()
	s.showSettings()
	if s.window != nil {
		s.window.Focus()
	}
}

func (s *shell) openOnboardingSettingsPage(page settingsPage) {
	if s.closing {
		return
	}
	s.page = page
	s.showSettings()
	if s.window != nil {
		s.window.Focus()
	}
}

func (s *shell) stopOnboardingVoiceLocations() {
	if s.onboarding.toolLocationCancel != nil {
		s.onboarding.toolLocationCancel()
		s.onboarding.toolLocationCancel = nil
	}
	s.onboarding.toolLocationGeneration++
}

func (s *shell) refreshOnboardingVoiceLocations() {
	if s.onboarding.window == nil || s.onboarding.window.IsDestroyed() || s.onboarding.step != onboardingStepVoice {
		return
	}
	s.stopOnboardingVoiceLocations()
	ctx, cancel := context.WithCancel(context.Background())
	s.onboarding.toolLocationCancel = cancel
	generation := s.onboarding.toolLocationGeneration
	window := s.onboarding.window
	s.onboarding.toolLocations = make(map[string]voicetools.State)
	s.onboarding.toolLocationErrors = make(map[string]bool)
	window.Invalidate()
	if s.toolLoadFailed {
		return
	}
	for _, candidate := range s.tools {
		tool := candidate
		if !tool.releaseReady || tool.active || tool.id == appleSpeechToolID || tool.locateApplication == nil {
			continue
		}
		locate, locator, id := tool.locateApplication, s.toolLocator, tool.id
		go func() {
			_, state, err := locate(ctx, locator)
			mygo.RunOnMain(func() {
				if ctx.Err() != nil || s.onboarding.window != window || s.onboarding.toolLocationGeneration != generation || s.onboarding.step != onboardingStepVoice {
					return
				}
				s.onboarding.toolLocations[id] = state
				s.onboarding.toolLocationErrors[id] = err != nil
				window.Invalidate()
			})
		}()
	}
}

// Refresh read-only discovery on entry or focus return. The existing plugin
// owners still control enablement, model downloads and capture.
func (s *shell) refreshOnboardingChecks() {
	if s.closing || s.onboarding.window == nil || s.onboarding.window.IsDestroyed() {
		return
	}
	switch s.onboarding.step {
	case onboardingStepDevice:
		if s.monitor == nil && !s.deviceReadBusy {
			s.detectDevice()
		}
	case onboardingStepVoice:
		s.refreshOnboardingVoiceChecks()
	}
}

func (s *shell) refreshOnboardingVoiceChecks() {
	if s.onboarding.window == nil || s.onboarding.window.IsDestroyed() || s.onboarding.step != onboardingStepVoice {
		return
	}
	for _, tool := range s.tools {
		if !tool.releaseReady || !tool.active || tool.busy {
			continue
		}
		s.refreshVoiceTool(tool)
	}
	s.refreshOnboardingVoiceLocations()
}

func (s *shell) onboardingVoiceToolNeedsRetry(tool *voiceTool) bool {
	if tool.busy || s.toolLoadFailed {
		return false
	}
	if tool.active {
		return tool.snapshot.State == voicetools.ReadError || tool.lastAttemptCancelled || tool.snapshot.Source == appleSpeechCleanupPendingSource
	}
	return s.onboarding.toolLocationErrors[tool.id]
}
