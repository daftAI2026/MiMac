package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

const (
	voiceIOTraceLimit = 4096
	// Preserve terminal boundaries after a long recording exhausts PCM spans.
	// This is shared headroom, not a per-session guarantee. All spans still
	// share the same fixed total budget.
	voiceIOTraceLifecycleReserve = 32
	// Session summaries have their own bounded budget and are not reconstructed
	// from the detail spans. Overflow is surfaced in every snapshot.
	voiceIOTraceSessionLimit = 64
)

// Independent from the UI trace: audio workers never touch UI entries. These
// spans describe host I/O calls, not capture timestamps or receiver/GPU ACKs.
// No lock crosses an I/O call. A blocked call keeps its unfinished span.
type voiceIOTrace struct {
	mu                       sync.Mutex
	entries                  []voiceIOSpan
	sessions                 []voiceIOSessionSummary
	overflow                 voiceIOOverflowSummary
	dropped                  uint64
	nextCall                 uint64
	sessionOverflowCalls     uint64
	sessionOverflowStarted   uint64
	sessionOverflowCompleted uint64
	sessionOverflowInFlight  uint64
}

type voiceIOSpan struct {
	Call             uint64    `json:"call"`
	Operation        string    `json:"operation"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at,omitzero"`
	DurationNS       int64     `json:"duration_ns"`
	Session          uint64    `json:"session"`
	Generation       uint64    `json:"generation"`
	RequestedSamples int       `json:"requested_samples"`
	Samples          int       `json:"samples"`
	SamplesKnown     bool      `json:"samples_known"`
	Result           string    `json:"result"`
}

type voiceIOSnapshot struct {
	Limit                    int                     `json:"limit"`
	PCMEntryThreshold        int                     `json:"pcm_entry_threshold"`
	Dropped                  uint64                  `json:"dropped"`
	SessionLimit             int                     `json:"session_limit"`
	SessionOverflowCalls     uint64                  `json:"session_overflow_calls"`
	SessionOverflowStarted   uint64                  `json:"session_overflow_started"`
	SessionOverflowCompleted uint64                  `json:"session_overflow_completed"`
	SessionOverflowInFlight  uint64                  `json:"session_overflow_inflight"`
	SessionSummaryCoverage   string                  `json:"session_summary_coverage"`
	OverflowSummary          voiceIOOverflowSummary  `json:"overflow_summary"`
	SnapshotAt               time.Time               `json:"snapshot_at"`
	Sessions                 []voiceIOSessionSummary `json:"sessions"`
	Entries                  []voiceIOSpan           `json:"entries"`
}

// A fixed, JSON-safe aggregate. Operation counters are maintained on every
// call, even when the detailed span budget is exhausted. No payload or raw
// error is retained.
type voiceIOAggregateSummary struct {
	CallsStarted               uint64 `json:"calls_started"`
	CallsCompleted             uint64 `json:"calls_completed"`
	CallsInFlight              uint64 `json:"calls_inflight"`
	SourceReadSamples          int64  `json:"source_read_samples"`
	SourceReadSamplesComplete  bool   `json:"source_read_samples_complete"`
	SinkAcceptedSamples        int64  `json:"sink_accepted_samples"`
	SinkAcceptedSamplesKnown   bool   `json:"sink_accepted_samples_known"`
	SinkUnknownAcceptanceCalls uint64 `json:"sink_unknown_acceptance_calls"`
	SinkBackpressureCalls      uint64 `json:"sink_backpressure_calls"`
	// Duration is the sum/max of individual ErrBackpressure TryWrite calls;
	// it excludes retry sleeps and time between calls.
	SinkBackpressureDurationNS    int64 `json:"sink_backpressure_call_duration_ns"`
	SinkBackpressureMaxDurationNS int64 `json:"sink_backpressure_call_max_duration_ns"`
	// ErrorCalls counts non-EOF/non-backpressure unsuccessful calls, including
	// cancellation and deadline results.
	ErrorCalls      uint64                  `json:"error_calls"`
	FirstStartedAt  time.Time               `json:"first_started_at,omitzero"`
	LastStartedAt   time.Time               `json:"last_started_at,omitzero"`
	FirstFinishedAt time.Time               `json:"first_finished_at,omitzero"`
	LastFinishedAt  time.Time               `json:"last_finished_at,omitzero"`
	MaxDurationNS   int64                   `json:"max_duration_ns"`
	SourceRead      voiceIOOperationSummary `json:"source_read"`
	SinkTryWrite    voiceIOOperationSummary `json:"sink_try_write"`
	SourceStop      voiceIOOperationSummary `json:"source_stop"`
	SourceClose     voiceIOOperationSummary `json:"source_close"`
	SinkDrain       voiceIOOperationSummary `json:"sink_drain"`
	SinkClose       voiceIOOperationSummary `json:"sink_close"`
}

type voiceIOSessionSummary struct {
	Session    uint64 `json:"session"`
	Generation uint64 `json:"generation"`
	FirstCall  uint64 `json:"first_call"`
	LastCall   uint64 `json:"last_call"`
	voiceIOAggregateSummary
	DetailSpansRecorded uint64 `json:"detail_spans_recorded"`
	DetailSpansDropped  uint64 `json:"detail_spans_dropped"`
	FirstDroppedCall    uint64 `json:"first_dropped_call,omitempty"`
	LastDroppedCall     uint64 `json:"last_dropped_call,omitempty"`
	DetailCoverage      string `json:"detail_coverage"`
}

// Calls from sessions beyond the per-session table limit stay measurable as
// one explicitly mixed aggregate, but cannot be reconciled by session.
type voiceIOOverflowSummary struct {
	Scope     string `json:"scope"`
	FirstCall uint64 `json:"first_call"`
	LastCall  uint64 `json:"last_call"`
	voiceIOAggregateSummary
	DetailSpansRecorded uint64 `json:"detail_spans_recorded"`
	DetailSpansDropped  uint64 `json:"detail_spans_dropped"`
	FirstDroppedCall    uint64 `json:"first_dropped_call,omitempty"`
	LastDroppedCall     uint64 `json:"last_dropped_call,omitempty"`
	DetailCoverage      string `json:"detail_coverage"`
}

type voiceIOOperationSummary struct {
	CallsStarted    uint64    `json:"calls_started"`
	CallsCompleted  uint64    `json:"calls_completed"`
	CallsInFlight   uint64    `json:"calls_inflight"`
	OK              uint64    `json:"ok"`
	EOF             uint64    `json:"eof"`
	Cancelled       uint64    `json:"cancelled"`
	Deadline        uint64    `json:"deadline"`
	Backpressure    uint64    `json:"backpressure"`
	Error           uint64    `json:"error"`
	FirstStartedAt  time.Time `json:"first_started_at,omitzero"`
	LastStartedAt   time.Time `json:"last_started_at,omitzero"`
	FirstFinishedAt time.Time `json:"first_finished_at,omitzero"`
	LastFinishedAt  time.Time `json:"last_finished_at,omitzero"`
	MaxDurationNS   int64     `json:"max_duration_ns"`
}

type voiceIOCall struct {
	entryIndex   int
	sessionIndex int
	operationID  int
	startedAt    time.Time
}

const (
	voiceIOOpSourceRead = iota
	voiceIOOpSinkTryWrite
	voiceIOOpSourceStop
	voiceIOOpSourceClose
	voiceIOOpSinkDrain
	voiceIOOpSinkClose
)

func (t *voiceIOTrace) prepare() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prepareLocked()
}

func (t *voiceIOTrace) prepareLocked() {
	if t.entries == nil {
		t.entries = make([]voiceIOSpan, 0, voiceIOTraceLimit)
	}
	if t.sessions == nil {
		t.sessions = make([]voiceIOSessionSummary, 0, voiceIOTraceSessionLimit)
	}
}

func voiceIOOperationID(operation string) int {
	switch operation {
	case "source.read":
		return voiceIOOpSourceRead
	case "sink.try_write":
		return voiceIOOpSinkTryWrite
	case "source.stop":
		return voiceIOOpSourceStop
	case "source.close":
		return voiceIOOpSourceClose
	case "sink.drain":
		return voiceIOOpSinkDrain
	case "sink.close":
		return voiceIOOpSinkClose
	default:
		return -1
	}
}

func (s *voiceIOAggregateSummary) operation(id int) *voiceIOOperationSummary {
	switch id {
	case voiceIOOpSourceRead:
		return &s.SourceRead
	case voiceIOOpSinkTryWrite:
		return &s.SinkTryWrite
	case voiceIOOpSourceStop:
		return &s.SourceStop
	case voiceIOOpSourceClose:
		return &s.SourceClose
	case voiceIOOpSinkDrain:
		return &s.SinkDrain
	case voiceIOOpSinkClose:
		return &s.SinkClose
	default:
		return nil
	}
}

func (s *voiceIOAggregateSummary) begin(operationID int, at time.Time) {
	if s.CallsStarted == 0 {
		s.FirstStartedAt = at
	}
	s.LastStartedAt = at
	s.CallsStarted++
	s.CallsInFlight++
	if stats := s.operation(operationID); stats != nil {
		if stats.CallsStarted == 0 {
			stats.FirstStartedAt = at
		}
		stats.LastStartedAt = at
		stats.CallsStarted++
		stats.CallsInFlight++
	}
}

func (s *voiceIOAggregateSummary) end(operationID int, samples int, err error, result string, duration int64, at time.Time) {
	s.CallsCompleted++
	if s.CallsInFlight > 0 {
		s.CallsInFlight--
	}
	if s.FirstFinishedAt.IsZero() {
		s.FirstFinishedAt = at
	}
	s.LastFinishedAt = at
	if duration > s.MaxDurationNS {
		s.MaxDurationNS = duration
	}
	if stats := s.operation(operationID); stats != nil {
		stats.CallsCompleted++
		if stats.CallsInFlight > 0 {
			stats.CallsInFlight--
		}
		if stats.FirstFinishedAt.IsZero() {
			stats.FirstFinishedAt = at
		}
		stats.LastFinishedAt = at
		if duration > stats.MaxDurationNS {
			stats.MaxDurationNS = duration
		}
		switch result {
		case "ok":
			stats.OK++
		case "eof":
			stats.EOF++
		case "cancelled":
			stats.Cancelled++
		case "deadline":
			stats.Deadline++
		case "backpressure":
			stats.Backpressure++
		case "error":
			stats.Error++
		}
	}
	if result == "error" || result == "cancelled" || result == "deadline" {
		s.ErrorCalls++
	}
	switch operationID {
	case voiceIOOpSourceRead:
		if samples > 0 {
			s.SourceReadSamples += int64(samples)
		}
	case voiceIOOpSinkTryWrite:
		switch result {
		case "ok":
			if samples > 0 {
				s.SinkAcceptedSamples += int64(samples)
			}
		case "backpressure":
			s.SinkBackpressureCalls++
			s.SinkBackpressureDurationNS += duration
			if duration > s.SinkBackpressureMaxDurationNS {
				s.SinkBackpressureMaxDurationNS = duration
			}
		default:
			if err != nil {
				s.SinkUnknownAcceptanceCalls++
				s.SinkAcceptedSamplesKnown = false
			}
		}
	}
}

func (t *voiceIOTrace) sessionIndexLocked(token bridge.SessionToken) int {
	session, generation := token.ID(), token.Device().Generation()
	for i := range t.sessions {
		if t.sessions[i].Session == session && t.sessions[i].Generation == generation {
			return i
		}
	}
	if len(t.sessions) >= voiceIOTraceSessionLimit {
		return -1
	}
	t.sessions = append(t.sessions, voiceIOSessionSummary{
		Session: session, Generation: generation,
		voiceIOAggregateSummary: voiceIOAggregateSummary{SinkAcceptedSamplesKnown: true},
	})
	return len(t.sessions) - 1
}

func (t *voiceIOTrace) begin(token bridge.SessionToken, operation string, samples int) voiceIOCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prepareLocked()
	t.nextCall++
	callNumber := t.nextCall
	startedAt := time.Now()
	sessionIndex := t.sessionIndexLocked(token)
	operationID := voiceIOOperationID(operation)
	var aggregate *voiceIOAggregateSummary
	if sessionIndex >= 0 {
		summary := &t.sessions[sessionIndex]
		if summary.CallsStarted == 0 {
			summary.FirstCall = callNumber
		}
		summary.LastCall = callNumber
		aggregate = &summary.voiceIOAggregateSummary
	} else {
		// This is a call-level overflow counter, not a claimed count of unique
		// session IDs: once full, deduplicating arbitrary additional IDs would
		// require unbounded state.
		t.sessionOverflowCalls++
		t.sessionOverflowStarted++
		t.sessionOverflowInFlight++
		if t.overflow.CallsStarted == 0 {
			t.overflow.FirstCall = callNumber
			t.overflow.SinkAcceptedSamplesKnown = true
		}
		t.overflow.Scope = "mixed_untracked_sessions"
		t.overflow.LastCall = callNumber
		aggregate = &t.overflow.voiceIOAggregateSummary
	}
	aggregate.begin(operationID, startedAt)
	limit := voiceIOTraceLimit
	if operation == "source.read" || operation == "sink.try_write" {
		limit -= voiceIOTraceLifecycleReserve
	}
	if len(t.entries) >= limit {
		t.dropped++
		if sessionIndex >= 0 {
			summary := &t.sessions[sessionIndex]
			summary.DetailSpansDropped++
			if summary.FirstDroppedCall == 0 {
				summary.FirstDroppedCall = callNumber
			}
			summary.LastDroppedCall = callNumber
		} else {
			t.overflow.DetailSpansDropped++
			if t.overflow.FirstDroppedCall == 0 {
				t.overflow.FirstDroppedCall = callNumber
			}
			t.overflow.LastDroppedCall = callNumber
		}
		return voiceIOCall{entryIndex: -1, sessionIndex: sessionIndex,
			operationID: operationID, startedAt: startedAt}
	}
	index := len(t.entries)
	t.entries = append(t.entries, voiceIOSpan{Call: callNumber, Operation: operation, StartedAt: startedAt, Session: token.ID(), Generation: token.Device().Generation(), RequestedSamples: samples})
	if sessionIndex >= 0 {
		t.sessions[sessionIndex].DetailSpansRecorded++
	} else {
		t.overflow.DetailSpansRecorded++
	}
	return voiceIOCall{entryIndex: index, sessionIndex: sessionIndex,
		operationID: operationID, startedAt: startedAt}
}

func (t *voiceIOTrace) end(call voiceIOCall, samples int, err error) {
	at := time.Now()
	result := voiceIOResult(err)
	duration := at.Sub(call.startedAt).Nanoseconds()
	if duration < 0 {
		duration = 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if call.entryIndex >= 0 {
		span := &t.entries[call.entryIndex]
		span.FinishedAt = at
		span.DurationNS = duration
		span.Samples = samples
		span.SamplesKnown = span.Operation == "source.read" ||
			(span.Operation == "sink.try_write" && (result == "ok" || result == "backpressure"))
		span.Result = result
	}
	var aggregate *voiceIOAggregateSummary
	if call.sessionIndex < 0 {
		t.sessionOverflowCompleted++
		if t.sessionOverflowInFlight > 0 {
			t.sessionOverflowInFlight--
		}
		aggregate = &t.overflow.voiceIOAggregateSummary
	} else {
		aggregate = &t.sessions[call.sessionIndex].voiceIOAggregateSummary
	}
	aggregate.end(call.operationID, samples, err, result, duration, at)
}

func voiceIOResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, audiooutput.ErrBackpressure):
		return "backpressure"
	default:
		return "error"
	}
}

func (t *voiceIOTrace) snapshot() voiceIOSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	sessions := append([]voiceIOSessionSummary{}, t.sessions...)
	for i := range sessions {
		sessions[i].SourceReadSamplesComplete = sessions[i].SourceRead.CallsInFlight == 0
		sessions[i].SinkAcceptedSamplesKnown = sessions[i].SinkAcceptedSamplesKnown && sessions[i].SinkTryWrite.CallsInFlight == 0
		if sessions[i].DetailSpansDropped == 0 {
			sessions[i].DetailCoverage = "complete"
		} else {
			sessions[i].DetailCoverage = "truncated"
		}
	}
	overflow := t.overflow
	if t.sessionOverflowCalls == 0 {
		overflow.Scope = "none"
		overflow.SinkAcceptedSamplesKnown = true
	}
	if overflow.DetailSpansDropped == 0 {
		overflow.DetailCoverage = "complete"
	} else {
		overflow.DetailCoverage = "truncated"
	}
	overflow.SourceReadSamplesComplete = overflow.SourceRead.CallsInFlight == 0
	overflow.SinkAcceptedSamplesKnown = overflow.SinkAcceptedSamplesKnown && overflow.SinkTryWrite.CallsInFlight == 0
	coverage := "complete"
	if t.sessionOverflowCalls > 0 {
		coverage = "overflow"
	}
	return voiceIOSnapshot{Limit: voiceIOTraceLimit, PCMEntryThreshold: voiceIOTraceLimit - voiceIOTraceLifecycleReserve,
		Dropped: t.dropped, SessionLimit: voiceIOTraceSessionLimit, SessionOverflowCalls: t.sessionOverflowCalls,
		SessionOverflowStarted: t.sessionOverflowStarted, SessionOverflowCompleted: t.sessionOverflowCompleted,
		SessionOverflowInFlight: t.sessionOverflowInFlight, SessionSummaryCoverage: coverage, OverflowSummary: overflow,
		SnapshotAt: time.Now(), Sessions: sessions, Entries: append([]voiceIOSpan{}, t.entries...)}
}

type voiceIOCallTrace struct {
	trace *voiceIOTrace
	token bridge.SessionToken
}

type voiceTracedSource struct {
	audiosession.Source
	calls voiceIOCallTrace
}

func (s *voiceTracedSource) Read(ctx context.Context, pcm []int16) (int, error) {
	call := s.calls.trace.begin(s.calls.token, "source.read", len(pcm))
	n, err := s.Source.Read(ctx, pcm)
	s.calls.trace.end(call, n, err)
	return n, err
}

func (s *voiceTracedSource) Stop(ctx context.Context) error {
	call := s.calls.trace.begin(s.calls.token, "source.stop", 0)
	err := s.Source.Stop(ctx)
	s.calls.trace.end(call, 0, err)
	return err
}

func (s *voiceTracedSource) Close() error {
	call := s.calls.trace.begin(s.calls.token, "source.close", 0)
	err := s.Source.Close()
	s.calls.trace.end(call, 0, err)
	return err
}

type voiceTracedSink struct {
	audiosession.Sink
	calls voiceIOCallTrace
}

func (s *voiceTracedSink) TryWrite(pcm []int16) error {
	call := s.calls.trace.begin(s.calls.token, "sink.try_write", len(pcm))
	err := s.Sink.TryWrite(pcm)
	// Success accepts the block; backpressure accepts none. Other failures
	// may follow a partial enqueue, so their acceptance count stays unknown.
	// None of these counts acknowledges consumption by the target recorder.
	n := 0
	if err == nil {
		n = len(pcm)
	}
	s.calls.trace.end(call, n, err)
	return err
}

func (s *voiceTracedSink) Drain(ctx context.Context) error {
	call := s.calls.trace.begin(s.calls.token, "sink.drain", 0)
	err := s.Sink.Drain(ctx)
	s.calls.trace.end(call, 0, err)
	return err
}

func (s *voiceTracedSink) Close() error {
	call := s.calls.trace.begin(s.calls.token, "sink.close", 0)
	err := s.Sink.Close()
	s.calls.trace.end(call, 0, err)
	return err
}

type voiceSourceHealth interface{ Err() error }

// Preserve the exact optional method set Runtime uses. A generic wrapper that
// always implements StartSession/Transcript would invent adapter capabilities.
func (t *voiceOverlayTrace) traceResources(token bridge.SessionToken, r audiosession.Resources) audiosession.Resources {
	if t == nil {
		return r
	}
	t.io.prepare()
	calls := voiceIOCallTrace{trace: &t.io, token: token}
	if r.Source != nil {
		wrapped := &voiceTracedSource{Source: r.Source, calls: calls}
		if health, ok := r.Source.(voiceSourceHealth); ok {
			r.Source = struct {
				audiosession.Source
				voiceSourceHealth
			}{wrapped, health}
		} else {
			r.Source = wrapped
		}
	}
	if r.Sink != nil {
		wrapped := &voiceTracedSink{Sink: r.Sink, calls: calls}
		session, hasSession := r.Sink.(audiosession.SessionSink)
		transcript, hasTranscript := r.Sink.(audiosession.TranscriptSink)
		switch {
		case hasSession && hasTranscript:
			r.Sink = struct {
				audiosession.Sink
				audiosession.SessionSink
				audiosession.TranscriptSink
			}{wrapped, session, transcript}
		case hasSession:
			r.Sink = struct {
				audiosession.Sink
				audiosession.SessionSink
			}{wrapped, session}
		case hasTranscript:
			r.Sink = struct {
				audiosession.Sink
				audiosession.TranscriptSink
			}{wrapped, transcript}
		default:
			r.Sink = wrapped
		}
	}
	if r.WaitReceiver != nil {
		waitReceiver := r.WaitReceiver
		r.WaitReceiver = func(ctx context.Context) error {
			call := calls.trace.begin(token, "receiver.wait", 0)
			err := waitReceiver(ctx)
			calls.trace.end(call, 0, err)
			return err
		}
	}
	return r
}
