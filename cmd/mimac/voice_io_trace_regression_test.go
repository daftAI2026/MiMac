package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

// This regression intentionally uses only the existing resource wrapper and
// JSON snapshot API. It can be copied onto the pre-summary source revision:
// that revision compiles, then fails because it has no independent summaries.
func TestVoiceIOSummaryJSONSurvivesDetailCapAcrossSessions(t *testing.T) {
	trace := &voiceOverlayTrace{}
	firstToken := voiceIOSummaryRegressionToken(t, 91)
	secondToken := voiceIOSummaryRegressionToken(t, 92)
	first := trace.traceResources(firstToken, audiosession.Resources{
		Source: voiceIOSummaryRegressionSource{samples: 1},
		Sink:   voiceIOSummaryRegressionSink{},
	})
	for range voiceIOTraceLimit - voiceIOTraceLifecycleReserve {
		n, err := first.Source.Read(context.Background(), make([]int16, 1))
		if n != 1 || err != nil {
			t.Fatalf("first-session source read: n=%d err=%v", n, err)
		}
	}
	second := trace.traceResources(secondToken, audiosession.Resources{
		Source: voiceIOSummaryRegressionSource{samples: 2},
		Sink:   voiceIOSummaryRegressionSink{},
	})
	for range 3 {
		n, err := second.Source.Read(context.Background(), make([]int16, 2))
		if n != 2 || err != nil {
			t.Fatalf("second-session source read: n=%d err=%v", n, err)
		}
	}
	for range 2 {
		if err := second.Sink.TryWrite([]int16{1, 2}); err != nil {
			t.Fatalf("second-session sink write: %v", err)
		}
	}

	var data bytes.Buffer
	if err := trace.write(&data); err != nil {
		t.Fatal(err)
	}
	var report struct {
		IO struct {
			Dropped                uint64 `json:"dropped"`
			SessionSummaryCoverage string `json:"session_summary_coverage"`
			Sessions               []struct {
				Session                  uint64 `json:"session"`
				Generation               uint64 `json:"generation"`
				SourceReadSamples        int64  `json:"source_read_samples"`
				SinkAcceptedSamples      int64  `json:"sink_accepted_samples"`
				SinkAcceptedSamplesKnown bool   `json:"sink_accepted_samples_known"`
			} `json:"sessions"`
		} `json:"io"`
	}
	if err := json.Unmarshal(data.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.IO.Dropped != 5 || report.IO.SessionSummaryCoverage != "complete" || len(report.IO.Sessions) != 2 {
		t.Fatalf("summary or detail coverage missing after cap: dropped=%d coverage=%q sessions=%d",
			report.IO.Dropped, report.IO.SessionSummaryCoverage, len(report.IO.Sessions))
	}
	firstSummary, secondSummary := report.IO.Sessions[0], report.IO.Sessions[1]
	if firstSummary.Session != firstToken.ID() || firstSummary.Generation != firstToken.Device().Generation() || firstSummary.SourceReadSamples != voiceIOTraceLimit-voiceIOTraceLifecycleReserve {
		t.Fatalf("first-session aggregate incorrect: %+v", firstSummary)
	}
	if secondSummary.Session != secondToken.ID() || secondSummary.Generation != secondToken.Device().Generation() ||
		secondSummary.SourceReadSamples != 6 || secondSummary.SinkAcceptedSamples != 4 || !secondSummary.SinkAcceptedSamplesKnown {
		t.Fatalf("second-session aggregate incorrect after detail cap: %+v", secondSummary)
	}
}

func voiceIOSummaryRegressionToken(t *testing.T, generation uint64) bridge.SessionToken {
	t.Helper()
	core, err := bridge.NewCore(bridge.NewAudioBinding())
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("voice-io-summary-regression", generation)
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

type voiceIOSummaryRegressionSource struct{ samples int }

func (s voiceIOSummaryRegressionSource) Read(_ context.Context, pcm []int16) (int, error) {
	for i := 0; i < s.samples && i < len(pcm); i++ {
		pcm[i] = int16(i + 1)
	}
	return s.samples, nil
}
func (voiceIOSummaryRegressionSource) Stop(context.Context) error { return nil }
func (voiceIOSummaryRegressionSource) Close() error               { return nil }

type voiceIOSummaryRegressionSink struct{}

func (voiceIOSummaryRegressionSink) TryWrite([]int16) error      { return nil }
func (voiceIOSummaryRegressionSink) Drain(context.Context) error { return nil }
func (voiceIOSummaryRegressionSink) Close() error                { return nil }
