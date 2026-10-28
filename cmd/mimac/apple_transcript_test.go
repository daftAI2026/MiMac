package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/textoutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

func TestAudioHostReportsFinalTextFailureWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		prompt     string
		permission bool
	}{
		{"access_revoked", textoutput.ErrPermissionDenied, "请允许辅助功能权限", true},
		{"event_access_revoked", textoutput.ErrPostEventPermissionDenied, "请允许辅助功能权限", true},
		{"control_text", textoutput.ErrUnsupportedText, "此输入框不支持多行或控制字符", false},
		{"target_moved", textoutput.ErrTargetChanged, "输入位置已变化，请重试", false},
		{"native_payload", textoutput.ErrEventPayload, "文字未写入，此输入方式暂不可用", false},
		{"unknown_delivery", errors.New("uncertain native output"), "请检查输入结果", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, tasks, source, sink, _ := newTranscriptAudioHost(t, nil)
			calls := 0
			host.audioSession.deliver = func(bridge.SessionToken, string) error { calls++; return tc.err }
			press, _ := beginTranscriptAudioSession(t, host)
			runHostTask(t, tasks)
			waitHostSignal(t, source.reading)
			if err := host.coordinator.step(bridge.VoiceEnded(press)); err != nil {
				t.Fatal(err)
			}
			waitHostSignal(t, sink.drainStarted)
			completion := runAndReturnHostTask(t, tasks)
			completion()
			waitHostSignal(t, sink.closed)
			if host.voiceStartBlocked != tc.prompt || host.outputNeedsPermission != tc.permission {
				t.Fatalf("prompt=%q permission=%t, want %q/%t", host.voiceStartBlocked, host.outputNeedsPermission, tc.prompt, tc.permission)
			}
			completion() // A duplicated queued completion must not deliver again.
			if calls != 1 || host.coordinator.core.Snapshot().HasSession {
				t.Fatal("failed text was retried or kept the audio session alive")
			}
		})
	}
}

func TestAudioHostDeliversFinalTranscriptOnceAfterTailDrain(t *testing.T) {
	host, tasks, source, sink, delivered := newTranscriptAudioHost(t, nil)
	press, token := beginTranscriptAudioSession(t, host)
	runHostTask(t, tasks) // SourcePrepared is applied on the Core owner.
	waitHostSignal(t, source.reading)

	if err := host.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	waitHostSignal(t, sink.drainStarted)
	completion := runAndReturnHostTask(t, tasks)
	completion()
	waitHostSignal(t, sink.closed)

	if got := <-sink.writes; !reflect.DeepEqual(got, []int16{7, -7, 9}) {
		t.Fatalf("final source tail = %v, want [7 -7 9] before drain", got)
	}
	if sink.drains.Load() != 1 || sink.transcriptCalls.Load() != 1 {
		t.Fatalf("sink completion calls: drains=%d transcript=%d", sink.drains.Load(), sink.transcriptCalls.Load())
	}
	if !sink.drainSawTail.Load() {
		t.Fatal("sink Drain began before the source's final PCM tail was written")
	}
	if got, want := delivered.entries(), []transcriptDelivery{{token: token, text: "最终识别文本"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered transcript = %#v, want %#v", got, want)
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("successful transcript delivery left Core state: %+v", snapshot)
	}
}

func TestAudioHostDropsQueuedTranscriptAfterCoreRetiresSession(t *testing.T) {
	host, tasks, source, sink, delivered := newTranscriptAudioHost(t, nil)
	press, token := beginTranscriptAudioSession(t, host)
	runHostTask(t, tasks) // SourcePrepared opens the read gate.
	waitHostSignal(t, source.reading)
	if err := host.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	waitHostSignal(t, sink.drainStarted)
	lateCompletion := runAndReturnHostTask(t, tasks) // Already queued, but not applied yet.
	if sink.transcriptCalls.Load() != 1 {
		t.Fatalf("runtime did not finish transcript before queueing: calls=%d", sink.transcriptCalls.Load())
	}

	if err := host.coordinator.step(bridge.RouteAvailable(false)); err != nil {
		t.Fatal(err)
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession {
		t.Fatalf("route cancellation retained session before queued callback: %+v", snapshot)
	}
	waitHostSignal(t, sink.closed)
	lateCompletion()

	if got := delivered.entries(); len(got) != 0 {
		t.Fatalf("late transcript for retired token %v was delivered: %#v", token, got)
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("late completion changed retired Core state: %+v", snapshot)
	}
}

func TestAudioHostDoesNotDeliverTranscriptWhenDrainFails(t *testing.T) {
	drainErr := errors.New("recognizer drain failed")
	host, tasks, source, sink, delivered := newTranscriptAudioHost(t, drainErr)
	press, _ := beginTranscriptAudioSession(t, host)
	runHostTask(t, tasks)
	waitHostSignal(t, source.reading)
	if err := host.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	waitHostSignal(t, sink.drainStarted)
	runHostTask(t, tasks) // SourceFailed is applied; no Transcript is attached.
	waitHostSignal(t, sink.closed)

	if got := delivered.entries(); len(got) != 0 {
		t.Fatalf("transcript delivered after failed drain: %#v", got)
	}
	if sink.drains.Load() != 1 || sink.transcriptCalls.Load() != 0 {
		t.Fatalf("failed drain called Transcript: drains=%d transcript=%d", sink.drains.Load(), sink.transcriptCalls.Load())
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession || host.report.AudioError == "" {
		t.Fatalf("failed drain cleanup/report: snapshot=%+v report=%+v", snapshot, host.report)
	}
}

func TestAppleRoutePrearmsSourceButOpensRecognizerAtSessionStart(t *testing.T) {
	const generation = 94
	var sourceCalls, openCalls atomic.Int32
	target := &transcriptTargetTestStub{}
	route, err := newAppleAudioRoute(generation,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) {
			sourceCalls.Add(1)
			return newHostPCMSource(), nil
		},
		func() error { return nil },
		func(context.Context) (localRecognizer, error) {
			openCalls.Add(1)
			return &appleRecognizerTestStub{hostPCMSink: newHostPCMSink()}, nil
		},
		func() (transcriptTarget, error) { return target, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	var resources audiosession.Resources
	t.Cleanup(func() {
		if resources.Source != nil {
			_ = resources.Source.Close()
		}
		if resources.Sink != nil {
			_ = resources.Sink.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := route.Close(ctx); err != nil {
			t.Errorf("close Apple route: %v", err)
		}
	})

	if err := route.Start(); err != nil {
		t.Fatal(err)
	}
	waitXiaomiRoute(t, route.Ready)
	if sourceCalls.Load() != 1 || openCalls.Load() != 0 {
		t.Fatalf("idle route preparation opened recognizer: sources=%d recognizers=%d", sourceCalls.Load(), openCalls.Load())
	}
	if err := route.BeforeStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	resources, err = route.Prepare(context.Background(), xiaomiRouteSessionToken(t, generation))
	if err != nil {
		t.Fatal(err)
	}
	if openCalls.Load() != 0 {
		t.Fatalf("resource transfer opened recognizer before session start: %d", openCalls.Load())
	}
	sessionSink, ok := resources.Sink.(audiosession.SessionSink)
	if !ok {
		t.Fatalf("prepared Apple sink %T does not defer consumer start", resources.Sink)
	}
	if err := sessionSink.StartSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if openCalls.Load() != 1 {
		t.Fatalf("StartSession opened recognizer %d times, want once", openCalls.Load())
	}
	if err := resources.Source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatal(err)
	}
	resources = audiosession.Resources{}
}

func TestAppleRoutePartialRecognizerFailureReopensWithFreshTarget(t *testing.T) {
	const generation = 95
	openErr := errors.New("recognizer open returned partial state")
	firstOpenStarted := make(chan struct{})
	releaseFirstOpen := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(releaseFirstOpen) }) }
	firstRecognizer := &appleRecognizerTestStub{hostPCMSink: newHostPCMSink(), text: "discarded first transcript"}
	secondRecognizer := &appleRecognizerTestStub{hostPCMSink: newHostPCMSink(), text: "second session transcript"}
	firstTarget, secondTarget := &transcriptTargetTestStub{}, &transcriptTargetTestStub{}
	targets := []*transcriptTargetTestStub{firstTarget, secondTarget}
	var captureCalls atomic.Int32
	type leasedSource struct {
		source *hostPCMSource
		err    error
	}
	leasedSources := make(chan leasedSource, 4)
	var openCalls atomic.Int32
	secondOpenStarted := make(chan struct{})
	leasedSourceFor := func(session string) *hostPCMSource {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case lease := <-leasedSources:
			if lease.err != nil {
				t.Fatalf("%s source lease: %v", session, lease.err)
			}
			return lease.source
		case <-timer.C:
			t.Fatalf("timed out waiting for %s source lease", session)
			return nil
		}
	}
	route, err := newAppleAudioRoute(generation,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) {
			return &appleRouteTestPCMSource{hostPCMSource: newHostPCMSource()}, nil
		},
		func() error { return nil },
		func(ctx context.Context) (localRecognizer, error) {
			switch openCalls.Add(1) {
			case 1:
				close(firstOpenStarted)
				select {
				case <-releaseFirstOpen:
					return firstRecognizer, openErr
				case <-ctx.Done():
					return firstRecognizer, ctx.Err()
				}
			case 2:
				close(secondOpenStarted)
				return secondRecognizer, nil
			default:
				return nil, errors.New("unexpected extra recognizer open")
			}
		},
		func() (transcriptTarget, error) {
			i := int(captureCalls.Add(1)) - 1
			if i >= len(targets) {
				return nil, errors.New("unexpected extra target capture")
			}
			return targets[i], nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := route.Start(); err != nil {
		t.Fatal(err)
	}

	host := &shell{}
	if err := host.setupPluginsWith(func(func(xiaomi.MonitorEvent)) (hidMonitor, error) {
		t.Fatal("hardware opened")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	tasks := make(chan func(), 8)
	host.audioPost = func(fn func()) { tasks <- fn }
	host.audioResources = func(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
		resources, prepareErr := route.Prepare(ctx, token)
		lease, ok := resources.Source.(preparedAudioSource)
		if !ok {
			leasedSources <- leasedSource{err: errors.New("Apple route did not return a prepared source lease")}
			return resources, prepareErr
		}
		checked, ok := lease.inner.(*xiaomiRateCheckedSource)
		if !ok {
			leasedSources <- leasedSource{err: errors.New("Apple route lease did not contain a rate-checked source")}
			return resources, prepareErr
		}
		source, ok := checked.inner.(*appleRouteTestPCMSource)
		if !ok {
			leasedSources <- leasedSource{err: errors.New("Apple route lease did not contain the test PCM source")}
			return resources, prepareErr
		}
		leasedSources <- leasedSource{source: source.hostPCMSource}
		return resources, prepareErr
	}
	host.audioResourcesReady = route.Ready
	host.audioResourcesBeforeStart = route.BeforeStart
	host.audioResourcesClose = route.Close
	host.audioResourcesDeliver = route.DeliverTranscript
	t.Cleanup(func() {
		releaseFirst()
		if host.audioSession != nil {
			host.audioSession.cancel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := host.audioSession.close(ctx); err != nil {
				t.Errorf("close Apple transcript host: %v", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := route.Close(ctx); err != nil {
			t.Errorf("close Apple route: %v", err)
		}
		if host.plugins != nil {
			if err := host.plugins.Close(); err != nil {
				t.Errorf("close Apple transcript plugins: %v", err)
			}
		}
	})
	waitAppleRouteReady(t, route)
	if err := host.plugins.Enable(audioSessionPlugin); err != nil {
		t.Fatal(err)
	}

	device, err := bridge.NewDevice("apple-route-reopen", generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.coordinator.step(bridge.DeviceConnected(device)); err != nil {
		t.Fatal(err)
	}
	firstPress, firstToken := beginAppleRouteTranscriptSession(t, host, device, 1)
	select {
	case <-firstOpenStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first recognizer Open did not start")
	}
	firstSource := leasedSourceFor("first")
	releaseFirst()
	waitAppleRouteReady(t, route)

	failedCompletion := runAndReturnHostTask(t, tasks)
	if snapshot := host.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Token != firstToken || snapshot.Session.Phase != bridge.PhasePreparing {
		t.Fatalf("failed recognizer Open unexpectedly prepared source: %+v", snapshot)
	}
	failedCompletion()
	if host.report.AudioError != openErr.Error() {
		t.Fatalf("first recognizer failure report = %q, want %q", host.report.AudioError, openErr)
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession {
		t.Fatalf("failed first session remained active: %+v", snapshot)
	}
	if got := firstSource.reading; len(got) != 0 {
		t.Fatal("source Read began after recognizer Open failed")
	}
	if got := firstTarget.inserted(); len(got) != 0 {
		t.Fatalf("failed session inserted text into old target: %v", got)
	}
	if firstSource.closed.Load() != 1 || firstTarget.closeCalls.Load() != 1 || firstRecognizer.closeCalls.Load() != 1 {
		t.Fatalf("partial failure cleanup: source=%d target=%d recognizer=%d", firstSource.closed.Load(), firstTarget.closeCalls.Load(), firstRecognizer.closeCalls.Load())
	}
	route.mu.Lock()
	_, retainedFirstToken := route.sessions[firstToken]
	route.mu.Unlock()
	if retainedFirstToken {
		t.Fatalf("failed token %v remained in Apple route", firstToken)
	}
	if err := host.coordinator.step(bridge.VoiceEnded(firstPress)); err != nil {
		t.Fatal(err)
	}

	secondPress, secondToken := beginAppleRouteTranscriptSession(t, host, device, 2)
	select {
	case <-secondOpenStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("second recognizer Open did not start")
	}
	secondSource := leasedSourceFor("second")
	runHostTask(t, tasks) // SourcePrepared opens the gate for the second token.
	if snapshot := host.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Token != secondToken || snapshot.Session.Phase != bridge.PhaseCapturing {
		t.Fatalf("second SourcePrepared was not applied to its token: %+v", snapshot)
	}
	select {
	case <-secondSource.reading:
	case <-time.After(3 * time.Second):
		t.Fatalf("second source did not Read after OpenGate: source closed=%d core=%+v", secondSource.closed.Load(), host.coordinator.core.Snapshot())
	}
	if err := route.DeliverTranscript(firstToken, "stale first-token text"); err == nil {
		t.Fatal("retired first token delivered a transcript during the reopened session")
	}
	if err := host.coordinator.step(bridge.VoiceEnded(secondPress)); err != nil {
		t.Fatal(err)
	}

	drainedCompletion := runAndReturnHostTask(t, tasks)
	drainedCompletion()
	waitAppleRouteReady(t, route)
	if got, want := secondTarget.inserted(), []string{"second session transcript"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second session target inserts = %v, want %v", got, want)
	}
	if got := firstTarget.inserted(); len(got) != 0 {
		t.Fatalf("first target received a transcript after reopen: %v", got)
	}
	if firstSource.closed.Load() != 1 || firstTarget.closeCalls.Load() != 1 || firstRecognizer.closeCalls.Load() != 1 ||
		secondSource.closed.Load() != 1 ||
		secondTarget.closeCalls.Load() != 1 || secondRecognizer.closeCalls.Load() != 1 {
		t.Fatalf("session handle close counts: first source/target/recognizer=%d/%d/%d second=%d/%d/%d",
			firstSource.closed.Load(), firstTarget.closeCalls.Load(), firstRecognizer.closeCalls.Load(),
			secondSource.closed.Load(), secondTarget.closeCalls.Load(), secondRecognizer.closeCalls.Load())
	}
	route.mu.Lock()
	firstRetained, secondRetained := route.sessions[firstToken] != nil, route.sessions[secondToken] != nil
	route.mu.Unlock()
	if firstRetained || secondRetained {
		t.Fatalf("completed Apple route retained sessions: first=%t second=%t", firstRetained, secondRetained)
	}
	if snapshot := host.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 || len(snapshot.PressedButtons) != 0 {
		t.Fatalf("continuous Apple session left Core state: %+v", snapshot)
	}
}

func beginAppleRouteTranscriptSession(t *testing.T, host *shell, device bridge.Device, pressID uint64) (bridge.PressToken, bridge.SessionToken) {
	t.Helper()
	press, err := bridge.NewPressToken(device, "voice", pressID)
	if err != nil {
		t.Fatal(err)
	}
	event, err := host.coordinator.audioInput(press, bridge.NewAudioBinding())
	if err != nil {
		t.Fatalf("Apple route BeforeStart: %v", err)
	}
	if err := host.coordinator.step(event); err != nil {
		t.Fatal(err)
	}
	snapshot := host.coordinator.core.Snapshot()
	if !snapshot.HasSession || snapshot.Session.Binding.Policy() != bridge.PolicyAudio {
		t.Fatalf("synthetic press did not start Apple audio session: %+v", snapshot)
	}
	return press, snapshot.Session.Token
}

func waitAppleRouteReady(t *testing.T, route *appleAudioRoute) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for !route.Ready() {
		select {
		case <-route.Changes():
		case <-timer.C:
			t.Fatal("timed out waiting for Apple route readiness")
		}
	}
}

type appleRouteTestPCMSource struct{ *hostPCMSource }

func (*appleRouteTestPCMSource) Ready(context.Context) (int, error) { return 16000, nil }

type hostTranscriptTestSink struct {
	*hostPCMSink
	text            string
	drainErr        error
	drainStarted    chan struct{}
	drainStartOnce  sync.Once
	transcriptCalls atomic.Int32
	tailWritten     atomic.Bool
	drainSawTail    atomic.Bool
}

func newHostTranscriptTestSink(drainErr error) *hostTranscriptTestSink {
	return &hostTranscriptTestSink{
		hostPCMSink: newHostPCMSink(), text: "最终识别文本", drainErr: drainErr,
		drainStarted: make(chan struct{}),
	}
}

func (s *hostTranscriptTestSink) TryWrite(pcm []int16) error {
	if reflect.DeepEqual(pcm, []int16{7, -7, 9}) {
		s.tailWritten.Store(true)
	}
	return s.hostPCMSink.TryWrite(pcm)
}

func (s *hostTranscriptTestSink) Drain(ctx context.Context) error {
	s.drainStartOnce.Do(func() { close(s.drainStarted) })
	s.drainSawTail.Store(s.tailWritten.Load())
	if err := ctx.Err(); err != nil {
		return err
	}
	s.drains.Add(1)
	return s.drainErr
}

func (s *hostTranscriptTestSink) Transcript() (string, bool, error) {
	s.transcriptCalls.Add(1)
	return s.text, true, nil
}

type transcriptDelivery struct {
	token bridge.SessionToken
	text  string
}

type transcriptDeliveryRecorder struct {
	mu          sync.Mutex
	entriesList []transcriptDelivery
}

func (r *transcriptDeliveryRecorder) deliver(token bridge.SessionToken, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entriesList = append(r.entriesList, transcriptDelivery{token: token, text: text})
	return nil
}

func (r *transcriptDeliveryRecorder) entries() []transcriptDelivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]transcriptDelivery(nil), r.entriesList...)
}

func newTranscriptAudioHost(t *testing.T, drainErr error) (*shell, chan func(), *hostPCMSource, *hostTranscriptTestSink, *transcriptDeliveryRecorder) {
	t.Helper()
	host := &shell{}
	if err := host.setupPluginsWith(func(func(xiaomi.MonitorEvent)) (hidMonitor, error) {
		t.Fatal("hardware opened")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	tasks := make(chan func(), 8)
	host.audioPost = func(fn func()) { tasks <- fn }
	source, sink := newHostPCMSource(), newHostTranscriptTestSink(drainErr)
	deliveries := &transcriptDeliveryRecorder{}
	host.audioResources = func(context.Context, bridge.SessionToken) (audiosession.Resources, error) {
		return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	}
	host.audioResourcesDeliver = deliveries.deliver
	if err := host.plugins.Enable(audioSessionPlugin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if host.audioSession != nil {
			host.audioSession.cancel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := host.audioSession.close(ctx); err != nil {
				t.Errorf("close transcript host: %v", err)
			}
		}
		if host.plugins != nil {
			if err := host.plugins.Close(); err != nil {
				t.Errorf("close transcript host plugins: %v", err)
			}
		}
	})
	return host, tasks, source, sink, deliveries
}

func beginTranscriptAudioSession(t *testing.T, host *shell) (bridge.PressToken, bridge.SessionToken) {
	t.Helper()
	device, err := bridge.NewDevice("transcript-host", 1)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.Configure(bridge.NewAudioBinding())} {
		if err := host.coordinator.step(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := host.coordinator.step(bridge.VoiceBegan(press)); err != nil {
		t.Fatal(err)
	}
	snapshot := host.coordinator.core.Snapshot()
	if !snapshot.HasSession || snapshot.Session.Binding.Policy() != bridge.PolicyAudio {
		t.Fatalf("host did not start the keyless session: %+v", snapshot)
	}
	return press, snapshot.Session.Token
}

func runAndReturnHostTask(t *testing.T, tasks <-chan func()) func() {
	t.Helper()
	select {
	case fn := <-tasks:
		return fn
	case <-time.After(3 * time.Second):
		t.Fatal("missing queued main-thread completion")
		return nil
	}
}

type appleRecognizerTestStub struct {
	*hostPCMSink
	text       string
	closeCalls atomic.Int32
}

func (s *appleRecognizerTestStub) FinalText() (string, error) {
	if s.text != "" {
		return s.text, nil
	}
	return "识别文本", nil
}

func (s *appleRecognizerTestStub) Close() error {
	s.closeCalls.Add(1)
	return s.hostPCMSink.Close()
}

type transcriptTargetTestStub struct {
	mu         sync.Mutex
	texts      []string
	closeCalls atomic.Int32
}

func (t *transcriptTargetTestStub) Insert(text string) error {
	t.mu.Lock()
	t.texts = append(t.texts, text)
	t.mu.Unlock()
	return nil
}

func (t *transcriptTargetTestStub) inserted() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.texts...)
}

func (t *transcriptTargetTestStub) Close() error {
	t.closeCalls.Add(1)
	return nil
}

var _ audiosession.Source = (*hostPCMSource)(nil)
var _ audiosession.TranscriptSink = (*hostTranscriptTestSink)(nil)
var _ localRecognizer = (*appleRecognizerTestStub)(nil)
var _ transcriptTarget = (*transcriptTargetTestStub)(nil)
