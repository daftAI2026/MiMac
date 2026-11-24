package main

import (
	"context"
	"path/filepath"
	"time"

	"mimac.local/mimac/internal/adapters/doubao"
	"mimac.local/mimac/internal/adapters/windowlease"
	"mimac.local/mimac/internal/bridge"
)

// A voice tool registers only its own native-prompt suppressor. The host
// supplies the process identity already captured by the selected audio route;
// it never searches unrelated input methods for a matching window.
type voicePromptLease interface {
	RestoreAfterDismissal(context.Context) error
	Restore(context.Context) error
}

type voicePromptSuppressor interface {
	Suppress(context.Context) (voicePromptLease, string, error)
}

type voicePromptTarget interface {
	voicePromptTarget() (bundleID string, pid int32, executableName string, ok bool)
}

type suppressRecordingWindow func(context.Context) (*windowlease.Lease, windowlease.Outcome, error)

func (suppress suppressRecordingWindow) Suppress(ctx context.Context) (voicePromptLease, string, error) {
	lease, outcome, err := suppress(ctx)
	if lease == nil {
		return nil, string(outcome), err
	}
	return lease, string(outcome), err
}

func newDoubaoVoicePromptSuppressor(pid int32, executable string) voicePromptSuppressor {
	baseline := doubao.CaptureRecordingBarBaseline(pid, executable)
	if baseline == nil {
		return nil
	}
	return suppressRecordingWindow(baseline.Suppress)
}

// voicePromptPresentation owns one pending baseline and any still-restoring
// session leases. It is main-thread state; each worker owns immutable copies.
type voicePromptPresentation struct {
	baseline voicePromptSuppressor
	jobs     map[bridge.SessionToken]*voicePromptPresentationJob
}

type voicePromptPresentationJob struct {
	token        bridge.SessionToken
	cancel       context.CancelFunc
	cancelSearch context.CancelFunc
	sessionEnd   chan struct{}
	done         chan struct{}
	explicitStop bool
}

func (s *shell) canSuppressVoiceToolPrompt(tool *voiceTool) bool {
	if s == nil {
		return false
	}
	mode := s.preferencesState.value.VoicePresentation
	return tool != nil && tool.voicePromptSuppressor != nil && tool.voicePromptBundleID != "" &&
		s.preferencesState.value.OnlyShowMiMacVoicePrompt && !s.preferencesState.loadFailed && !s.closing &&
		(mode == voicePresentationMiMacSphere || mode == voicePresentationDotBar) &&
		s.currentVoiceTool == tool.id && s.liveAudio.toolID == tool.id && !s.liveAudio.retiring &&
		s.voicePresentationActive() && s.voiceOverlay.err == nil
}

// prepareVoicePromptPresentation captures the selected adapter's baseline
// after route preflight and immediately before Core posts its existing key.
func (s *shell) prepareVoicePromptPresentation() {
	tool := s.selectedVoiceTool()
	if !s.canSuppressVoiceToolPrompt(tool) {
		return
	}
	a := &s.voiceOverlay.voicePrompt
	a.finishCompletedWorkers()
	for _, job := range a.jobs {
		if job.explicitStop {
			return // Wait for an explicitly requested immediate restore.
		}
		// Core has admitted a new press only after the previous session ended.
		// Stop its remaining discovery window before this press posts its key.
		s.finishVoicePromptPresentation(job.token)
		if job.cancelSearch != nil {
			job.cancelSearch()
		}
	}
	a.baseline = nil
	connection := s.liveAudio.connection
	if connection == nil {
		return
	}
	select {
	case <-connection.Done():
	default:
		return
	}
	route, ok := connection.route.(voicePromptTarget)
	if !ok {
		return
	}
	bundleID, pid, executableName, ok := route.voicePromptTarget()
	if !ok || pid <= 0 || bundleID != tool.voicePromptBundleID || executableName == "" || s.liveAudio.appPath == "" {
		return
	}
	executable := filepath.Join(s.liveAudio.appPath, "Contents", "MacOS", executableName)
	if filepath.Base(executable) != executableName || !filepath.IsAbs(executable) {
		return
	}
	a.baseline = tool.voicePromptSuppressor(pid, executable)
}

func (a *voicePromptPresentation) finishCompletedWorkers() {
	if a == nil {
		return
	}
	for token, job := range a.jobs {
		select {
		case <-job.done:
			delete(a.jobs, token)
		default:
		}
	}
}

func (s *shell) syncVoicePromptPresentation(frame voiceOverlayFrame) {
	a := &s.voiceOverlay.voicePrompt
	a.finishCompletedWorkers()
	if !s.canSuppressVoiceToolPrompt(s.selectedVoiceTool()) {
		s.stopVoicePromptPresentation()
		return
	}
	if !frame.Visible {
		a.baseline = nil
		for token := range a.jobs {
			s.finishVoicePromptPresentation(token)
		}
		return
	}
	for token := range a.jobs {
		if token != frame.Token {
			// A stale lease can outlive a session ending. Let its owner wait for
			// orderOut; never restore it merely because a new session appeared.
			s.finishVoicePromptPresentation(token)
			if job := a.jobs[token]; job != nil && job.cancelSearch != nil {
				job.cancelSearch()
			}
		}
	}
	if _, exists := a.jobs[frame.Token]; exists {
		return
	}
	if frame.Phase != bridge.PhaseCapturing || a.baseline == nil {
		return
	}

	baseline := a.baseline
	a.baseline = nil
	ctx, cancel := context.WithCancel(context.Background())
	ended, done := make(chan struct{}), make(chan struct{})
	searchCtx, cancelSearch := context.WithTimeout(ctx, 2*time.Second)
	job := &voicePromptPresentationJob{
		token: frame.Token, cancel: cancel, cancelSearch: cancelSearch,
		sessionEnd: ended, done: done,
	}
	if a.jobs == nil {
		a.jobs = make(map[bridge.SessionToken]*voicePromptPresentationJob)
	}
	a.jobs[frame.Token] = job
	go func(job *voicePromptPresentationJob) {
		defer close(done)
		lease, _, _ := baseline.Suppress(searchCtx)
		job.cancelSearch()
		if lease == nil {
			return
		}
		select {
		case <-ctx.Done():
			_ = lease.Restore(context.Background())
		case <-job.sessionEnd:
			_ = lease.RestoreAfterDismissal(ctx)
		}
	}(job)
}

// finishVoicePromptPresentation requests normal end-of-session cleanup. The
// adapter waits for its window to disappear before restoring the old position.
func (s *shell) finishVoicePromptPresentation(token bridge.SessionToken) {
	if s == nil || token == (bridge.SessionToken{}) {
		return
	}
	a := &s.voiceOverlay.voicePrompt
	job := a.jobs[token]
	if job == nil {
		return
	}
	select {
	case <-job.sessionEnd:
	default:
		close(job.sessionEnd)
	}
}

// stopVoicePromptPresentation is reserved for an explicit preference, target,
// style, or app-lifecycle change. Cancelling the worker restores immediately.
func (s *shell) stopVoicePromptPresentation() {
	if s == nil {
		return
	}
	a := &s.voiceOverlay.voicePrompt
	a.baseline = nil
	for _, job := range a.jobs {
		if job.explicitStop {
			continue
		}
		job.explicitStop = true
		job.cancel()
	}
}

// waitVoicePromptPresentationRestore is reserved for app shutdown. Preference
// changes cancel workers and let their immediate restores finish off-thread.
// Shutdown joins so the host does not exit while a native window lease remains.
func (s *shell) waitVoicePromptPresentationRestore() {
	if s == nil {
		return
	}
	a := &s.voiceOverlay.voicePrompt
	for _, job := range a.jobs {
		<-job.done
	}
	a.finishCompletedWorkers()
}
