// Package bridge contains the single-owner, side-effect-free state core used
// to coordinate button presses, voice sessions, and synthesized key leases.
// It deliberately has no platform, audio, timer, or goroutine dependencies.
package bridge

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidInput   = errors.New("bridge: invalid input")
	ErrInvalidBinding = errors.New("bridge: invalid binding")
	ErrUnknownEvent   = errors.New("bridge: unknown event")
)

// Key is an abstract key identifier. The core does not infer OS keycodes or
// assign special semantics to names such as Fn; an adapter owns that mapping
// and should canonicalize aliases before sending keys to this package.
type Key string

func NewKey(name string) (Key, error) {
	key := Key(name)
	if !validKey(key) {
		return "", fmt.Errorf("%w: empty or malformed key name", ErrInvalidInput)
	}
	return key, nil
}

func validKey(key Key) bool {
	name := string(key)
	return strings.TrimSpace(name) != "" && !strings.ContainsRune(name, '\x00') && utf8.ValidString(name)
}

// EndPolicy determines how a key-based target request ends. ExactEdge
// releases its held key lease on the physical release edge; AfterDrain keeps
// the lease until the adapter reports local source drain; Toggle emits a
// target tap at begin and after drain. Cancellation never guesses a toggle's
// external state.
type EndPolicy uint8

const (
	PolicyExactEdge EndPolicy = iota + 1
	PolicyAfterDrain
	PolicyToggle
)

func (p EndPolicy) valid() bool {
	return p == PolicyExactEdge || p == PolicyAfterDrain || p == PolicyToggle
}

// Binding is immutable outside this package. Keys preserves the caller's
// order, which is used for key-downs and reversed for key-ups.
type Binding struct {
	policy EndPolicy
	keys   []Key
}

func NewBinding(policy EndPolicy, keys ...Key) (Binding, error) {
	b := Binding{policy: policy, keys: cloneKeys(keys)}
	if err := b.validate(); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func (b Binding) Policy() EndPolicy { return b.policy }

func (b Binding) Keys() []Key { return cloneKeys(b.keys) }

func (b Binding) clone() Binding {
	return Binding{policy: b.policy, keys: cloneKeys(b.keys)}
}

func (b Binding) validate() error {
	if !b.policy.valid() {
		return fmt.Errorf("%w: unknown end policy", ErrInvalidBinding)
	}
	return validateKeys(b.keys, ErrInvalidBinding)
}

func cloneKeys(keys []Key) []Key {
	return append([]Key(nil), keys...)
}

func validateKeys(keys []Key, base error) error {
	if len(keys) == 0 {
		return fmt.Errorf("%w: key sequence is empty", base)
	}
	seen := make(map[Key]struct{}, len(keys))
	for _, key := range keys {
		if !validKey(key) {
			return fmt.Errorf("%w: empty or malformed key name", base)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate key in chord", base)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Device identifies one connection generation. A reconnect or replacement
// on one Core must use a strictly larger generation so callbacks from an
// earlier connection stay stale even if the same physical device returns.
type Device struct {
	id         string
	generation uint64
}

func NewDevice(id string, generation uint64) (Device, error) {
	d := Device{id: id, generation: generation}
	if !d.valid() {
		return Device{}, fmt.Errorf("%w: device id and nonzero generation are required", ErrInvalidInput)
	}
	return d, nil
}

func (d Device) ID() string         { return d.id }
func (d Device) Generation() uint64 { return d.generation }

func (d Device) valid() bool {
	return strings.TrimSpace(d.id) != "" && !strings.ContainsRune(d.id, '\x00') && utf8.ValidString(d.id) && d.generation != 0
}

// PressToken identifies one physical button press. PressID must be unique
// and monotonically increasing within a device generation.
type PressToken struct {
	device   Device
	buttonID string
	pressID  uint64
}

func NewPressToken(device Device, physicalButtonID string, pressID uint64) (PressToken, error) {
	p := PressToken{device: device, buttonID: physicalButtonID, pressID: pressID}
	if !p.valid() {
		return PressToken{}, fmt.Errorf("%w: invalid press token", ErrInvalidInput)
	}
	return p, nil
}

func (p PressToken) Device() Device           { return p.device }
func (p PressToken) PhysicalButtonID() string { return p.buttonID }
func (p PressToken) PressID() uint64          { return p.pressID }

func (p PressToken) valid() bool {
	return p.device.valid() && strings.TrimSpace(p.buttonID) != "" && !strings.ContainsRune(p.buttonID, '\x00') && utf8.ValidString(p.buttonID) && p.pressID != 0
}

// SessionToken scopes asynchronous source callbacks to one session and
// connection generation.
type SessionToken struct {
	device Device
	id     uint64
}

func (t SessionToken) Device() Device { return t.device }
func (t SessionToken) ID() uint64     { return t.id }

func (t SessionToken) valid() bool { return t.device.valid() && t.id != 0 }

// Event is sealed so adapters can only submit the event forms defined here.
// Constructors copy any supplied slices; Step still validates all payloads.
type Event interface{ bridgeEvent() }

type connectedEvent struct{ device Device }
type disconnectedEvent struct{ device Device }
type voiceBeganEvent struct{ press PressToken }
type voiceEndedEvent struct{ press PressToken }
type sourcePreparedEvent struct {
	token      SessionToken
	resourceID string
}
type sourceFailedEvent struct{ token SessionToken }
type sourceDrainedEvent struct{ token SessionToken }
type timeoutEvent struct{ token SessionToken }
type routeEvent struct{ available bool }
type scopeEvent struct{ available bool }
type configureEvent struct{ binding Binding }
type ordinaryBeganEvent struct {
	press PressToken
	keys  []Key
}
type ordinaryEndedEvent struct{ press PressToken }
type confirmToggleIdleEvent struct{ keys []Key }

func (connectedEvent) bridgeEvent()         {}
func (disconnectedEvent) bridgeEvent()      {}
func (voiceBeganEvent) bridgeEvent()        {}
func (voiceEndedEvent) bridgeEvent()        {}
func (sourcePreparedEvent) bridgeEvent()    {}
func (sourceFailedEvent) bridgeEvent()      {}
func (sourceDrainedEvent) bridgeEvent()     {}
func (timeoutEvent) bridgeEvent()           {}
func (routeEvent) bridgeEvent()             {}
func (scopeEvent) bridgeEvent()             {}
func (configureEvent) bridgeEvent()         {}
func (ordinaryBeganEvent) bridgeEvent()     {}
func (ordinaryEndedEvent) bridgeEvent()     {}
func (confirmToggleIdleEvent) bridgeEvent() {}

func DeviceConnected(device Device) Event    { return connectedEvent{device: device} }
func DeviceDisconnected(device Device) Event { return disconnectedEvent{device: device} }
func VoiceBegan(press PressToken) Event      { return voiceBeganEvent{press: press} }
func VoiceEnded(press PressToken) Event      { return voiceEndedEvent{press: press} }

// SourcePrepared reports one resource prepared for token. resourceID names a
// resource within that session; disposal effects carry both values.
func SourcePrepared(token SessionToken, resourceID string) Event {
	return sourcePreparedEvent{token: token, resourceID: resourceID}
}
func SourceFailed(token SessionToken) Event { return sourceFailedEvent{token: token} }

// SourceDrained means local source delivery has drained. It does not mean a
// target app received audio, recognized speech, or committed text.
func SourceDrained(token SessionToken) Event  { return sourceDrainedEvent{token: token} }
func SessionTimeout(token SessionToken) Event { return timeoutEvent{token: token} }
func RouteAvailable(available bool) Event     { return routeEvent{available: available} }
func ScopeAvailable(available bool) Event     { return scopeEvent{available: available} }
func Configure(binding Binding) Event         { return configureEvent{binding: binding.clone()} }
func OrdinaryBegan(press PressToken, keys ...Key) Event {
	return ordinaryBeganEvent{press: press, keys: cloneKeys(keys)}
}
func OrdinaryEnded(press PressToken) Event { return ordinaryEndedEvent{press: press} }
func ConfirmToggleIdle(keys ...Key) Event {
	return confirmToggleIdleEvent{keys: cloneKeys(keys)}
}

// Effect is a typed request for an adapter. Effects never report that a
// target app actually started, stopped, received PCM, or recognized speech.
type Effect interface{ bridgeEffect() }

type KeyDownEffect struct{ Key Key }
type KeyUpEffect struct{ Key Key }
type TargetTapEffect struct{ keys []Key }
type PrepareSourceEffect struct{ Token SessionToken }
type StopSourceEffect struct{ Token SessionToken }
type OpenGateEffect struct{ Token SessionToken }
type CloseGateEffect struct{ Token SessionToken }
type DisposeResourceEffect struct {
	Token      SessionToken
	ResourceID string
}
type BlockedEffect struct{ Reason BlockReason }

func (KeyDownEffect) bridgeEffect()         {}
func (KeyUpEffect) bridgeEffect()           {}
func (TargetTapEffect) bridgeEffect()       {}
func (PrepareSourceEffect) bridgeEffect()   {}
func (StopSourceEffect) bridgeEffect()      {}
func (OpenGateEffect) bridgeEffect()        {}
func (CloseGateEffect) bridgeEffect()       {}
func (DisposeResourceEffect) bridgeEffect() {}
func (BlockedEffect) bridgeEffect()         {}

// Keys returns the ordered chord for one atomic target tap. The adapter
// should press in this order and release in reverse, without retaining a
// lease for the tap.
func (e TargetTapEffect) Keys() []Key { return cloneKeys(e.keys) }

type BlockReason uint8

const (
	BlockTapConflictsWithHeldKey BlockReason = iota + 1
	BlockOrdinaryConflictsWithToggle
	BlockToggleRecoveryRequired
)

type Phase uint8

const (
	PhasePreparing Phase = iota + 1
	PhaseCapturing
	PhaseDraining
)

type ownerKind uint8

const (
	ownerVoice ownerKind = iota + 1
	ownerOrdinary
)

type leaseOwner struct {
	kind    ownerKind
	press   PressToken
	session SessionToken
}

type buttonSlot struct {
	device Device
	button string
}

type activePress struct {
	token PressToken
	kind  ownerKind
	keys  []Key
	owned bool
}

type sessionState struct {
	token      SessionToken
	press      PressToken
	binding    Binding
	phase      Phase
	resourceID string
}

type coreState struct {
	hasDevice      bool
	device         Device
	connected      bool
	routeKnown     bool
	routeAvailable bool
	scopeKnown     bool
	scopeAvailable bool
	lastPressID    uint64
	nextSessionID  uint64
	adopted        Binding
	pending        Binding
	hasPending     bool
	session        *sessionState
	active         map[buttonSlot]activePress
	leases         map[Key]map[leaseOwner]struct{}
	uncertain      map[Key]struct{}
}

// Core is single-owner state. Call Step and Snapshot on one serialized
// owner; audio, device and UI adapters communicate by events and effects.
type Core struct {
	state       coreState
	initialized bool
}

func NewCore(initial Binding) (*Core, error) {
	if err := initial.validate(); err != nil {
		return nil, err
	}
	return &Core{initialized: true, state: coreState{
		nextSessionID: 1,
		adopted:       initial.clone(),
		active:        make(map[buttonSlot]activePress),
		leases:        make(map[Key]map[leaseOwner]struct{}),
		uncertain:     make(map[Key]struct{}),
	}}, nil
}

// Step applies one event and returns new effects. Invalid/unknown events do
// not mutate state. Stale callbacks are harmless; stale prepared resources
// are explicitly disposed.
func (c *Core) Step(event Event) ([]Effect, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil core", ErrInvalidInput)
	}
	if !c.initialized {
		return nil, fmt.Errorf("%w: core was not constructed with NewCore", ErrInvalidInput)
	}
	effects := make([]Effect, 0, 4)
	switch e := event.(type) {
	case connectedEvent:
		if !e.device.valid() {
			return nil, fmt.Errorf("%w: invalid device", ErrInvalidInput)
		}
		c.connect(e.device, &effects)
	case disconnectedEvent:
		if !e.device.valid() {
			return nil, fmt.Errorf("%w: invalid device", ErrInvalidInput)
		}
		c.disconnect(e.device, &effects)
	case voiceBeganEvent:
		if !e.press.valid() {
			return nil, fmt.Errorf("%w: invalid voice press", ErrInvalidInput)
		}
		c.voiceBegan(e.press, &effects)
	case voiceEndedEvent:
		if !e.press.valid() {
			return nil, fmt.Errorf("%w: invalid voice release", ErrInvalidInput)
		}
		c.pressEnded(e.press, &effects)
	case sourcePreparedEvent:
		if !e.token.valid() || strings.TrimSpace(e.resourceID) == "" || strings.ContainsRune(e.resourceID, '\x00') || !utf8.ValidString(e.resourceID) {
			return nil, fmt.Errorf("%w: invalid prepared source", ErrInvalidInput)
		}
		c.sourcePrepared(e.token, e.resourceID, &effects)
	case sourceFailedEvent:
		if !e.token.valid() {
			return nil, fmt.Errorf("%w: invalid failed session", ErrInvalidInput)
		}
		if c.state.session != nil && c.state.session.token == e.token {
			c.cancelSession(&effects)
		}
	case sourceDrainedEvent:
		if !e.token.valid() {
			return nil, fmt.Errorf("%w: invalid drained session", ErrInvalidInput)
		}
		c.sourceDrained(e.token, &effects)
	case timeoutEvent:
		if !e.token.valid() {
			return nil, fmt.Errorf("%w: invalid timeout session", ErrInvalidInput)
		}
		if c.state.session != nil && c.state.session.token == e.token {
			c.cancelSession(&effects)
		}
	case routeEvent:
		if !c.state.routeKnown || c.state.routeAvailable != e.available {
			c.state.routeKnown = true
			c.state.routeAvailable = e.available
			if !e.available {
				c.cancelSession(&effects)
			}
		}
	case scopeEvent:
		if !c.state.scopeKnown || c.state.scopeAvailable != e.available {
			c.state.scopeKnown = true
			c.state.scopeAvailable = e.available
			if !e.available {
				c.cancelSession(&effects)
				c.releaseOrdinary(&effects)
			}
		}
	case configureEvent:
		if err := e.binding.validate(); err != nil {
			return nil, err
		}
		binding := e.binding.clone()
		if c.state.session == nil {
			c.state.adopted = binding
			c.state.pending = Binding{}
			c.state.hasPending = false
		} else {
			c.state.pending = binding
			c.state.hasPending = true
		}
	case ordinaryBeganEvent:
		if !e.press.valid() {
			return nil, fmt.Errorf("%w: invalid ordinary press", ErrInvalidInput)
		}
		if err := validateKeys(e.keys, ErrInvalidInput); err != nil {
			return nil, err
		}
		c.ordinaryBegan(e.press, e.keys, &effects)
	case ordinaryEndedEvent:
		if !e.press.valid() {
			return nil, fmt.Errorf("%w: invalid ordinary release", ErrInvalidInput)
		}
		c.pressEnded(e.press, &effects)
	case confirmToggleIdleEvent:
		if err := validateKeys(e.keys, ErrInvalidInput); err != nil {
			return nil, err
		}
		if c.state.session == nil {
			for _, key := range e.keys {
				delete(c.state.uncertain, key)
			}
		}
	default:
		return nil, ErrUnknownEvent
	}
	return effects, nil
}

func (c *Core) connect(device Device, effects *[]Effect) {
	if c.state.hasDevice {
		if device == c.state.device {
			// A duplicate connect must not revive a generation after disconnect.
			return
		}
		if device.generation <= c.state.device.generation {
			return
		}
		c.teardownDevice(effects)
	}
	c.state.hasDevice = true
	c.state.device = device
	c.state.connected = true
	c.state.lastPressID = 0
}

func (c *Core) disconnect(device Device, effects *[]Effect) {
	if !c.state.hasDevice || !c.state.connected || device != c.state.device {
		return
	}
	c.teardownDevice(effects)
	c.state.connected = false
}

func (c *Core) teardownDevice(effects *[]Effect) {
	c.cancelSession(effects)
	c.releaseOrdinary(effects)
	c.releaseOrphanLeases(effects)
	c.state.active = make(map[buttonSlot]activePress)
}

func (c *Core) voiceBegan(press PressToken, effects *[]Effect) {
	if !c.matchesCurrentDevice(press) || press.pressID <= c.state.lastPressID {
		return
	}
	c.state.lastPressID = press.pressID
	slot := buttonSlot{device: press.device, button: press.buttonID}
	if _, exists := c.state.active[slot]; exists {
		return
	}
	c.state.active[slot] = activePress{token: press, kind: ownerVoice}
	if !c.state.routeAvailable || !c.state.scopeAvailable || c.state.session != nil {
		return
	}
	binding := c.state.adopted.clone()
	if c.hasUncertain(binding.keys) {
		*effects = append(*effects, BlockedEffect{Reason: BlockToggleRecoveryRequired})
		return
	}
	if binding.policy == PolicyToggle {
		if c.anyLeased(binding.keys) {
			*effects = append(*effects, BlockedEffect{Reason: BlockTapConflictsWithHeldKey})
			return
		}
	}
	token := SessionToken{device: press.device, id: c.state.nextSessionID}
	c.state.nextSessionID++
	c.state.session = &sessionState{token: token, press: press, binding: binding, phase: PhasePreparing}
	if binding.policy == PolicyToggle {
		*effects = append(*effects, TargetTapEffect{keys: cloneKeys(binding.keys)})
	} else {
		c.acquire(binding.keys, leaseOwner{kind: ownerVoice, session: token}, effects)
	}
	*effects = append(*effects, PrepareSourceEffect{Token: token})
}

func (c *Core) ordinaryBegan(press PressToken, keys []Key, effects *[]Effect) {
	if !c.matchesCurrentDevice(press) || press.pressID <= c.state.lastPressID {
		return
	}
	c.state.lastPressID = press.pressID
	slot := buttonSlot{device: press.device, button: press.buttonID}
	if _, exists := c.state.active[slot]; exists {
		return
	}
	active := activePress{token: press, kind: ownerOrdinary, keys: cloneKeys(keys)}
	c.state.active[slot] = active
	if !c.state.scopeAvailable {
		return
	}
	if c.hasUncertain(keys) {
		*effects = append(*effects, BlockedEffect{Reason: BlockToggleRecoveryRequired})
		return
	}
	if s := c.state.session; s != nil && s.binding.policy == PolicyToggle && intersects(keys, s.binding.keys) {
		*effects = append(*effects, BlockedEffect{Reason: BlockOrdinaryConflictsWithToggle})
		return
	}
	owner := leaseOwner{kind: ownerOrdinary, press: press}
	c.acquire(keys, owner, effects)
	active.owned = true
	c.state.active[slot] = active
}

func (c *Core) pressEnded(press PressToken, effects *[]Effect) {
	if !c.matchesCurrentDevice(press) {
		return
	}
	// Reserve an observed press sequence even when its matching down event
	// was lost or reordered. A later stale began must not open a session.
	if press.pressID > c.state.lastPressID {
		c.state.lastPressID = press.pressID
	}
	slot := buttonSlot{device: press.device, button: press.buttonID}
	active, exists := c.state.active[slot]
	if !exists || active.token != press {
		return
	}
	delete(c.state.active, slot)
	if active.kind == ownerOrdinary {
		if active.owned {
			c.releaseKeys(active.keys, leaseOwner{kind: ownerOrdinary, press: press}, effects)
		}
		return
	}
	s := c.state.session
	if s == nil || s.press != press {
		return
	}
	if s.resourceID == "" {
		// A release before source readiness cannot open a gate later. Cancel
		// the pending preparation; any late resource callback is disposed.
		c.cancelSession(effects)
		return
	}
	if s.binding.policy == PolicyExactEdge {
		c.releaseKeys(s.binding.keys, leaseOwner{kind: ownerVoice, session: s.token}, effects)
	}
	s.phase = PhaseDraining
	*effects = append(*effects, StopSourceEffect{Token: s.token})
}

func (c *Core) sourcePrepared(token SessionToken, resourceID string, effects *[]Effect) {
	s := c.state.session
	if s == nil || s.token != token {
		*effects = append(*effects, DisposeResourceEffect{Token: token, ResourceID: resourceID})
		return
	}
	if s.resourceID != "" {
		if s.resourceID != resourceID {
			*effects = append(*effects, DisposeResourceEffect{Token: token, ResourceID: resourceID})
		}
		return
	}
	if s.phase != PhasePreparing {
		*effects = append(*effects, DisposeResourceEffect{Token: token, ResourceID: resourceID})
		return
	}
	s.resourceID = resourceID
	s.phase = PhaseCapturing
	*effects = append(*effects, OpenGateEffect{Token: token})
}

func (c *Core) sourceDrained(token SessionToken, effects *[]Effect) {
	s := c.state.session
	if s == nil || s.token != token || s.phase != PhaseDraining || s.resourceID == "" {
		return
	}
	if s.binding.policy == PolicyAfterDrain {
		c.releaseKeys(s.binding.keys, leaseOwner{kind: ownerVoice, session: token}, effects)
	}
	if s.binding.policy == PolicyToggle {
		*effects = append(*effects, TargetTapEffect{keys: cloneKeys(s.binding.keys)})
	}
	*effects = append(*effects, CloseGateEffect{Token: token})
	*effects = append(*effects, DisposeResourceEffect{Token: token, ResourceID: s.resourceID})
	c.finishSession()
}

func (c *Core) cancelSession(effects *[]Effect) {
	s := c.state.session
	if s == nil {
		return
	}
	if s.binding.policy == PolicyToggle {
		for _, key := range s.binding.keys {
			c.state.uncertain[key] = struct{}{}
		}
	} else {
		c.releaseKeys(s.binding.keys, leaseOwner{kind: ownerVoice, session: s.token}, effects)
	}
	*effects = append(*effects, CloseGateEffect{Token: s.token}, StopSourceEffect{Token: s.token})
	if s.resourceID != "" {
		*effects = append(*effects, DisposeResourceEffect{Token: s.token, ResourceID: s.resourceID})
	}
	c.finishSession()
}

func (c *Core) finishSession() {
	c.state.session = nil
	if c.state.hasPending {
		c.state.adopted = c.state.pending.clone()
		c.state.pending = Binding{}
		c.state.hasPending = false
	}
}

func (c *Core) acquire(keys []Key, owner leaseOwner, effects *[]Effect) {
	for _, key := range keys {
		owners := c.state.leases[key]
		if owners == nil {
			owners = make(map[leaseOwner]struct{})
			c.state.leases[key] = owners
		}
		if len(owners) == 0 {
			*effects = append(*effects, KeyDownEffect{Key: key})
		}
		owners[owner] = struct{}{}
	}
}

func (c *Core) releaseKeys(keys []Key, owner leaseOwner, effects *[]Effect) {
	for i := len(keys) - 1; i >= 0; i-- {
		key := keys[i]
		owners := c.state.leases[key]
		if _, exists := owners[owner]; !exists {
			continue
		}
		delete(owners, owner)
		if len(owners) == 0 {
			delete(c.state.leases, key)
			*effects = append(*effects, KeyUpEffect{Key: key})
		}
	}
}

func (c *Core) releaseOrdinary(effects *[]Effect) {
	presses := make([]activePress, 0)
	for _, active := range c.state.active {
		if active.kind == ownerOrdinary && active.owned {
			presses = append(presses, active)
		}
	}
	sort.Slice(presses, func(i, j int) bool { return presses[i].token.pressID < presses[j].token.pressID })
	for _, active := range presses {
		c.releaseKeys(active.keys, leaseOwner{kind: ownerOrdinary, press: active.token}, effects)
		active.owned = false
		slot := buttonSlot{device: active.token.device, button: active.token.buttonID}
		c.state.active[slot] = active
	}
}

func (c *Core) releaseOrphanLeases(effects *[]Effect) {
	keys := make([]Key, 0, len(c.state.leases))
	for key := range c.state.leases {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, key := range keys {
		delete(c.state.leases, key)
		*effects = append(*effects, KeyUpEffect{Key: key})
	}
}

func (c *Core) anyLeased(keys []Key) bool {
	for _, key := range keys {
		if len(c.state.leases[key]) > 0 {
			return true
		}
	}
	return false
}

func (c *Core) hasUncertain(keys []Key) bool {
	for _, key := range keys {
		if _, exists := c.state.uncertain[key]; exists {
			return true
		}
	}
	return false
}

func (c *Core) matchesCurrentDevice(press PressToken) bool {
	return c.state.hasDevice && c.state.connected && press.device == c.state.device
}

func intersects(left, right []Key) bool {
	set := make(map[Key]struct{}, len(left))
	for _, key := range left {
		set[key] = struct{}{}
	}
	for _, key := range right {
		if _, exists := set[key]; exists {
			return true
		}
	}
	return false
}

type SessionSnapshot struct {
	Token   SessionToken
	Press   PressToken
	Binding Binding
	Phase   Phase
}

// Snapshot contains copies only. No slice or map aliases mutable core state.
type Snapshot struct {
	HasDevice      bool
	DeviceID       string
	Generation     uint64
	Connected      bool
	RouteKnown     bool
	RouteAvailable bool
	ScopeKnown     bool
	ScopeAvailable bool
	Adopted        Binding
	HasPending     bool
	Pending        Binding
	HasSession     bool
	Session        SessionSnapshot
	HeldKeys       []Key
	RecoveryKeys   []Key
}

func (c *Core) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	s := Snapshot{
		HasDevice:      c.state.hasDevice,
		Connected:      c.state.connected,
		RouteKnown:     c.state.routeKnown,
		RouteAvailable: c.state.routeAvailable,
		ScopeKnown:     c.state.scopeKnown,
		ScopeAvailable: c.state.scopeAvailable,
		Adopted:        c.state.adopted.clone(),
		HasPending:     c.state.hasPending,
		Pending:        c.state.pending.clone(),
		HasSession:     c.state.session != nil,
	}
	if c.state.hasDevice {
		s.DeviceID = c.state.device.id
		s.Generation = c.state.device.generation
	}
	if c.state.hasPending {
		s.Pending = c.state.pending.clone()
	}
	if current := c.state.session; current != nil {
		s.Session = SessionSnapshot{
			Token:   current.token,
			Press:   current.press,
			Binding: current.binding.clone(),
			Phase:   current.phase,
		}
	}
	for key := range c.state.leases {
		s.HeldKeys = append(s.HeldKeys, key)
	}
	for key := range c.state.uncertain {
		s.RecoveryKeys = append(s.RecoveryKeys, key)
	}
	sort.Slice(s.HeldKeys, func(i, j int) bool { return s.HeldKeys[i] < s.HeldKeys[j] })
	sort.Slice(s.RecoveryKeys, func(i, j int) bool { return s.RecoveryKeys[i] < s.RecoveryKeys[j] })
	return s
}
