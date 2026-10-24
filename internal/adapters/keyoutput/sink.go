package keyoutput

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"mimac.local/mimac/internal/bridge"
)

var (
	ErrPermissionRequired = errors.New("keyoutput: post-event access is required")
	ErrUnknownTarget      = errors.New("keyoutput: unknown target")
	ErrSinkClosed         = errors.New("keyoutput: sink is closed")
	ErrSinkFaulted        = errors.New("keyoutput: sink is faulted; release held keys to recover")
)

// Target selects either the production HID event stream or a self-only probe.
// SelfTarget is fixed to this process; callers cannot supply another PID.
type Target uint8

const (
	GlobalTarget Target = iota + 1
	SelfTarget
)

const (
	privateEventSourceState int32  = -1
	hidEventTapLocation     int32  = 0
	eventSourceUserData     uint32 = 42
	eventMarker             int64  = 0x4d694d61634b6579
)

// eventAPI isolates CoreGraphics so the state machine can be tested without
// sending system events.
type eventAPI struct {
	preflight      func() bool
	request        func() bool
	sourceCreate   func(int32) uintptr
	sourceUserData func(uintptr, int64)
	suppression    func(uintptr, float64)
	eventCreate    func(uintptr, uint16, bool) uintptr
	setFlags       func(uintptr, uint64)
	setInteger     func(uintptr, uint32, int64)
	postGlobal     func(int32, uintptr)
	postToPID      func(int32, uintptr)
	release        func(uintptr)
}

var defaultAPI struct {
	sync.Once
	api *eventAPI
	err error
}

func getDefaultAPI() (*eventAPI, error) {
	defaultAPI.Do(func() { defaultAPI.api, defaultAPI.err = loadEventAPI() })
	return defaultAPI.api, defaultAPI.err
}

// HasAccess reports the current Post Event permission state. It never requests
// permission; call RequestAccess only from an explicit user action.
func HasAccess() bool {
	api, err := getDefaultAPI()
	return err == nil && hasAccess(api)
}

// RequestAccess asks macOS for Post Event permission. Call it only from an
// explicit user action such as an Enable Output button.
func RequestAccess() bool {
	api, err := getDefaultAPI()
	if err != nil {
		return false
	}
	return requestAccess(api)
}

func hasAccess(api *eventAPI) bool {
	return api != nil && api.preflight != nil && api.preflight()
}

func requestAccess(api *eventAPI) bool {
	if api == nil || api.preflight == nil || api.request == nil {
		return false
	}
	if api.preflight() {
		return true
	}
	return api.request()
}

// Open creates a private event source after verifying permission. It never
// prompts automatically. GlobalTarget posts to the system HID event stream;
// SelfTarget posts only to this process for controlled verification.
func Open(target Target) (*Sink, error) {
	if target != GlobalTarget && target != SelfTarget {
		return nil, fmt.Errorf("%w: %d", ErrUnknownTarget, target)
	}
	api, err := getDefaultAPI()
	if err != nil {
		return nil, err
	}
	return openWithAPI(target, api)
}

// Sink serializes key effects on its caller's thread. After an error it keeps
// the last committed held-key ledger and rejects Submit until ReleaseAll
// succeeds. A posted stroke is recorded as submitted, not as delivered.
type Sink struct {
	target Target
	api    *eventAPI
	source uintptr
	held   []bridge.Key
	fault  error
	closed bool
}

func openWithAPI(target Target, api *eventAPI) (*Sink, error) {
	if target != GlobalTarget && target != SelfTarget {
		return nil, fmt.Errorf("%w: %d", ErrUnknownTarget, target)
	}
	if !validAPI(api, target) {
		return nil, errors.New("keyoutput: incomplete CoreGraphics API")
	}
	if !api.preflight() {
		return nil, ErrPermissionRequired
	}
	source := api.sourceCreate(privateEventSourceState)
	if source == 0 {
		return nil, errors.New("keyoutput: private event source allocation failed")
	}
	api.sourceUserData(source, eventMarker)
	api.suppression(source, 0)
	return &Sink{target: target, api: api, source: source}, nil
}

func validAPI(api *eventAPI, target Target) bool {
	if api == nil || api.preflight == nil || api.sourceCreate == nil || api.sourceUserData == nil ||
		api.suppression == nil || api.eventCreate == nil || api.setFlags == nil ||
		api.setInteger == nil || api.release == nil {
		return false
	}
	if target == GlobalTarget {
		return api.postGlobal != nil
	}
	return target == SelfTarget && api.postToPID != nil
}

// Submit validates the entire Core batch, rechecks permission, allocates and
// annotates every event, then posts the batch. Any error preserves held and
// fault-locks normal submissions until ReleaseAll succeeds.
func (s *Sink) Submit(effects []bridge.Effect) error {
	if s == nil {
		return errors.New("keyoutput: nil sink")
	}
	if s.closed {
		return ErrSinkClosed
	}
	if s.fault != nil {
		return errors.Join(ErrSinkFaulted, s.fault)
	}
	strokes, nextHeld, err := PlanKeys(s.held, effects)
	if err != nil {
		return s.fail(err)
	}
	if len(strokes) == 0 {
		return nil
	}
	if err := s.postBatch(strokes); err != nil {
		return s.fail(err)
	}
	s.held = nextHeld
	return nil
}

// ReleaseAll submits reverse-order key-ups for the last committed held ledger.
// It is allowed while faulted and can be retried; only success clears held and
// the fault. The source remains open after any failure.
func (s *Sink) ReleaseAll() error {
	if s == nil {
		return errors.New("keyoutput: nil sink")
	}
	if s.closed {
		return ErrSinkClosed
	}
	if len(s.held) == 0 {
		s.fault = nil
		return nil
	}
	effects := make([]bridge.Effect, 0, len(s.held))
	for i := len(s.held) - 1; i >= 0; i-- {
		effects = append(effects, bridge.KeyUpEffect{Key: s.held[i]})
	}
	strokes, nextHeld, err := PlanKeys(s.held, effects)
	if err != nil {
		return s.fail(err)
	}
	if err := s.postBatch(strokes); err != nil {
		return s.fail(err)
	}
	s.held = nextHeld
	s.fault = nil
	return nil
}

// Close releases the committed held keys before releasing the source. If
// ReleaseAll fails, the source and held ledger remain intact for a retry.
func (s *Sink) Close() error {
	if s == nil || s.closed {
		return nil
	}
	if err := s.ReleaseAll(); err != nil {
		return err
	}
	s.api.release(s.source)
	s.source = 0
	s.closed = true
	return nil
}

// Held returns a copy of the last fully submitted key ledger.
func (s *Sink) Held() []bridge.Key {
	if s == nil {
		return nil
	}
	return append([]bridge.Key(nil), s.held...)
}

func (s *Sink) fail(err error) error {
	if s.fault == nil {
		s.fault = err
	}
	return err
}

func (s *Sink) postBatch(strokes []Stroke) error {
	if s.source == 0 {
		return errors.New("keyoutput: event source is unavailable")
	}
	if !s.api.preflight() {
		return ErrPermissionRequired
	}
	events := make([]uintptr, 0, len(strokes))
	defer func() {
		for _, event := range events {
			s.api.release(event)
		}
	}()
	for _, stroke := range strokes {
		event := s.api.eventCreate(s.source, stroke.Code, stroke.Down)
		if event == 0 {
			return fmt.Errorf("keyoutput: event allocation failed for %s; batch not posted", stroke.Key)
		}
		events = append(events, event)
		s.api.setFlags(event, stroke.Flags)
		s.api.setInteger(event, eventSourceUserData, eventMarker)
	}
	for _, event := range events {
		switch s.target {
		case GlobalTarget:
			s.api.postGlobal(hidEventTapLocation, event)
		case SelfTarget:
			s.api.postToPID(int32(os.Getpid()), event)
		}
	}
	return nil
}
