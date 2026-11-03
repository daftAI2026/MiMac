package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"reflect"
	"testing"
	"time"
)

// Exercises the actual host resource factory and Runtime, with no hardware or
// native target. The report must locate the I/O gap independently of the UI.
func TestVoiceReportIncludesRuntimeIOWithoutChangingDelivery(t *testing.T) {
	s, tasks, source, sink := newSessionHostTest(t)
	trace := &voiceOverlayTrace{}
	s.voiceOverlay.meter.trace = trace
	press := beginHostVoice(t, s)
	runHostTask(t, tasks)
	pcm := []int16{123, -234, 345}
	source.samples <- pcm
	select {
	case got := <-sink.writes:
		if !reflect.DeepEqual(got, pcm) {
			t.Fatal("diagnostics changed PCM")
		}
	case <-time.After(time.Second):
		t.Fatal("missing delivery")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
	waitHostSignal(t, sink.closed)
	var buffer bytes.Buffer
	if err := trace.write(&buffer); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Schema int `json:"schema"`
		IO     struct {
			Entries []struct {
				Operation  string     `json:"operation"`
				FinishedAt *time.Time `json:"finished_at"`
				Samples    int        `json:"samples"`
				Session    uint64     `json:"session"`
				Generation uint64     `json:"generation"`
				Result     string     `json:"result"`
			} `json:"entries"`
		} `json:"io"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != 2 || len(report.IO.Entries) == 0 {
		t.Fatalf("report lacks source/sink timing: schema=%d entries=%d", report.Schema, len(report.IO.Entries))
	}
	var read, write, eof int
	lifecycle := map[string]int{}
	for _, e := range report.IO.Entries {
		switch e.Operation {
		case "source.stop", "source.close", "sink.drain", "sink.close":
			lifecycle[e.Operation]++
		}
		if e.Session == 0 || e.Generation == 0 {
			t.Fatal("missing session attribution")
		}
		if e.FinishedAt == nil {
			continue
		}
		switch e.Operation {
		case "source.read":
			read++
			if e.Result == "eof" {
				eof++
			}
		case "sink.try_write":
			write++
			if e.Result != "ok" {
				t.Fatal("accepted write reported failure")
			}
		}
	}
	if read != 2 || write != 2 || eof != 1 {
		t.Fatalf("missing exact delivery/tail lifecycle: reads=%d writes=%d eof=%d", read, write, eof)
	}
	for _, operation := range []string{"source.stop", "sink.drain", "source.close", "sink.close"} {
		if lifecycle[operation] != 1 {
			t.Fatalf("Runtime lifecycle %s count=%d", operation, lifecycle[operation])
		}
	}
}

// A blocked underlying call must not hold the diagnostic lock or prevent a
// report snapshot. Later completion must not mutate an already taken snapshot.
func TestVoiceIOSpansExposeBlockedCalls(t *testing.T) {
	for _, operation := range []string{"source.read", "sink.try_write"} {
		t.Run(operation, func(t *testing.T) {
			trace := &voiceOverlayTrace{}
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(release)
			block := func() { close(entered); <-release }
			var call func()
			switch operation {
			case "source.read":
				source := &voiceIOTestSource{read: func(_ context.Context, p []int16) (int, error) { block(); p[0] = 7; return 1, io.EOF }}
				wrapped := trace.traceResources(bridge.SessionToken{}, audiosession.Resources{Source: source})
				call = func() {
					p := make([]int16, 3)
					n, err := wrapped.Source.Read(context.Background(), p)
					if n != 1 || err != io.EOF || p[0] != 7 {
						t.Error("source result changed")
					}
				}
			case "sink.try_write":
				sink := &voiceIOTestSink{write: func(p []int16) error { block(); return audiooutput.ErrBackpressure }}
				wrapped := trace.traceResources(bridge.SessionToken{}, audiosession.Resources{Sink: sink})
				call = func() {
					if err := wrapped.Sink.TryWrite([]int16{7, 8, 9}); err != audiooutput.ErrBackpressure {
						t.Error("sink result changed")
					}
				}
			}
			go func() { defer close(done); call() }()
			waitHostSignal(t, entered)
			snap := trace.io.snapshot()
			if len(snap.Entries) != 1 || snap.Entries[0].Operation != operation || !snap.Entries[0].FinishedAt.IsZero() || snap.Entries[0].RequestedSamples != 3 {
				t.Fatalf("missing unfinished call: %+v", snap)
			}
			if len(snap.Sessions) != 1 || snap.Sessions[0].CallsStarted != 1 || snap.Sessions[0].CallsCompleted != 0 || snap.Sessions[0].CallsInFlight != 1 {
				t.Fatalf("blocked call missing from aggregate: %+v", snap.Sessions)
			}
			if operation == "source.read" && snap.Sessions[0].SourceReadSamplesComplete {
				t.Fatal("in-flight read presented as a complete sample total")
			}
			if operation == "sink.try_write" && snap.Sessions[0].SinkAcceptedSamplesKnown {
				t.Fatal("in-flight write presented as a known acceptance total")
			}
			var report bytes.Buffer
			if err := trace.write(&report); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(report.Bytes(), []byte(`"finished_at"`)) {
				t.Fatal("unfinished I/O fabricated an end timestamp")
			}
			// Release this one waiter; deferred close also unblocks failure paths.
			release <- struct{}{}
			waitHostSignal(t, done)
			after := trace.io.snapshot().Entries[0]
			if after.FinishedAt.IsZero() || after.DurationNS <= 0 || after.Call != snap.Entries[0].Call {
				t.Fatalf("completion missing: %+v", after)
			}
			afterSummary := trace.io.snapshot().Sessions[0]
			if afterSummary.CallsCompleted != 1 || afterSummary.CallsInFlight != 0 {
				t.Fatalf("late aggregate completion missing: %+v", afterSummary)
			}
			if operation == "source.read" && (afterSummary.SourceReadSamples != 1 || !afterSummary.SourceReadSamplesComplete) {
				t.Fatalf("read total not completed: %+v", afterSummary)
			}
			if operation == "sink.try_write" && (afterSummary.SinkBackpressureCalls != 1 || !afterSummary.SinkAcceptedSamplesKnown) {
				t.Fatalf("backpressure acceptance semantics changed: %+v", afterSummary)
			}
			if !snap.Entries[0].FinishedAt.IsZero() {
				t.Fatal("late completion mutated an earlier snapshot")
			}
			if operation == "source.read" && (after.Result != "eof" || after.Samples != 1) {
				t.Fatal("EOF short block lost")
			}
			if operation == "sink.try_write" && (after.Result != "backpressure" || after.Samples != 0) {
				t.Fatal("backpressure presented as acceptance")
			}
		})
	}
}

func TestVoiceIOTraceExposesBlockedReceiverWaitWithoutChangingResult(t *testing.T) {
	trace := &voiceOverlayTrace{}
	token := voiceIOTraceTestToken(t, 83)
	entered, release, done := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	waitErr := errors.New("private receiver wait failure")
	var calls int
	var receivedContext context.Context
	resources := audiosession.Resources{WaitReceiver: func(ctx context.Context) error {
		calls++
		receivedContext = ctx
		close(entered)
		<-release
		return waitErr
	}}
	wrapped := trace.traceResources(token, resources)
	if calls != 0 {
		t.Fatal("WaitReceiver ran during decoration")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotErr error
	go func() {
		defer close(done)
		gotErr = wrapped.WaitReceiver(ctx)
	}()
	waitHostSignal(t, entered)
	if calls != 1 || receivedContext != ctx {
		t.Fatalf("WaitReceiver passthrough: calls=%d same_context=%t", calls, receivedContext == ctx)
	}

	blocked := trace.io.snapshot()
	if len(blocked.Entries) != 1 {
		t.Fatalf("blocked wait entry count=%d", len(blocked.Entries))
	}
	entry := blocked.Entries[0]
	if entry.Operation != "receiver.wait" || !entry.FinishedAt.IsZero() || entry.RequestedSamples != 0 ||
		entry.Samples != 0 || entry.SamplesKnown {
		t.Fatalf("blocked wait span has incorrect fields: %+v", entry)
	}
	if len(blocked.Sessions) != 1 || blocked.Sessions[0].CallsStarted != 1 ||
		blocked.Sessions[0].CallsCompleted != 0 || blocked.Sessions[0].CallsInFlight != 1 {
		t.Fatalf("blocked wait missing from generic call summary: %+v", blocked.Sessions)
	}

	var inFlightReport bytes.Buffer
	if err := trace.write(&inFlightReport); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(inFlightReport.Bytes(), []byte(waitErr.Error())) {
		t.Fatal("in-flight report retained a receiver error")
	}

	release <- struct{}{}
	waitHostSignal(t, done)
	if gotErr != waitErr || calls != 1 || receivedContext != ctx {
		t.Fatalf("WaitReceiver result changed: calls=%d same_context=%t err=%v", calls, receivedContext == ctx, gotErr)
	}
	if !entry.FinishedAt.IsZero() {
		t.Fatal("late wait completion mutated an earlier snapshot")
	}

	after := trace.io.snapshot()
	if len(after.Entries) != 1 || after.Entries[0].FinishedAt.IsZero() ||
		after.Entries[0].Result != "error" || after.Entries[0].SamplesKnown {
		t.Fatalf("completed wait span incorrect: %+v", after.Entries)
	}
	if len(after.Sessions) != 1 || after.Sessions[0].CallsCompleted != 1 ||
		after.Sessions[0].CallsInFlight != 0 || after.Sessions[0].ErrorCalls != 1 {
		t.Fatalf("completed wait missing from generic summary: %+v", after.Sessions)
	}

	var report bytes.Buffer
	if err := trace.write(&report); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(report.Bytes(), []byte("receiver.wait")) ||
		!bytes.Contains(report.Bytes(), []byte(`"result":"error"`)) ||
		bytes.Contains(report.Bytes(), []byte(waitErr.Error())) {
		t.Fatal("receiver wait report missing operation/result or retained raw error text")
	}
}

func TestVoiceIOTracePreservesNilAndDisabledReceiverWait(t *testing.T) {
	token := voiceIOTraceTestToken(t, 84)
	trace := &voiceOverlayTrace{}
	if got := trace.traceResources(token, audiosession.Resources{}); got.WaitReceiver != nil {
		t.Fatal("tracing invented a receiver wait callback")
	}

	waitErr := errors.New("disabled receiver wait sentinel")
	var calls int
	var receivedContext context.Context
	resources := audiosession.Resources{WaitReceiver: func(ctx context.Context) error {
		calls++
		receivedContext = ctx
		return waitErr
	}}
	var disabled *voiceOverlayTrace
	got := disabled.traceResources(token, resources)
	if got.WaitReceiver == nil {
		t.Fatal("disabled tracing removed the receiver wait callback")
	}
	ctx := context.Background()
	if err := got.WaitReceiver(ctx); err != waitErr || calls != 1 || receivedContext != ctx {
		t.Fatalf("disabled receiver wait passthrough: calls=%d same_context=%t err=%v", calls, receivedContext == ctx, err)
	}
}

type voiceIOTestSource struct {
	read func(context.Context, []int16) (int, error)
}

func (s *voiceIOTestSource) Read(c context.Context, p []int16) (int, error) { return s.read(c, p) }
func (*voiceIOTestSource) Stop(context.Context) error                       { return nil }
func (*voiceIOTestSource) Close() error                                     { return nil }

type voiceIOTestSink struct{ write func([]int16) error }

func (s *voiceIOTestSink) TryWrite(p []int16) error  { return s.write(p) }
func (*voiceIOTestSink) Drain(context.Context) error { return nil }
func (*voiceIOTestSink) Close() error                { return nil }

func voiceIOTraceTestToken(t *testing.T, generation uint64) bridge.SessionToken {
	t.Helper()
	core, err := bridge.NewCore(bridge.NewAudioBinding())
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("voice-io-trace-test", generation)
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

func TestVoiceIOSummariesSurviveDetailCapAcrossSessions(t *testing.T) {
	var trace voiceIOTrace
	trace.prepare()
	firstToken := voiceIOTraceTestToken(t, 81)
	secondToken := voiceIOTraceTestToken(t, 82)
	for range voiceIOTraceLimit - voiceIOTraceLifecycleReserve {
		call := trace.begin(firstToken, "source.read", 1)
		trace.end(call, 1, nil)
	}
	for _, c := range []struct {
		token           bridge.SessionToken
		readSamples     int
		acceptedSamples int
	}{
		{firstToken, 4, 3},
		{secondToken, 5, 2},
	} {
		read := trace.begin(c.token, "source.read", c.readSamples)
		trace.end(read, c.readSamples, nil)
		accepted := trace.begin(c.token, "sink.try_write", c.acceptedSamples)
		trace.end(accepted, c.acceptedSamples, nil)
		backpressure := trace.begin(c.token, "sink.try_write", 7)
		backpressure.startedAt = backpressure.startedAt.Add(-17 * time.Millisecond)
		trace.end(backpressure, 0, audiooutput.ErrBackpressure)
		unknown := trace.begin(c.token, "sink.try_write", 9)
		trace.end(unknown, 0, errors.New("native acceptance unknown"))
	}

	snapshot := trace.snapshot()
	if len(snapshot.Entries) != voiceIOTraceLimit-voiceIOTraceLifecycleReserve || snapshot.Dropped != 8 {
		t.Fatalf("detail cap changed: entries=%d dropped=%d", len(snapshot.Entries), snapshot.Dropped)
	}
	if snapshot.SessionSummaryCoverage != "complete" || snapshot.SessionOverflowCalls != 0 || len(snapshot.Sessions) != 2 {
		t.Fatalf("session summary coverage missing: %+v", snapshot)
	}
	for i, want := range []struct {
		samples  int64
		accepted int64
	}{
		{int64(voiceIOTraceLimit - voiceIOTraceLifecycleReserve + 4), 3},
		{5, 2},
	} {
		got := snapshot.Sessions[i]
		if got.SourceReadSamples != want.samples || got.SinkAcceptedSamples != want.accepted || got.SinkAcceptedSamplesKnown {
			t.Fatalf("session %d cumulative samples/known flag: %+v", i, got)
		}
		if got.SinkUnknownAcceptanceCalls != 1 || got.SinkBackpressureCalls != 1 || got.SinkBackpressureDurationNS < int64(17*time.Millisecond) || got.SinkBackpressureMaxDurationNS < int64(17*time.Millisecond) {
			t.Fatalf("session %d sink outcomes/duration: %+v", i, got)
		}
		if got.DetailCoverage != "truncated" || got.DetailSpansDropped != 4 || got.CallsStarted != got.CallsCompleted || got.CallsInFlight != 0 {
			t.Fatalf("session %d detail/call coverage: %+v", i, got)
		}
	}
}

func TestVoiceIOSessionSummaryOverflowIsExplicit(t *testing.T) {
	var trace voiceIOTrace
	trace.prepare()
	for generation := uint64(1); generation <= voiceIOTraceSessionLimit; generation++ {
		token := voiceIOTraceTestToken(t, generation)
		read := trace.begin(token, "source.read", 1)
		trace.end(read, 1, nil)
		write := trace.begin(token, "sink.try_write", 1)
		trace.end(write, 1, nil)
	}
	firstOverflowToken := voiceIOTraceTestToken(t, voiceIOTraceSessionLimit+1)
	read := trace.begin(firstOverflowToken, "source.read", 5)
	trace.end(read, 5, nil)
	write := trace.begin(firstOverflowToken, "sink.try_write", 5)
	trace.end(write, 5, nil)
	backpressure := trace.begin(firstOverflowToken, "sink.try_write", 3)
	backpressure.startedAt = backpressure.startedAt.Add(-11 * time.Millisecond)
	trace.end(backpressure, 0, audiooutput.ErrBackpressure)
	unknown := trace.begin(firstOverflowToken, "sink.try_write", 3)
	trace.end(unknown, 0, errors.New("overflow acceptance unknown"))

	lateToken := voiceIOTraceTestToken(t, voiceIOTraceSessionLimit+2)
	late := trace.begin(lateToken, "source.read", 7)
	blocked := trace.snapshot()
	if blocked.SessionLimit != voiceIOTraceSessionLimit || len(blocked.Sessions) != voiceIOTraceSessionLimit ||
		blocked.SessionSummaryCoverage != "overflow" || blocked.SessionOverflowCalls != 5 ||
		blocked.SessionOverflowStarted != 5 || blocked.SessionOverflowCompleted != 4 || blocked.SessionOverflowInFlight != 1 {
		t.Fatalf("session overflow counts not explicit: %+v", blocked)
	}
	overflow := blocked.OverflowSummary
	if overflow.Scope != "mixed_untracked_sessions" || overflow.CallsStarted != 5 || overflow.CallsCompleted != 4 || overflow.CallsInFlight != 1 {
		t.Fatalf("untracked calls were not aggregated: %+v", overflow)
	}
	if overflow.SourceReadSamples != 5 || overflow.SourceReadSamplesComplete ||
		overflow.SinkAcceptedSamples != 5 || overflow.SinkAcceptedSamplesKnown ||
		overflow.SinkUnknownAcceptanceCalls != 1 || overflow.SinkBackpressureCalls != 1 ||
		overflow.SinkBackpressureDurationNS < int64(11*time.Millisecond) || overflow.SinkBackpressureMaxDurationNS < int64(11*time.Millisecond) {
		t.Fatalf("untracked sample/error/backpressure totals incorrect: %+v", overflow)
	}

	trace.end(late, 7, nil)
	after := trace.snapshot().OverflowSummary
	if after.SourceReadSamples != 12 || !after.SourceReadSamplesComplete ||
		after.CallsStarted != 5 || after.CallsCompleted != 5 || after.CallsInFlight != 0 ||
		after.SinkAcceptedSamples != 5 || after.SinkAcceptedSamplesKnown {
		t.Fatalf("late completion did not update overflow aggregate: %+v", after)
	}
	if blocked.OverflowSummary.SourceReadSamples != 5 || blocked.OverflowSummary.CallsInFlight != 1 {
		t.Fatal("late completion mutated earlier overflow snapshot")
	}
	var totalRead, totalAccepted int64
	for _, session := range blocked.Sessions {
		totalRead += session.SourceReadSamples
		totalAccepted += session.SinkAcceptedSamples
	}
	if totalRead+after.SourceReadSamples != voiceIOTraceSessionLimit+12 ||
		totalAccepted+after.SinkAcceptedSamples != voiceIOTraceSessionLimit+5 {
		t.Fatal("tracked and mixed summaries lost or double-counted samples")
	}
}

func TestVoiceIOBufferBoundAndLateCompletion(t *testing.T) {
	var trace voiceIOTrace
	trace.prepare()
	first := trace.begin(bridge.SessionToken{}, "source.stop", 0)
	for range voiceIOTraceLimit - 1 {
		call := trace.begin(bridge.SessionToken{}, "sink.drain", 0)
		trace.end(call, 0, nil)
	}
	for range 3 {
		call := trace.begin(bridge.SessionToken{}, "source.close", 0)
		trace.end(call, 0, nil)
	}
	before := trace.snapshot()
	if len(before.Entries) != voiceIOTraceLimit || before.Dropped != 3 || !before.Entries[first.entryIndex].FinishedAt.IsZero() {
		t.Fatal("capacity/drop or unfinished span incorrect")
	}
	trace.end(first, 0, nil)
	after := trace.snapshot()
	if after.Entries[first.entryIndex].FinishedAt.IsZero() || after.Entries[first.entryIndex].SamplesKnown || after.Entries[first.entryIndex].Samples != 0 || after.Entries[first.entryIndex].Result != "ok" || after.Dropped != 3 || !before.Entries[first.entryIndex].FinishedAt.IsZero() {
		t.Fatal("full buffer lost late completion or aliased snapshot")
	}
	if n := testing.AllocsPerRun(100, func() { call := trace.begin(bridge.SessionToken{}, "source.read", 1); trace.end(call, 1, nil) }); n != 0 {
		t.Fatalf("full buffer allocates: %v", n)
	}
	var steady voiceIOTrace
	steady.prepare()
	if n := testing.AllocsPerRun(100, func() { call := steady.begin(bridge.SessionToken{}, "sink.try_write", 1); steady.end(call, 1, nil) }); n != 0 {
		t.Fatalf("span collection allocates: %v", n)
	}
}

func TestVoiceIOTerminalSpansSurvivePCMBudget(t *testing.T) {
	trace := &voiceOverlayTrace{}
	trace.io.prepare()
	for range voiceIOTraceLimit {
		call := trace.io.begin(bridge.SessionToken{}, "source.read", 320)
		trace.io.end(call, 320, nil)
	}
	resources := trace.traceResources(bridge.SessionToken{}, audiosession.Resources{
		Source: &voiceIOTestSource{}, Sink: &voiceIOTestSink{},
	})
	if err := resources.Source.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := resources.Sink.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := resources.Source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.Sink.Close(); err != nil {
		t.Fatal(err)
	}
	snap := trace.io.snapshot()
	if snap.Dropped == 0 || len(snap.Entries) > voiceIOTraceLimit {
		t.Fatal("unbounded PCM history")
	}
	tail := snap.Entries[len(snap.Entries)-4:]
	for i, name := range []string{"source.stop", "sink.drain", "source.close", "sink.close"} {
		span := tail[i]
		if span.Operation != name || span.FinishedAt.IsZero() || span.Result != "ok" || span.SamplesKnown {
			t.Fatalf("terminal boundary missing or claims PCM acceptance: %+v", span)
		}
	}
}

func TestVoiceIOConcurrentSnapshotsAndClassification(t *testing.T) {
	var trace voiceIOTrace
	trace.prepare()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			call := trace.begin(bridge.SessionToken{}, "source.read", 320)
			trace.end(call, 0, context.Canceled)
		}
	}()
	for range 50 {
		snap := trace.snapshot()
		for _, e := range snap.Entries {
			if !e.FinishedAt.IsZero() && (e.Result != "cancelled" || e.DurationNS < 0) {
				t.Error("torn span")
			}
		}
	}
	waitHostSignal(t, done)
	for _, c := range []struct {
		err  error
		want string
	}{{nil, "ok"}, {io.EOF, "eof"}, {context.Canceled, "cancelled"}, {context.DeadlineExceeded, "deadline"}, {audiooutput.ErrBackpressure, "backpressure"}, {errors.New("private words that must not be retained"), "error"}} {
		if got := voiceIOResult(c.err); got != c.want {
			t.Errorf("result %q want %q", got, c.want)
		}
	}
}

func TestVoiceIOSinkFailureDoesNotClaimZeroAcceptance(t *testing.T) {
	for _, c := range []struct {
		name  string
		err   error
		known bool
	}{{"accepted", nil, true}, {"backpressure", audiooutput.ErrBackpressure, true}, {"failed after partial enqueue", errors.New("native enqueue failed after prefix"), false}} {
		t.Run(c.name, func(t *testing.T) {
			trace := &voiceOverlayTrace{}
			sink := &voiceIOTestSink{write: func([]int16) error { return c.err }}
			wrapped := trace.traceResources(bridge.SessionToken{}, audiosession.Resources{Sink: sink})
			if err := wrapped.Sink.TryWrite([]int16{1, 2, 3}); err != c.err {
				t.Fatal("changed original result")
			}
			summary := trace.io.snapshot().Sessions[0]
			if c.known {
				if !summary.SinkAcceptedSamplesKnown || summary.SinkUnknownAcceptanceCalls != 0 {
					t.Fatalf("known sink result marked unknown: %+v", summary)
				}
				wantAccepted := int64(3)
				if c.err == audiooutput.ErrBackpressure {
					wantAccepted = 0
				}
				if summary.SinkAcceptedSamples != wantAccepted {
					t.Fatalf("accepted samples=%d want %d", summary.SinkAcceptedSamples, wantAccepted)
				}
			} else if summary.SinkAcceptedSamplesKnown || summary.SinkAcceptedSamples != 0 || summary.SinkUnknownAcceptanceCalls != 1 {
				t.Fatalf("unknown acceptance was represented as zero: %+v", summary)
			}
			var out bytes.Buffer
			if err := trace.write(&out); err != nil {
				t.Fatal(err)
			}
			var report struct {
				IO struct {
					Entries []map[string]any `json:"entries"`
				} `json:"io"`
			}
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			got, present := report.IO.Entries[0]["samples_known"]
			if !present || got != c.known {
				t.Fatalf("acceptance certainty: got %v present=%v want %v", got, present, c.known)
			}
		})
	}
}

// A blocked lifecycle call must remain visible without changing its context,
// result, or call count. PCM acceptance does not apply to lifecycle operations.
func TestVoiceIOLifecycleCallsRemainObservable(t *testing.T) {
	for _, operation := range []string{"source.stop", "source.close", "sink.drain", "sink.close"} {
		t.Run(operation, func(t *testing.T) {
			trace := &voiceOverlayTrace{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(release)
			wantErr := errors.New("private lifecycle failure must not be retained")
			calls := 0
			block := func(got context.Context) error {
				calls++
				if got != ctx {
					t.Error("changed lifecycle context")
				}
				close(entered)
				<-release
				return wantErr
			}
			source := &voiceLifecycleTraceSource{stop: block, close: func() error { return block(ctx) }}
			sink := &voiceLifecycleTraceSink{drain: block, close: func() error { return block(ctx) }}
			resources := trace.traceResources(bridge.SessionToken{}, audiosession.Resources{Source: source, Sink: sink})
			var invoke func() error
			switch operation {
			case "source.stop":
				invoke = func() error { return resources.Source.Stop(ctx) }
			case "source.close":
				invoke = resources.Source.Close
			case "sink.drain":
				invoke = func() error { return resources.Sink.Drain(ctx) }
			case "sink.close":
				invoke = resources.Sink.Close
			}
			go func() {
				defer close(done)
				if err := invoke(); err != wantErr {
					t.Error("changed lifecycle error")
				}
			}()
			waitHostSignal(t, entered)
			before := trace.io.snapshot()
			if len(before.Entries) != 1 || before.Entries[0].Operation != operation || !before.Entries[0].FinishedAt.IsZero() {
				t.Errorf("blocked lifecycle missing: %+v", before)
			}
			release <- struct{}{}
			waitHostSignal(t, done)
			after := trace.io.snapshot()
			if len(after.Entries) != 1 {
				t.Fatalf("expected one lifecycle span: %+v", after)
			}
			span := after.Entries[0]
			if calls != 1 || span.FinishedAt.IsZero() || span.DurationNS <= 0 || span.Result != "error" || span.SamplesKnown {
				t.Fatalf("incorrect lifecycle completion: calls=%d span=%+v", calls, span)
			}
			if len(before.Entries) == 1 && !before.Entries[0].FinishedAt.IsZero() {
				t.Fatal("completion mutated old snapshot")
			}
			var out bytes.Buffer
			if err := trace.write(&out); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(out.Bytes(), []byte(wantErr.Error())) {
				t.Fatal("raw private error was retained")
			}
		})
	}
}

type voiceLifecycleTraceSource struct {
	stop  func(context.Context) error
	close func() error
}

func (*voiceLifecycleTraceSource) Read(context.Context, []int16) (int, error) { return 0, io.EOF }
func (s *voiceLifecycleTraceSource) Stop(ctx context.Context) error           { return s.stop(ctx) }
func (s *voiceLifecycleTraceSource) Close() error                             { return s.close() }

type voiceLifecycleTraceSink struct {
	drain func(context.Context) error
	close func() error
}

func (*voiceLifecycleTraceSink) TryWrite([]int16) error            { return nil }
func (s *voiceLifecycleTraceSink) Drain(ctx context.Context) error { return s.drain(ctx) }
func (s *voiceLifecycleTraceSink) Close() error                    { return s.close() }

func TestVoiceIOReportNamesItsSharedEntryThreshold(t *testing.T) {
	trace := &voiceOverlayTrace{}
	// Lifecycle entries count toward the threshold too.
	call := trace.io.begin(bridge.SessionToken{}, "source.stop", 0)
	trace.io.end(call, 0, nil)
	var out bytes.Buffer
	if err := trace.write(&out); err != nil {
		t.Fatal(err)
	}
	var report struct {
		IO struct {
			Limit     int `json:"limit"`
			Threshold int `json:"pcm_entry_threshold"`
		} `json:"io"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.IO.Limit != 4096 || report.IO.Threshold != 4064 {
		t.Fatalf("misreported shared budget: %+v", report.IO)
	}
}
