package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

type xiaomiRouteTestSource struct {
	mu          sync.Mutex
	rate        int
	readyErr    error
	err         error
	readyCalls  int
	readCalls   int
	closeCalls  int
	closeErrors []error
}

func (s *xiaomiRouteTestSource) Read(_ context.Context, dst []int16) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readCalls++
	if len(dst) == 0 {
		return 0, nil
	}
	dst[0] = 27
	return 1, nil
}
func (*xiaomiRouteTestSource) Stop(context.Context) error { return nil }
func (s *xiaomiRouteTestSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	if len(s.closeErrors) == 0 {
		return nil
	}
	err := s.closeErrors[0]
	s.closeErrors = s.closeErrors[1:]
	return err
}
func (s *xiaomiRouteTestSource) Ready(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyCalls++
	return s.rate, s.readyErr
}
func (s *xiaomiRouteTestSource) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *xiaomiRouteTestSource) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}
func (s *xiaomiRouteTestSource) counts() (ready, read, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readyCalls, s.readCalls, s.closeCalls
}

type xiaomiRouteTestSink struct {
	mu          sync.Mutex
	prepareErr  error
	closeErrors []error
	prepareCall int
	writeCalls  int
	closeCalls  int
}

func (s *xiaomiRouteTestSink) Prepare(context.Context) error {
	s.mu.Lock()
	s.prepareCall++
	err := s.prepareErr
	s.mu.Unlock()
	return err
}
func (s *xiaomiRouteTestSink) TryWrite([]int16) error {
	s.mu.Lock()
	s.writeCalls++
	s.mu.Unlock()
	return nil
}
func (*xiaomiRouteTestSink) Drain(context.Context) error { return nil }
func (s *xiaomiRouteTestSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	if len(s.closeErrors) == 0 {
		return nil
	}
	err := s.closeErrors[0]
	s.closeErrors = s.closeErrors[1:]
	return err
}
func (s *xiaomiRouteTestSink) counts() (prepared, writes, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepareCall, s.writeCalls, s.closeCalls
}

func newXiaomiRouteTest(t *testing.T, generation uint64, config audiooutput.Config,
	sourceFactory xiaomiAudioSourceFactory,
	closeDevice func() error,
	sinkFactory xiaomiAudioSinkFactory,
) *xiaomiAudioRoute {
	t.Helper()
	route, err := newXiaomiAudioRouteWithFactories(generation, sourceFactory, closeDevice, config, sinkFactory)
	if err != nil {
		t.Fatal(err)
	}
	if err := route.Start(); err != nil {
		t.Fatal(err)
	}
	return route
}

func waitXiaomiRoute(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for Xiaomi audio route")
}

func closeXiaomiResources(t *testing.T, resources audiosession.Resources) {
	t.Helper()
	if err := resources.Source.Close(); err != nil {
		t.Fatalf("source close: %v", err)
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatalf("sink close: %v", err)
	}
}

func xiaomiRouteSessionToken(t *testing.T, generation uint64) bridge.SessionToken {
	t.Helper()
	key, err := bridge.NewKey("Space")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bridge.NewBinding(bridge.PolicyExactEdge, key)
	if err != nil {
		t.Fatal(err)
	}
	core, err := bridge.NewCore(binding)
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("xiaomi-audio-route-test", generation)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{
		bridge.RouteAvailable(true), bridge.ScopeAvailable(true),
		bridge.DeviceConnected(device), bridge.VoiceBegan(press),
	} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := core.Snapshot()
	if !snapshot.HasSession {
		t.Fatal("test core did not create a session")
	}
	return snapshot.Session.Token
}

func TestXiaomiAudioRoutePreparesOpaqueSourceAndReusesExplicitFormat(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "selected-test-route", SampleRate: 16000}
	var keys []atvv.StreamKey
	var sources []*xiaomiRouteTestSource
	var sinks []*xiaomiRouteTestSink
	route := newXiaomiRouteForTest(t, 41, config, &keys, &sources, &sinks, nil)
	if route.Changes() != route.resources.Changes() {
		t.Fatal("route Changes did not forward the prepared owner channel")
	}
	waitXiaomiRoute(t, route.Ready)
	if len(keys) != 1 || keys[0] != (atvv.StreamKey{Generation: 41, Session: 1}) {
		t.Fatalf("first opaque source key = %+v", keys)
	}
	if len(sinks) != 1 {
		t.Fatalf("sink opens = %d", len(sinks))
	}
	prepared, _, _ := sinks[0].counts()
	if prepared != 1 {
		t.Fatalf("sink Prepare calls = %d", prepared)
	}

	resources, err := route.Prepare(context.Background(), xiaomiRouteSessionToken(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	closeXiaomiResources(t, resources)
	waitXiaomiRoute(t, func() bool { return route.Ready() && len(keys) == 2 })
	if keys[1] != (atvv.StreamKey{Generation: 41, Session: 2}) {
		t.Fatalf("rearmed opaque source key = %+v", keys[1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := route.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestXiaomiAudioRouteRejectsWrongGenerationWithoutTakingResources(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "selected-test-route", SampleRate: 16000}
	var keys []atvv.StreamKey
	var sources []*xiaomiRouteTestSource
	var sinks []*xiaomiRouteTestSink
	route := newXiaomiRouteForTest(t, 61, config, &keys, &sources, &sinks, nil)
	waitXiaomiRoute(t, route.Ready)

	if _, err := route.Prepare(context.Background(), xiaomiRouteSessionToken(t, 60)); !errors.Is(err, errXiaomiAudioTokenGeneration) {
		t.Fatalf("Prepare with stale device generation = %v, want generation rejection", err)
	}
	if !route.Ready() {
		t.Fatal("generation rejection consumed the prepared resources")
	}
	resources, err := route.Prepare(context.Background(), xiaomiRouteSessionToken(t, 61))
	if err != nil {
		t.Fatalf("Prepare with matching generation: %v", err)
	}
	closeXiaomiResources(t, resources)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := route.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func newXiaomiRouteForTest(t *testing.T, generation uint64, config audiooutput.Config,
	keys *[]atvv.StreamKey,
	sources *[]*xiaomiRouteTestSource,
	sinks *[]*xiaomiRouteTestSink,
	closeDevice func() error,
) *xiaomiAudioRoute {
	t.Helper()
	if closeDevice == nil {
		closeDevice = func() error { return nil }
	}
	return newXiaomiRouteTest(t, generation, config,
		func(_ context.Context, key atvv.StreamKey) (audiosession.Source, error) {
			*keys = append(*keys, key)
			source := &xiaomiRouteTestSource{rate: config.SampleRate}
			*sources = append(*sources, source)
			return source, nil
		}, closeDevice,
		func(got audiooutput.Config) (xiaomiPreparedSink, error) {
			if got != config {
				t.Errorf("sink config = %+v, want explicit config %+v", got, config)
			}
			sink := &xiaomiRouteTestSink{}
			*sinks = append(*sinks, sink)
			return sink, nil
		})
}

func TestXiaomiAudioRouteRejectsMismatchBeforeSourceEmitsPCM(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "selected-test-route", SampleRate: 16000}
	source := &xiaomiRouteTestSource{rate: 8000}
	sink := &xiaomiRouteTestSink{}
	route := newXiaomiRouteTest(t, 42, config,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) { return source, nil },
		func() error { return nil },
		func(audiooutput.Config) (xiaomiPreparedSink, error) { return sink, nil })
	waitXiaomiRoute(t, route.Ready)
	resources, err := route.Prepare(context.Background(), xiaomiRouteSessionToken(t, 42))
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, 320)
	n, err := resources.Source.Read(context.Background(), pcm)
	if !errors.Is(err, errXiaomiAudioRateMismatch) || n != 0 {
		t.Fatalf("Read = (%d, %v), want zero samples and rate mismatch", n, err)
	}
	_, reads, _ := source.counts()
	_, writes, _ := sink.counts()
	if reads != 0 || writes != 0 {
		t.Fatalf("wrong-rate data escaped validation: source reads=%d sink writes=%d", reads, writes)
	}
	closeXiaomiResources(t, resources)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := route.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestXiaomiAudioRouteChecksFaultedPrewarmedSourceBeforeTake(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "selected-test-route", SampleRate: 16000}
	source := &xiaomiRouteTestSource{rate: 16000}
	sink := &xiaomiRouteTestSink{}
	route := newXiaomiRouteTest(t, 43, config,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) { return source, nil },
		func() error { return nil },
		func(audiooutput.Config) (xiaomiPreparedSink, error) { return sink, nil })
	waitXiaomiRoute(t, route.Ready)
	sourceFault := errors.New("ATVV source ring overflow")
	source.setErr(sourceFault)
	if route.Ready() {
		t.Fatal("faulted prewarmed source still reports ready")
	}
	if err := route.Error(); !errors.Is(err, sourceFault) {
		t.Fatalf("route Error() = %v, want source fault", err)
	}
	if _, err := route.Prepare(context.Background(), xiaomiRouteSessionToken(t, 43)); !errors.Is(err, sourceFault) {
		t.Fatalf("Prepare with source fault = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := route.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestXiaomiAudioRouteCloseWaitsForResourcesAndRetriesDeviceClose(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "selected-test-route", SampleRate: 16000}
	source := &xiaomiRouteTestSource{rate: 16000}
	sinkErr := errors.New("sink close failed")
	sink := &xiaomiRouteTestSink{closeErrors: []error{sinkErr}}
	deviceErr := errors.New("device close failed")
	deviceCloseCalls := 0
	route := newXiaomiRouteTest(t, 44, config,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) { return source, nil },
		func() error {
			deviceCloseCalls++
			if deviceCloseCalls == 1 {
				return deviceErr
			}
			return nil
		},
		func(audiooutput.Config) (xiaomiPreparedSink, error) { return sink, nil })
	waitXiaomiRoute(t, route.Ready)
	resources, err := route.Prepare(context.Background(), xiaomiRouteSessionToken(t, 44))
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = route.Close(short)
	cancel()
	if !errors.Is(err, audiosession.ErrCleanupPending) || deviceCloseCalls != 0 {
		t.Fatalf("Close with active session = %v, device close calls=%d", err, deviceCloseCalls)
	}
	if err := resources.Source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.Sink.Close(); !errors.Is(err, sinkErr) {
		t.Fatalf("first sink Close = %v", err)
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatalf("retry sink Close: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err = route.Close(ctx)
	cancel()
	if !errors.Is(err, deviceErr) {
		t.Fatalf("Close after resource cleanup = %v, want device close failure", err)
	}
	if deviceCloseCalls != 1 {
		t.Fatalf("device close calls after resource failure path = %d", deviceCloseCalls)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := route.Close(ctx); err != nil {
		t.Fatalf("retry route Close: %v", err)
	}
	if deviceCloseCalls != 2 {
		t.Fatalf("device close retry calls = %d", deviceCloseCalls)
	}
}

func TestXiaomiAudioRouteForceClosesDeviceAndRetriesUnleasedResourceCleanup(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "selected-test-route", SampleRate: 16000}
	sourceCloseErr := errors.New("source callback barrier failed")
	source := &xiaomiRouteTestSource{rate: 16000, closeErrors: []error{sourceCloseErr}}
	sink := &xiaomiRouteTestSink{}
	deviceCloseCalls := 0
	route := newXiaomiRouteTest(t, 46, config,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) { return source, nil },
		func() error { deviceCloseCalls++; return nil },
		func(audiooutput.Config) (xiaomiPreparedSink, error) { return sink, nil })
	waitXiaomiRoute(t, route.Ready)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := route.Close(ctx); err != nil {
		t.Fatalf("route Close after native teardown and resource retry: %v", err)
	}
	_, _, sourceCloses := source.counts()
	_, _, sinkCloses := sink.counts()
	if sourceCloses != 2 || sinkCloses != 1 || deviceCloseCalls != 1 {
		t.Fatalf("cleanup calls source=%d sink=%d device=%d, want 2/1/1", sourceCloses, sinkCloses, deviceCloseCalls)
	}
}

func TestXiaomiAudioRoutePrepareFailureRetainsPartialSourceForClose(t *testing.T) {
	config := audiooutput.Config{DeviceUID: "selected-test-route", SampleRate: 16000}
	source := &xiaomiRouteTestSource{rate: 16000}
	sinkPrepareErr := errors.New("native sink prepare failed")
	sink := &xiaomiRouteTestSink{prepareErr: sinkPrepareErr}
	deviceCloseCalls := 0
	route := newXiaomiRouteTest(t, 45, config,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) { return source, nil },
		func() error { deviceCloseCalls++; return nil },
		func(audiooutput.Config) (xiaomiPreparedSink, error) { return sink, nil })
	waitXiaomiRoute(t, func() bool { return errors.Is(route.Error(), sinkPrepareErr) })
	if route.Ready() {
		t.Fatal("route reported ready after sink preparation failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := route.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, sourceClosed := source.counts()
	_, _, sinkClosed := sink.counts()
	if sourceClosed != 1 || sinkClosed != 1 || deviceCloseCalls != 1 {
		t.Fatalf("partial resources not cleaned in order: source=%d sink=%d device=%d", sourceClosed, sinkClosed, deviceCloseCalls)
	}
}

func TestXiaomiRateCheckedSourceRequiresReadyCapability(t *testing.T) {
	underlying := &xiaomiRouteBareSource{}
	source := &xiaomiRateCheckedSource{inner: underlying, expectedRate: 16000}
	n, err := source.Read(context.Background(), make([]int16, 320))
	if n != 0 || !errors.Is(err, errXiaomiAudioNoReady) {
		t.Fatalf("Read = (%d, %v), want no samples and readiness error", n, err)
	}
	if underlying.readCalls != 0 {
		t.Fatalf("underlying Read calls = %d", underlying.readCalls)
	}
}

type xiaomiRouteBareSource struct{ readCalls int }

func (s *xiaomiRouteBareSource) Read(context.Context, []int16) (int, error) {
	s.readCalls++
	return 1, nil
}
func (*xiaomiRouteBareSource) Stop(context.Context) error { return nil }
func (*xiaomiRouteBareSource) Close() error               { return nil }

var _ audiosession.Source = (*xiaomiRouteBareSource)(nil)

// Health inspection must stay responsive while the remote has not sent START.
func TestXiaomiRateCheckedSourceHealthDoesNotWaitForStart(t *testing.T) {
	underlying := &xiaomiWaitingReadySource{entered: make(chan struct{})}
	source := &xiaomiRateCheckedSource{inner: underlying, expectedRate: 16000}
	ctx, cancel := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	go func() { defer close(readDone); _, _ = source.Read(ctx, make([]int16, 320)) }()
	defer func() { cancel(); <-readDone }()
	<-underlying.entered
	healthDone := make(chan error, 1)
	go func() { healthDone <- source.Err() }()
	select {
	case err := <-healthDone:
		if err != nil {
			t.Fatalf("health before START: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("health query blocked behind remote START")
	}
	transportErr := errors.New("transport disconnected while awaiting START")
	underlying.setErr(transportErr)
	go func() { healthDone <- source.Err() }()
	select {
	case err := <-healthDone:
		if !errors.Is(err, transportErr) {
			t.Fatalf("health lost transport failure: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("transport failure blocked behind remote START")
	}

}

type xiaomiWaitingReadySource struct {
	xiaomiRouteTestSource
	entered chan struct{}
}

func (s *xiaomiWaitingReadySource) Ready(ctx context.Context) (int, error) {
	close(s.entered)
	<-ctx.Done()
	return 0, ctx.Err()
}

type xiaomiReceiverTestSink struct {
	xiaomiRouteTestSink
	wait func(context.Context) error
}

func (s *xiaomiReceiverTestSink) WaitReceiver(ctx context.Context) error { return s.wait(ctx) }

func TestXiaomiAudioRoutePreservesReceiverGateWithoutCallingItDuringPrewarm(t *testing.T) {
	calls := make(chan context.Context, 2)
	expected := errors.New("receiver not ready")
	route := newXiaomiRouteTest(t, 81, audiooutput.Config{DeviceUID: "test-route", SampleRate: 16000},
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) {
			return &xiaomiRouteTestSource{rate: 16000}, nil
		},
		func() error { return nil },
		func(audiooutput.Config) (xiaomiPreparedSink, error) {
			return &xiaomiReceiverTestSink{wait: func(ctx context.Context) error { calls <- ctx; return expected }}, nil
		})
	defer func() {
		if err := route.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	waitXiaomiRoute(t, route.Ready)
	select {
	case <-calls:
		t.Fatal("prewarm invoked receiver")
	default:
	}
	resources, err := route.Prepare(context.Background(), xiaomiRouteSessionToken(t, 81))
	if err != nil {
		t.Fatal(err)
	}
	defer closeXiaomiResources(t, resources)
	if resources.WaitReceiver == nil {
		t.Fatal("receiver gate lost during prepare/take")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := resources.WaitReceiver(ctx); !errors.Is(err, expected) {
		t.Fatalf("receiver error=%v", err)
	}
	if got := <-calls; got != ctx {
		t.Fatal("receiver context changed")
	}
}
