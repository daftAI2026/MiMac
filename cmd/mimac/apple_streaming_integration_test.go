package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"mimac.local/mimac/internal/adapters/applespeech"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

// TestNativeAppleRoutePartialCaptionAndFinalTargetCommit is an opt-in
// integration test for one fixed synthetic PCM phrase. It uses the public
// Speech bridge only; it opens no microphone and never logs transcript text.
func TestNativeAppleRoutePartialCaptionAndFinalTargetCommit(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native Apple Speech route integration requires macOS")
	}
	libraryPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_LIBRARY"))
	pcmPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_PCM"))
	if libraryPath == "" || pcmPath == "" {
		t.Skip("set MIMAC_SPEECH_LIBRARY and MIMAC_SPEECH_PCM to opt into native route integration")
	}
	expected := os.Getenv("MIMAC_SPEECH_EXPECTED")
	if expected == "" {
		expected = "苹果语音识别测试第一句话结束第二句话继续测试自动标点"
	}
	pcmBytes, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatalf("read fixed synthetic PCM fixture: %v", err)
	}
	if len(pcmBytes) == 0 || len(pcmBytes)%2 != 0 {
		t.Fatalf("PCM fixture must contain a non-empty whole number of s16le samples; got %d bytes", len(pcmBytes))
	}
	samples := make([]int16, len(pcmBytes)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcmBytes[index*2 : index*2+2]))
	}

	const generation = 1
	token := xiaomiRouteSessionToken(t, generation)
	var feed voiceInterimFeed
	feed.SetEnabled(true)
	feed.Begin(token)
	target := &transcriptTargetTestStub{}
	var partialCount, stableCount, tentativeCount, staleTokenCount atomic.Int32
	var submittedSamples atomic.Int64
	var firstPartial time.Time
	var firstPartialSubmitted int64
	var firstPartialMu sync.Mutex
	partialEnabled := func() bool { return feed.PartialsEnabled() }
	openSpeech := func(ctx context.Context, enabled bool) (localRecognizer, error) {
		sink, err := applespeech.Open(ctx, applespeech.Config{
			LibraryPath: libraryPath, Locale: "zh-CN", SampleRate: applespeech.RequiredSampleRate,
			EnablePartials: enabled, InstallAssets: false,
		})
		if sink == nil {
			return nil, err
		}
		return sink, err
	}
	partialPublisher := func(actualToken bridge.SessionToken, stable, tentative string) bool {
		if actualToken != token {
			staleTokenCount.Add(1)
			return false
		}
		if stable != "" {
			stableCount.Add(1)
		}
		if tentative != "" {
			tentativeCount.Add(1)
		}
		if !feed.Publish(actualToken, stable, tentative) {
			return false
		}
		if draft := feed.Snapshot(token); draft.Stable != "" || draft.Tentative != "" {
			partialCount.Add(1)
			firstPartialMu.Lock()
			if firstPartial.IsZero() {
				firstPartial = time.Now()
				firstPartialSubmitted = submittedSamples.Load()
			}
			firstPartialMu.Unlock()
		}
		return true
	}
	route, err := newAppleAudioRoute(generation,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) {
			return &idleAppleRouteSource{stopped: make(chan struct{})}, nil
		},
		func() error { return nil },
		func(ctx context.Context) (localRecognizer, error) { return openSpeech(ctx, false) },
		func() (transcriptTarget, error) { return target, nil },
		appleRoutePartialConfig{
			onPartial:        partialPublisher,
			enabled:          partialEnabled,
			openWithPartials: openSpeech,
		},
	)
	if err != nil {
		t.Fatalf("create Apple route: %v", err)
	}
	var resources audiosession.Resources
	t.Cleanup(func() {
		feed.End(token)
		if resources.Source != nil {
			_ = resources.Source.Close()
		}
		if resources.Sink != nil {
			_ = resources.Sink.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := route.Close(ctx); err != nil {
			t.Errorf("close native Apple route: %v", err)
		}
	})

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := route.Start(); err != nil {
		t.Fatalf("start Apple route: %v", err)
	}
	waitXiaomiRoute(t, route.Ready)
	if err := route.BeforeStart(context.Background()); err != nil {
		t.Fatalf("capture test target: %v", err)
	}
	resources, err = route.Prepare(context.Background(), token)
	if err != nil {
		t.Fatalf("prepare Apple route: %v", err)
	}
	sessionSink, ok := resources.Sink.(audiosession.SessionSink)
	if !ok {
		t.Fatalf("Apple route consumer %T does not defer its recognition session", resources.Sink)
	}
	openStarted := time.Now()
	if err := sessionSink.StartSession(ctx); err != nil {
		t.Fatalf("prepare Apple Speech recognizer without asset installation: %v", err)
	}
	openDuration := time.Since(openStarted)
	writeStarted := time.Now()
	if err := writeSyntheticToAudioSinkRealtime(ctx, resources.Sink, samples, &submittedSamples); err != nil {
		t.Fatalf("write synthetic PCM to the Apple route: %v", err)
	}
	writeDuration := time.Since(writeStarted)
	drainStarted := time.Now()
	if err := resources.Sink.Drain(ctx); err != nil {
		t.Fatalf("drain Apple Speech route: %v", err)
	}
	drainDuration := time.Since(drainStarted)
	transcriptSink, ok := resources.Sink.(audiosession.TranscriptSink)
	if !ok {
		t.Fatalf("Apple route consumer %T has no final transcript contract", resources.Sink)
	}
	finalText, available, err := transcriptSink.Transcript()
	if err != nil || !available {
		t.Fatalf("final transcript available=%t err=%v", available, err)
	}
	if normalizeAppleIntegrationText(finalText) != normalizeAppleIntegrationText(expected) {
		t.Fatalf("final transcript did not match fixed synthetic phrase (got %d UTF-8 bytes, expected %d)", len(finalText), len(expected))
	}
	firstPartialMu.Lock()
	partialAt := firstPartial
	partialSubmitted := firstPartialSubmitted
	firstPartialMu.Unlock()
	if partialCount.Load() == 0 || partialAt.IsZero() || !partialAt.Before(drainStarted) {
		t.Fatal("the same Core session had no non-empty caption before the final source block and drain")
	}
	unsentSamples := int64(len(samples)) - partialSubmitted
	if unsentSamples <= 0 {
		t.Fatal("first caption arrived only after all synthetic PCM had been submitted")
	}
	if staleTokenCount.Load() != 0 {
		t.Fatalf("partial callbacks with a different Core session token: %d", staleTokenCount.Load())
	}
	if got := target.inserted(); len(got) != 0 {
		t.Fatalf("partial result was committed before final delivery: %d target writes", len(got))
	}
	if err := route.DeliverTranscript(token, finalText); err != nil {
		t.Fatalf("deliver the final result once: %v", err)
	}
	if got := target.inserted(); len(got) != 1 || got[0] != finalText {
		t.Fatalf("target final commits do not match one final result: count=%d bytes=%d", len(got), len(finalText))
	}
	t.Logf("native Apple route pid=%d started=%s fixture_samples=%d input_pacing=realtime_100ms_blocks installAssets=false open_ms=%d write_ms=%d drain_ms=%d partial_updates=%d stable_updates=%d tentative_updates=%d first_partial_ms=%d first_partial_unsent_samples=%d first_partial_unsent_ms=%d partial_before_last_block=true final_utf8_bytes=%d expected_phrase_match=true same_token=true target_commit_count=1 target_matches_final=true total_ms=%d",
		os.Getpid(), started.Format(time.RFC3339Nano), len(samples), openDuration.Milliseconds(), writeDuration.Milliseconds(), drainDuration.Milliseconds(), partialCount.Load(), stableCount.Load(), tentativeCount.Load(), partialAt.Sub(started).Milliseconds(), unsentSamples, (unsentSamples*1000)/applespeech.RequiredSampleRate, len(finalText), time.Since(started).Milliseconds())
}

func writeSyntheticToAudioSinkRealtime(ctx context.Context, sink audiosession.Sink, samples []int16, submitted *atomic.Int64) error {
	const samplesPerBlock = applespeech.RequiredSampleRate / 10
	deadline := time.Now()
	for offset := 0; offset < len(samples); {
		end := min(offset+samplesPerBlock, len(samples))
		block := samples[offset:end]
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := sink.TryWrite(block)
			if err == nil {
				break
			}
			if !errors.Is(err, applespeech.ErrQueueFull) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		offset = end
		if submitted != nil {
			submitted.Store(int64(offset))
		}
		deadline = deadline.Add(time.Duration(len(block)) * time.Second / applespeech.RequiredSampleRate)
		if offset < len(samples) {
			if wait := time.Until(deadline); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	return nil
}

// This counterpart verifies the normal final-only Apple route when the
// caption preference is off. The recognizer must still complete the phrase.
func TestNativeAppleRouteFinalOnlyWithoutCaptionSubscriber(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native Apple Speech route integration requires macOS")
	}
	libraryPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_LIBRARY"))
	pcmPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_PCM"))
	if libraryPath == "" || pcmPath == "" {
		t.Skip("set MIMAC_SPEECH_LIBRARY and MIMAC_SPEECH_PCM to opt into native route integration")
	}
	expected := os.Getenv("MIMAC_SPEECH_EXPECTED")
	if expected == "" {
		expected = "苹果语音识别测试第一句话结束第二句话继续测试自动标点"
	}
	pcmBytes, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatalf("read fixed synthetic PCM fixture: %v", err)
	}
	if len(pcmBytes) == 0 || len(pcmBytes)%2 != 0 {
		t.Fatalf("PCM fixture must contain a non-empty whole number of s16le samples; got %d bytes", len(pcmBytes))
	}
	samples := make([]int16, len(pcmBytes)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcmBytes[index*2 : index*2+2]))
	}

	const generation = 2
	token := xiaomiRouteSessionToken(t, generation)
	target := &transcriptTargetTestStub{}
	var partialCallbacks atomic.Int32
	openSpeech := func(ctx context.Context, enabled bool) (localRecognizer, error) {
		sink, err := applespeech.Open(ctx, applespeech.Config{
			LibraryPath: libraryPath, Locale: "zh-CN", SampleRate: applespeech.RequiredSampleRate,
			EnablePartials: enabled, InstallAssets: false,
		})
		if sink == nil {
			return nil, err
		}
		return sink, err
	}
	route, err := newAppleAudioRoute(generation,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) {
			return &idleAppleRouteSource{stopped: make(chan struct{})}, nil
		},
		func() error { return nil },
		func(ctx context.Context) (localRecognizer, error) { return openSpeech(ctx, false) },
		func() (transcriptTarget, error) { return target, nil },
		appleRoutePartialConfig{
			onPartial: func(bridge.SessionToken, string, string) bool { partialCallbacks.Add(1); return true },
			enabled:   func() bool { return false },
			openWithPartials: func(ctx context.Context, enabled bool) (localRecognizer, error) {
				if enabled {
					return nil, errors.New("partial recognition enabled without a caption subscriber")
				}
				return openSpeech(ctx, enabled)
			},
		},
	)
	if err != nil {
		t.Fatalf("create final-only Apple route: %v", err)
	}
	var resources audiosession.Resources
	t.Cleanup(func() {
		if resources.Source != nil {
			_ = resources.Source.Close()
		}
		if resources.Sink != nil {
			_ = resources.Sink.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := route.Close(ctx); err != nil {
			t.Errorf("close final-only Apple route: %v", err)
		}
	})
	started := time.Now()
	if err := route.Start(); err != nil {
		t.Fatalf("start final-only Apple route: %v", err)
	}
	waitXiaomiRoute(t, route.Ready)
	if err := route.BeforeStart(context.Background()); err != nil {
		t.Fatalf("capture test target: %v", err)
	}
	resources, err = route.Prepare(context.Background(), token)
	if err != nil {
		t.Fatalf("prepare final-only Apple route: %v", err)
	}
	sessionSink, ok := resources.Sink.(audiosession.SessionSink)
	if !ok {
		t.Fatalf("Apple route consumer %T does not defer recognition preparation", resources.Sink)
	}
	openStarted := time.Now()
	if err := sessionSink.StartSession(context.Background()); err != nil {
		t.Fatalf("prepare final-only Apple Speech recognizer: %v", err)
	}
	openDuration := time.Since(openStarted)
	writeStarted := time.Now()
	if err := writeSyntheticToAudioSink(context.Background(), resources.Sink, samples); err != nil {
		t.Fatalf("write synthetic PCM to final-only Apple route: %v", err)
	}
	writeDuration := time.Since(writeStarted)
	drainStarted := time.Now()
	if err := resources.Sink.Drain(context.Background()); err != nil {
		t.Fatalf("drain final-only Apple route: %v", err)
	}
	drainDuration := time.Since(drainStarted)
	transcriptSink, ok := resources.Sink.(audiosession.TranscriptSink)
	if !ok {
		t.Fatalf("Apple route consumer %T has no final transcript contract", resources.Sink)
	}
	finalText, available, err := transcriptSink.Transcript()
	if err != nil || !available {
		t.Fatalf("final transcript available=%t err=%v", available, err)
	}
	if normalizeAppleIntegrationText(finalText) != normalizeAppleIntegrationText(expected) {
		t.Fatalf("final-only transcript did not match fixed synthetic phrase (got %d UTF-8 bytes, expected %d)", len(finalText), len(expected))
	}
	if partialCallbacks.Load() != 0 {
		t.Fatalf("caption-disabled route produced %d partial callbacks", partialCallbacks.Load())
	}
	if err := route.DeliverTranscript(token, finalText); err != nil {
		t.Fatalf("deliver final-only result: %v", err)
	}
	if got := target.inserted(); len(got) != 1 || got[0] != finalText {
		t.Fatalf("final-only target commits do not match one final result: count=%d bytes=%d", len(got), len(finalText))
	}
	t.Logf("native Apple final-only route pid=%d started=%s fixture_samples=%d installAssets=false open_ms=%d write_ms=%d drain_ms=%d partial_updates=0 final_utf8_bytes=%d expected_phrase_match=true target_commit_count=1 target_matches_final=true total_ms=%d",
		os.Getpid(), started.Format(time.RFC3339Nano), len(samples), openDuration.Milliseconds(), writeDuration.Milliseconds(), drainDuration.Milliseconds(), len(finalText), time.Since(started).Milliseconds())
}

func writeSyntheticToAudioSink(ctx context.Context, sink audiosession.Sink, samples []int16) error {
	for offset := 0; offset < len(samples); {
		end := min(offset+applespeech.MaxBlockSamples, len(samples))
		block := samples[offset:end]
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := sink.TryWrite(block)
			if err == nil {
				break
			}
			if !errors.Is(err, applespeech.ErrQueueFull) {
				return err
			}
			time.Sleep(time.Millisecond)
		}
		offset = end
	}
	return nil
}

func normalizeAppleIntegrationText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return r
	}, text)
}

type idleAppleRouteSource struct {
	stopped chan struct{}
	once    sync.Once
}

func (s *idleAppleRouteSource) Read(ctx context.Context, _ []int16) (int, error) {
	select {
	case <-s.stopped:
		return 0, io.EOF
	case <-ctx.Done():
		return 0, io.EOF
	}
}

func (s *idleAppleRouteSource) Stop(context.Context) error {
	s.once.Do(func() { close(s.stopped) })
	return nil
}

func (s *idleAppleRouteSource) Close() error {
	s.once.Do(func() { close(s.stopped) })
	return nil
}
