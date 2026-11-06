package doubao

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"

	"howett.net/plist"
)

const (
	voiceStateRequestNotificationName  = "com.bytedance.inputmethod.doubaoime.voiceState.request"
	voiceStateResponseNotificationName = "com.bytedance.inputmethod.doubaoime.voiceState.response"
	maxControlSettingsPropertyListSize = 64 * 1024
)

var (
	// ErrControlSettingsUnsupported means the native query is unavailable on this platform.
	ErrControlSettingsUnsupported = errors.New("doubao control settings query is supported only on macOS")
	// ErrControlSettingsNotReady means Doubao answered the request but has not built its settings snapshot.
	ErrControlSettingsNotReady = errors.New("doubao control settings are not ready")
	// ErrMalformedControlSettings means a correlated reply did not match the reviewed property-list schema.
	ErrMalformedControlSettings = errors.New("doubao control settings reply is malformed")
)

// ControlSettings contains only the settings snapshot reported by Doubao's
// voiceState response. It does not describe microphone, recognizer, or text
// submission state.
type ControlSettings struct {
	VoiceShortcutEnabled bool
	GlobalVoiceEnabled   bool
	SingleClick          ControlShortcutSettings
	LongPress            ControlShortcutSettings
}

// ControlShortcutSettings preserves Doubao's configured flag and native key tuple.
type ControlShortcutSettings struct {
	Configured    bool
	KeyCode       int64
	ModifierFlags int64
}

type controlSettingsResult struct {
	settings ControlSettings
	err      error
}

type controlSettingsQuerySession struct {
	ctx         context.Context
	requestID   string
	result      chan controlSettingsResult
	accepting   bool
	observer    uintptr
	nativeState any
}

type controlSettingsSubscription interface {
	// removeObserver stops future native deliveries but retains references used
	// by callbacks that entered before removal.
	removeObserver()
	// release is called only after active-session ownership has been cleared.
	release()
}

type controlSettingsTransport interface {
	subscribe(*controlSettingsQuerySession) (controlSettingsSubscription, error)
	postRequest(*controlSettingsQuerySession) error
}

var (
	controlSettingsQueryGate     = make(chan struct{}, 1)
	controlSettingsSessionMu     sync.Mutex
	activeControlSettingsSession *controlSettingsQuerySession
)

// QueryControlSettings sends one read-only voiceState request and waits for its
// UUID-correlated response or ctx cancellation. The caller should provide a
// deadline when a fixed upper bound is needed. The UUID correlates replies but
// does not authenticate the sender. Distributed notification delivery depends
// on the host's main run loop running in a common mode; this function does not
// start, stop, or pump that loop.
func QueryControlSettings(ctx context.Context) (ControlSettings, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return queryControlSettings(ctx, newControlSettingsTransport(), newControlSettingsRequestID)
}

func queryControlSettings(ctx context.Context, transport controlSettingsTransport, makeRequestID func() (string, error)) (ControlSettings, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ControlSettings{}, err
	}
	if transport == nil || makeRequestID == nil {
		return ControlSettings{}, errors.New("doubao control settings query: transport and UUID generator are required")
	}
	select {
	case controlSettingsQueryGate <- struct{}{}:
		defer func() { <-controlSettingsQueryGate }()
	case <-ctx.Done():
		return ControlSettings{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return ControlSettings{}, err
	}
	requestID, err := makeRequestID()
	if err != nil {
		return ControlSettings{}, fmt.Errorf("doubao control settings query: create UUID: %w", err)
	}
	if !canonicalUUID(requestID) {
		return ControlSettings{}, errors.New("doubao control settings query: UUID generator returned a non-canonical UUID")
	}
	session := &controlSettingsQuerySession{
		ctx:       ctx,
		requestID: requestID,
		result:    make(chan controlSettingsResult, 1),
		accepting: true,
	}
	controlSettingsSessionMu.Lock()
	if activeControlSettingsSession != nil {
		controlSettingsSessionMu.Unlock()
		return ControlSettings{}, errors.New("doubao control settings query: active session invariant violated")
	}
	activeControlSettingsSession = session
	controlSettingsSessionMu.Unlock()

	var subscription controlSettingsSubscription
	defer func() {
		deactivateControlSettingsSession(session)
		if subscription != nil {
			subscription.removeObserver()
		}
		clearControlSettingsSession(session)
		if subscription != nil {
			subscription.release()
		}
	}()

	subscription, err = transport.subscribe(session)
	if err != nil {
		return ControlSettings{}, fmt.Errorf("doubao control settings query: register observer: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ControlSettings{}, err
	}
	if err := transport.postRequest(session); err != nil {
		return ControlSettings{}, fmt.Errorf("doubao control settings query: post request: %w", err)
	}
	select {
	case <-ctx.Done():
		return ControlSettings{}, ctx.Err()
	case result := <-session.result:
		if err := ctx.Err(); err != nil {
			return ControlSettings{}, err
		}
		return result.settings, result.err
	}
}

func newControlSettingsRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	var encoded [36]byte
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return string(encoded[:]), nil
}

func canonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, digit := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((digit >= '0' && digit <= '9') || (digit >= 'a' && digit <= 'f') || (digit >= 'A' && digit <= 'F')) {
			return false
		}
	}
	return true
}

func deactivateControlSettingsSession(session *controlSettingsQuerySession) {
	controlSettingsSessionMu.Lock()
	if activeControlSettingsSession == session {
		session.accepting = false
	}
	controlSettingsSessionMu.Unlock()
}

func clearControlSettingsSession(session *controlSettingsQuerySession) {
	controlSettingsSessionMu.Lock()
	if activeControlSettingsSession == session {
		activeControlSettingsSession = nil
	}
	controlSettingsSessionMu.Unlock()
}

func receiveControlSettingsPayload(session *controlSettingsQuerySession, object string, payload []byte) {
	controlSettingsSessionMu.Lock()
	defer controlSettingsSessionMu.Unlock()
	if !controlSettingsReplyIsActiveLocked(session, object) {
		return
	}
	settings, err := decodeControlSettingsPayload(payload)
	sendControlSettingsResultLocked(session, settings, err)
}

func receiveControlSettingsError(session *controlSettingsQuerySession, object string, err error) {
	controlSettingsSessionMu.Lock()
	defer controlSettingsSessionMu.Unlock()
	if !controlSettingsReplyIsActiveLocked(session, object) {
		return
	}
	if err == nil {
		err = ErrMalformedControlSettings
	}
	sendControlSettingsResultLocked(session, ControlSettings{}, err)
}

func controlSettingsReplyIsActiveLocked(session *controlSettingsQuerySession, object string) bool {
	return session != nil && activeControlSettingsSession == session && session.accepting &&
		session.ctx.Err() == nil && object == session.requestID
}

func sendControlSettingsResultLocked(session *controlSettingsQuerySession, settings ControlSettings, err error) {
	select {
	case session.result <- controlSettingsResult{settings: settings, err: err}:
	default:
		// The first matching response wins; duplicates and later replies are ignored.
	}
}

func decodeControlSettingsPayload(payload []byte) (ControlSettings, error) {
	if len(payload) == 0 || len(payload) > maxControlSettingsPropertyListSize {
		return ControlSettings{}, fmt.Errorf("%w: plist size is outside 1..%d bytes", ErrMalformedControlSettings, maxControlSettingsPropertyListSize)
	}
	var raw any
	if _, err := plist.Unmarshal(payload, &raw); err != nil {
		return ControlSettings{}, fmt.Errorf("%w: decode property list: %v", ErrMalformedControlSettings, err)
	}
	root, ok := raw.(map[string]any)
	if !ok {
		return ControlSettings{}, fmt.Errorf("%w: top-level value must be a dictionary", ErrMalformedControlSettings)
	}
	status, ok := root["status"].(string)
	if !ok {
		return ControlSettings{}, fmt.Errorf("%w: status must be a string", ErrMalformedControlSettings)
	}
	if status == "notReady" {
		return ControlSettings{}, ErrControlSettingsNotReady
	}
	if status != "ok" {
		return ControlSettings{}, fmt.Errorf("%w: unsupported status %q", ErrMalformedControlSettings, status)
	}
	state, ok := root["state"].(map[string]any)
	if !ok {
		return ControlSettings{}, fmt.Errorf("%w: state must be a dictionary", ErrMalformedControlSettings)
	}
	voiceShortcutEnabled, ok := state["voiceShortcutEnabled"].(bool)
	if !ok {
		return ControlSettings{}, fmt.Errorf("%w: voiceShortcutEnabled must be a boolean", ErrMalformedControlSettings)
	}
	globalVoiceEnabled, ok := state["globalVoiceEnabled"].(bool)
	if !ok {
		return ControlSettings{}, fmt.Errorf("%w: globalVoiceEnabled must be a boolean", ErrMalformedControlSettings)
	}
	singleClick, err := decodeControlShortcut(state["singleClick"], "singleClick")
	if err != nil {
		return ControlSettings{}, err
	}
	longPress, err := decodeControlShortcut(state["longPress"], "longPress")
	if err != nil {
		return ControlSettings{}, err
	}
	return ControlSettings{
		VoiceShortcutEnabled: voiceShortcutEnabled,
		GlobalVoiceEnabled:   globalVoiceEnabled,
		SingleClick:          singleClick,
		LongPress:            longPress,
	}, nil
}

func decodeControlShortcut(raw any, field string) (ControlShortcutSettings, error) {
	shortcut, ok := raw.(map[string]any)
	if !ok {
		return ControlShortcutSettings{}, fmt.Errorf("%w: %s must be a dictionary", ErrMalformedControlSettings, field)
	}
	configured, ok := shortcut["configured"].(bool)
	if !ok {
		return ControlShortcutSettings{}, fmt.Errorf("%w: %s.configured must be a boolean", ErrMalformedControlSettings, field)
	}
	keyCode, ok := plistSignedInteger(shortcut["keyCode"])
	if !ok {
		return ControlShortcutSettings{}, fmt.Errorf("%w: %s.keyCode must be an integer", ErrMalformedControlSettings, field)
	}
	modifierFlags, ok := plistSignedInteger(shortcut["modifierFlags"])
	if !ok {
		return ControlShortcutSettings{}, fmt.Errorf("%w: %s.modifierFlags must be an integer", ErrMalformedControlSettings, field)
	}
	return ControlShortcutSettings{Configured: configured, KeyCode: keyCode, ModifierFlags: modifierFlags}, nil
}

func plistSignedInteger(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int8:
		return int64(number), true
	case int16:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case uint:
		if uint64(number) <= math.MaxInt64 {
			return int64(number), true
		}
	case uint8:
		return int64(number), true
	case uint16:
		return int64(number), true
	case uint32:
		return int64(number), true
	case uint64:
		if number <= math.MaxInt64 {
			return int64(number), true
		}
	}
	return 0, false
}
