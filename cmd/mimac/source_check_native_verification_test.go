package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func TestNativeSourceCheckSyntheticReplay(t *testing.T) {
	manifestPath := os.Getenv("MIMAC_SOURCE_CHECK_FIXTURE")
	libraryPath := os.Getenv("MIMAC_SOURCE_CHECK_LIBRARY")
	reportDirectory := os.Getenv("MIMAC_SOURCE_CHECK_RESULTS")
	if manifestPath == "" || libraryPath == "" || reportDirectory == "" {
		t.Skip("explicit own-synthetic/native-library paths required; no microphone")
	}
	var fixture struct {
		PCM      string `json:"pcm_path"`
		SHA      string `json:"sha256"`
		Frames   int    `json:"frames"`
		Rate     int    `json:"sample_rate"`
		Channels int    `json:"channels"`
		Text     string `json:"text"`
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fixture.PCM)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.Rate != 16000 || fixture.Channels != 1 || len(data) != fixture.Frames*2 || fmt.Sprintf("%x", sha256.Sum256(data)) != fixture.SHA {
		t.Fatal("fixture identity/format mismatch")
	}
	samples := make([]int16, len(data)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	base := &sourceCheckTestSource{}
	for offset := 0; offset < len(samples); offset += 320 {
		block := samples[offset:min(offset+320, len(samples))]
		base.reads = append(base.reads, sourceCheckReadResult{pcm: block, n: len(block)})
	}
	if err := sourceCheckModelReady(libraryPath); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(reportDirectory, "native-source-check.json")
	check, err := newSourceCheck(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	source := wrapSourceCheckTestSource(check, base)
	destination := make([]int16, 320)
	for {
		_, err = source.Read(context.Background(), destination)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = source.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = source.Close(); err != nil {
		t.Fatal(err)
	}
	if err = check.Finish(libraryPath); err != nil {
		t.Fatal(err)
	}
	report, _ := readSourceCheckReport(t, reportPath)
	normalize := func(s string) string {
		return strings.Map(func(c rune) rune {
			if unicode.IsLetter(c) || unicode.IsDigit(c) {
				return c
			}
			return -1
		}, s)
	}
	if report.PCMSHA256 != fixture.SHA || report.Stats == nil || report.Stats.ObservedSamples != uint64(fixture.Frames) || !report.CompleteCapture || !report.ReplayStarted || !report.ReplayCompleted || report.ReplaySampleCount != fixture.Frames || len(report.Errors) != 0 || report.AppleSinkRetained {
		t.Fatalf("incomplete native report: %+v", report)
	}
	if normalize(report.SecondaryText) != normalize(fixture.Text) {
		t.Fatalf("native result %q expected %q", report.SecondaryText, fixture.Text)
	}
	if sourceCheckPCMHash(samples) != fixture.SHA {
		t.Fatal("diagnostic altered original source samples")
	}
	t.Logf("%d source samples mirrored unchanged, locally replayed and cleared; secondary=%q; no microphone/target shortcut", fixture.Frames, report.SecondaryText)
}
