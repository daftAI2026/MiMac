package main

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"mimac.local/mimac/internal/bridge"
)

// voiceInterimFeed keeps only the latest stable and tentative draft for the
// one current Core session. Provider callbacks may publish from a worker;
// presentation reads the same bounded snapshot from the UI thread.
type voiceInterimFeed struct {
	mu      sync.RWMutex
	enabled bool
	token   bridge.SessionToken
	draft   voiceInterimDraft
}

type voiceInterimDraft struct {
	Stable    string
	Tentative string
}

const (
	maxVoiceInterimBytes     = 512
	maxVoiceInterimScanBytes = 4096
)

func (f *voiceInterimFeed) SetEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !enabled {
		f.enabled = false
		f.token = bridge.SessionToken{}
		f.draft = voiceInterimDraft{}
		return
	}
	if f.enabled {
		return
	}
	f.enabled = true
}

// Begin binds the feed to the active Core session and drops any prior text.
// The full token also fences callbacks from an earlier device generation.
func (f *voiceInterimFeed) Begin(token bridge.SessionToken) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if token == (bridge.SessionToken{}) || token.ID() == 0 {
		f.token = bridge.SessionToken{}
		f.draft = voiceInterimDraft{}
		return
	}
	if f.token != token {
		f.token = token
		f.draft = voiceInterimDraft{}
	}
}

// Publish replaces the current draft only for the enabled, active session.
// Stable and tentative words are kept separate for visual emphasis. It never
// queues text and never touches an editor or a final transcript.
func (f *voiceInterimFeed) Publish(token bridge.SessionToken, stable, tentative string) bool {
	if token == (bridge.SessionToken{}) || token.ID() == 0 {
		return false
	}
	draft, valid := boundVoiceInterim(stable, tentative)
	if !valid {
		return false
	}
	if draft.Stable == "" && draft.Tentative == "" {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.enabled || f.token != token {
		return false
	}
	f.draft = draft
	return true
}

// Snapshot returns the latest draft only when the caller asks for its exact
// current session. A stale view cannot read a newer session's text.
func (f *voiceInterimFeed) Snapshot(token bridge.SessionToken) voiceInterimDraft {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if token == (bridge.SessionToken{}) || f.token != token {
		return voiceInterimDraft{}
	}
	return f.draft
}

// End drops text only for the session that is actually retiring. A late end
// from an older session cannot clear a newer session's draft.
func (f *voiceInterimFeed) End(token bridge.SessionToken) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if token != (bridge.SessionToken{}) && f.token == token {
		f.token = bridge.SessionToken{}
		f.draft = voiceInterimDraft{}
	}
}

// Clear is used when presentation is disabled or no Core session is active.
func (f *voiceInterimFeed) Clear() {
	f.mu.Lock()
	f.token = bridge.SessionToken{}
	f.draft = voiceInterimDraft{}
	f.mu.Unlock()
}

// boundVoiceInterim normalizes line breaks to spaces while preserving
// whitespace within each part, then caps retained bytes and scan work. If
// stable text must be shortened, its head gives way so recent words remain.
func boundVoiceInterim(stable, tentative string) (voiceInterimDraft, bool) {
	stable, valid := normalizeVoiceInterimPart(stable, true)
	if !valid {
		return voiceInterimDraft{}, false
	}
	tentative, valid = normalizeVoiceInterimPart(tentative, false)
	if !valid {
		return voiceInterimDraft{}, false
	}
	if strings.TrimSpace(stable) == "" {
		stable = ""
	}
	if strings.TrimSpace(tentative) == "" {
		tentative = ""
	}
	if stable == "" && tentative == "" {
		return voiceInterimDraft{}, true
	}
	if len(stable)+len(tentative) <= maxVoiceInterimBytes {
		return voiceInterimDraft{Stable: stable, Tentative: tentative}, true
	}

	// Keep some room for a live tentative word, then give the remaining bytes
	// to stable text. If stable is short, the tentative part can use the rest.
	tentativeBudget := min(len(tentative), maxVoiceInterimBytes/4)
	stableBudget := maxVoiceInterimBytes - tentativeBudget
	if len(stable) < stableBudget {
		tentativeBudget = min(len(tentative), maxVoiceInterimBytes-len(stable))
		stableBudget = len(stable)
	}
	stable = truncateVoiceInterimStart(stable, stableBudget)
	tentative = truncateVoiceInterimEnd(tentative, tentativeBudget)
	return voiceInterimDraft{Stable: stable, Tentative: tentative}, true
}

func normalizeVoiceInterimPart(text string, preserveEnd bool) (string, bool) {
	source := text
	truncatedStart, truncatedEnd := false, false
	if len(source) > maxVoiceInterimScanBytes {
		if preserveEnd {
			start := len(source) - maxVoiceInterimScanBytes
			for start < len(source) && !utf8.RuneStart(source[start]) {
				start++
			}
			source = source[start:]
			truncatedStart = true
		} else {
			end := maxVoiceInterimScanBytes
			for end > 0 && !utf8.RuneStart(source[end]) {
				end--
			}
			source = source[:end]
			truncatedEnd = true
		}
	}
	var out strings.Builder
	out.Grow(min(len(source), maxVoiceInterimBytes))
	for offset := 0; offset < len(source); {
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == utf8.RuneError && size == 1 {
			return "", false
		}
		offset += size
		if unicode.IsSpace(r) {
			out.WriteByte(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		out.WriteRune(r)
	}
	if truncatedStart {
		return "…" + out.String(), true
	}
	if truncatedEnd {
		out.WriteRune('…')
	}
	return out.String(), true
}

func truncateVoiceInterimStart(text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if len(text) <= budget {
		return text
	}
	marker := "…"
	contentBudget := max(0, budget-len(marker))
	candidate := len(text) - contentBudget
	for candidate < len(text) && !utf8.RuneStart(text[candidate]) {
		candidate++
	}
	start := candidate
	// Prefer a word boundary so the rightmost words remain readable.
	foundBoundary := false
	for start < len(text) {
		r, size := utf8.DecodeRuneInString(text[start:])
		if unicode.IsSpace(r) {
			start += size
			foundBoundary = true
			break
		}
		start += size
	}
	if !foundBoundary {
		// Languages without whitespace-delimited words still retain their
		// newest rune-aligned suffix instead of collapsing to just an ellipsis.
		start = candidate
	}
	result := marker + strings.TrimLeft(text[start:], " ")
	return clipVoiceInterimEnd(result, budget)
}

func truncateVoiceInterimEnd(text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if len(text) <= budget {
		return text
	}
	marker := "…"
	contentBudget := max(0, budget-len(marker))
	end := contentBudget
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	// Avoid ending on half a word when a boundary is readily available.
	if boundary := strings.LastIndexFunc(text[:end], unicode.IsSpace); boundary >= 0 {
		end = boundary
	}
	return text[:end] + marker
}

func clipVoiceInterimEnd(text string, budget int) string {
	if len(text) <= budget {
		return text
	}
	end := budget
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}
