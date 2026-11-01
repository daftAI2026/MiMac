package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

func TestVoiceIOTraceDisabledPreservesResourcesWithoutAllocations(t *testing.T) {
	token := voiceIOContractToken(t)
	source := &voiceIOContractSource{}
	sink := &voiceIOContractSink{}
	observer := &voiceIOContractObserver{}
	resources := audiosession.Resources{
		Source: source, Sink: sink, SampleRate: 12345, Observer: observer,
		WaitReceiver:  func(context.Context) error { return nil },
		WatchReceiver: func(context.Context) error { return nil },
	}
	var got audiosession.Resources
	var trace *voiceOverlayTrace
	if allocations := testing.AllocsPerRun(100, func() {
		got = trace.traceResources(token, resources)
	}); allocations != 0 {
		t.Fatalf("disabled resource tracing allocated: %v", allocations)
	}
	if got.Source != source || got.Sink != sink {
		t.Fatal("disabled tracing changed source or sink identity")
	}
	if got.SampleRate != resources.SampleRate || got.Observer != observer || got.WaitReceiver == nil || got.WatchReceiver == nil {
		t.Fatal("disabled tracing changed non-I/O resources")
	}

	got = trace.traceResources(token, audiosession.Resources{})
	if got.Source != nil || got.Sink != nil {
		t.Fatal("disabled tracing changed nil resources")
	}
}

func TestVoiceIOTraceEnabledPreservesOptionalSourceErr(t *testing.T) {
	token := voiceIOContractToken(t)
	trace := &voiceOverlayTrace{}
	readErr := errors.New("DO_NOT_LOG_SOURCE_READ_SECRET")
	stopErr := errors.New("DO_NOT_LOG_SOURCE_STOP_SECRET")
	closeErr := errors.New("DO_NOT_LOG_SOURCE_CLOSE_SECRET")
	asyncErr := errors.New("DO_NOT_LOG_SOURCE_HEALTH_SECRET")
	inner := &voiceIOContractSource{
		reads: []voiceIOContractRead{
			{samples: []int16{23117, -19503, 14009}},
			{err: readErr},
		},
		stopErr: stopErr, closeErr: closeErr,
	}
	withErr := &voiceIOContractSourceWithErr{voiceIOContractSource: inner, err: asyncErr}
	decorated := trace.traceResources(token, audiosession.Resources{Source: withErr}).Source
	if decorated == withErr {
		t.Fatal("enabled tracing did not decorate Source.Read")
	}
	if _, ok := decorated.(interface{ Err() error }); !ok {
		t.Fatal("decorated source lost its optional Err method")
	}
	buffer := make([]int16, 5)
	n, err := decorated.Read(context.Background(), buffer)
	if err != nil || n != 3 || !reflect.DeepEqual(buffer[:n], []int16{23117, -19503, 14009}) {
		t.Fatalf("decorated Read changed returned PCM: n=%d pcm=%v err=%v", n, buffer[:n], err)
	}
	n, err = decorated.Read(context.Background(), buffer)
	if n != 0 || err != readErr {
		t.Fatalf("decorated Read changed the original error: n=%d err=%v", n, err)
	}
	health, ok := decorated.(interface{ Err() error })
	if !ok || health.Err() != asyncErr || withErr.errCalls != 1 {
		t.Fatalf("decorated Err passthrough: available=%t calls=%d err=%v", ok, withErr.errCalls, asyncErr)
	}
	if err := decorated.(audiosession.Source).Stop(context.Background()); err != stopErr || inner.stopCalls != 1 {
		t.Fatalf("Stop passthrough: calls=%d err=%v", inner.stopCalls, err)
	}
	if err := decorated.(audiosession.Source).Close(); err != closeErr || inner.closeCalls != 1 {
		t.Fatalf("Close passthrough: calls=%d err=%v", inner.closeCalls, err)
	}
	report := voiceIOContractReport(t, trace)
	assertVoiceIOContractOperations(t, report, "source.read", "source.read")
	for _, secret := range []string{readErr.Error(), stopErr.Error(), closeErr.Error(), asyncErr.Error()} {
		if bytes.Contains(report, []byte(secret)) {
			t.Fatalf("trace report retained source error %q", secret)
		}
	}
	if bytes.Contains(report, []byte("[23117,-19503,14009]")) {
		t.Fatal("trace report retained source PCM")
	}

	bareSource := &voiceIOContractSource{}
	bare := trace.traceResources(token, audiosession.Resources{Source: bareSource}).Source
	if _, ok := bare.(interface{ Err() error }); ok {
		t.Fatal("decorated source fabricated an Err method")
	}
	if got := trace.traceResources(token, audiosession.Resources{Sink: &voiceIOContractSink{}}); got.Source != nil {
		t.Fatal("decorator fabricated a source for a nil resource")
	}
	if got := trace.traceResources(token, audiosession.Resources{Source: bareSource}); got.Sink != nil {
		t.Fatal("decorator fabricated a sink for a nil resource")
	}
}

func TestVoiceIOTraceEnabledPreservesSinkCapabilitySetsAndMethods(t *testing.T) {
	token := voiceIOContractToken(t)
	candidate := "DO_NOT_LOG_TRANSCRIPT_SECRET"
	writeErr := fmt.Errorf("DO_NOT_LOG_WRITE_SECRET: %w", audiooutput.ErrBackpressure)
	startErr := errors.New("DO_NOT_LOG_START_SECRET")
	transcriptErr := errors.New("DO_NOT_LOG_TRANSCRIPT_ERROR_SECRET")
	drainErr := errors.New("DO_NOT_LOG_DRAIN_SECRET")
	closeErr := errors.New("DO_NOT_LOG_SINK_CLOSE_SECRET")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cases := []struct {
		name           string
		wrap           func(*voiceIOContractSink) audiosession.Sink
		wantSession    bool
		wantTranscript bool
	}{
		{name: "base", wrap: func(s *voiceIOContractSink) audiosession.Sink { return s }},
		{name: "session", wantSession: true, wrap: func(s *voiceIOContractSink) audiosession.Sink { return &voiceIOContractSessionSink{s} }},
		{name: "transcript", wantTranscript: true, wrap: func(s *voiceIOContractSink) audiosession.Sink { return &voiceIOContractTranscriptSink{s} }},
		{name: "session and transcript", wantSession: true, wantTranscript: true, wrap: func(s *voiceIOContractSink) audiosession.Sink { return &voiceIOContractSessionTranscriptSink{s} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &voiceIOContractSink{
				writeErr: writeErr, startErr: startErr, transcript: candidate, transcriptErr: transcriptErr,
				drainErr: drainErr, closeErr: closeErr,
			}
			raw := tc.wrap(inner)
			trace := &voiceOverlayTrace{}
			decorated := trace.traceResources(token, audiosession.Resources{Sink: raw}).Sink
			if decorated == raw {
				t.Fatal("enabled tracing did not decorate Sink.TryWrite")
			}
			_, hasSession := decorated.(audiosession.SessionSink)
			_, hasTranscript := decorated.(audiosession.TranscriptSink)
			if hasSession != tc.wantSession || hasTranscript != tc.wantTranscript {
				t.Fatalf("optional sink interfaces = session:%t transcript:%t; want %t/%t", hasSession, hasTranscript, tc.wantSession, tc.wantTranscript)
			}

			pcm := []int16{23117, -19503, 14009}
			if err := decorated.TryWrite(pcm); err != writeErr || !reflect.DeepEqual(inner.written, pcm) || inner.writeCalls != 1 {
				t.Fatalf("TryWrite passthrough: calls=%d pcm=%v err=%v", inner.writeCalls, inner.written, err)
			}
			if hasSession {
				session := decorated.(audiosession.SessionSink)
				if err := session.StartSession(ctx); err != startErr || inner.startCalls != 1 || inner.startContext != ctx {
					t.Fatalf("StartSession passthrough: calls=%d same_context=%t err=%v", inner.startCalls, inner.startContext == ctx, err)
				}
			}
			if hasTranscript {
				text, final, err := decorated.(audiosession.TranscriptSink).Transcript()
				if text != candidate || !final || err != transcriptErr || inner.transcriptCalls != 1 {
					t.Fatalf("Transcript passthrough: text=%q final=%t calls=%d err=%v", text, final, inner.transcriptCalls, err)
				}
			}
			if err := decorated.Drain(ctx); err != drainErr || inner.drainCalls != 1 {
				t.Fatalf("Drain passthrough: calls=%d err=%v", inner.drainCalls, err)
			}
			if err := decorated.Close(); err != closeErr || inner.closeCalls != 1 {
				t.Fatalf("Close passthrough: calls=%d err=%v", inner.closeCalls, err)
			}

			report := voiceIOContractReport(t, trace)
			assertVoiceIOContractOperations(t, report, "sink.try_write")
			for _, secret := range []string{
				candidate, writeErr.Error(), startErr.Error(), transcriptErr.Error(), drainErr.Error(), closeErr.Error(),
			} {
				if bytes.Contains(report, []byte(secret)) {
					t.Fatalf("trace report retained private text/error %q", secret)
				}
			}
			if bytes.Contains(report, []byte("[23117,-19503,14009]")) {
				t.Fatal("trace report retained PCM samples")
			}
		})
	}
}

func TestVoiceIOTraceLeavesObserverAndReceiverCallbacksAlone(t *testing.T) {
	token := voiceIOContractToken(t)
	trace := &voiceOverlayTrace{}
	observer := &voiceIOContractObserver{}
	waitErr := errors.New("receiver wait sentinel")
	watchErr := errors.New("receiver watch sentinel")
	waitCalls, watchCalls := 0, 0
	var waitContext, watchContext context.Context
	resources := audiosession.Resources{
		SampleRate: 16000,
		Observer:   observer,
		WaitReceiver: func(ctx context.Context) error {
			waitCalls++
			waitContext = ctx
			return waitErr
		},
		WatchReceiver: func(ctx context.Context) error {
			watchCalls++
			watchContext = ctx
			return watchErr
		},
	}
	got := trace.traceResources(token, resources)
	if got.Source != nil || got.Sink != nil || got.SampleRate != resources.SampleRate || got.Observer != observer {
		t.Fatal("I/O decorator changed nil resources, rate, or observer")
	}
	if waitCalls != 0 || watchCalls != 0 {
		t.Fatalf("receiver callback ran during decoration: wait=%d watch=%d", waitCalls, watchCalls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := got.WaitReceiver(ctx); err != waitErr || waitCalls != 1 || waitContext != ctx {
		t.Fatalf("WaitReceiver changed: calls=%d err=%v", waitCalls, err)
	}
	if err := got.WatchReceiver(ctx); err != watchErr || watchCalls != 1 || watchContext != ctx {
		t.Fatalf("WatchReceiver changed: calls=%d err=%v", watchCalls, err)
	}
}

type voiceIOContractRead struct {
	samples []int16
	err     error
}

type voiceIOContractSource struct {
	reads      []voiceIOContractRead
	readCalls  int
	stopCalls  int
	closeCalls int
	stopErr    error
	closeErr   error
}

func (s *voiceIOContractSource) Read(_ context.Context, dst []int16) (int, error) {
	s.readCalls++
	if s.readCalls > len(s.reads) {
		return 0, io.EOF
	}
	result := s.reads[s.readCalls-1]
	return copy(dst, result.samples), result.err
}

func (s *voiceIOContractSource) Stop(context.Context) error {
	s.stopCalls++
	return s.stopErr
}

func (s *voiceIOContractSource) Close() error {
	s.closeCalls++
	return s.closeErr
}

type voiceIOContractSourceWithErr struct {
	*voiceIOContractSource
	err      error
	errCalls int
}

func (s *voiceIOContractSourceWithErr) Err() error {
	s.errCalls++
	return s.err
}

type voiceIOContractSink struct {
	written         []int16
	writeCalls      int
	writeErr        error
	startCalls      int
	startContext    context.Context
	startErr        error
	transcript      string
	transcriptCalls int
	transcriptErr   error
	drainCalls      int
	drainErr        error
	closeCalls      int
	closeErr        error
}

func (s *voiceIOContractSink) TryWrite(pcm []int16) error {
	s.writeCalls++
	s.written = append(s.written[:0], pcm...)
	return s.writeErr
}

func (s *voiceIOContractSink) Drain(context.Context) error {
	s.drainCalls++
	return s.drainErr
}

func (s *voiceIOContractSink) Close() error {
	s.closeCalls++
	return s.closeErr
}

func (s *voiceIOContractSink) startSession(ctx context.Context) error {
	s.startCalls++
	s.startContext = ctx
	return s.startErr
}

func (s *voiceIOContractSink) readTranscript() (string, bool, error) {
	s.transcriptCalls++
	return s.transcript, true, s.transcriptErr
}

type voiceIOContractSessionSink struct{ *voiceIOContractSink }

func (s *voiceIOContractSessionSink) StartSession(ctx context.Context) error {
	return s.voiceIOContractSink.startSession(ctx)
}

type voiceIOContractTranscriptSink struct{ *voiceIOContractSink }

func (s *voiceIOContractTranscriptSink) Transcript() (string, bool, error) {
	return s.voiceIOContractSink.readTranscript()
}

type voiceIOContractSessionTranscriptSink struct{ *voiceIOContractSink }

func (s *voiceIOContractSessionTranscriptSink) StartSession(ctx context.Context) error {
	return s.voiceIOContractSink.startSession(ctx)
}

func (s *voiceIOContractSessionTranscriptSink) Transcript() (string, bool, error) {
	return s.voiceIOContractSink.readTranscript()
}

type voiceIOContractObserver struct{}

func (*voiceIOContractObserver) Observe([]int16, time.Time) {}
func (*voiceIOContractObserver) Clear()                     {}

func voiceIOContractToken(t *testing.T) bridge.SessionToken {
	t.Helper()
	core, err := bridge.NewCore(bridge.NewAudioBinding())
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("voice-io-contract", 41)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{
		bridge.DeviceConnected(device), bridge.RouteAvailable(true), bridge.ScopeAvailable(true),
	} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := core.Step(bridge.VoiceBegan(press))
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range effects {
		if prepared, ok := effect.(bridge.PrepareSourceEffect); ok {
			return prepared.Token
		}
	}
	t.Fatal("voice start did not create a preparation token")
	return bridge.SessionToken{}
}

func voiceIOContractReport(t *testing.T, trace *voiceOverlayTrace) []byte {
	t.Helper()
	var report bytes.Buffer
	if err := trace.write(&report); err != nil {
		t.Fatal(err)
	}
	return report.Bytes()
}

func assertVoiceIOContractOperations(t *testing.T, report []byte, want ...string) {
	t.Helper()
	var decoded struct {
		IO struct {
			Entries []struct {
				Operation string `json:"operation"`
			} `json:"entries"`
		} `json:"io"`
	}
	if err := json.Unmarshal(report, &decoded); err != nil {
		t.Fatalf("decode I/O trace report: %v", err)
	}
	operations := make([]string, len(decoded.IO.Entries))
	for i, entry := range decoded.IO.Entries {
		operations[i] = entry.Operation
	}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("traced operations = %v, want %v", operations, want)
	}
}

var _ audiosession.Source = (*voiceIOContractSource)(nil)
var _ audiosession.Sink = (*voiceIOContractSink)(nil)
var _ audiosession.SessionSink = (*voiceIOContractSessionSink)(nil)
var _ audiosession.TranscriptSink = (*voiceIOContractTranscriptSink)(nil)
var _ audiosession.SessionSink = (*voiceIOContractSessionTranscriptSink)(nil)
var _ audiosession.TranscriptSink = (*voiceIOContractSessionTranscriptSink)(nil)
var _ audiosession.PCMObserver = (*voiceIOContractObserver)(nil)
