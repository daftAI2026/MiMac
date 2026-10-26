package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/adapters/doubao"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/platform/audioroute"
	"mimac.local/mimac/internal/platform/processidentity"
	"mimac.local/mimac/internal/voicetools"
)

type compatRouteTestInspector struct {
	mu sync.Mutex

	device       audioroute.DeviceCapabilities
	processes    []audioroute.ProcessInput
	input        audioroute.ProcessInput
	deviceUID    string
	waitPID      int32
	waitUID      string
	waitCalls    int
	processCalls int
}

func newCompatRouteTestInspector() *compatRouteTestInspector {
	return &compatRouteTestInspector{
		device:    audioroute.DeviceCapabilities{Exists: true, InputChannels: 1, OutputChannels: 1},
		processes: []audioroute.ProcessInput{{Found: true, PID: 42}},
		input:     audioroute.ProcessInput{Found: true, PID: 42},
	}
}

func (i *compatRouteTestInspector) DeviceByUID(uid string) (audioroute.DeviceCapabilities, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.deviceUID = uid
	return i.device, nil
}

func (i *compatRouteTestInspector) InputsForBundle(string) ([]audioroute.ProcessInput, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]audioroute.ProcessInput(nil), i.processes...), nil
}

func (i *compatRouteTestInspector) ProcessInput(pid int32) (audioroute.ProcessInput, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.processCalls++
	if i.input.PID == 0 {
		i.input.PID = pid
	}
	return i.input, nil
}

func (i *compatRouteTestInspector) WaitForInput(_ context.Context, pid int32, uid string) error {
	i.mu.Lock()
	i.waitCalls++
	i.waitPID, i.waitUID = pid, uid
	i.mu.Unlock()
	return nil
}

func (i *compatRouteTestInspector) setRunning(running bool) {
	i.mu.Lock()
	i.input.Found, i.input.PID, i.input.RunningInput = true, 42, running
	i.mu.Unlock()
}

func (i *compatRouteTestInspector) snapshot() (deviceUID string, waitPID int32, waitUID string, waitCalls, processCalls int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.deviceUID, i.waitPID, i.waitUID, i.waitCalls, i.processCalls
}

type compatRouteTestIdentity struct {
	mu  sync.Mutex
	err error
}

func (i *compatRouteTestIdentity) Check() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.err
}

func (i *compatRouteTestIdentity) setError(err error) {
	i.mu.Lock()
	i.err = err
	i.mu.Unlock()
}

type compatRouteTestOwner struct {
	mu sync.Mutex

	source       audiosession.Source
	sourceErr    error
	closeErrors  []error
	sourceCalls  int
	closeCalls   int
	eventLog     *[]string
	eventLogLock *sync.Mutex
}

func (o *compatRouteTestOwner) newSource(context.Context, atvv.StreamKey) (audiosession.Source, error) {
	o.mu.Lock()
	o.sourceCalls++
	source, err := o.source, o.sourceErr
	o.mu.Unlock()
	return source, err
}

func (o *compatRouteTestOwner) close() error {
	o.mu.Lock()
	o.closeCalls++
	var err error
	if len(o.closeErrors) != 0 {
		err = o.closeErrors[0]
		o.closeErrors = o.closeErrors[1:]
	}
	eventLog, logLock := o.eventLog, o.eventLogLock
	o.mu.Unlock()
	if eventLog != nil {
		logLock.Lock()
		*eventLog = append(*eventLog, "device-close")
		logLock.Unlock()
	}
	return err
}

func (o *compatRouteTestOwner) counts() (newSource, closeCalls int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sourceCalls, o.closeCalls
}

type compatRouteTestSource struct {
	mu         sync.Mutex
	closeCalls int
	readyErr   error
	eventLog   *[]string
	logMu      *sync.Mutex
}

func (s *compatRouteTestSource) Read(_ context.Context, dst []int16) (int, error) {
	if len(dst) > 0 {
		dst[0] = 19
		return 1, nil
	}
	return 0, nil
}
func (*compatRouteTestSource) Stop(context.Context) error { return nil }
func (s *compatRouteTestSource) Close() error {
	s.mu.Lock()
	s.closeCalls++
	eventLog, logMu := s.eventLog, s.logMu
	s.mu.Unlock()
	if eventLog != nil {
		logMu.Lock()
		*eventLog = append(*eventLog, "source-close")
		logMu.Unlock()
	}
	return nil
}
func (s *compatRouteTestSource) Ready(context.Context) (int, error) {
	return compatAudioSampleRate, s.readyErr
}
func (*compatRouteTestSource) Err() error { return nil }
func (s *compatRouteTestSource) closes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

type compatRouteTestSink struct {
	mu         sync.Mutex
	config     audiooutput.Config
	prepareErr error
	closeCalls int
	eventLog   *[]string
	logMu      *sync.Mutex
}

func (s *compatRouteTestSink) Prepare(context.Context) error { return s.prepareErr }
func (*compatRouteTestSink) TryWrite([]int16) error          { return nil }
func (*compatRouteTestSink) Drain(context.Context) error     { return nil }
func (s *compatRouteTestSink) Close() error {
	s.mu.Lock()
	s.closeCalls++
	eventLog, logMu := s.eventLog, s.logMu
	s.mu.Unlock()
	if eventLog != nil {
		logMu.Lock()
		*eventLog = append(*eventLog, "sink-close")
		logMu.Unlock()
	}
	return nil
}
func (s *compatRouteTestSink) closes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

type compatRouteTestRequest struct {
	mu     sync.Mutex
	values []*string
	errors []error
	log    *[]string
	logMu  *sync.Mutex
}

func (r *compatRouteTestRequest) request(_ context.Context, _ voicetools.Locator, value *string) error {
	r.mu.Lock()
	r.values = append(r.values, cloneMicrophoneOptional(value))
	var err error
	if len(r.errors) != 0 {
		err = r.errors[0]
		r.errors = r.errors[1:]
	}
	log, logMu := r.log, r.logMu
	r.mu.Unlock()
	if log != nil {
		logMu.Lock()
		*log = append(*log, "selection-request")
		logMu.Unlock()
	}
	return err
}

func (r *compatRouteTestRequest) snapshot() []*string {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make([]*string, len(r.values))
	for index, value := range r.values {
		values[index] = cloneMicrophoneOptional(value)
	}
	return values
}

func compatRouteTestFixture(t *testing.T, saved *string) (*compatLiveAudioRoute, *compatRouteTestInspector, *compatRouteTestIdentity, *compatRouteTestOwner, *compatRouteTestRequest, *compatRouteTestSource, *compatRouteTestSink) {
	t.Helper()
	return compatRouteTestFixtureWith(t, saved, nil, nil, nil)
}

func compatRouteTestFixtureWith(t *testing.T, saved *string, requestErrors, ownerErrors []error, sourceErr error) (*compatLiveAudioRoute, *compatRouteTestInspector, *compatRouteTestIdentity, *compatRouteTestOwner, *compatRouteTestRequest, *compatRouteTestSource, *compatRouteTestSink) {
	t.Helper()
	inspector := newCompatRouteTestInspector()
	identity := &compatRouteTestIdentity{}
	source := &compatRouteTestSource{}
	sink := &compatRouteTestSink{}
	owner := &compatRouteTestOwner{source: source, sourceErr: sourceErr, closeErrors: append([]error(nil), ownerErrors...)}
	requests := &compatRouteTestRequest{errors: append([]error(nil), requestErrors...)}

	root := t.TempDir()
	inputMethods := filepath.Join(root, "Library", "Input Methods")
	bundle := filepath.Join(inputMethods, "DoubaoIme.app")
	executable := filepath.Join(bundle, "Contents", "MacOS", compatDoubaoExecutableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	locator := voicetools.Locator{Home: root, InputMethodDirs: []string{inputMethods}}
	deps := compatAudioRouteDependencies{
		newInspector: func() (compatAudioInspector, error) { return inspector, nil },
		readSelection: func(context.Context, voicetools.Locator) (doubao.MicrophoneSelection, error) {
			return doubao.MicrophoneSelection{SelectedMicrophoneID: cloneMicrophoneOptional(saved)}, nil
		},
		requestSelection: requests.request,
		captureIdentity: func(pid int32, gotExecutable string) (compatProcessIdentity, error) {
			if pid != 42 || filepath.Clean(gotExecutable) != filepath.Clean(executable) {
				t.Fatalf("identity capture=(%d,%q), want (42,%q)", pid, gotExecutable, executable)
			}
			return identity, nil
		},
		openDeviceOwner: func(generation uint64, path string) (compatATVVOwner, error) {
			if generation != 7 || path != "/tmp/libMiMacATVV.dylib" {
				t.Fatalf("openDeviceOwner=(%d,%q), want (7, test library path)", generation, path)
			}
			return owner, nil
		},
		openSink: func(config audiooutput.Config) (xiaomiPreparedSink, error) {
			sink.config = config
			return sink, nil
		},
		idleTimeout: 45 * time.Millisecond,
		idlePoll:    2 * time.Millisecond,
	}
	got, err := openLiveXiaomiRouteWithDependencies(context.Background(), 7, "/tmp/libMiMacATVV.dylib", compatDoubaoBundleID, executable, locator, deps)
	if err != nil {
		t.Fatalf("openLiveXiaomiRouteWithDependencies() error = %v", err)
	}
	route, ok := got.(*compatLiveAudioRoute)
	if !ok {
		t.Fatalf("route type = %T, want *compatLiveAudioRoute", got)
	}
	return route, inspector, identity, owner, requests, source, sink
}

func openCompatRouteForTest(t *testing.T, saved *string) (*compatLiveAudioRoute, *compatRouteTestInspector, *compatRouteTestIdentity, *compatRouteTestOwner, *compatRouteTestRequest, *compatRouteTestSource, *compatRouteTestSink) {
	t.Helper()
	return compatRouteTestFixture(t, saved)
}

func TestOpenLiveXiaomiRouteUsesOnlyMiMacUIDAndCapturedDoubaoIdentity(t *testing.T) {
	saved := "com.example.previous-input"
	route, inspector, _, owner, requests, _, sink := openCompatRouteForTest(t, &saved)
	defer closeCompatRouteForTest(t, route)

	if route.pid != 42 || route.bundleID != compatDoubaoBundleID || route.generation != 7 {
		t.Fatalf("captured route identity = pid %d bundle %q generation %d", route.pid, route.bundleID, route.generation)
	}
	if got := requests.snapshot(); len(got) != 1 || got[0] == nil || *got[0] != compatAudioDeviceUID {
		t.Fatalf("startup selection requests = %v, want only %q", got, compatAudioDeviceUID)
	}
	if got, _, _, _, _ := inspector.snapshot(); got != compatAudioDeviceUID {
		t.Fatalf("inspected UID = %q, want %q", got, compatAudioDeviceUID)
	}
	if newSourceCalls, closeCalls := owner.counts(); newSourceCalls != 0 || closeCalls != 0 {
		t.Fatalf("owner counts before Start = (%d,%d), want (0,0)", newSourceCalls, closeCalls)
	}

	if err := route.Start(); err != nil {
		t.Fatal(err)
	}
	waitXiaomiRoute(t, route.Ready)
	if sink.config != (audiooutput.Config{DeviceUID: compatAudioDeviceUID, SampleRate: compatAudioSampleRate}) {
		t.Fatalf("sink config = %+v, want exact MiMac UID and 16 kHz", sink.config)
	}
	resources, err := route.Prepare(context.Background(), xiaomiRouteSessionToken(t, 7))
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if resources.WaitReceiver == nil {
		t.Fatal("WaitReceiver was not transferred to audiosession resources")
	}
	if err := resources.WaitReceiver(ctx); err != nil {
		t.Fatalf("WaitReceiver() error = %v", err)
	}
	_, waitPID, waitUID, waitCalls, _ := inspector.snapshot()
	if waitPID != 42 || waitUID != compatAudioDeviceUID || waitCalls != 1 {
		t.Fatalf("receiver wait=(pid %d uid %q calls %d), want captured PID and exact UID", waitPID, waitUID, waitCalls)
	}
	closeXiaomiResources(t, resources)

	if err := route.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	got := requests.snapshot()
	if len(got) != 2 || got[1] == nil || *got[1] != saved {
		t.Fatalf("selection requests after close = %v, want original optional %q", got, saved)
	}
}

func TestCompatRoutePartialDeviceOwnerOnOpenErrorRemainsClosable(t *testing.T) {
	setupErr := errors.New("open owner returned a partial handle")
	inspector := newCompatRouteTestInspector()
	identity := &compatRouteTestIdentity{}
	owner := &compatRouteTestOwner{}
	requests := &compatRouteTestRequest{}
	root := t.TempDir()
	inputMethods := filepath.Join(root, "Input Methods")
	bundle := filepath.Join(inputMethods, "DoubaoIme.app")
	executable := filepath.Join(bundle, "Contents", "MacOS", compatDoubaoExecutableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	saved := "original-device"
	locator := voicetools.Locator{Home: root, InputMethodDirs: []string{inputMethods}}
	deps := compatAudioRouteDependencies{
		newInspector: func() (compatAudioInspector, error) { return inspector, nil },
		readSelection: func(context.Context, voicetools.Locator) (doubao.MicrophoneSelection, error) {
			return doubao.MicrophoneSelection{SelectedMicrophoneID: &saved}, nil
		},
		requestSelection: requests.request,
		captureIdentity:  func(int32, string) (compatProcessIdentity, error) { return identity, nil },
		openDeviceOwner:  func(uint64, string) (compatATVVOwner, error) { return owner, setupErr },
		openSink:         func(audiooutput.Config) (xiaomiPreparedSink, error) { return &compatRouteTestSink{}, nil },
		idleTimeout:      50 * time.Millisecond,
		idlePoll:         time.Millisecond,
	}
	got, err := openLiveXiaomiRouteWithDependencies(context.Background(), 7, "/tmp/libMiMacATVV.dylib", compatDoubaoBundleID, executable, locator, deps)
	if got == nil || !errors.Is(err, setupErr) {
		t.Fatalf("open result=(%T,%v), want nonnil closable owner and setup error", got, err)
	}
	if err := got.Close(context.Background()); err != nil {
		t.Fatalf("Close() partial owner: %v", err)
	}
	if _, closeCalls := owner.counts(); closeCalls != 1 {
		t.Fatalf("partial device owner Close calls = %d, want 1", closeCalls)
	}
	values := requests.snapshot()
	if len(values) != 2 || values[0] == nil || *values[0] != compatAudioDeviceUID || values[1] == nil || *values[1] != saved {
		t.Fatalf("requests = %v, want MiMac request followed by saved selection", values)
	}
}

func TestCompatRouteBusyTargetCloseDeadlineRetainsSelectionLease(t *testing.T) {
	saved := "old-input"
	route, inspector, _, owner, requests, _, _ := openCompatRouteForTest(t, &saved)
	inspector.setRunning(true)

	err := route.Close(context.Background())
	if !errors.Is(err, audiosession.ErrCleanupPending) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() while target busy = %v, want bounded cleanup pending", err)
	}
	if _, closeCalls := owner.counts(); closeCalls != 1 {
		t.Fatalf("device owner Close calls = %d, want route closed before waiting", closeCalls)
	}
	if got := requests.snapshot(); len(got) != 1 {
		t.Fatalf("selection requests while target busy = %v, want no restore request", got)
	}

	inspector.setRunning(false)
	if err := route.Close(context.Background()); err != nil {
		t.Fatalf("Close() retry after target idle = %v", err)
	}
	if _, closeCalls := owner.counts(); closeCalls != 1 {
		t.Fatalf("device owner Close retried after confirmed close: %d calls", closeCalls)
	}
	if got := requests.snapshot(); len(got) != 2 || got[1] == nil || *got[1] != saved {
		t.Fatalf("selection requests after retry = %v, want saved value", got)
	}
}

func TestCompatRouteRestoreFailureRetainsLeaseForRetry(t *testing.T) {
	saved := "saved-input"
	restoreErr := errors.New("notification center post failed")
	route, _, _, owner, requests, _, _ := compatRouteTestFixtureWith(t, &saved, []error{nil, restoreErr, nil}, nil, nil)

	if err := route.Close(context.Background()); !errors.Is(err, restoreErr) {
		t.Fatalf("first Close() = %v, want restoration error", err)
	}
	if _, closeCalls := owner.counts(); closeCalls != 1 {
		t.Fatalf("device owner Close calls after restore error = %d, want 1", closeCalls)
	}
	if err := route.Close(context.Background()); err != nil {
		t.Fatalf("retry Close() = %v", err)
	}
	got := requests.snapshot()
	if len(got) != 3 || got[0] == nil || *got[0] != compatAudioDeviceUID || got[1] == nil || *got[1] != saved || got[2] == nil || *got[2] != saved {
		t.Fatalf("selection requests = %v, want startup plus two saved-value posts", got)
	}
}

func TestCompatRouteRestoresNilAndExplicitEmptyOptionalValues(t *testing.T) {
	empty := ""
	for _, test := range []struct {
		name  string
		saved *string
	}{
		{name: "nil optional"},
		{name: "explicit empty string", saved: &empty},
	} {
		t.Run(test.name, func(t *testing.T) {
			route, _, _, _, requests, _, _ := openCompatRouteForTest(t, test.saved)
			if err := route.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := requests.snapshot()
			if len(got) != 2 || got[0] == nil || *got[0] != compatAudioDeviceUID {
				t.Fatalf("selection requests = %v, want startup MiMac request and saved Optional", got)
			}
			if test.saved == nil {
				if got[1] != nil {
					t.Fatalf("restored Optional = %q, want nil", *got[1])
				}
			} else if got[1] == nil || *got[1] != *test.saved {
				t.Fatalf("restored Optional = %v, want present %q", got[1], *test.saved)
			}
		})
	}
}

func TestCompatRouteIdentityChangeRetiresLeaseWithoutRestorePost(t *testing.T) {
	saved := "saved-input"
	route, _, identity, owner, requests, _, _ := openCompatRouteForTest(t, &saved)
	identity.setError(processidentity.ErrIdentityChanged)

	if err := route.Close(context.Background()); err != nil {
		t.Fatalf("Close() for replaced target = %v", err)
	}
	if _, closeCalls := owner.counts(); closeCalls != 1 {
		t.Fatalf("device owner Close calls = %d, want 1", closeCalls)
	}
	got := requests.snapshot()
	if len(got) != 1 || got[0] == nil || *got[0] != compatAudioDeviceUID {
		t.Fatalf("selection requests = %v, want no post to the new process generation", got)
	}
}

func TestCompatRoutePrepareFailureStillClosesPartialSourceAndRestores(t *testing.T) {
	saved := "saved-input"
	prepareErr := errors.New("ATVV source initialization failed after partial allocation")
	route, _, _, owner, requests, source, sink := compatRouteTestFixtureWith(t, &saved, nil, nil, prepareErr)
	if err := route.Start(); err != nil {
		t.Fatalf("Start() returned synchronous error = %v", err)
	}
	waitXiaomiRoute(t, func() bool { return route.Error() != nil })
	if route.Ready() {
		t.Fatal("route became ready after partial source initialization failed")
	}
	if err := route.Close(context.Background()); err != nil {
		t.Fatalf("Close() after partial source initialization = %v", err)
	}
	if source.closes() != 1 {
		t.Fatalf("partial source Close calls = %d, want 1", source.closes())
	}
	if sink.closes() != 0 {
		t.Fatalf("unused sink Close calls = %d, want 0", sink.closes())
	}
	if _, closeCalls := owner.counts(); closeCalls != 1 {
		t.Fatalf("device owner Close calls = %d, want 1", closeCalls)
	}
	values := requests.snapshot()
	if len(values) != 2 || values[1] == nil || *values[1] != saved {
		t.Fatalf("selection requests = %v, want saved value restored after partial init", values)
	}
}

func TestCompatRouteRequestFailureStillReturnsRestoreOwner(t *testing.T) {
	saved := "saved-input"
	postErr := errors.New("notification post returned error after uncertain delivery")
	inspector := newCompatRouteTestInspector()
	identity := &compatRouteTestIdentity{}
	owner := &compatRouteTestOwner{}
	requests := &compatRouteTestRequest{errors: []error{postErr, nil}}
	root := t.TempDir()
	inputMethods := filepath.Join(root, "Input Methods")
	bundle := filepath.Join(inputMethods, "DoubaoIme.app")
	executable := filepath.Join(bundle, "Contents", "MacOS", compatDoubaoExecutableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	locator := voicetools.Locator{Home: root, InputMethodDirs: []string{inputMethods}}
	deps := compatAudioRouteDependencies{
		newInspector: func() (compatAudioInspector, error) { return inspector, nil },
		readSelection: func(context.Context, voicetools.Locator) (doubao.MicrophoneSelection, error) {
			return doubao.MicrophoneSelection{SelectedMicrophoneID: &saved}, nil
		},
		requestSelection: requests.request,
		captureIdentity:  func(int32, string) (compatProcessIdentity, error) { return identity, nil },
		openDeviceOwner:  func(uint64, string) (compatATVVOwner, error) { return owner, nil },
		openSink:         func(audiooutput.Config) (xiaomiPreparedSink, error) { return &compatRouteTestSink{}, nil },
		idleTimeout:      50 * time.Millisecond,
		idlePoll:         time.Millisecond,
	}
	got, err := openLiveXiaomiRouteWithDependencies(context.Background(), 7, "/tmp/libMiMacATVV.dylib", compatDoubaoBundleID, executable, locator, deps)
	if got == nil || !errors.Is(err, postErr) {
		t.Fatalf("open result=(%T,%v), want owner retained after uncertain post", got, err)
	}
	if closeErr := got.Close(context.Background()); closeErr != nil {
		t.Fatalf("Close() after request error = %v", closeErr)
	}
	values := requests.snapshot()
	if len(values) != 2 || values[0] == nil || *values[0] != compatAudioDeviceUID || values[1] == nil || *values[1] != saved {
		t.Fatalf("requests = %v, want uncertain MiMac post and saved-value restoration", values)
	}
}

func TestCompatRouteRejectsBusyOrAmbiguousTargetBeforeRequest(t *testing.T) {
	for _, test := range []struct {
		name      string
		processes []audioroute.ProcessInput
		input     audioroute.ProcessInput
	}{
		{name: "busy", processes: []audioroute.ProcessInput{{Found: true, PID: 42}}, input: audioroute.ProcessInput{Found: true, PID: 42, RunningInput: true}},
		{name: "ambiguous", processes: []audioroute.ProcessInput{{Found: true, PID: 42}, {Found: true, PID: 43}}, input: audioroute.ProcessInput{Found: true, PID: 42}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspector := newCompatRouteTestInspector()
			inspector.processes, inspector.input = test.processes, test.input
			requests := &compatRouteTestRequest{}
			identity := &compatRouteTestIdentity{}
			root := t.TempDir()
			inputMethods := filepath.Join(root, "Input Methods")
			bundle := filepath.Join(inputMethods, "DoubaoIme.app")
			executable := filepath.Join(bundle, "Contents", "MacOS", compatDoubaoExecutableName)
			if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
				t.Fatal(err)
			}
			locator := voicetools.Locator{Home: root, InputMethodDirs: []string{inputMethods}}
			deps := compatAudioRouteDependencies{
				newInspector: func() (compatAudioInspector, error) { return inspector, nil },
				readSelection: func(context.Context, voicetools.Locator) (doubao.MicrophoneSelection, error) {
					return doubao.MicrophoneSelection{}, nil
				},
				requestSelection: requests.request,
				captureIdentity:  func(int32, string) (compatProcessIdentity, error) { return identity, nil },
				openDeviceOwner:  func(uint64, string) (compatATVVOwner, error) { return &compatRouteTestOwner{}, nil },
				openSink:         func(audiooutput.Config) (xiaomiPreparedSink, error) { return &compatRouteTestSink{}, nil },
			}
			got, err := openLiveXiaomiRouteWithDependencies(context.Background(), 7, "/tmp/libMiMacATVV.dylib", compatDoubaoBundleID, executable, locator, deps)
			if got != nil || err == nil {
				t.Fatalf("open result=(%T,%v), want pre-request rejection", got, err)
			}
			if values := requests.snapshot(); len(values) != 0 {
				t.Fatalf("microphone request issued before target eligibility: %v", values)
			}
		})
	}
}

func TestCompatRouteCloseOrdersDeviceBeforeSavedSelectionRequest(t *testing.T) {
	saved := "saved-device"
	route, _, _, owner, requests, source, sink := openCompatRouteForTest(t, &saved)
	var events []string
	var eventsMu sync.Mutex
	owner.eventLog, owner.eventLogLock = &events, &eventsMu
	requests.log, requests.logMu = &events, &eventsMu
	source.eventLog, source.logMu = &events, &eventsMu
	sink.eventLog, sink.logMu = &events, &eventsMu
	if err := route.Start(); err != nil {
		t.Fatal(err)
	}
	waitXiaomiRoute(t, route.Ready)
	if err := route.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := eventSnapshot(&events, &eventsMu), []string{"source-close", "sink-close", "device-close", "selection-request"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("close event order = %v, want %v", got, want)
	}
}

func eventSnapshot(events *[]string, mu *sync.Mutex) []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), (*events)...)
}

func closeCompatRouteForTest(t *testing.T, route *compatLiveAudioRoute) {
	t.Helper()
	if route == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := route.Close(ctx); err != nil {
		t.Errorf("route cleanup: %v", err)
	}
}
