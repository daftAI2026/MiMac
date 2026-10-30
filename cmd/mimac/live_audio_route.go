package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/adapters/doubao"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/audioroute"
	"mimac.local/mimac/internal/platform/processidentity"
	"mimac.local/mimac/internal/voicetools"
)

const (
	compatAudioDeviceUID       = "dev.mimac.audio.compat.v1"
	compatDoubaoBundleID       = "com.bytedance.inputmethod.doubaoime"
	compatDoubaoBundleName     = "DoubaoIme.app"
	compatDoubaoExecutableName = "DoubaoIme"
	compatAudioSampleRate      = 16000
	compatTargetIdleTimeout    = 1500 * time.Millisecond
	compatTargetIdlePoll       = 20 * time.Millisecond
)

var (
	errCompatRouteContext       = errors.New("mimac-compat route: context is required")
	errCompatRouteGeneration    = errors.New("mimac-compat route: nonzero device generation is required")
	errCompatRouteTarget        = errors.New("mimac-compat route: only a verified input method target is supported")
	errCompatRouteTargetBusy    = errors.New("mimac-compat route: target input is busy")
	errCompatRouteTargetMissing = errors.New("mimac-compat route: captured target process is no longer the unique Core Audio client")
	errCompatRouteTargetNil     = errors.New("mimac-compat route: captured target identity is unavailable")
	errCompatRouteDeviceUID     = errors.New("mimac-compat route: verified MiMac endpoint is unavailable")
	errCompatRouteOwnerNil      = errors.New("mimac-compat route: Xiaomi device owner is unavailable")
	errCompatRouteUnavailable   = errors.New("mimac-compat route: Xiaomi route is unavailable")
	errCompatRouteOperationBusy = errors.New("mimac-compat route: start or close operation is already in progress")
)

type compatAudioInspector interface {
	DeviceByUID(string) (audioroute.DeviceCapabilities, error)
	InputsForBundle(string) ([]audioroute.ProcessInput, error)
	ProcessInput(int32) (audioroute.ProcessInput, error)
	WaitForInput(context.Context, int32, string) error
}

type compatProcessIdentity interface {
	Check() error
}

type compatAudioRouteTarget struct {
	bundleID       string
	bundleName     string
	executableName string
	name           string
}

type compatAudioSelection struct {
	preferredUID *string
}

// The adapter keeps atvv's concrete return type behind a testable boundary.
type compatATVVOwner interface {
	newSource(context.Context, atvv.StreamKey) (audiosession.Source, error)
	close() error
}

type compatAudioRouteDependencies struct {
	newInspector     func() (compatAudioInspector, error)
	readSelection    func(context.Context, voicetools.Locator) (compatAudioSelection, error)
	requestSelection func(context.Context, voicetools.Locator, *string) error
	captureIdentity  func(int32, string) (compatProcessIdentity, error)
	openDeviceOwner  func(uint64, string) (compatATVVOwner, error)
	openSink         xiaomiAudioSinkFactory

	idleTimeout time.Duration
	idlePoll    time.Duration
}

type nativeCompatATVVOwner struct{ owner *atvv.DeviceOwner }

func (o *nativeCompatATVVOwner) newSource(ctx context.Context, key atvv.StreamKey) (audiosession.Source, error) {
	if o == nil || o.owner == nil {
		return nil, errCompatRouteOwnerNil
	}
	return o.owner.NewSource(ctx, key)
}

func (o *nativeCompatATVVOwner) close() error {
	if o == nil || o.owner == nil {
		return nil
	}
	return o.owner.Close()
}

func defaultCompatAudioRouteDependencies() compatAudioRouteDependencies {
	return compatAudioRouteDependencies{
		newInspector: func() (compatAudioInspector, error) { return audioroute.New() },
		captureIdentity: func(pid int32, executable string) (compatProcessIdentity, error) {
			return processidentity.Capture(pid, executable)
		},
		openDeviceOwner: func(generation uint64, path string) (compatATVVOwner, error) {
			owner, err := atvv.OpenDeviceOwner(generation, path)
			if owner == nil {
				return nil, err
			}
			return &nativeCompatATVVOwner{owner: owner}, err
		},
		openSink:    openXiaomiAudioSink,
		idleTimeout: compatTargetIdleTimeout,
		idlePoll:    compatTargetIdlePoll,
	}
}

func defaultDoubaoAudioRouteDependencies() compatAudioRouteDependencies {
	deps := defaultCompatAudioRouteDependencies()
	deps.readSelection = func(ctx context.Context, locator voicetools.Locator) (compatAudioSelection, error) {
		selection, err := doubao.ReadMicrophoneSelection(ctx, locator)
		if err != nil {
			return compatAudioSelection{}, err
		}
		return compatAudioSelection{preferredUID: cloneMicrophoneOptional(selection.SelectedMicrophoneID)}, nil
	}
	deps.requestSelection = doubao.RequestMicrophoneSelection
	return deps
}

func doubaoAudioRouteTarget() compatAudioRouteTarget {
	return compatAudioRouteTarget{
		bundleID: compatDoubaoBundleID, bundleName: compatDoubaoBundleName,
		executableName: compatDoubaoExecutableName, name: "Doubao",
	}
}

// openLiveXiaomiRoute is restricted to the Doubao input method contract and
// the single MiMac HAL UID. The request API is a one-way
// distributed notification with no ACK; success below means only that the
// local post completed. Source and sink are prepared ahead of the session;
// receiver readiness is independently checked against the captured PID and
// exact UID before any PCM is delivered to the sink.
func openLiveXiaomiRoute(
	ctx context.Context,
	generation uint64,
	libraryPath string,
	targetBundleID string,
	targetExecutablePath string,
	locator voicetools.Locator,
) (liveAudioRoute, error) {
	return openLiveXiaomiRouteWithDependencies(ctx, generation, libraryPath, targetBundleID, targetExecutablePath, locator, defaultDoubaoAudioRouteDependencies())
}

func openLiveXiaomiRouteWithDependencies(
	ctx context.Context,
	generation uint64,
	libraryPath string,
	targetBundleID string,
	targetExecutablePath string,
	locator voicetools.Locator,
	deps compatAudioRouteDependencies,
) (liveAudioRoute, error) {
	if targetBundleID != compatDoubaoBundleID {
		return nil, errCompatRouteTarget
	}
	return openLiveXiaomiRouteForTargetWithDependencies(
		ctx, generation, libraryPath, doubaoAudioRouteTarget(), targetExecutablePath, locator, deps,
	)
}

func openLiveXiaomiRouteForTargetWithDependencies(
	ctx context.Context,
	generation uint64,
	libraryPath string,
	target compatAudioRouteTarget,
	targetExecutablePath string,
	locator voicetools.Locator,
	deps compatAudioRouteDependencies,
) (liveAudioRoute, error) {
	if ctx == nil {
		return nil, errCompatRouteContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if generation == 0 {
		return nil, errCompatRouteGeneration
	}
	if err := validateCompatAudioRouteTarget(target, targetExecutablePath, locator); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(libraryPath) || strings.ContainsRune(libraryPath, '\x00') {
		return nil, errors.New("mimac-compat route: absolute ATVV library path is required")
	}
	if deps.newInspector == nil || deps.readSelection == nil || deps.requestSelection == nil ||
		deps.captureIdentity == nil || deps.openDeviceOwner == nil || deps.openSink == nil {
		return nil, errors.New("mimac-compat route: native dependencies are incomplete")
	}
	if deps.idleTimeout <= 0 {
		deps.idleTimeout = compatTargetIdleTimeout
	}
	if deps.idlePoll <= 0 {
		deps.idlePoll = compatTargetIdlePoll
	}

	inspector, err := deps.newInspector()
	if err != nil {
		return nil, err
	}
	capabilities, err := inspector.DeviceByUID(compatAudioDeviceUID)
	if err != nil {
		return nil, fmt.Errorf("mimac-compat route: inspect endpoint: %w", err)
	}
	if !capabilities.Exists || capabilities.InputChannels == 0 || capabilities.OutputChannels == 0 {
		return nil, errCompatRouteDeviceUID
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	process, err := uniqueCompatTargetProcess(inspector, target.bundleID)
	if err != nil {
		return nil, err
	}
	identity, err := deps.captureIdentity(process.PID, targetExecutablePath)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, errCompatRouteTargetNil
	}
	if err := identity.Check(); err != nil {
		return nil, fmt.Errorf("mimac-compat route: capture target identity: %w", err)
	}
	if err := requireCompatTargetIdle(ctx, inspector, identity, process.PID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	saved, err := deps.readSelection(ctx, locator)
	if err != nil {
		return nil, fmt.Errorf("mimac-compat route: snapshot %s microphone selection: %w", target.name, err)
	}
	if err := identity.Check(); err != nil {
		return nil, fmt.Errorf("mimac-compat route: target changed while snapshotting selection: %w", err)
	}
	if err := requireCompatTargetIdle(ctx, inspector, identity, process.PID); err != nil {
		return nil, err
	}

	lease := &compatLiveAudioRoute{
		generation:  generation,
		locator:     locator,
		target:      target,
		pid:         process.PID,
		identity:    identity,
		inspector:   inspector,
		saved:       cloneMicrophoneOptional(saved.preferredUID),
		request:     deps.requestSelection,
		idleTimeout: deps.idleTimeout,
		idlePoll:    deps.idlePoll,
		closeGate:   make(chan struct{}, 1),
	}

	var owner compatATVVOwner
	newSource := func(sourceCtx context.Context, key atvv.StreamKey) (audiosession.Source, error) {
		if owner == nil {
			return nil, errCompatRouteOwnerNil
		}
		return owner.newSource(sourceCtx, key)
	}
	closeDevice := func() error {
		if owner == nil {
			return nil
		}
		return owner.close()
	}
	inner, err := newXiaomiAudioRouteWithFactories(
		generation,
		newSource,
		closeDevice,
		audiooutput.Config{DeviceUID: compatAudioDeviceUID, SampleRate: compatAudioSampleRate},
		func(config audiooutput.Config) (xiaomiPreparedSink, error) {
			sink, openErr := deps.openSink(config)
			if sink == nil {
				return nil, openErr
			}
			return &compatAudioReceiverSink{
				xiaomiPreparedSink: sink,
				inspector:          inspector,
				identity:           identity,
				pid:                process.PID,
				deviceUID:          compatAudioDeviceUID,
			}, openErr
		},
	)
	if err != nil {
		return nil, err // No selection request has been sent yet.
	}
	lease.route = inner

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := lease.verifyCapturedTargetIdle(ctx); err != nil {
		return nil, err
	}
	owner, err = deps.openDeviceOwner(generation, libraryPath)
	if err != nil {
		return lease, fmt.Errorf("mimac-compat route: open Xiaomi device owner: %w", err)
	}
	if owner == nil {
		return lease, errCompatRouteOwnerNil
	}
	if err := ctx.Err(); err != nil {
		return lease, err
	}
	return lease, nil
}

func validateCompatAudioRouteTarget(target compatAudioRouteTarget, executablePath string, locator voicetools.Locator) error {
	if target.bundleID == "" || target.bundleName == "" || target.executableName == "" || target.name == "" ||
		!filepath.IsAbs(executablePath) || filepath.Base(executablePath) != target.executableName {
		return errCompatRouteTarget
	}
	cleanExecutable := filepath.Clean(executablePath)
	for _, bundlePath := range voicetools.ApplicationCandidates(locator.InputMethodDirs, target.bundleName) {
		if cleanExecutable == filepath.Join(bundlePath, "Contents", "MacOS", target.executableName) {
			return nil
		}
	}
	return fmt.Errorf("%w: executable is outside the locator's %s bundle candidates", errCompatRouteTarget, target.name)
}

func uniqueCompatTargetProcess(inspector compatAudioInspector, bundleID string) (audioroute.ProcessInput, error) {
	processes, err := inspector.InputsForBundle(bundleID)
	if err != nil {
		return audioroute.ProcessInput{}, fmt.Errorf("mimac-compat route: inspect target process: %w", err)
	}
	if len(processes) != 1 || !processes[0].Found || processes[0].PID <= 0 {
		return audioroute.ProcessInput{}, fmt.Errorf("mimac-compat route: expected one Core Audio client for %q, found %d", bundleID, len(processes))
	}
	return processes[0], nil
}

func requireCompatTargetIdle(ctx context.Context, inspector compatAudioInspector, identity compatProcessIdentity, pid int32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := identity.Check(); err != nil {
		return err
	}
	process, err := inspector.ProcessInput(pid)
	if err != nil {
		return fmt.Errorf("mimac-compat route: inspect target input state: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := identity.Check(); err != nil {
		return err
	}
	if !process.Found || process.PID != pid {
		return errCompatRouteTargetMissing
	}
	if process.RunningInput {
		return errCompatRouteTargetBusy
	}
	return nil
}

func cloneMicrophoneOptional(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

type compatLiveAudioRoute struct {
	generation uint64
	locator    voicetools.Locator
	target     compatAudioRouteTarget
	pid        int32
	identity   compatProcessIdentity
	inspector  compatAudioInspector
	route      *xiaomiAudioRoute
	saved      *string
	request    func(context.Context, voicetools.Locator, *string) error

	idleTimeout time.Duration
	idlePoll    time.Duration

	closeGate          chan struct{}
	mu                 sync.Mutex
	closing            bool
	closed             bool
	routeDone          bool
	leaseDone          bool
	retired            bool
	selectionAttempted bool
}

var _ liveAudioRoute = (*compatLiveAudioRoute)(nil)

func (r *compatLiveAudioRoute) Start() error {
	if r == nil || r.route == nil {
		return errCompatRouteUnavailable
	}
	return r.route.Start()
}

// BeforeStart reasserts the selected input immediately before a verified voice
// action. The request is one-way; WaitReceiver remains the independent exact
// PID/UID gate before PCM is delivered.
func (r *compatLiveAudioRoute) BeforeStart(ctx context.Context) error {
	if r == nil || r.route == nil {
		return errCompatRouteUnavailable
	}
	if ctx == nil {
		return errCompatRouteContext
	}
	select {
	case r.closeGate <- struct{}{}:
	default:
		return errCompatRouteOperationBusy
	}
	defer func() { <-r.closeGate }()
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	closing := r.closing || r.closed || r.routeDone
	r.mu.Unlock()
	if closing {
		return errCompatRouteUnavailable
	}
	if err := r.verifyCapturedTargetIdle(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.request == nil {
		return errors.New("mimac-compat route: microphone selection requester is unavailable")
	}

	uid := compatAudioDeviceUID
	// Mark before posting: a local error does not prove the notification was
	// undelivered, so Close must still attempt restoration of the captured Optional.
	r.mu.Lock()
	if r.closing || r.closed || r.routeDone {
		r.mu.Unlock()
		return errCompatRouteUnavailable
	}
	r.selectionAttempted = true
	r.mu.Unlock()

	if err := r.request(ctx, r.locator, &uid); err != nil {
		return fmt.Errorf("mimac-compat route: request MiMac microphone selection (unacknowledged): %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.verifyCapturedTargetIdle(ctx); err != nil {
		return fmt.Errorf("mimac-compat route: verify target after microphone request: %w", err)
	}
	return nil
}

func (r *compatLiveAudioRoute) verifyCapturedTargetIdle(ctx context.Context) error {
	if r.identity == nil || r.inspector == nil || r.pid <= 0 {
		return errCompatRouteTargetNil
	}
	process, err := uniqueCompatTargetProcess(r.inspector, r.target.bundleID)
	if err != nil {
		return err
	}
	if process.PID != r.pid {
		return errCompatRouteTargetMissing
	}
	return requireCompatTargetIdle(ctx, r.inspector, r.identity, r.pid)
}

func (r *compatLiveAudioRoute) Ready() bool {
	if r == nil || r.route == nil {
		return false
	}
	return r.route.Ready()
}

func (r *compatLiveAudioRoute) Error() error {
	if r == nil {
		return errCompatRouteUnavailable
	}
	if r.route == nil {
		return errCompatRouteUnavailable
	}
	return r.route.Error()
}

func (r *compatLiveAudioRoute) Changes() <-chan struct{} {
	if r == nil || r.route == nil {
		return nil
	}
	return r.route.Changes()
}

func (r *compatLiveAudioRoute) Prepare(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
	if r == nil || r.route == nil {
		return audiosession.Resources{}, errCompatRouteUnavailable
	}
	if token.Device().Generation() != r.generation {
		return audiosession.Resources{}, errXiaomiAudioTokenGeneration
	}
	return r.route.Prepare(ctx, token)
}

// Close first quiesces the audio resources and ATVV owner. Only then does it
// wait, with a bound, for the captured target process to become input-idle and
// post the saved Optional value. A changed/exited original process retires its
// in-memory lease; the global notification is never sent to a known new PID.
// A nil result from RequestMicrophoneSelection confirms only local posting,
// not effective target restoration.
func (r *compatLiveAudioRoute) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errCompatRouteContext
	}
	if r.closeGate == nil {
		r.closeGate = make(chan struct{}, 1)
	}
	select {
	case r.closeGate <- struct{}{}:
	case <-ctx.Done():
		return errors.Join(audiosession.ErrCleanupPending, ctx.Err())
	}
	defer func() { <-r.closeGate }()
	if err := ctx.Err(); err != nil {
		return errors.Join(audiosession.ErrCleanupPending, err)
	}

	r.mu.Lock()
	closed, routeDone := r.closed, r.routeDone
	if !closed {
		r.closing = true
	}
	r.mu.Unlock()
	if closed {
		return nil
	}
	if !routeDone && r.route != nil {
		if err := r.route.Close(ctx); err != nil {
			return err
		}
		r.mu.Lock()
		r.routeDone = true
		r.mu.Unlock()
	} else if r.route == nil {
		r.mu.Lock()
		r.routeDone = true
		r.mu.Unlock()
	}

	r.mu.Lock()
	selectionAttempted, leaseDone, retired := r.selectionAttempted, r.leaseDone, r.retired
	r.mu.Unlock()
	if selectionAttempted && !leaseDone && !retired {
		retired, err := r.waitForCapturedTargetIdle(ctx)
		if err != nil {
			return err
		}
		if retired {
			r.mu.Lock()
			r.retired, r.leaseDone = true, true
			r.mu.Unlock()
		} else {
			alive, err := r.capturedTargetAlive()
			if err != nil {
				return err
			}
			if !alive {
				r.mu.Lock()
				r.retired, r.leaseDone = true, true
				r.mu.Unlock()
			}
			if err := r.verifyRestoreTargetIdle(ctx); err != nil {
				if !r.isLeaseDone() {
					return err
				}
			}
			if !r.isLeaseDone() {
				if err := r.identity.Check(); err != nil {
					if errors.Is(err, processidentity.ErrIdentityChanged) {
						r.mu.Lock()
						r.retired, r.leaseDone = true, true
						r.mu.Unlock()
					} else {
						return fmt.Errorf("mimac-compat route: verify original target before restoring selection: %w", err)
					}
				}
			}
			if !r.isLeaseDone() {
				if r.request == nil {
					return errors.New("mimac-compat route: microphone selection requester is unavailable")
				}
				if err := ctx.Err(); err != nil {
					return errors.Join(audiosession.ErrCleanupPending, err)
				}
				if err := r.request(ctx, r.locator, cloneMicrophoneOptional(r.saved)); err != nil {
					return fmt.Errorf("mimac-compat route: restore saved microphone selection (unacknowledged): %w", err)
				}
				r.mu.Lock()
				r.leaseDone = true
				r.mu.Unlock()
			}
		}
	}

	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

func (r *compatLiveAudioRoute) isLeaseDone() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaseDone
}

func (r *compatLiveAudioRoute) waitForCapturedTargetIdle(ctx context.Context) (bool, error) {
	if r.identity == nil || r.inspector == nil {
		return false, errCompatRouteTargetNil
	}
	waitCtx, cancel := context.WithTimeout(ctx, r.idleTimeout)
	defer cancel()
	for {
		if err := waitCtx.Err(); err != nil {
			return false, errors.Join(audiosession.ErrCleanupPending, err)
		}
		alive, err := r.capturedTargetAlive()
		if err != nil {
			return false, err
		}
		if !alive {
			return true, nil
		}
		process, err := r.inspector.ProcessInput(r.pid)
		if err != nil {
			return false, fmt.Errorf("mimac-compat route: wait for %s input idle: %w", r.target.name, err)
		}
		if err := waitCtx.Err(); err != nil {
			return false, errors.Join(audiosession.ErrCleanupPending, err)
		}
		if !process.Found {
			// The process identity is still alive but Core Audio no longer exposes
			// its client object; retain the lease instead of broadcasting to an
			// unverified notification recipient.
			return false, errCompatRouteTargetMissing
		}
		if process.PID != r.pid {
			return false, errCompatRouteTargetMissing
		}
		if !process.RunningInput {
			if err := r.identity.Check(); err != nil {
				if errors.Is(err, processidentity.ErrIdentityChanged) {
					return true, nil
				}
				return false, fmt.Errorf("mimac-compat route: verify idle %s identity: %w", r.target.name, err)
			}
			return false, nil
		}
		timer := time.NewTimer(r.idlePoll)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return false, errors.Join(audiosession.ErrCleanupPending, waitCtx.Err())
		case <-timer.C:
		}
	}
}

// capturedTargetAlive distinguishes a retired target from a transient native
// identity read failure. It never considers another bundle PID to be the
// captured target and never requests restoration for that new generation.
func (r *compatLiveAudioRoute) capturedTargetAlive() (bool, error) {
	err := r.identity.Check()
	if err == nil {
		return true, nil
	}
	if errors.Is(err, processidentity.ErrIdentityChanged) {
		return false, nil
	}
	if !errors.Is(err, processidentity.ErrReadFailed) {
		return false, fmt.Errorf("mimac-compat route: check captured %s identity: %w", r.target.name, err)
	}
	processes, listErr := r.inspector.InputsForBundle(r.target.bundleID)
	if listErr != nil {
		return false, errors.Join(fmt.Errorf("mimac-compat route: check captured %s exit: %w", r.target.name, err), listErr)
	}
	for _, process := range processes {
		if process.PID == r.pid {
			return false, fmt.Errorf("mimac-compat route: captured PID is still a Core Audio client but identity is unreadable: %w", err)
		}
	}
	return false, nil
}

func (r *compatLiveAudioRoute) verifyRestoreTargetIdle(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(audiosession.ErrCleanupPending, err)
	}
	processes, err := r.inspector.InputsForBundle(r.target.bundleID)
	if err != nil {
		return fmt.Errorf("mimac-compat route: verify target before restoring selection: %w", err)
	}
	if len(processes) != 1 || !processes[0].Found || processes[0].PID != r.pid {
		return errCompatRouteTargetMissing
	}
	if err := r.identity.Check(); err != nil {
		return fmt.Errorf("mimac-compat route: verify target before restoring selection: %w", err)
	}
	process, err := r.inspector.ProcessInput(r.pid)
	if err != nil {
		return fmt.Errorf("mimac-compat route: verify target idle before restoring selection: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(audiosession.ErrCleanupPending, err)
	}
	if !process.Found || process.PID != r.pid {
		return errCompatRouteTargetMissing
	}
	if process.RunningInput {
		return errCompatRouteTargetBusy
	}
	return nil
}
