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

const voiceIOTraceLimit = 4096

// Independent from the UI trace: audio workers never touch UI entries. These
// spans describe host I/O calls, not capture timestamps or receiver/GPU ACKs.
// No lock crosses an I/O call. A blocked call keeps its unfinished span.
type voiceIOTrace struct {
	mu       sync.Mutex
	entries  []voiceIOSpan
	dropped  uint64
	nextCall uint64
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
	Limit      int           `json:"limit"`
	Dropped    uint64        `json:"dropped"`
	SnapshotAt time.Time     `json:"snapshot_at"`
	Entries    []voiceIOSpan `json:"entries"`
}

func (t *voiceIOTrace) prepare() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = make([]voiceIOSpan, 0, voiceIOTraceLimit)
	}
}

func (t *voiceIOTrace) begin(token bridge.SessionToken, operation string, samples int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextCall++
	if len(t.entries) >= voiceIOTraceLimit {
		t.dropped++
		return -1
	}
	index := len(t.entries)
	t.entries = append(t.entries, voiceIOSpan{Call: t.nextCall, Operation: operation, StartedAt: time.Now(), Session: token.ID(), Generation: token.Device().Generation(), RequestedSamples: samples})
	return index
}

func (t *voiceIOTrace) end(index int, samples int, err error) {
	if index < 0 {
		return
	}
	at := time.Now()
	result := voiceIOResult(err)
	t.mu.Lock()
	defer t.mu.Unlock()
	span := &t.entries[index]
	span.FinishedAt = at
	span.DurationNS = at.Sub(span.StartedAt).Nanoseconds()
	span.Samples = samples
	span.SamplesKnown = span.Operation == "source.read" || result == "ok" || result == "backpressure"
	span.Result = result
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
	return voiceIOSnapshot{Limit: voiceIOTraceLimit, Dropped: t.dropped, SnapshotAt: time.Now(), Entries: append([]voiceIOSpan{}, t.entries...)}
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
	index := s.calls.trace.begin(s.calls.token, "source.read", len(pcm))
	n, err := s.Source.Read(ctx, pcm)
	s.calls.trace.end(index, n, err)
	return n, err
}

type voiceTracedSink struct {
	audiosession.Sink
	calls voiceIOCallTrace
}

func (s *voiceTracedSink) TryWrite(pcm []int16) error {
	index := s.calls.trace.begin(s.calls.token, "sink.try_write", len(pcm))
	err := s.Sink.TryWrite(pcm)
	// Success accepts the block; backpressure accepts none. Other failures
	// may follow a partial enqueue, so their acceptance count stays unknown.
	// None of these counts acknowledges consumption by the target recorder.
	n := 0
	if err == nil {
		n = len(pcm)
	}
	s.calls.trace.end(index, n, err)
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
	return r
}
