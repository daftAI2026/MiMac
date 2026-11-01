package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"time"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/siriglass"
)

const voiceOverlayTraceLimit = 4096

// Optional diagnostics. UI entries stay on the UI thread; I/O timing has its
// own bounded worker-safe buffer. No report content is written during capture.
// UpdateMeters may deduplicate a successful call; its return value is not a native update or GPU-present ACK.
type voiceOverlayTrace struct {
	io      voiceIOTrace
	Entries []voiceOverlayTraceEntry `json:"entries"`
	Dropped uint64                   `json:"dropped"`
	Binary  string                   `json:"executable_sha256"`
}

type voiceOverlayTraceEntry struct {
	Kind           string              `json:"kind"`
	At             time.Time           `json:"at"`
	Session        uint64              `json:"session"`
	Generation     uint64              `json:"generation"`
	Epoch          uint64              `json:"epoch,omitempty"`
	Sequence       uint64              `json:"sequence"`
	Visible        bool                `json:"visible"`
	AgeNS          int64               `json:"age_ns"`
	Enabled        bool                `json:"enabled"`
	TokenMatch     bool                `json:"token_match"`
	Alive          bool                `json:"context_alive"`
	TimestampFresh bool                `json:"timestamp_fresh"`
	Usable         bool                `json:"snapshot_usable"`
	Raw            *siriglass.Spectrum `json:"raw,omitempty"`
	Displayed      siriglass.Spectrum  `json:"displayed"`
	Meters         *siriglass.Meters   `json:"meters,omitempty"`
	Accepted       bool                `json:"update_meters_returned_nil"`
}

func openVoiceOverlayTrace(path string) (*voiceOverlayTrace, *os.File, error) {
	if path == "" {
		return nil, nil, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	binary, err := os.Open(executable)
	if err != nil {
		return nil, nil, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, binary)
	closeErr := binary.Close()
	if copyErr != nil {
		return nil, nil, copyErr
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	return &voiceOverlayTrace{Entries: make([]voiceOverlayTraceEntry, 0, voiceOverlayTraceLimit), Binary: hex.EncodeToString(hash.Sum(nil))}, file, nil
}

func (t *voiceOverlayTrace) append(entry voiceOverlayTraceEntry) {
	if t == nil {
		return
	}
	if len(t.Entries) >= voiceOverlayTraceLimit {
		t.Dropped++
		return
	}
	t.Entries = append(t.Entries, entry)
}

// The caller holds the existing meter lock. Reading diagnostics never advances
// smoothing or reads PCM a second time.
func (t *voiceOverlayTrace) sample(m *voiceSpectrumState, token bridge.SessionToken, now time.Time, displayed siriglass.Spectrum) {
	if t == nil {
		return
	}
	if len(t.Entries) >= voiceOverlayTraceLimit {
		t.Dropped++
		return
	}
	age := int64(-1)
	if !m.at.IsZero() {
		age = now.Sub(m.at).Nanoseconds()
	}
	raw := m.value
	match := token == m.token
	alive := m.ctx != nil && m.ctx.Err() == nil
	fresh := !m.at.IsZero() && age >= 0 && time.Duration(age) <= voiceSpectrumFreshness
	entry := voiceOverlayTraceEntry{Kind: "sample", At: now, Session: token.ID(), Generation: token.Device().Generation(), Epoch: m.epoch,
		Sequence: displayed.Sequence, AgeNS: age, Enabled: m.enabled, TokenMatch: token == m.token,
		Alive: alive, TimestampFresh: fresh, Usable: m.enabled && match && alive && fresh, Displayed: displayed}
	if match {
		entry.Raw = &raw
	}
	t.append(entry)
}

func (t *voiceOverlayTrace) hidden(token bridge.SessionToken, err error) {
	if t == nil {
		return
	}
	t.append(voiceOverlayTraceEntry{Kind: "hide", At: time.Now(), Session: token.ID(), Generation: token.Device().Generation(), AgeNS: -1, Accepted: err == nil})
}

func (t *voiceOverlayTrace) render(frame voiceOverlayFrame, err error) {
	if t == nil {
		return
	}
	if len(t.Entries) >= voiceOverlayTraceLimit {
		t.Dropped++
		return
	}
	meters := siriglass.MetersFromSpectrum(frame.Spectrum)
	t.append(voiceOverlayTraceEntry{Kind: "render", At: time.Now(), Session: frame.Token.ID(), Generation: frame.Token.Device().Generation(),
		Sequence: frame.Spectrum.Sequence, Visible: frame.Visible, AgeNS: -1, Displayed: frame.Spectrum, Meters: &meters, Accepted: err == nil})
}

func (t *voiceOverlayTrace) write(w io.Writer) error {
	return json.NewEncoder(w).Encode(struct {
		Schema                 int             `json:"schema"`
		IO                     voiceIOSnapshot `json:"io"`
		Limit                  int             `json:"limit"`
		GPUPresentAcknowledged bool            `json:"gpu_present_ack_observed"`
		*voiceOverlayTrace
	}{Schema: 2, IO: t.io.snapshot(), Limit: voiceOverlayTraceLimit, voiceOverlayTrace: t})
}
