package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/audioroute"
	"mimac.local/mimac/internal/platform/processidentity"
	"mimac.local/mimac/internal/voicetools"
)

func TestLiveAudioProcessReplacementRequiresExplicitRouteRebuild(t *testing.T) {
	oldRoute, oldInspector, oldIdentity, oldOwner, oldRequests, oldSource, oldSink := openProcessRecoveryRoute(t, 42)
	oldConnection := processRecoveryConnection(t, oldRoute)
	if err := oldConnection.BeforeStart(context.Background()); err != nil {
		t.Fatalf("old target start preflight: %v", err)
	}
	oldInspector.setProcessInput(audioroute.ProcessInput{
		Found: true, PID: 42, RunningInput: true, InputDeviceUIDs: []string{compatAudioDeviceUID},
	})

	core, device, toggleKey := processRecoveryCore(t)
	oldRuntime := processRecoveryRuntime(t, oldConnection)
	oldPress, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	toggleTaps := processRecoveryApply(t, core, oldRuntime, bridge.VoiceBegan(oldPress))
	if toggleTaps != 1 {
		t.Fatalf("initial target toggle count = %d, want 1", toggleTaps)
	}
	prepared := processRecoveryCompletion(t, oldRuntime)
	if prepared.Err != nil {
		t.Fatalf("old route preparation failed: %v", prepared.Err)
	}
	toggleTaps += processRecoveryApply(t, core, oldRuntime, prepared.Event)
	waitProcessRecoverySignal(t, oldSource.readStarted, "old source capture")
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseCapturing {
		t.Fatalf("old route did not reach capture: %+v", snapshot)
	}

	// The original process exits and a new process with the same bundle appears.
	// The active route must fail closed against its captured identity and PID.
	oldIdentity.setError(processidentity.ErrIdentityChanged)
	oldInspector.setProcesses([]audioroute.ProcessInput{{Found: true, PID: 43}})
	oldInspector.setProcessInput(audioroute.ProcessInput{Found: false, PID: 42})
	failed := processRecoveryCompletion(t, oldRuntime)
	if !errors.Is(failed.Err, processidentity.ErrIdentityChanged) {
		t.Fatalf("old session failure = %v, want captured-identity change", failed.Err)
	}
	failureTaps := processRecoveryApply(t, core, oldRuntime, failed.Event)
	if failureTaps != 0 {
		t.Fatalf("PID replacement emitted %d target toggles, want none", failureTaps)
	}
	toggleTaps += failureTaps
	if snapshot := core.Snapshot(); snapshot.HasSession || len(snapshot.RecoveryKeys) != 1 || snapshot.RecoveryKeys[0] != toggleKey {
		t.Fatalf("failed toggle session did not retain its explicit recovery gate: %+v", snapshot)
	}

	// A late physical release after the receiver failure must not synthesize a
	// second toggle. The failed target's state remains explicitly uncertain.
	if taps := processRecoveryApply(t, core, oldRuntime, bridge.VoiceEnded(oldPress)); taps != 0 {
		t.Fatalf("late release emitted %d target toggles, want none", taps)
	}
	if err := oldRuntime.Close(context.Background()); err != nil {
		t.Fatalf("close old runtime: %v", err)
	}
	if oldSource.closeCount() != 1 || oldSink.closes() != 1 {
		t.Fatalf("old Runtime cleanup counts source=%d sink=%d, want one each", oldSource.closeCount(), oldSink.closes())
	}

	// Explicitly retire the connection. Its captured selection lease must not
	// restore into the replacement PID discovered above.
	if taps := processRecoveryApply(t, core, oldRuntime, bridge.RouteAvailable(false)); taps != 0 {
		t.Fatalf("route retirement emitted %d target toggles, want none", taps)
	}
	closeProcessRecoveryConnection(t, oldConnection)
	if _, closeCalls := oldOwner.counts(); closeCalls != 1 {
		t.Fatalf("old route owner close calls = %d, want 1", closeCalls)
	}
	if got := oldRequests.snapshot(); len(got) != 1 || got[0] == nil || *got[0] != compatAudioDeviceUID {
		t.Fatalf("old PID selection requests after retirement = %v, want only its initial MiMac request", got)
	}

	// Rebuilding is explicit: the new route captures PID 43 independently and
	// must pass its own preflight before Core can admit another press.
	newRoute, newInspector, newIdentity, newOwner, newRequests, newSource, newSink := openProcessRecoveryRoute(t, 43)
	newConnection := processRecoveryConnection(t, newRoute)
	if err := newConnection.BeforeStart(context.Background()); err != nil {
		t.Fatalf("rebuilt target start preflight: %v", err)
	}
	newInspector.setProcessInput(audioroute.ProcessInput{
		Found: true, PID: 43, RunningInput: true, InputDeviceUIDs: []string{compatAudioDeviceUID},
	})
	if newIdentity.Check() != nil || newRoute.pid != 43 {
		t.Fatalf("rebuilt route identity not bound to replacement PID: pid=%d identityErr=%v", newRoute.pid, newIdentity.Check())
	}
	if got := newRequests.snapshot(); len(got) != 1 || got[0] == nil || *got[0] != compatAudioDeviceUID {
		t.Fatalf("new PID start requests = %v, want one request from the rebuilt route", got)
	}

	// The user confirms the replacement target is idle after the explicit
	// rebuild. Core can then admit the new press; no automatic retry occurred.
	if taps := processRecoveryApply(t, core, oldRuntime, bridge.ConfirmToggleIdle(toggleKey)); taps != 0 {
		t.Fatalf("toggle-idle confirmation emitted %d target toggles", taps)
	}
	if taps := processRecoveryApply(t, core, oldRuntime, bridge.RouteAvailable(true)); taps != 0 {
		t.Fatalf("new route readiness emitted %d target toggles", taps)
	}
	newRuntime := processRecoveryRuntime(t, newConnection)
	newPress, err := bridge.NewPressToken(device, "voice", 2)
	if err != nil {
		t.Fatal(err)
	}
	toggleTaps += processRecoveryApply(t, core, newRuntime, bridge.VoiceBegan(newPress))
	prepared = processRecoveryCompletion(t, newRuntime)
	if prepared.Err != nil {
		t.Fatalf("replacement route preparation failed: %v", prepared.Err)
	}
	toggleTaps += processRecoveryApply(t, core, newRuntime, prepared.Event)
	waitProcessRecoverySignal(t, newSource.readStarted, "replacement source capture")
	if snapshot := core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseCapturing || snapshot.Session.Press.PressID() != 2 {
		t.Fatalf("replacement PID session was not admitted: %+v", snapshot)
	}
	if toggleTaps != 2 {
		t.Fatalf("target toggle count after replacement capture = %d, want one user toggle per admitted session", toggleTaps)
	}

	// Stop the replacement route through the ordinary host shutdown path and
	// verify every transferred resource is released before restoring selection.
	if taps := processRecoveryApply(t, core, newRuntime, bridge.RouteAvailable(false)); taps != 0 {
		t.Fatalf("replacement route shutdown emitted %d target toggles", taps)
	}
	if err := newRuntime.Close(context.Background()); err != nil {
		t.Fatalf("close replacement runtime: %v", err)
	}
	if newSource.closeCount() != 1 || newSink.closes() != 1 {
		t.Fatalf("replacement Runtime cleanup counts source=%d sink=%d, want one each", newSource.closeCount(), newSink.closes())
	}
	newInspector.setProcessInput(audioroute.ProcessInput{Found: true, PID: 43})
	closeProcessRecoveryConnection(t, newConnection)
	if _, closeCalls := newOwner.counts(); closeCalls != 1 {
		t.Fatalf("replacement route owner close calls = %d, want 1", closeCalls)
	}
}

type processRecoverySource struct {
	readStarted chan struct{}
	closed      chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
	closeCalls  atomic.Int32
}

func (s *processRecoverySource) Read(ctx context.Context, _ []int16) (int, error) {
	s.readOnce.Do(func() { close(s.readStarted) })
	<-ctx.Done()
	return 0, ctx.Err()
}

func (*processRecoverySource) Stop(context.Context) error { return nil }

func (s *processRecoverySource) Close() error {
	s.closeCalls.Add(1)
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func (*processRecoverySource) Ready(context.Context) (int, error) { return compatAudioSampleRate, nil }
func (*processRecoverySource) Err() error                         { return nil }

func (s *processRecoverySource) closeCount() int { return int(s.closeCalls.Load()) }

func openProcessRecoveryRoute(t *testing.T, pid int32) (*compatLiveAudioRoute, *compatRouteTestInspector, *compatRouteTestIdentity, *compatRouteTestOwner, *compatRouteTestRequest, *processRecoverySource, *compatRouteTestSink) {
	t.Helper()
	inspector := newCompatRouteTestInspector()
	inspector.mu.Lock()
	inspector.processes = []audioroute.ProcessInput{{Found: true, PID: pid}}
	inspector.input = audioroute.ProcessInput{Found: true, PID: pid}
	inspector.mu.Unlock()
	identity := &compatRouteTestIdentity{}
	source := &processRecoverySource{readStarted: make(chan struct{}), closed: make(chan struct{})}
	owner := &compatRouteTestOwner{source: source}
	requests := &compatRouteTestRequest{}
	sink := &compatRouteTestSink{}

	root := t.TempDir()
	inputMethods := filepath.Join(root, "Library", "Input Methods")
	executable := filepath.Join(inputMethods, "DoubaoIme.app", "Contents", "MacOS", compatDoubaoExecutableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	locator := voicetools.Locator{Home: root, InputMethodDirs: []string{inputMethods}}
	deps := compatAudioRouteDependencies{
		newInspector: func() (compatAudioInspector, error) { return inspector, nil },
		readSelection: func(context.Context, voicetools.Locator) (compatAudioSelection, error) {
			return compatAudioSelection{}, nil
		},
		requestSelection: requests.request,
		captureIdentity: func(gotPID int32, gotExecutable string) (compatProcessIdentity, error) {
			if gotPID != pid || filepath.Clean(gotExecutable) != filepath.Clean(executable) {
				return nil, errors.New("route captured a different target identity")
			}
			return identity, nil
		},
		openDeviceOwner: func(generation uint64, path string) (compatATVVOwner, error) {
			if generation != 7 || path != "/tmp/libMiMacATVV.dylib" {
				return nil, errors.New("unexpected device owner identity")
			}
			return owner, nil
		},
		openSink: func(config audiooutput.Config) (xiaomiPreparedSink, error) {
			sink.config = config
			return sink, nil
		},
		idleTimeout: 50 * time.Millisecond,
		idlePoll:    time.Millisecond,
	}
	opened, err := openLiveXiaomiRouteWithDependencies(context.Background(), 7, "/tmp/libMiMacATVV.dylib", compatDoubaoBundleID, executable, locator, deps)
	if err != nil {
		t.Fatalf("open process-bound route for PID %d: %v", pid, err)
	}
	route, ok := opened.(*compatLiveAudioRoute)
	if !ok {
		t.Fatalf("route type %T, want *compatLiveAudioRoute", opened)
	}
	if route.pid != pid {
		t.Fatalf("captured route PID = %d, want %d", route.pid, pid)
	}
	return route, inspector, identity, owner, requests, source, sink
}

func processRecoveryConnection(t *testing.T, route *compatLiveAudioRoute) *liveAudioConnection {
	t.Helper()
	connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) { return route, nil })
	waitLiveAudioDone(t, connection)
	if err := connection.Err(); err != nil {
		t.Fatalf("start process-bound connection: %v", err)
	}
	waitXiaomiRoute(t, connection.Ready)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = connection.Close(ctx)
	})
	return connection
}

func processRecoveryCore(t *testing.T) (*bridge.Core, bridge.Device, bridge.Key) {
	t.Helper()
	binding, err := bridge.NewBinding(bridge.PolicyToggle, "space")
	if err != nil {
		t.Fatal(err)
	}
	core, err := bridge.NewCore(binding)
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("process-recovery-test", 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.RouteAvailable(true), bridge.ScopeAvailable(true)} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	key, err := bridge.NewKey("space")
	if err != nil {
		t.Fatal(err)
	}
	return core, device, key
}

func processRecoveryRuntime(t *testing.T, connection *liveAudioConnection) *audiosession.Runtime {
	t.Helper()
	runtime, err := audiosession.New(connection.Prepare)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = runtime.Close(ctx)
	})
	return runtime
}

func processRecoveryApply(t *testing.T, core *bridge.Core, runtime *audiosession.Runtime, event bridge.Event) int {
	t.Helper()
	effects, err := core.Step(event)
	if err != nil {
		t.Fatalf("Core.Step(%T): %v", event, err)
	}
	taps := 0
	for _, effect := range effects {
		if _, ok := effect.(bridge.TargetTapEffect); ok {
			taps++
			continue
		}
		if err := runtime.Submit(effect); err != nil {
			t.Fatalf("Runtime.Submit(%T): %v", effect, err)
		}
	}
	return taps
}

func processRecoveryCompletion(t *testing.T, runtime *audiosession.Runtime) audiosession.Completion {
	t.Helper()
	select {
	case completion := <-runtime.Completions():
		return completion
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for audio runtime completion")
		return audiosession.Completion{}
	}
}

func waitProcessRecoverySignal(t *testing.T, signal <-chan struct{}, stage string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", stage)
	}
}

func closeProcessRecoveryConnection(t *testing.T, connection *liveAudioConnection) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := connection.Close(ctx); err != nil {
		t.Fatalf("close process-bound connection: %v", err)
	}
	waitLiveAudioClosed(t, connection)
}

func (i *compatRouteTestInspector) setProcessInput(input audioroute.ProcessInput) {
	i.mu.Lock()
	i.input = input
	i.mu.Unlock()
}
