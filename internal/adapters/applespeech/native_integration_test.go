package applespeech

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode"
)

// This integration path is opt-in. The caller supplies a synthesized 16 kHz
// mono signed-16-bit little-endian fixture and a separately built Swift dylib.
func TestNativeLifecycleNormalCancelNormal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Speech integration requires macOS")
	}
	libraryPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_LIBRARY"))
	pcmPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_PCM"))
	if libraryPath == "" || pcmPath == "" {
		t.Skip("set MIMAC_SPEECH_LIBRARY and MIMAC_SPEECH_PCM to opt into native integration")
	}

	pcmBytes, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatalf("read synthesized PCM fixture: %v", err)
	}
	if len(pcmBytes) == 0 || len(pcmBytes)%2 != 0 {
		t.Fatalf("PCM fixture must contain a non-empty whole number of s16le samples; got %d bytes", len(pcmBytes))
	}
	samples := make([]int16, len(pcmBytes)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcmBytes[index*2 : index*2+2]))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := Config{LibraryPath: libraryPath, Locale: "zh-CN", SampleRate: RequiredSampleRate}

	firstText := transcribeFixture(t, ctx, cfg, samples)
	t.Logf("first transcript: %q", firstText)
	assertFixtureTranscript(t, firstText)

	cancelled, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open session to cancel: %v", err)
	}
	if err := writeFixture(ctx, cancelled, samples[:min(len(samples), RequiredSampleRate/2)]); err != nil {
		_ = cancelled.Close()
		t.Fatalf("feed cancellation prefix: %v", err)
	}
	if err := cancelled.Close(); err != nil {
		t.Fatalf("cancel and join session: %v", err)
	}
	if text, err := cancelled.FinalText(); text != "" || !errors.Is(err, ErrNotDrained) {
		t.Fatalf("cancelled FinalText() = %q, %v; want no published text", text, err)
	}

	secondText := transcribeFixture(t, ctx, cfg, samples)
	t.Logf("after cancellation: %q", secondText)
	assertFixtureTranscript(t, secondText)
}

// This opt-in path proves one cold-process, first-session Open/Write/Drain
// using a caller-generated TTS fixture. It intentionally runs one normal
// session only; lifecycle cancellation is covered by the separate matrix.
func TestNativeColdFirstTTSInput(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Speech integration requires macOS")
	}
	libraryPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_LIBRARY"))
	pcmPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_PCM"))
	if libraryPath == "" || pcmPath == "" {
		t.Skip("set MIMAC_SPEECH_LIBRARY and MIMAC_SPEECH_PCM to opt into native integration")
	}

	pcmBytes, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatalf("read synthesized TTS fixture: %v", err)
	}
	if len(pcmBytes) == 0 || len(pcmBytes)%2 != 0 {
		t.Fatalf("PCM fixture must contain a non-empty whole number of s16le samples; got %d bytes", len(pcmBytes))
	}
	samples := make([]int16, len(pcmBytes)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcmBytes[index*2 : index*2+2]))
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := Config{LibraryPath: libraryPath, Locale: "zh-CN", SampleRate: RequiredSampleRate}
	openStarted := time.Now()
	sink, err := Open(ctx, cfg)
	openDuration := time.Since(openStarted)
	closed := false
	closeSession := func() error {
		if sink == nil || closed {
			return nil
		}
		if err := sink.Close(); err != nil {
			return err
		}
		closed = true
		return nil
	}
	t.Cleanup(func() {
		if closeErr := closeSession(); closeErr != nil {
			t.Errorf("cleanup cold first Apple Speech session: %v", closeErr)
		}
	})
	if err != nil {
		t.Fatalf("cold first Apple Speech Open after %s: %v", openDuration, err)
	}
	writeStarted := time.Now()
	if err := writeFixture(ctx, sink, samples); err != nil {
		_ = closeSession()
		t.Fatalf("write synthesized TTS fixture: %v", err)
	}
	writeDuration := time.Since(writeStarted)
	drainStarted := time.Now()
	if err := sink.Drain(ctx); err != nil {
		_ = closeSession()
		t.Fatalf("drain first Apple Speech TTS session: %v", err)
	}
	drainDuration := time.Since(drainStarted)
	text, err := sink.FinalText()
	if err != nil {
		_ = closeSession()
		t.Fatalf("read final Apple Speech text: %v", err)
	}
	assertFixtureTranscript(t, text)
	closeStarted := time.Now()
	if err := closeSession(); err != nil {
		t.Fatalf("close first Apple Speech TTS session: %v", err)
	}
	t.Logf("cold first TTS session pid=%d started=%s locale=zh-CN installAssets=false pcm_samples=%d open_ms=%d write_ms=%d drain_ms=%d close_ms=%d total_ms=%d final_utf8_bytes=%d expected_phrase_match=true",
		os.Getpid(), started.Format(time.RFC3339Nano), len(samples), openDuration.Milliseconds(), writeDuration.Milliseconds(), drainDuration.Milliseconds(), time.Since(closeStarted).Milliseconds(), time.Since(started).Milliseconds(), len(text))
}

// This opt-in integration path uses the same fixed synthetic phrase with
// volatile reporting enabled. It logs timings and counts, never transcript text.
func TestNativeStreamingPartialAndFinal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Speech integration requires macOS")
	}
	libraryPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_LIBRARY"))
	pcmPath := strings.TrimSpace(os.Getenv("MIMAC_SPEECH_PCM"))
	if libraryPath == "" || pcmPath == "" {
		t.Skip("set MIMAC_SPEECH_LIBRARY and MIMAC_SPEECH_PCM to opt into native integration")
	}
	pcmBytes, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatalf("read synthesized PCM fixture: %v", err)
	}
	if len(pcmBytes) == 0 || len(pcmBytes)%2 != 0 {
		t.Fatalf("PCM fixture must contain a non-empty whole number of s16le samples; got %d bytes", len(pcmBytes))
	}
	samples := make([]int16, len(pcmBytes)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcmBytes[index*2 : index*2+2]))
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := Config{LibraryPath: libraryPath, Locale: "zh-CN", SampleRate: RequiredSampleRate, EnablePartials: true}
	openStarted := time.Now()
	sink, err := Open(ctx, cfg)
	openDuration := time.Since(openStarted)
	if err != nil {
		t.Fatalf("open streaming Apple Speech session after %s: %v", openDuration, err)
	}
	defer func() {
		if err := sink.Close(); err != nil {
			t.Errorf("close streaming Apple Speech session: %v", err)
		}
	}()
	type observedUpdate struct {
		at                time.Time
		stable, tentative string
	}
	updates := make(chan observedUpdate, 256)
	if err := sink.SetPartialHandler(func(stable, tentative string) {
		select {
		case updates <- observedUpdate{at: time.Now(), stable: stable, tentative: tentative}:
		default:
		}
	}); err != nil {
		t.Fatalf("subscribe to native partials: %v", err)
	}
	writeStarted := time.Now()
	if err := writeFixture(ctx, sink, samples); err != nil {
		t.Fatalf("write synthesized PCM fixture: %v", err)
	}
	writeDuration := time.Since(writeStarted)
	drainStarted := time.Now()
	if err := sink.Drain(ctx); err != nil {
		t.Fatalf("drain streaming Apple Speech session: %v", err)
	}
	drainDuration := time.Since(drainStarted)
	drainCompleted := time.Now()
	text, err := sink.FinalText()
	if err != nil {
		t.Fatalf("read streaming final transcription: %v", err)
	}
	assertFixtureTranscript(t, text)
	var updateCount, nonemptyCount int
	var firstAt, lastAt time.Time
	for {
		select {
		case update := <-updates:
			updateCount++
			if update.stable != "" || update.tentative != "" {
				nonemptyCount++
			}
			if firstAt.IsZero() {
				firstAt = update.at
			}
			lastAt = update.at
		default:
			goto observed
		}
	}
observed:
	if nonemptyCount == 0 {
		t.Fatal("volatile reporting produced no non-empty partial caption")
	}
	if firstAt.IsZero() || !firstAt.Before(drainCompleted) {
		t.Fatal("no partial caption was observed before final drain completed")
	}
	t.Logf("streaming TTS session pid=%d started=%s locale=zh-CN installAssets=false pcm_samples=%d open_ms=%d write_ms=%d drain_ms=%d updates=%d nonempty_updates=%d first_partial_ms=%d last_partial_ms=%d final_utf8_bytes=%d expected_phrase_match=true total_ms=%d",
		os.Getpid(), started.Format(time.RFC3339Nano), len(samples), openDuration.Milliseconds(), writeDuration.Milliseconds(), drainDuration.Milliseconds(), updateCount, nonemptyCount, firstAt.Sub(started).Milliseconds(), lastAt.Sub(started).Milliseconds(), len(text), time.Since(started).Milliseconds())
}

func transcribeFixture(t *testing.T, ctx context.Context, cfg Config, samples []int16) string {
	t.Helper()
	sink, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open Apple Speech session: %v", err)
	}
	defer func() {
		if err := sink.Close(); err != nil {
			t.Errorf("close Apple Speech session: %v", err)
		}
	}()
	if err := writeFixture(ctx, sink, samples); err != nil {
		t.Fatalf("write synthesized PCM fixture: %v", err)
	}
	if err := sink.Drain(ctx); err != nil {
		t.Fatalf("drain Apple Speech session: %v", err)
	}
	text, err := sink.FinalText()
	if err != nil {
		t.Fatalf("read final transcription: %v", err)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("Apple Speech returned empty final text")
	}
	return text
}

func writeFixture(ctx context.Context, sink *Sink, samples []int16) error {
	for offset := 0; offset < len(samples); {
		end := min(offset+MaxBlockSamples, len(samples))
		block := samples[offset:end]
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := sink.TryWrite(block)
			if err == nil {
				break
			}
			if !errors.Is(err, ErrQueueFull) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		offset = end
	}
	return nil
}

func assertFixtureTranscript(t *testing.T, text string) {
	t.Helper()
	expected := os.Getenv("MIMAC_SPEECH_EXPECTED")
	if expected == "" {
		expected = "这是我的小米遥控器，现在开始测试"
	}
	normalize := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				return -1
			}
			return r
		}, s)
	}
	if normalize(text) != normalize(expected) {
		t.Fatalf("incomplete transcription: got %q, want %q (ignoring punctuation/spacing)", text, expected)
	}
}
