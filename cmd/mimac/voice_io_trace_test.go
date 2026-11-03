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

func TestVoiceIOBufferBoundAndLateCompletion(t *testing.T) {
	var trace voiceIOTrace
	trace.prepare()
	first := trace.begin(bridge.SessionToken{}, "source.stop", 0)
	for range voiceIOTraceLimit - 1 {
		i := trace.begin(bridge.SessionToken{}, "sink.drain", 0)
		trace.end(i, 0, nil)
	}
	for range 3 {
		i := trace.begin(bridge.SessionToken{}, "source.close", 0)
		trace.end(i, 0, nil)
	}
	before := trace.snapshot()
	if len(before.Entries) != voiceIOTraceLimit || before.Dropped != 3 || !before.Entries[first].FinishedAt.IsZero() {
		t.Fatal("capacity/drop or unfinished span incorrect")
	}
	trace.end(first, 0, nil)
	after := trace.snapshot()
	if after.Entries[first].FinishedAt.IsZero() || after.Entries[first].SamplesKnown || after.Entries[first].Samples != 0 || after.Entries[first].Result != "ok" || after.Dropped != 3 || !before.Entries[first].FinishedAt.IsZero() {
		t.Fatal("full buffer lost late completion or aliased snapshot")
	}
	if n := testing.AllocsPerRun(100, func() { i := trace.begin(bridge.SessionToken{}, "source.read", 1); trace.end(i, 1, nil) }); n != 0 {
		t.Fatalf("full buffer allocates: %v", n)
	}
	var steady voiceIOTrace
	steady.prepare()
	if n := testing.AllocsPerRun(100, func() { i := steady.begin(bridge.SessionToken{}, "sink.try_write", 1); steady.end(i, 1, nil) }); n != 0 {
		t.Fatalf("span collection allocates: %v", n)
	}
}

func TestVoiceIOTerminalSpansSurvivePCMBudget(t *testing.T) {
	trace := &voiceOverlayTrace{}
	trace.io.prepare()
	for range voiceIOTraceLimit {
		i := trace.io.begin(bridge.SessionToken{}, "source.read", 320)
		trace.io.end(i, 320, nil)
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
			i := trace.begin(bridge.SessionToken{}, "source.read", 320)
			trace.end(i, 0, context.Canceled)
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
	i := trace.io.begin(bridge.SessionToken{}, "source.stop", 0)
	trace.io.end(i, 0, nil)
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
