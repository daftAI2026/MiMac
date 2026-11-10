package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/chatgpt"
	"mimac.local/mimac/internal/adapters/doubao"
	"mimac.local/mimac/internal/adapters/shandian"
	"mimac.local/mimac/internal/adapters/typeless"
	"mimac.local/mimac/internal/adapters/wetype"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/inputsource"
	"mimac.local/mimac/internal/pluginrt"
	"mimac.local/mimac/internal/voicetools"
)

type voiceTool struct {
	id, name             string
	kind                 voiceToolKind
	discover             func(context.Context, voicetools.Locator) voicetools.Snapshot
	locateApplication    func(context.Context, voicetools.Locator) (voicetools.Bundle, voicetools.State, error)
	audioRouteFactory    audioRouteFactory
	fallbackIconPNG      []byte
	systemArtwork        string
	prepareAssets        func(context.Context, voicetools.Locator) voicetools.Snapshot
	requiredInputSource  func(string) (string, bool)
	active, busy         bool
	preparingAssets      bool
	cancelRequested      bool
	lastAttemptCancelled bool
	sequence             uint64
	cancel               context.CancelFunc
	snapshot             voicetools.Snapshot
}

// Kind describes the user-facing capability, independent of how the adapter
// executes. Both kinds use the same plugin lifecycle and discovery contract.
type voiceToolKind string

const (
	voiceInputMethod voiceToolKind = "语音输入法"
	voiceApplication voiceToolKind = "应用语音"
	voiceRecognition voiceToolKind = "语音识别"
)

type voiceToolsState struct {
	tools               []*voiceTool
	currentVoiceTool    string
	pendingInputSource  *pendingVoiceToolSelection
	inputSource         inputSourceAccess
	toolLocator         voicetools.Locator
	toolPreferences     toolPreferences
	toolPreferencesPath string
	toolLoadFailed      bool
	toolError           string
	voiceStartBlocked   string
}

type pendingVoiceToolSelection struct {
	tool     *voiceTool
	sequence uint64
}

const (
	inputSourceDeferredToolError      = "请在当前操作结束后重新选择此工具"
	inputSourceFailedToolError        = "请在系统设置中检查输入法后重选"
	inputSourceCleanupFailedToolError = "语音清理未完成，请先恢复再重选"
)

var errInputSourceCleanupFailed = errors.New("inputsource: previous audio owner cleanup is not confirmed")

type inputSourceAccess interface {
	CurrentID() (string, error)
	SelectEnabledID(string) error
}

type nativeInputSourceAccess struct{}

func (nativeInputSourceAccess) CurrentID() (string, error) { return inputsource.CurrentID() }
func (nativeInputSourceAccess) SelectEnabledID(id string) error {
	return inputsource.SelectEnabledID(id)
}

func (s *shell) inputSourceController() inputSourceAccess {
	if s.inputSource != nil {
		return s.inputSource
	}
	return nativeInputSourceAccess{}
}

// requiredInputSourceForSelection derives one target only from the current,
// ready adapter snapshot and its executable, supported actions.
func requiredInputSourceForSelection(tool *voiceTool) (string, bool) {
	if tool == nil || tool.requiredInputSource == nil || tool.busy || tool.sequence == 0 || tool.snapshot.State != voicetools.Ready {
		return "", false
	}
	var target string
	for _, action := range tool.snapshot.Actions {
		for _, candidate := range action.Candidates {
			ref := voicetools.ActionRef{ToolID: tool.id, ActionID: action.ID, CandidateRaw: candidate.Raw}
			if _, err := voicetools.ResolveAction(ref, tool.sequence, tool.snapshot); err != nil {
				continue
			}
			sourceID, known := tool.requiredInputSource(action.ID)
			if !known || sourceID == "" || (target != "" && target != sourceID) {
				return "", false
			}
			target = sourceID
		}
	}
	return target, target != ""
}

func (s *shell) ensureVoiceInputSource(tool *voiceTool) error {
	target, required := requiredInputSourceForSelection(tool)
	if !required {
		return nil
	}
	// Selecting a tool still saves Dev preferences, without changing the
	// system input source used by the daily app.
	if s.isolatedDevelopment {
		return nil
	}
	if err := s.requireDeviceAccess(); err != nil {
		return err
	}
	controller := s.inputSourceController()
	actual, err := controller.CurrentID()
	if err != nil {
		return fmt.Errorf("inputsource: read current ID: %w", err)
	}
	if actual == target {
		return nil
	}
	if err := controller.SelectEnabledID(target); err != nil {
		return err
	}
	actual, err = controller.CurrentID()
	if err != nil {
		return fmt.Errorf("inputsource: verify current ID: %w", err)
	}
	if actual != target {
		return fmt.Errorf("inputsource: current ID is %q after selecting %q", actual, target)
	}
	return nil
}

// TIS changes are explicit user actions and only run after the existing voice
// action/route owners are idle. A route for another target is still retiring
// work even after its Core action has been cancelled.
func (s *shell) inputSourceTransitionBusy(tool *voiceTool) bool {
	if tool == nil || tool.requiredInputSource == nil {
		return false
	}
	if s.closing || s.audioQuitPending || s.liveAudio.retiring || s.remoteState.stopPending ||
		s.remoteState.cleanupInFlight || s.remoteState.reserveInFlight || s.remoteState.reserveQueued != 0 {
		return true
	}
	if s.inputSourceCleanupFailed() {
		return true
	}
	if s.coordinator != nil && s.coordinator.core != nil {
		snapshot := s.coordinator.core.Snapshot()
		if snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 0 || snapshot.OutputFailureID != 0 {
			return true
		}
	}
	oldRouteOwned := s.liveAudio.connection != nil || s.audioSession != nil
	return oldRouteOwned && s.liveAudio.toolID != tool.id
}

// A failed route close retains its source owner even after Core is idle and
// retiring returns false. Keep TIS unchanged until existing recovery clears it.
func (s *shell) inputSourceCleanupFailed() bool {
	return s.report.AudioCleanupFailed && (s.liveAudio.connection != nil || s.audioSession != nil)
}

func (s *shell) ensureSelectedVoiceInputSource() error {
	var tool *voiceTool
	for _, candidate := range s.tools {
		if candidate.id == s.currentVoiceTool && candidate.active {
			tool = candidate
			break
		}
	}
	s.pendingInputSource = nil
	if tool != nil && tool.busy && tool.requiredInputSource != nil {
		s.pendingInputSource = &pendingVoiceToolSelection{tool: tool, sequence: tool.sequence}
		return nil
	}
	if _, required := requiredInputSourceForSelection(tool); !required {
		return nil
	}
	if s.inputSourceCleanupFailed() {
		s.toolError = inputSourceCleanupFailedToolError
		return errInputSourceCleanupFailed
	}
	if s.inputSourceTransitionBusy(tool) {
		s.toolError = inputSourceDeferredToolError
		return errors.New("inputsource: selection deferred until voice action and route cleanup finish")
	}
	if err := s.ensureVoiceInputSource(tool); err != nil {
		s.toolError = inputSourceFailedToolError
		return err
	}
	if s.toolError == inputSourceDeferredToolError || s.toolError == inputSourceFailedToolError || s.toolError == inputSourceCleanupFailedToolError {
		s.toolError = ""
	}
	return nil
}

func builtInVoiceTools() []*voiceTool {
	return []*voiceTool{
		{id: appleSpeechToolID, name: "Apple 语音识别", kind: voiceRecognition, discover: discoverAppleSpeech, prepareAssets: prepareAppleSpeechAssets, audioRouteFactory: appleSpeechAudioRouteFactory, systemArtwork: "apple.logo"},
		{id: "voice-doubao", name: "豆包输入法", kind: voiceInputMethod, discover: doubao.Discover, locateApplication: doubao.LocateApplication, audioRouteFactory: doubaoAudioRouteFactory, requiredInputSource: doubao.RequiredInputSourceID},
		{id: "voice-typeless", name: "Typeless", kind: voiceInputMethod, discover: typeless.Discover, locateApplication: typeless.LocateApplication, fallbackIconPNG: typeless.IconPNG},
		{id: "voice-wetype", name: "微信输入法", kind: voiceInputMethod, discover: wetype.Discover, locateApplication: wetype.LocateApplication, audioRouteFactory: wetypeAudioRouteFactory, fallbackIconPNG: wetype.IconPNG, requiredInputSource: wetype.RequiredInputSourceID},
		{id: "voice-shandian", name: "闪电说", kind: voiceInputMethod, discover: shandian.Discover, locateApplication: shandian.LocateApplication},
		{id: "voice-chatgpt", name: "ChatGPT", kind: voiceApplication, discover: chatgpt.Discover, locateApplication: chatgpt.LocateApplication},
	}
}

// Discovery is one cancellable read per activation or explicit refresh. MyGo
// serializes completions; there is no watcher, polling loop, or target process.
func (t *voiceTool) refresh(locator voicetools.Locator, dispatch func(func()), changed func(uint64)) {
	t.refreshUsing(locator, dispatch, changed, t.discover)
}

func (t *voiceTool) refreshUsing(locator voicetools.Locator, dispatch func(func()), changed func(uint64), discover func(context.Context, voicetools.Locator) voicetools.Snapshot) {
	t.refreshAttempt(locator, dispatch, changed, discover, false)
}

func (t *voiceTool) refreshAssetsUsing(locator voicetools.Locator, dispatch func(func()), changed func(uint64), discover func(context.Context, voicetools.Locator) voicetools.Snapshot) {
	t.refreshAttempt(locator, dispatch, changed, discover, true)
}

func (t *voiceTool) refreshAttempt(locator voicetools.Locator, dispatch func(func()), changed func(uint64), discover func(context.Context, voicetools.Locator) voicetools.Snapshot, preparingAssets bool) {
	if !t.active {
		return
	}
	// Asset preparation can outlive cancellation while the system finishes an
	// installation request. Keep its owner until the existing discovery returns;
	// re-enabling the plugin waits on that attempt instead of starting another.
	if t.prepareAssets != nil && t.busy {
		return
	}
	if t.cancel != nil {
		t.cancel()
	}
	t.sequence++
	sequence := t.sequence
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.busy = cancel, true
	t.preparingAssets = preparingAssets
	t.cancelRequested, t.lastAttemptCancelled = false, false
	t.snapshot = voicetools.Snapshot{}
	go func() {
		result := discover(ctx, locator)
		cancel()
		dispatch(func() {
			t.completeAttempt(sequence, result, changed)
		})
	}()
}

func (t *voiceTool) requestCancel() bool {
	if !t.preparingAssets || !t.busy || t.cancel == nil || t.cancelRequested {
		return false
	}
	t.cancelRequested = true
	t.cancel()
	return true
}

func (t *voiceTool) completeAttempt(sequence uint64, result voicetools.Snapshot, changed func(uint64)) bool {
	if t.sequence != sequence {
		return false
	}
	wasCancelled := t.cancelRequested
	t.busy, t.cancel = false, nil
	t.preparingAssets = false
	t.cancelRequested = false
	t.lastAttemptCancelled = wasCancelled && result.State != voicetools.Ready
	t.snapshot = result
	if t.active && changed != nil {
		changed(sequence)
	}
	return true
}

func (t *voiceTool) stop() {
	if t.prepareAssets != nil && t.busy && t.cancel != nil {
		// A context cancellation requests cleanup; it does not prove the native
		// install worker has terminated. Retain busy/cancel until its callback.
		t.active = false
		if !t.cancelRequested {
			t.cancelRequested = true
			t.cancel()
		}
		return
	}
	t.sequence++
	if t.cancel != nil {
		t.cancel()
	}
	t.active, t.busy, t.cancel = false, false, nil
	t.preparingAssets = false
	t.cancelRequested, t.lastAttemptCancelled = false, false
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

func (s *shell) refreshVoiceTool(tool *voiceTool) { s.refreshVoiceToolUsing(tool, tool.discover) }

func (s *shell) refreshVoiceToolUsing(tool *voiceTool, discover func(context.Context, voicetools.Locator) voicetools.Snapshot) {
	s.refreshVoiceToolAttemptUsingWithDispatch(tool, discover, mygo.RunOnMain, false)
}

func (s *shell) refreshVoiceToolUsingWithDispatch(tool *voiceTool, discover func(context.Context, voicetools.Locator) voicetools.Snapshot, dispatch func(func())) {
	s.refreshVoiceToolAttemptUsingWithDispatch(tool, discover, dispatch, false)
}

func (s *shell) refreshVoiceToolAssetsUsing(tool *voiceTool, discover func(context.Context, voicetools.Locator) voicetools.Snapshot) {
	s.refreshVoiceToolAttemptUsingWithDispatch(tool, discover, mygo.RunOnMain, true)
}

func (s *shell) refreshVoiceToolAttemptUsingWithDispatch(tool *voiceTool, discover func(context.Context, voicetools.Locator) voicetools.Snapshot, dispatch func(func()), preparingAssets bool) {
	if s.closing {
		return
	}
	changed := func(sequence uint64) {
		s.finishVoiceToolDiscovery(tool, sequence)
	}
	if preparingAssets {
		tool.refreshAssetsUsing(s.toolLocator, dispatch, changed, discover)
	} else {
		tool.refreshUsing(s.toolLocator, dispatch, changed, discover)
	}
}

func (s *shell) finishVoiceToolDiscovery(tool *voiceTool, sequence uint64) {
	if s.closing {
		s.pendingInputSource = nil
		return
	}
	s.finishPendingInputSourceSelection(tool, sequence)
	s.refreshRemoteOutput(false)
	s.invalidateDeviceView()
}

func (s *shell) finishPendingInputSourceSelection(tool *voiceTool, sequence uint64) {
	pending := s.pendingInputSource
	if pending == nil || pending.tool != tool {
		return
	}
	// Consume even stale or now-ineligible intent. A later refresh or tool
	// switch must not revive an earlier explicit selection.
	s.pendingInputSource = nil
	if pending.sequence != sequence || tool == nil || !tool.active || tool.busy || tool.sequence != sequence || s.currentVoiceTool != tool.id {
		return
	}
	if _, required := requiredInputSourceForSelection(tool); !required {
		return
	}
	if s.inputSourceCleanupFailed() {
		s.toolError = inputSourceCleanupFailedToolError
		return
	}
	if s.inputSourceTransitionBusy(tool) {
		s.toolError = inputSourceDeferredToolError
		return
	}
	if err := s.ensureVoiceInputSource(tool); err != nil {
		s.toolError = inputSourceFailedToolError
		return
	}
	if s.toolError == inputSourceDeferredToolError || s.toolError == inputSourceFailedToolError || s.toolError == inputSourceCleanupFailedToolError {
		s.toolError = ""
	}
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
	s.toolPreferencesPath, err = hostDataPath("tools.json")
	if err != nil {
		s.toolLoadFailed, s.toolError = true, "插件设置位置不可用"
		return
	}
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
	if !enabled && s.pendingInputSource != nil && s.pendingInputSource.tool == tool {
		s.pendingInputSource = nil
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
	s.pendingInputSource = nil
	if s.currentVoiceTool == tool.id {
		if tool.busy && tool.requiredInputSource != nil {
			s.pendingInputSource = &pendingVoiceToolSelection{tool: tool, sequence: tool.sequence}
			return nil
		}
		if _, required := requiredInputSourceForSelection(tool); !required {
			return nil
		}
		if s.inputSourceCleanupFailed() {
			s.toolError = inputSourceCleanupFailedToolError
			return errInputSourceCleanupFailed
		}
		if s.inputSourceTransitionBusy(tool) {
			s.toolError = inputSourceDeferredToolError
			return errors.New("inputsource: selection deferred until voice action and route cleanup finish")
		}
		if err := s.ensureVoiceInputSource(tool); err != nil {
			s.toolError = inputSourceFailedToolError
			return err
		}
		s.toolError = ""
		s.refreshRemoteOutput(false)
		s.invalidateDeviceView()
		return nil
	}
	deferInputSourceSelection := tool.busy && tool.requiredInputSource != nil
	_, needsInputSource := requiredInputSourceForSelection(tool)
	inputSourceBusy := needsInputSource && s.inputSourceTransitionBusy(tool)
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
	if deferInputSourceSelection {
		s.pendingInputSource = &pendingVoiceToolSelection{tool: tool, sequence: tool.sequence}
	}
	if needsInputSource {
		if s.inputSourceCleanupFailed() {
			s.toolError = inputSourceCleanupFailedToolError
			s.refreshRemoteOutput(false)
			s.invalidateDeviceView()
			return errInputSourceCleanupFailed
		}
		if inputSourceBusy {
			s.toolError = inputSourceDeferredToolError
			s.refreshRemoteOutput(false)
			s.invalidateDeviceView()
			return errors.New("inputsource: selection deferred until voice action and route cleanup finish")
		}
		if err := s.ensureVoiceInputSource(tool); err != nil {
			s.toolError = inputSourceFailedToolError
			s.refreshRemoteOutput(false)
			s.invalidateDeviceView()
			return err
		}
	}
	s.toolError = ""
	s.refreshRemoteOutput(false)
	s.invalidateDeviceView()
	return nil
}

func voiceToolStatus(tool *voiceTool) string {
	if tool.id == appleSpeechToolID {
		if !tool.active {
			return "未启用"
		}
		if tool.busy {
			if tool.cancelRequested {
				return "正在取消；等待系统模型操作结束"
			}
			return "正在准备"
		}
		if tool.snapshot.Source == appleSpeechCleanupPendingSource {
			return "等待系统模型操作结束，请重新检查"
		}
		if tool.lastAttemptCancelled {
			return "准备已取消"
		}
		switch tool.snapshot.State {
		case voicetools.Ready:
			return "已就绪"
		case voicetools.ConfigMissing:
			return "请下载中文模型"
		case voicetools.Unsupported:
			return "此 Mac 暂不支持"
		default:
			return "准备失败，请重试"
		}
	}
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
