package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mimac.local/mimac/internal/adapters/applespeech"
	"mimac.local/mimac/internal/audiodiagnostics"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

const (
	sourceCheckDuration        = 15 * time.Second
	sourceCheckReplayDeadline  = 30 * time.Second
	sourceCheckModelDeadline   = 10 * time.Second
	sourceCheckQueueYield      = 2 * time.Millisecond
	sourceCheckCapacitySamples = applespeech.RequiredSampleRate * 15
	sourceCheckMaxErrors       = 32
)

var errSourceCheckFinished = errors.New("source diagnostic has already finished")

var sourceCheckPreflightOwner struct {
	sync.Mutex
	sink *applespeech.Sink
}

type sourceCheckReplay func(context.Context, string, []int16) (string, error)

type sourceCheck struct {
	mu sync.Mutex

	reportPath string
	report     sourceCheckReport
	sessions   int
	session    *sourceCheckSession
	capture    *audiodiagnostics.Capture
	finished   bool

	pendingAppleSink *applespeech.Sink
	replay           sourceCheckReplay
}

type sourceCheckSession struct {
	token bridge.SessionToken

	eof             bool
	readErr         error
	healthErr       error
	stopCalled      bool
	stopErr         error
	closeSucceeded  bool
	closeErr        error
	invalidReadSize bool
}

type sourceCheckReport struct {
	SchemaVersion         int                     `json:"schema_version"`
	ScopeNote             string                  `json:"scope_note"`
	SessionCount          int                     `json:"session_count"`
	SessionTokenID        uint64                  `json:"session_token_id,omitempty"`
	DeviceID              string                  `json:"device_id,omitempty"`
	DeviceGeneration      uint64                  `json:"device_generation,omitempty"`
	SampleRate            int                     `json:"sample_rate,omitempty"`
	SourceEOF             bool                    `json:"source_eof"`
	SourceHealthError     string                  `json:"source_health_error,omitempty"`
	SourceReadError       string                  `json:"source_read_error,omitempty"`
	SourceStopCalled      bool                    `json:"source_stop_called"`
	SourceStopError       string                  `json:"source_stop_error,omitempty"`
	SourceCloseSucceeded  bool                    `json:"source_close_succeeded"`
	SourceCloseError      string                  `json:"source_close_error,omitempty"`
	InvalidReadSize       bool                    `json:"invalid_read_size,omitempty"`
	CompleteCapture       bool                    `json:"complete_capture"`
	Stats                 *audiodiagnostics.Stats `json:"stats,omitempty"`
	PCMSHA256             string                  `json:"pcm_sha256,omitempty"`
	PCMSHA256Encoding     string                  `json:"pcm_sha256_encoding,omitempty"`
	ReplayStarted         bool                    `json:"replay_started"`
	ReplayCompleted       bool                    `json:"replay_completed"`
	ReplaySampleCount     int                     `json:"replay_sample_count,omitempty"`
	SecondaryText         string                  `json:"secondary_text"`
	AppleSinkRetained     bool                    `json:"apple_sink_retained,omitempty"`
	AppleSinkCleanupError string                  `json:"apple_sink_cleanup_error,omitempty"`
	Errors                []string                `json:"errors,omitempty"`
	SuppressedErrorCount  uint64                  `json:"suppressed_error_count,omitempty"`
}

// newSourceCheck enables a one-run, in-memory source mirror only when the user
// supplies an explicit report path. The report is created later by Finish.
func newSourceCheck(reportPath string) (*sourceCheck, error) {
	if reportPath == "" {
		return nil, nil
	}
	if !filepath.IsAbs(reportPath) {
		return nil, fmt.Errorf("source diagnostic report path must be absolute: %q", reportPath)
	}
	reportPath = filepath.Clean(reportPath)
	parent := filepath.Dir(reportPath)
	info, err := os.Stat(parent)
	if err != nil {
		return nil, fmt.Errorf("source diagnostic report parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source diagnostic report parent is not a directory: %s", parent)
	}
	if _, err := os.Lstat(reportPath); err == nil {
		return nil, fmt.Errorf("source diagnostic report already exists: %s", reportPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect source diagnostic report path: %w", err)
	}

	check := &sourceCheck{
		reportPath: reportPath,
		report: sourceCheckReport{
			SchemaVersion: 1,
			ScopeNote:     "secondary_text is local Apple Speech replay of mirrored source PCM. It does not prove main-target consumption, diagnose a root cause, or repair one.",
		},
	}
	check.replay = func(ctx context.Context, libraryPath string, samples []int16) (string, error) {
		return replaySourceCheckPCM(ctx, libraryPath, samples, check.retainAppleSink)
	}
	return check, nil
}

// sourceCheckModelReady opens only the requested local zh-CN recognizer model,
// writes no PCM, and closes it before the app can start a user session.
func sourceCheckModelReady(libraryPath string) error {
	if !sourceCheckPreflightOwner.TryLock() {
		return errors.New("Apple Speech preflight already owns a native sink")
	}
	defer sourceCheckPreflightOwner.Unlock()
	retain := func(sink *applespeech.Sink, _ error) {
		sourceCheckPreflightOwner.sink = sink
	}
	if pending := sourceCheckPreflightOwner.sink; pending != nil {
		if err := closeSourceCheckSink(pending, retain); err != nil {
			return fmt.Errorf("Apple Speech preflight cleanup is still pending: %w", err)
		}
		sourceCheckPreflightOwner.sink = nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), sourceCheckModelDeadline)
	defer cancel()
	sink, openErr := applespeech.Open(ctx, applespeech.Config{
		LibraryPath:   libraryPath,
		Locale:        "zh-CN",
		SampleRate:    applespeech.RequiredSampleRate,
		InstallAssets: false,
	})
	if sink == nil {
		if openErr == nil {
			return errors.New("Apple Speech model preflight returned no sink")
		}
		return openErr
	}
	closeErr := closeSourceCheckSink(sink, retain)
	if closeErr == nil {
		sourceCheckPreflightOwner.sink = nil
	}
	return errors.Join(openErr, closeErr)
}

// Wrap mirrors the same Source.Read blocks after each underlying Read returns.
// With no explicit source check it is a no-op. It never changes the original
// Read, Stop, Close, or optional Err result and never adds a reader.
func (c *sourceCheck) Wrap(token bridge.SessionToken, resources audiosession.Resources) audiosession.Resources {
	if c == nil {
		return resources
	}

	c.mu.Lock()
	c.sessions++
	c.report.SessionCount = c.sessions
	if c.finished {
		c.addErrorLocked("source was wrapped after Finish")
		c.mu.Unlock()
		return resources
	}
	if c.sessions > 1 {
		c.report.CompleteCapture = false
		c.addErrorLocked("multiple audio sessions were wrapped; one capture cannot represent the complete run")
		if c.capture != nil {
			c.capture.Release()
			c.capture = nil
		}
		c.mu.Unlock()
		return resources
	}

	session := &sourceCheckSession{token: token}
	c.session = session
	c.report.SessionTokenID = token.ID()
	c.report.DeviceID = token.Device().ID()
	c.report.DeviceGeneration = token.Device().Generation()
	c.report.SampleRate = resources.SampleRate
	if resources.Source == nil {
		c.addErrorLocked("audio resources have no source")
	} else if resources.SampleRate != applespeech.RequiredSampleRate {
		c.addErrorLocked(fmt.Sprintf("unsupported source sample rate for Apple replay: got %d, want %d", resources.SampleRate, applespeech.RequiredSampleRate))
	} else {
		capture, err := audiodiagnostics.NewCapture(sourceCheckCapacitySamples)
		if err != nil {
			c.addErrorLocked("create bounded source capture: " + err.Error())
		} else {
			c.capture = capture
		}
	}
	inner := resources.Source
	c.mu.Unlock()

	if inner == nil {
		return resources
	}
	wrapped := &sourceCheckSource{check: c, session: session, inner: inner}
	if health, ok := inner.(interface{ Err() error }); ok {
		resources.Source = &sourceCheckSourceWithErr{sourceCheckSource: wrapped, sourceErr: health}
	} else {
		resources.Source = wrapped
	}
	return resources
}

// Finish must run after the app event loop returns. Only one complete source
// session with normal EOF and successful Close is replayed to local Apple
// Speech; secondary text is metadata and is never sent to the user's target.
func (c *sourceCheck) Finish(libraryPath string) error {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return errSourceCheckFinished
	}
	c.finished = true
	c.syncSessionReportLocked()
	capture := c.capture
	c.capture = nil
	eligibleErr := c.captureEligibilityLocked(capture)
	if eligibleErr != nil {
		c.report.CompleteCapture = false
		c.addErrorLocked("capture not replayed: " + eligibleErr.Error())
	}
	replay := c.replay
	c.mu.Unlock()

	if eligibleErr != nil {
		if capture != nil {
			capture.Release()
		}
		return errors.Join(eligibleErr, c.writeReport())
	}

	samples, stats, takeErr := capture.Take()
	if stats.ObservedSamples != 0 || stats.ObservedBlocks != 0 {
		c.setStats(stats)
	}
	if takeErr != nil {
		capture.Release()
		c.recordError("capture Take: " + takeErr.Error())
		return errors.Join(takeErr, c.writeReport())
	}
	defer clear(samples)
	if len(samples) == 0 {
		err := errors.New("complete source session contained no PCM samples")
		c.recordError(err.Error())
		return errors.Join(err, c.writeReport())
	}

	pcmHash := sourceCheckPCMHash(samples)
	c.mu.Lock()
	c.report.CompleteCapture = true
	c.report.PCMSHA256 = pcmHash
	c.report.PCMSHA256Encoding = "little-endian signed int16 PCM"
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), sourceCheckReplayDeadline)
	defer cancel()
	if replay == nil {
		err := errors.New("source diagnostic replay function is not configured")
		c.recordError(err.Error())
		return errors.Join(err, c.writeReport())
	}
	c.mu.Lock()
	c.report.ReplayStarted = true
	c.mu.Unlock()
	secondaryText, replayErr := replay(ctx, libraryPath, samples)
	c.mu.Lock()
	c.report.SecondaryText = secondaryText
	c.report.ReplayCompleted = replayErr == nil
	if replayErr == nil {
		c.report.ReplaySampleCount = len(samples)
	}
	if replayErr != nil {
		c.addErrorLocked("secondary Apple Speech replay: " + replayErr.Error())
	}
	c.mu.Unlock()
	return errors.Join(replayErr, c.writeReport())
}

func (c *sourceCheck) captureEligibilityLocked(capture *audiodiagnostics.Capture) error {
	var causes []error
	if c.sessions != 1 {
		causes = append(causes, fmt.Errorf("expected exactly one wrapped session, got %d", c.sessions))
	}
	if c.session == nil {
		causes = append(causes, errors.New("no audio source session was wrapped"))
		return errors.Join(causes...)
	}
	if capture == nil {
		causes = append(causes, errors.New("bounded capture is unavailable"))
	}
	if !c.session.eof {
		causes = append(causes, errors.New("source did not reach normal EOF"))
	}
	if c.session.readErr != nil {
		causes = append(causes, fmt.Errorf("source Read failed: %w", c.session.readErr))
	}
	if c.session.healthErr != nil {
		causes = append(causes, fmt.Errorf("source health failed: %w", c.session.healthErr))
	}
	if c.session.stopErr != nil {
		causes = append(causes, fmt.Errorf("source Stop failed: %w", c.session.stopErr))
	}
	if !c.session.closeSucceeded {
		causes = append(causes, errors.New("source Close did not succeed"))
	}
	if c.session.invalidReadSize {
		causes = append(causes, errors.New("source Read returned a sample count outside its destination"))
	}
	return errors.Join(causes...)
}

func (c *sourceCheck) syncSessionReportLocked() {
	c.report.SessionCount = c.sessions
	if c.session == nil {
		return
	}
	c.report.SourceEOF = c.session.eof
	c.report.SourceStopCalled = c.session.stopCalled
	c.report.SourceCloseSucceeded = c.session.closeSucceeded
	c.report.InvalidReadSize = c.session.invalidReadSize
	if c.session.readErr != nil {
		c.report.SourceReadError = c.session.readErr.Error()
	}
	if c.session.stopErr != nil {
		c.report.SourceStopError = c.session.stopErr.Error()
	}
	if c.session.closeErr != nil {
		c.report.SourceCloseError = c.session.closeErr.Error()
	}
}

func (c *sourceCheck) setStats(stats audiodiagnostics.Stats) {
	c.mu.Lock()
	statsCopy := stats
	c.report.Stats = &statsCopy
	c.mu.Unlock()
}

func (c *sourceCheck) recordError(message string) {
	c.mu.Lock()
	c.addErrorLocked(message)
	c.mu.Unlock()
}

func (c *sourceCheck) addErrorLocked(message string) {
	if len(c.report.Errors) < sourceCheckMaxErrors {
		c.report.Errors = append(c.report.Errors, message)
		return
	}
	c.report.SuppressedErrorCount++
}

func (c *sourceCheck) retainAppleSink(sink *applespeech.Sink, err error) {
	if c == nil || sink == nil {
		return
	}
	c.mu.Lock()
	c.pendingAppleSink = sink
	c.report.AppleSinkRetained = true
	if err != nil {
		c.report.AppleSinkCleanupError = err.Error()
		c.addErrorLocked("Apple Speech native sink retained after incomplete Close: " + err.Error())
	}
	c.mu.Unlock()
}

func (c *sourceCheck) writeReport() error {
	c.mu.Lock()
	path := c.reportPath
	report := c.report
	report.Errors = append([]string(nil), c.report.Errors...)
	if c.report.Stats != nil {
		statsCopy := *c.report.Stats
		report.Stats = &statsCopy
	}
	c.mu.Unlock()

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode source diagnostic report: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create source diagnostic report: %w", err)
	}
	writeErr := writeAll(file, data)
	var syncErr error
	if writeErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("write source diagnostic report: %w", errors.Join(writeErr, syncErr, closeErr))
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := writer.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

type sourceCheckSource struct {
	check   *sourceCheck
	session *sourceCheckSession
	inner   audiosession.Source
}

func (s *sourceCheckSource) Read(ctx context.Context, dst []int16) (int, error) {
	n, readErr := s.inner.Read(ctx, dst)
	s.check.mu.Lock()
	defer s.check.mu.Unlock()
	if s.session == s.check.session {
		if n < 0 || n > len(dst) {
			s.session.invalidReadSize = true
			s.check.addErrorLocked(fmt.Sprintf("source Read returned invalid sample count %d for destination length %d", n, len(dst)))
		} else if s.check.capture != nil && n != 0 {
			s.check.capture.Observe(dst[:n], time.Now())
		}
		if errors.Is(readErr, io.EOF) {
			s.session.eof = true
		} else if readErr != nil && s.session.readErr == nil {
			s.session.readErr = readErr
		}
	}
	return n, readErr
}

func (s *sourceCheckSource) Stop(ctx context.Context) error {
	err := s.inner.Stop(ctx)
	s.check.mu.Lock()
	s.session.stopCalled = true
	if err != nil && s.session.stopErr == nil {
		s.session.stopErr = err
		s.check.addErrorLocked("source Stop: " + err.Error())
	}
	s.check.mu.Unlock()
	return err
}

func (s *sourceCheckSource) Close() error {
	err := s.inner.Close()
	s.check.mu.Lock()
	if err == nil {
		s.session.closeSucceeded = true
	} else {
		s.session.closeErr = err
		s.check.addErrorLocked("source Close: " + err.Error())
	}
	s.check.mu.Unlock()
	return err
}

type sourceCheckHealth interface{ Err() error }

type sourceCheckSourceWithErr struct {
	*sourceCheckSource
	sourceErr sourceCheckHealth
}

func (s *sourceCheckSourceWithErr) Err() error {
	err := s.sourceErr.Err()
	if err != nil {
		s.check.mu.Lock()
		if s.session.healthErr == nil {
			s.session.healthErr = err
			s.check.report.SourceHealthError = err.Error()
		}
		s.check.mu.Unlock()
	}
	return err
}

func replaySourceCheckPCM(ctx context.Context, libraryPath string, samples []int16, retain func(*applespeech.Sink, error)) (string, error) {
	sink, err := applespeech.Open(ctx, applespeech.Config{
		LibraryPath:   libraryPath,
		Locale:        "zh-CN",
		SampleRate:    applespeech.RequiredSampleRate,
		InstallAssets: false,
	})
	if err != nil {
		if sink == nil {
			return "", err
		}
		closeErr := closeSourceCheckSink(sink, retain)
		return "", errors.Join(err, closeErr)
	}
	if sink == nil {
		return "", errors.New("Apple Speech Open returned no sink")
	}

	ticker := time.NewTicker(sourceCheckQueueYield)
	defer ticker.Stop()
	var replayErr error
	for offset := 0; offset < len(samples); {
		if err := ctx.Err(); err != nil {
			replayErr = err
			break
		}
		end := min(offset+applespeech.MaxBlockSamples, len(samples))
		for {
			if err := ctx.Err(); err != nil {
				replayErr = err
				break
			}
			err := sink.TryWrite(samples[offset:end])
			if errors.Is(err, applespeech.ErrQueueFull) {
				select {
				case <-ctx.Done():
					replayErr = ctx.Err()
				case <-ticker.C:
				}
				if replayErr != nil {
					break
				}
				continue
			}
			if err != nil {
				replayErr = err
				break
			}
			offset = end
			break
		}
		if replayErr != nil {
			break
		}
	}

	var secondaryText string
	if replayErr == nil {
		replayErr = sink.Drain(ctx)
		if replayErr == nil {
			secondaryText, replayErr = sink.FinalText()
		}
	}
	closeErr := closeSourceCheckSink(sink, retain)
	return secondaryText, errors.Join(replayErr, closeErr)
}

func closeSourceCheckSink(sink *applespeech.Sink, retain func(*applespeech.Sink, error)) error {
	if sink == nil {
		return nil
	}
	if err := sink.Close(); err != nil {
		if retain != nil {
			retain(sink, err)
		}
		return err
	}
	return nil
}

func sourceCheckPCMHash(samples []int16) string {
	digest := sha256.New()
	var block [8192]byte
	for offset := 0; offset < len(samples); {
		count := min(len(block)/2, len(samples)-offset)
		for i, sample := range samples[offset : offset+count] {
			binary.LittleEndian.PutUint16(block[i*2:], uint16(sample))
		}
		_, _ = digest.Write(block[:count*2])
		offset += count
	}
	sum := digest.Sum(nil)
	digest.Reset()
	clear(block[:])
	return hex.EncodeToString(sum)
}
