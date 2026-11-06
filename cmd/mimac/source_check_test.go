package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/applespeech"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

type sourceCheckReadResult struct {
	pcm []int16
	n   int
	err error
}

type sourceCheckTestSource struct {
	reads    []sourceCheckReadResult
	readAt   int
	stopErr  error
	closeErr error
}

func (s *sourceCheckTestSource) Read(_ context.Context, dst []int16) (int, error) {
	if s.readAt >= len(s.reads) {
		return 0, io.EOF
	}
	result := s.reads[s.readAt]
	s.readAt++
	copy(dst, result.pcm)
	return result.n, result.err
}

func (s *sourceCheckTestSource) Stop(context.Context) error { return s.stopErr }
func (s *sourceCheckTestSource) Close() error               { return s.closeErr }

type sourceCheckHealthTestSource struct {
	*sourceCheckTestSource
	healthErr error
}

func (s *sourceCheckHealthTestSource) Err() error { return s.healthErr }

func newSourceCheckTest(t *testing.T) (*sourceCheck, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source-check.json")
	check, err := newSourceCheck(path)
	if err != nil {
		t.Fatal(err)
	}
	return check, path
}

func wrapSourceCheckTestSource(check *sourceCheck, source audiosession.Source) audiosession.Source {
	return check.Wrap(bridge.SessionToken{}, audiosession.Resources{Source: source, SampleRate: applespeech.RequiredSampleRate}).Source
}

func installSourceCheckTestReplay(check *sourceCheck, replay func(context.Context, string, []int16) (string, error)) {
	check.replay = replay
}

func readSourceCheckReport(t *testing.T, path string) (sourceCheckReport, map[string]json.RawMessage) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report sourceCheckReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode source-check report: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("decode source-check fields: %v", err)
	}
	return report, fields
}

func TestNewSourceCheckRequiresExplicitSafeReportPath(t *testing.T) {
	if check, err := newSourceCheck(""); err != nil || check != nil {
		t.Fatalf("empty opt-in = (%v, %v), want (nil, nil)", check, err)
	}
	if _, err := newSourceCheck("relative/report.json"); err == nil {
		t.Fatal("relative report path was accepted")
	}
	if _, err := newSourceCheck(filepath.Join(t.TempDir(), "missing", "report.json")); err == nil {
		t.Fatal("report path with a missing parent was accepted")
	}

	existing := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(existing, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newSourceCheck(existing); err == nil {
		t.Fatal("existing report path was accepted")
	}
}

func TestSourceCheckWrapPreservesReadAndMirrorsSameSamples(t *testing.T) {
	check, reportPath := newSourceCheckTest(t)
	pcm := []int16{-32768, 0, 32767, 42}
	readEOF := io.EOF
	base := &sourceCheckTestSource{reads: []sourceCheckReadResult{{pcm: pcm, n: len(pcm), err: readEOF}}}
	wrapped := wrapSourceCheckTestSource(check, base)
	if _, ok := wrapped.(interface{ Err() error }); ok {
		t.Fatal("wrapper added an optional Err method absent from the source")
	}

	stopErr := wrapped.Stop(context.Background())
	if stopErr != nil {
		t.Fatalf("Stop() = %v, want nil", stopErr)
	}
	dst := make([]int16, len(pcm))
	n, err := wrapped.Read(context.Background(), dst)
	if n != len(pcm) || err != readEOF {
		t.Fatalf("Read() = (%d, %v), want (%d, original EOF)", n, err, len(pcm))
	}
	if !reflect.DeepEqual(dst, pcm) {
		t.Fatalf("wrapped source changed destination: got %v want %v", dst, pcm)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	var replayed []int16
	installSourceCheckTestReplay(check, func(ctx context.Context, library string, samples []int16) (string, error) {
		if library != "/local/libMiMacSpeech.dylib" {
			t.Errorf("replay library = %q", library)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > sourceCheckReplayDeadline || time.Until(deadline) <= 0 {
			t.Errorf("replay context deadline = %v, ok=%v", deadline, ok)
		}
		replayed = samples
		return "本机次级识别", nil
	})
	if err := check.Finish("/local/libMiMacSpeech.dylib"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, make([]int16, len(pcm))) {
		t.Fatalf("source samples retained after Finish = %v, want cleared buffer", replayed)
	}

	report, fields := readSourceCheckReport(t, reportPath)
	if !report.CompleteCapture || !report.SourceEOF || !report.SourceCloseSucceeded {
		t.Fatalf("source lifecycle report = %+v", report)
	}
	if !report.ReplayStarted || !report.ReplayCompleted || report.ReplaySampleCount != len(pcm) {
		t.Fatalf("replay status = started:%v completed:%v samples:%d", report.ReplayStarted, report.ReplayCompleted, report.ReplaySampleCount)
	}
	if _, ok := fields["secondary_text"]; !ok {
		t.Fatal("report omitted secondary_text")
	}
	if report.SecondaryText != "本机次级识别" {
		t.Fatalf("secondary text = %q", report.SecondaryText)
	}
	if report.Stats == nil || report.Stats.ObservedSamples != uint64(len(pcm)) || report.Stats.Peak != 32768 || report.Stats.ClippedSamples != 2 || report.Stats.ZeroSamples != 1 {
		t.Fatalf("sample stats = %+v", report.Stats)
	}
	if report.PCMSHA256 != sourceCheckPCMHash(pcm) || report.PCMSHA256Encoding != "little-endian signed int16 PCM" {
		t.Fatalf("PCM hash metadata = %q / %q", report.PCMSHA256, report.PCMSHA256Encoding)
	}
	if _, ok := fields["pcm"]; ok {
		t.Fatal("report contains a raw PCM field")
	}
	if !strings.Contains(report.ScopeNote, "does not prove main-target consumption") || !strings.Contains(report.ScopeNote, "diagnose a root cause, or repair one") {
		t.Fatalf("scope note omits diagnostic limits: %q", report.ScopeNote)
	}
	entries, err := os.ReadDir(filepath.Dir(reportPath))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(reportPath) {
		t.Fatalf("report directory entries = %v, err=%v", entries, err)
	}
}

func TestSourceCheckForwardsOptionalSourceErrOnly(t *testing.T) {
	check, _ := newSourceCheckTest(t)
	healthErr := errors.New("transport health")
	base := &sourceCheckHealthTestSource{
		sourceCheckTestSource: &sourceCheckTestSource{reads: []sourceCheckReadResult{{n: 0, err: io.EOF}}},
		healthErr:             healthErr,
	}
	wrapped := wrapSourceCheckTestSource(check, base)
	health, ok := wrapped.(interface{ Err() error })
	if !ok {
		t.Fatal("wrapper lost source's optional Err method")
	}
	if got := health.Err(); got != healthErr {
		t.Fatalf("Err() = %v, want original error %v", got, healthErr)
	}
	check.capture.Release()
}

func TestSourceCheckUnsupportedSampleRateDoesNotAllocateOrChangeRead(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, sampleRate := range []int{48000, maxInt} {
		t.Run(fmt.Sprintf("rate_%d", sampleRate), func(t *testing.T) {
			check, reportPath := newSourceCheckTest(t)
			pcm := []int16{17, -19}
			base := &sourceCheckTestSource{reads: []sourceCheckReadResult{{pcm: pcm, n: len(pcm), err: io.EOF}}}
			wrapped := check.Wrap(bridge.SessionToken{}, audiosession.Resources{Source: base, SampleRate: sampleRate}).Source
			if check.capture != nil {
				t.Fatalf("unsupported rate %d allocated a capture", sampleRate)
			}
			dst := make([]int16, len(pcm))
			if n, err := wrapped.Read(context.Background(), dst); n != len(pcm) || err != io.EOF {
				t.Fatalf("Read() = (%d, %v), want original (%d, EOF)", n, err, len(pcm))
			}
			if !reflect.DeepEqual(dst, pcm) {
				t.Fatalf("unsupported-rate wrapper changed source PCM: %v", dst)
			}
			if err := wrapped.Close(); err != nil {
				t.Fatal(err)
			}
			if err := check.Finish("unused"); err == nil {
				t.Fatal("Finish succeeded without a supported-rate capture")
			}
			report, _ := readSourceCheckReport(t, reportPath)
			if report.SampleRate != sampleRate || !report.SourceEOF || !report.SourceCloseSucceeded || report.CompleteCapture {
				t.Fatalf("unsupported-rate report = %+v", report)
			}
			if !strings.Contains(strings.Join(report.Errors, "\n"), "unsupported source sample rate") {
				t.Fatalf("unsupported rate missing from report: %+v", report)
			}
		})
	}
}

func TestSourceCheckIncompleteReadIsReportedAndNeverReplayed(t *testing.T) {
	check, reportPath := newSourceCheckTest(t)
	readErr := errors.New("truncated source")
	base := &sourceCheckTestSource{reads: []sourceCheckReadResult{{pcm: []int16{1, 2, 3}, n: 3, err: readErr}}}
	wrapped := wrapSourceCheckTestSource(check, base)
	if err := wrapped.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	dst := make([]int16, 3)
	if n, err := wrapped.Read(context.Background(), dst); n != 3 || err != readErr {
		t.Fatalf("Read() = (%d, %v), want original (3, readErr)", n, err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	replayed := false
	installSourceCheckTestReplay(check, func(context.Context, string, []int16) (string, error) {
		replayed = true
		return "", nil
	})
	if err := check.Finish("unused"); err == nil {
		t.Fatal("Finish succeeded for a truncated source")
	}
	if replayed {
		t.Fatal("incomplete source was replayed")
	}
	report, fields := readSourceCheckReport(t, reportPath)
	if report.SourceEOF || !strings.Contains(report.SourceReadError, readErr.Error()) || report.CompleteCapture {
		t.Fatalf("truncated source report = %+v", report)
	}
	if report.ReplayStarted || report.ReplayCompleted || report.SecondaryText != "" {
		t.Fatalf("truncated replay status = %+v", report)
	}
	if _, ok := fields["secondary_text"]; !ok {
		t.Fatal("incomplete report omitted empty secondary_text")
	}
	if check.capture != nil {
		t.Fatal("Finish retained an incomplete internal capture")
	}
}

func TestSourceCheckStopErrorPreventsReplay(t *testing.T) {
	check, reportPath := newSourceCheckTest(t)
	stopErr := errors.New("stop failed")
	base := &sourceCheckTestSource{reads: []sourceCheckReadResult{{n: 0, err: io.EOF}}, stopErr: stopErr}
	wrapped := wrapSourceCheckTestSource(check, base)
	if _, err := wrapped.Read(context.Background(), make([]int16, 1)); err != io.EOF {
		t.Fatal(err)
	}
	if err := wrapped.Stop(context.Background()); err != stopErr {
		t.Fatalf("Stop() = %v, want original error", err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	replayed := false
	installSourceCheckTestReplay(check, func(context.Context, string, []int16) (string, error) {
		replayed = true
		return "", nil
	})
	if err := check.Finish("unused"); err == nil {
		t.Fatal("Finish succeeded after Stop error")
	}
	if replayed {
		t.Fatal("source with Stop error was replayed")
	}
	report, _ := readSourceCheckReport(t, reportPath)
	if !strings.Contains(report.SourceStopError, stopErr.Error()) {
		t.Fatalf("Stop error missing from report: %+v", report)
	}
}

func TestSourceCheckCloseErrorPreventsReplay(t *testing.T) {
	check, reportPath := newSourceCheckTest(t)
	closeErr := errors.New("native source close not joined")
	base := &sourceCheckTestSource{reads: []sourceCheckReadResult{{n: 0, err: io.EOF}}, closeErr: closeErr}
	wrapped := wrapSourceCheckTestSource(check, base)
	if _, err := wrapped.Read(context.Background(), make([]int16, 1)); err != io.EOF {
		t.Fatal(err)
	}
	if err := wrapped.Close(); err != closeErr {
		t.Fatalf("Close() = %v, want original error", err)
	}
	replayed := false
	installSourceCheckTestReplay(check, func(context.Context, string, []int16) (string, error) {
		replayed = true
		return "", nil
	})
	if err := check.Finish("unused"); err == nil {
		t.Fatal("Finish succeeded after Close error")
	}
	if replayed {
		t.Fatal("source with unsuccessful Close was replayed")
	}
	report, _ := readSourceCheckReport(t, reportPath)
	if report.SourceCloseSucceeded || !strings.Contains(report.SourceCloseError, closeErr.Error()) {
		t.Fatalf("Close error missing from report: %+v", report)
	}
}

func TestSourceCheckOverflowIsReportedAndNeverReplayed(t *testing.T) {
	check, reportPath := newSourceCheckTest(t)
	const capacity = applespeech.RequiredSampleRate * int(sourceCheckDuration/time.Second)
	pcm := make([]int16, capacity+1)
	pcm[0], pcm[1], pcm[2] = math.MinInt16, math.MaxInt16, 0
	base := &sourceCheckTestSource{reads: []sourceCheckReadResult{
		{pcm: pcm, n: len(pcm)},
		{n: 0, err: io.EOF},
	}}
	wrapped := wrapSourceCheckTestSource(check, base)
	if _, err := wrapped.Read(context.Background(), make([]int16, len(pcm))); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Read(context.Background(), make([]int16, 1)); err != io.EOF {
		t.Fatalf("EOF read error = %v", err)
	}
	if err := wrapped.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	replayed := false
	installSourceCheckTestReplay(check, func(context.Context, string, []int16) (string, error) {
		replayed = true
		return "", nil
	})
	if err := check.Finish("unused"); err == nil {
		t.Fatal("Finish succeeded for overflowed PCM")
	}
	if replayed {
		t.Fatal("overflowed partial PCM was replayed")
	}
	report, _ := readSourceCheckReport(t, reportPath)
	if report.CompleteCapture || report.Stats == nil || report.Stats.OverflowBlocks != 1 || report.Stats.OverflowSamples != uint64(len(pcm)) {
		t.Fatalf("overflow report = %+v", report)
	}
	if report.Stats.Peak != 32768 || report.Stats.ClippedSamples != 2 || report.Stats.ZeroSamples != uint64(len(pcm)-2) {
		t.Fatalf("overflow extrema stats = %+v", report.Stats)
	}
}

func TestSourceCheckMultipleSessionsAreExplicitlyRejected(t *testing.T) {
	check, reportPath := newSourceCheckTest(t)
	first := &sourceCheckTestSource{reads: []sourceCheckReadResult{{pcm: []int16{1, 2}, n: 2}}}
	firstResources := check.Wrap(bridge.SessionToken{}, audiosession.Resources{Source: first, SampleRate: applespeech.RequiredSampleRate})
	second := &sourceCheckTestSource{reads: []sourceCheckReadResult{{n: 0, err: io.EOF}}}
	secondResources := check.Wrap(bridge.SessionToken{}, audiosession.Resources{Source: second, SampleRate: applespeech.RequiredSampleRate})
	if secondResources.Source != second {
		t.Fatal("second session was unexpectedly wrapped into the first capture")
	}
	if firstResources.Source == first {
		t.Fatal("first source was not wrapped")
	}
	replayed := false
	installSourceCheckTestReplay(check, func(context.Context, string, []int16) (string, error) {
		replayed = true
		return "", nil
	})
	if err := check.Finish("unused"); err == nil {
		t.Fatal("Finish succeeded with multiple sessions")
	}
	if replayed {
		t.Fatal("multiple sessions were collapsed into one replay")
	}
	report, _ := readSourceCheckReport(t, reportPath)
	if report.SessionCount != 2 || report.CompleteCapture || !strings.Contains(strings.Join(report.Errors, "\n"), "multiple audio sessions") {
		t.Fatalf("multiple-session report = %+v", report)
	}
}

func TestSourceCheckPCMHashIsLittleEndianInt16(t *testing.T) {
	pcm := []int16{-32768, 0, 32767}
	var raw [6]byte
	for i, sample := range pcm {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(sample))
	}
	want := sha256.Sum256(raw[:])
	if got := sourceCheckPCMHash(pcm); got != fmtHash(want[:]) {
		t.Fatalf("source PCM SHA-256 = %s, want %s", got, fmtHash(want[:]))
	}
}

func TestSourceCheckCompletedReplayCanHaveEmptyText(t *testing.T) {
	check, reportPath := newSourceCheckTest(t)
	base := &sourceCheckTestSource{reads: []sourceCheckReadResult{{pcm: []int16{4, 5}, n: 2, err: io.EOF}}}
	wrapped := wrapSourceCheckTestSource(check, base)
	if _, err := wrapped.Read(context.Background(), make([]int16, 2)); err != io.EOF {
		t.Fatal(err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	installSourceCheckTestReplay(check, func(context.Context, string, []int16) (string, error) {
		return "", nil
	})
	if err := check.Finish("unused"); err != nil {
		t.Fatal(err)
	}
	report, fields := readSourceCheckReport(t, reportPath)
	if !report.ReplayStarted || !report.ReplayCompleted || report.ReplaySampleCount != 2 || report.SecondaryText != "" {
		t.Fatalf("empty-text replay status = %+v", report)
	}
	if _, ok := fields["secondary_text"]; !ok {
		t.Fatal("completed empty-text report omitted secondary_text")
	}
}

func fmtHash(sum []byte) string {
	const hex = "0123456789abcdef"
	result := make([]byte, len(sum)*2)
	for i, value := range sum {
		result[i*2] = hex[value>>4]
		result[i*2+1] = hex[value&0x0f]
	}
	return string(result)
}

func TestSourceCheckAsyncHealthFaultRefusesReplayWithoutChangingSourceError(t *testing.T) {
	check, path := newSourceCheckTest(t)
	original := errors.New("asynchronous source fault")
	base := &sourceCheckHealthTestSource{sourceCheckTestSource: &sourceCheckTestSource{reads: []sourceCheckReadResult{{pcm: []int16{1, 2}, n: 2, err: io.EOF}}}, healthErr: original}
	wrapped := wrapSourceCheckTestSource(check, base)
	if got := wrapped.(interface{ Err() error }).Err(); got != original {
		t.Fatal("changed original source error")
	}
	if _, err := wrapped.Read(context.Background(), make([]int16, 2)); err != io.EOF {
		t.Fatal(err)
	}
	if err := wrapped.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	installSourceCheckTestReplay(check, func(context.Context, string, []int16) (string, error) {
		t.Fatal("faulted source was replayed")
		return "", nil
	})
	if err := check.Finish("unused"); err == nil {
		t.Fatal("health fault was hidden")
	}
	report, _ := readSourceCheckReport(t, path)
	if report.CompleteCapture || report.SourceHealthError != original.Error() {
		t.Fatalf("wrong health report: %+v", report)
	}
}
