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

// EndPolicy determines how a voice session interacts with a target. The
// key-based policies release their held key lease at the physical edge or
// source-drain boundary, or use a toggle tap. Audio has no target key output.
// Cancellation never guesses a toggle's external state.
type EndPolicy uint8

const (
	PolicyExactEdge EndPolicy = iota + 1
	PolicyAfterDrain
	PolicyToggle
	PolicyAudio
)

func (p EndPolicy) valid() bool {
	return p == PolicyExactEdge || p == PolicyAfterDrain || p == PolicyToggle || p == PolicyAudio
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

// NewAudioBinding creates an immutable voice binding that manages an audio
// source without requesting any keyboard output.
func NewAudioBinding() Binding { return Binding{policy: PolicyAudio} }

func (b Binding) Policy() EndPolicy { return b.policy }

func (b Binding) Keys() []Key { return cloneKeys(b.keys) }

func (b Binding) clone() Binding {
	return Binding{policy: b.policy, keys: cloneKeys(b.keys)}
}

func (b Binding) validate() error {
	if !b.policy.valid() {
		return fmt.Errorf("%w: unknown end policy", ErrInvalidBinding)
	}
	if b.policy == PolicyAudio {
		if len(b.keys) != 0 {
			return fmt.Errorf("%w: audio binding cannot contain keys", ErrInvalidBinding)
		}
		return nil
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
type observedBeganEvent struct{ press PressToken }
type targetActionBeganEvent struct {
	press PressToken
	plan  ActionPlan
}
type cancelToolActionsEvent struct{ toolID string }
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
type outputFailedEvent struct {
	failureID           uint64
	uncertainToggleKeys []Key
}
type outputRecoveredEvent struct{ failureID uint64 }
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
func (observedBeganEvent) bridgeEvent()     {}
func (targetActionBeganEvent) bridgeEvent() {}
func (cancelToolActionsEvent) bridgeEvent() {}
func (sourcePreparedEvent) bridgeEvent()    {}
func (sourceFailedEvent) bridgeEvent()      {}
func (sourceDrainedEvent) bridgeEvent()     {}
func (timeoutEvent) bridgeEvent()           {}
func (routeEvent) bridgeEvent()             {}
func (scopeEvent) bridgeEvent()             {}
func (configureEvent) bridgeEvent()         {}
func (outputFailedEvent) bridgeEvent()      {}
func (outputRecoveredEvent) bridgeEvent()   {}
func (ordinaryBeganEvent) bridgeEvent()     {}
func (ordinaryEndedEvent) bridgeEvent()     {}
func (confirmToggleIdleEvent) bridgeEvent() {}

func DeviceConnected(device Device) Event    { return connectedEvent{device: device} }
func DeviceDisconnected(device Device) Event { return disconnectedEvent{device: device} }
func VoiceBegan(press PressToken) Event      { return voiceBeganEvent{press: press} }
func VoiceEnded(press PressToken) Event      { return voiceEndedEvent{press: press} }

// ObservedBegan records a physical press that has no adopted ordinary-key
// mapping. It never acquires a key lease; release it with OrdinaryEnded.
func ObservedBegan(press PressToken) Event { return observedBeganEvent{press: press} }

// TargetActionBegan freezes one resolved tool action to a physical press.
// Release and interruption use the same active-press ledger as ordinary keys.
func TargetActionBegan(press PressToken, plan ActionPlan) Event {
	return targetActionBeganEvent{press: press, plan: plan.clone()}
}

// CancelToolActions interrupts active actions for toolID. It only releases
// local key leases; it never guesses a target-side cancel action.
func CancelToolActions(toolID string) Event { return cancelToolActionsEvent{toolID: toolID} }

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

// OutputFailed reports a failure from the system key-output sink. failureID
// must be nonzero and increase monotonically for each distinct failure within
// this Core's lifetime. If a TargetTapEffect may have been partially submitted,
// pass its full chord in uncertainToggleKeys. The returned effects cancel work
// and release logical key leases; the physical press ledger remains until
// matching releases arrive.
func OutputFailed(failureID uint64, uncertainToggleKeys ...Key) Event {
	return outputFailedEvent{failureID: failureID, uncertainToggleKeys: cloneKeys(uncertainToggleKeys)}
}

// OutputRecovered reports that the sink has successfully cleaned every key it
// may have submitted for failureID. Output remains blocked until all presses
// recorded during that failure have ended or their device is retired.
func OutputRecovered(failureID uint64) Event { return outputRecoveredEvent{failureID: failureID} }

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
	BlockActionScopeUnsupported
	BlockActionConflictsWithToggle
	BlockActionConflictsWithHeldKey
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
	ownerObserved
	ownerAction
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
	plan  ActionPlan
}

type sessionState struct {
	token      SessionToken
	press      PressToken
	binding    Binding
	phase      Phase
	resourceID string
}

type coreState struct {
	hasDevice           bool
	device              Device
	connected           bool
	routeKnown          bool
	routeAvailable      bool
	scopeKnown          bool
	scopeAvailable      bool
	lastPressID         uint64
	nextSessionID       uint64
	configured          bool
	adopted             Binding
	pending             Binding
	hasPending          bool
	lastOutputFailureID uint64
	outputFailureID     uint64
	outputRecovered     bool
	session             *sessionState
	lastAction          *ActionSnapshot
	active              map[buttonSlot]activePress
	leases              map[Key]map[leaseOwner]struct{}
	uncertain           map[Key]struct{}
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
	return newCore(initial, true), nil
}

// NewUnconfiguredCore creates a usable core with no adopted voice binding.
// Physical presses can still be observed, but voice presses cannot start
// sessions or request key/audio effects until Configure adopts a binding.
func NewUnconfiguredCore() *Core {
	return newCore(Binding{}, false)
}

func newCore(initial Binding, configured bool) *Core {
	return &Core{initialized: true, state: coreState{
		nextSessionID: 1,
		configured:    configured,
		adopted:       initial.clone(),
		active:        make(map[buttonSlot]activePress),
		leases:        make(map[Key]map[leaseOwner]struct{}),
		uncertain:     make(map[Key]struct{}),
	}}
}

// Step applies one event and returns new effects. Invalid/unknown events do
// not mutate state. Stale callbacks are harmless; stale prepared resources
// are explicitly disposed.
func (c *Core) Step(event Event) ([]Effect, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil core", ErrInvalidInput)
	}
	if !c.initialized {
		return nil, fmt.Errorf("%w: zero-value core must be constructed with NewCore or NewUnconfiguredCore", ErrInvalidInput)
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
	case observedBeganEvent:
		if !e.press.valid() {
			return nil, fmt.Errorf("%w: invalid observed press", ErrInvalidInput)
		}
		c.beginPress(e.press, ownerObserved, nil)
	case targetActionBeganEvent:
		if !e.press.valid() {
			return nil, fmt.Errorf("%w: invalid action press", ErrInvalidInput)
		}
		if err := e.plan.validate(); err != nil {
			return nil, err
		}
		c.targetActionBegan(e.press, e.plan, &effects)
	case cancelToolActionsEvent:
		if !validKey(Key(e.toolID)) {
			return nil, fmt.Errorf("%w: invalid tool ID", ErrInvalidInput)
		}
		c.cancelToolActions(e.toolID, &effects)
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
				if c.state.session != nil && c.state.session.binding.policy != PolicyAudio {
					c.cancelSession(&effects)
				}
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
		c.state.configured = true
	case outputFailedEvent:
		if e.failureID == 0 {
			return nil, fmt.Errorf("%w: output failure ID is required", ErrInvalidInput)
		}
		if len(e.uncertainToggleKeys) != 0 {
			if err := validateKeys(e.uncertainToggleKeys, ErrInvalidInput); err != nil {
				return nil, err
			}
		}
		c.outputFailed(e.failureID, e.uncertainToggleKeys, &effects)
	case outputRecoveredEvent:
		if e.failureID == 0 {
			return nil, fmt.Errorf("%w: output recovery ID is required", ErrInvalidInput)
		}
		c.outputRecovered(e.failureID)
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
	c.finishOutputRecovery()
}

func (c *Core) voiceBegan(press PressToken, effects *[]Effect) {
	if !c.beginPress(press, ownerVoice, nil) {
		return
	}
	allowed, reason := c.voiceStartAdmission()
	if reason != 0 {
		*effects = append(*effects, BlockedEffect{Reason: reason})
	}
	if !allowed {
		return
	}
	binding := c.state.adopted.clone()
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

// voiceStartAdmission is the side-effect-free session admission rule shared
// by CanBeginVoice and voiceBegan. reason is zero when the existing behavior
// rejects silently; otherwise it preserves the BlockedEffect reason/order.
func (c *Core) voiceStartAdmission() (allowed bool, reason BlockReason) {
	if c == nil || !c.initialized || c.state.outputFailureID != 0 || !c.state.configured {
		return false, 0
	}
	if c.hasActionLease() {
		return false, BlockActionConflictsWithHeldKey
	}
	if !c.state.routeAvailable || c.state.session != nil {
		return false, 0
	}
	binding := c.state.adopted
	if binding.policy != PolicyAudio && !c.state.scopeAvailable {
		return false, 0
	}
	if c.hasUncertain(binding.keys) {
		return false, BlockToggleRecoveryRequired
	}
	if binding.policy == PolicyToggle && c.anyLeased(binding.keys) {
		return false, BlockTapConflictsWithHeldKey
	}
	return true, 0
}

// CanBeginVoice reports whether VoiceBegan for press would pass this Core's
// current replay, device, button-slot, and session-admission checks. It does
// not reserve the press or mutate Core state; callers must serialize it with
// Configure and the subsequent VoiceBegan on the Core owner thread.
func (c *Core) CanBeginVoice(press PressToken) bool {
	if c == nil || !c.initialized || !press.valid() || !c.matchesCurrentDevice(press) || press.pressID <= c.state.lastPressID {
		return false
	}
	slot := buttonSlot{device: press.device, button: press.buttonID}
	if _, exists := c.state.active[slot]; exists {
		return false
	}
	allowed, _ := c.voiceStartAdmission()
	return allowed
}

func (c *Core) ordinaryBegan(press PressToken, keys []Key, effects *[]Effect) {
	if !c.beginPress(press, ownerOrdinary, keys) {
		return
	}
	slot := buttonSlot{device: press.device, button: press.buttonID}
	active := c.state.active[slot]
	if c.state.outputFailureID != 0 {
		return
	}
	if !c.state.scopeAvailable {
		return
	}
	if c.hasActionLease() {
		*effects = append(*effects, BlockedEffect{Reason: BlockActionConflictsWithHeldKey})
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

func (c *Core) targetActionBegan(press PressToken, plan ActionPlan, effects *[]Effect) {
	if !c.beginPress(press, ownerAction, plan.keys) {
		return
	}
	slot := buttonSlot{device: press.device, button: press.buttonID}
	active := c.state.active[slot]
	active.plan = plan.clone()
	c.state.active[slot] = active

	if plan.spec.Scope == ActionApp {
		c.recordAction(press, plan, actionStatusInterrupted, RequestNone)
		*effects = append(*effects, BlockedEffect{Reason: BlockActionScopeUnsupported})
		return
	}
	if c.state.outputFailureID != 0 || !c.state.scopeAvailable {
		c.recordAction(press, plan, actionStatusInterrupted, RequestNone)
		return
	}
	if c.hasUncertain(plan.keys) {
		c.recordAction(press, plan, actionStatusInterrupted, RequestNone)
		*effects = append(*effects, BlockedEffect{Reason: BlockToggleRecoveryRequired})
		return
	}
	if session := c.state.session; session != nil && session.binding.policy == PolicyToggle && intersects(plan.keys, session.binding.keys) {
		c.recordAction(press, plan, actionStatusInterrupted, RequestNone)
		*effects = append(*effects, BlockedEffect{Reason: BlockActionConflictsWithToggle})
		return
	}

	if len(c.state.leases) != 0 {
		c.recordAction(press, plan, actionStatusInterrupted, RequestNone)
		*effects = append(*effects, BlockedEffect{Reason: BlockActionConflictsWithHeldKey})
		return
	}

	if plan.spec.Trigger == ActionTap {
		c.recordAction(press, plan, actionStatusActive, plan.spec.Request)
		*effects = append(*effects, TargetTapEffect{keys: plan.Keys()})
		return
	}

	c.acquire(plan.keys, leaseOwner{kind: ownerAction, press: press}, effects)
	active.owned = true
	c.state.active[slot] = active
	c.recordAction(press, plan, actionStatusActive, plan.spec.Request)
}

func (c *Core) cancelToolActions(toolID string, effects *[]Effect) {
	presses := make([]activePress, 0)
	for _, active := range c.state.active {
		if active.kind == ownerAction && active.plan.spec.ToolID == toolID {
			presses = append(presses, active)
		}
	}
	sort.Slice(presses, func(i, j int) bool { return presses[i].token.pressID < presses[j].token.pressID })
	for _, active := range presses {
		if active.owned {
			c.releaseKeys(active.keys, leaseOwner{kind: ownerAction, press: active.token}, effects)
		}
		c.interruptAction(active.token)
		active.owned = false
		slot := buttonSlot{device: active.token.device, button: active.token.buttonID}
		c.state.active[slot] = active
	}
}

func (c *Core) recordAction(press PressToken, plan ActionPlan, status string, request TargetRequest) {
	c.state.lastAction = &ActionSnapshot{
		Press:         press,
		Plan:          plan.clone(),
		Status:        status,
		Request:       request,
		TargetOutcome: actionOutcomeUnknown,
	}
}

func (c *Core) interruptAction(press PressToken) {
	last := c.state.lastAction
	if last == nil || last.Press != press || last.Status != actionStatusActive {
		return
	}
	last.Status = actionStatusInterrupted
	last.Request = RequestNone
}

// beginPress applies the per-connection replay gate shared by every kind of
// physical press, then records the token in the active button slot.
func (c *Core) beginPress(press PressToken, kind ownerKind, keys []Key) bool {
	if !c.matchesCurrentDevice(press) || press.pressID <= c.state.lastPressID {
		return false
	}
	c.state.lastPressID = press.pressID
	slot := buttonSlot{device: press.device, button: press.buttonID}
	if _, exists := c.state.active[slot]; exists {
		return false
	}
	c.state.active[slot] = activePress{token: press, kind: kind, keys: cloneKeys(keys)}
	return true
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
	c.finishOutputRecovery()
	if active.kind == ownerObserved {
		return
	}
	if active.kind == ownerAction {
		if active.owned {
			c.releaseKeys(active.keys, leaseOwner{kind: ownerAction, press: press}, effects)
		}
		if last := c.state.lastAction; last != nil && last.Press == press && last.Status == actionStatusActive {
			if active.plan.spec.Trigger == ActionHold {
				last.Request = active.plan.spec.ReleaseRequest
			}
			last.Status = actionStatusReleased
		}
		return
	}
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
	canDrainBeforePrepared := s.binding.policy == PolicyAfterDrain || s.binding.policy == PolicyToggle || s.binding.policy == PolicyAudio
	if s.resourceID == "" && !canDrainBeforePrepared {
		// Exact-edge releases its target keys with the physical edge. If source
		// readiness has not arrived yet, cancel preparation so a late callback
		// cannot start capture after the target action has already ended. Only
		// the policies that finish at the drain boundary keep this preparation.
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
	wasPreparing := s.phase == PhasePreparing
	drainingBeforePrepared := s.phase == PhaseDraining && s.resourceID == "" &&
		(s.binding.policy == PolicyAfterDrain || s.binding.policy == PolicyToggle || s.binding.policy == PolicyAudio)
	if !wasPreparing && !drainingBeforePrepared {
		*effects = append(*effects, DisposeResourceEffect{Token: token, ResourceID: resourceID})
		return
	}
	s.resourceID = resourceID
	if wasPreparing {
		s.phase = PhaseCapturing
	}
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

func (c *Core) outputFailed(failureID uint64, uncertainToggleKeys []Key, effects *[]Effect) {
	if failureID <= c.state.lastOutputFailureID {
		return
	}
	c.state.lastOutputFailureID = failureID
	c.state.outputFailureID = failureID
	c.state.outputRecovered = false
	for _, key := range uncertainToggleKeys {
		c.state.uncertain[key] = struct{}{}
	}
	c.cancelSession(effects)
	c.releaseOrdinary(effects)
	c.releaseOrphanLeases(effects)
}

func (c *Core) outputRecovered(failureID uint64) {
	if c.state.outputFailureID != failureID {
		return
	}
	c.state.outputRecovered = true
	c.finishOutputRecovery()
}

func (c *Core) finishOutputRecovery() {
	if c.state.outputFailureID == 0 || !c.state.outputRecovered || len(c.state.active) != 0 {
		return
	}
	c.state.outputFailureID = 0
	c.state.outputRecovered = false
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
		if (active.kind == ownerOrdinary && active.owned) || active.kind == ownerAction {
			presses = append(presses, active)
		}
	}
	sort.Slice(presses, func(i, j int) bool { return presses[i].token.pressID < presses[j].token.pressID })
	for _, active := range presses {
		if active.owned {
			kind := ownerOrdinary
			if active.kind == ownerAction {
				kind = ownerAction
			}
			c.releaseKeys(active.keys, leaseOwner{kind: kind, press: active.token}, effects)
		}
		if active.kind == ownerAction {
			c.interruptAction(active.token)
		}
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

func (c *Core) hasActionLease() bool {
	for _, owners := range c.state.leases {
		for owner := range owners {
			if owner.kind == ownerAction {
				return true
			}
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
	// Configured reports whether an adopted voice binding exists. It does not
	// describe mappings for ordinary physical buttons.
	Configured bool
	// OutputFailureID is nonzero while system key output is faulted or waiting
	// for the sink to confirm cleanup and all physical presses to end.
	OutputFailureID uint64
	Adopted         Binding
	HasPending      bool
	Pending         Binding
	HasSession      bool
	Session         SessionSnapshot
	LastAction      *ActionSnapshot
	// PressedButtons lists every physical press still active, regardless of
	// whether it owns a voice session or an ordinary key lease.
	PressedButtons []PressToken
	HeldKeys       []Key
	RecoveryKeys   []Key
}

func (c *Core) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	s := Snapshot{
		HasDevice:       c.state.hasDevice,
		Connected:       c.state.connected,
		RouteKnown:      c.state.routeKnown,
		RouteAvailable:  c.state.routeAvailable,
		ScopeKnown:      c.state.scopeKnown,
		ScopeAvailable:  c.state.scopeAvailable,
		Configured:      c.state.configured,
		OutputFailureID: c.state.outputFailureID,
		Adopted:         c.state.adopted.clone(),
		HasPending:      c.state.hasPending,
		Pending:         c.state.pending.clone(),
		HasSession:      c.state.session != nil,
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
	if last := c.state.lastAction; last != nil {
		action := last.clone()
		s.LastAction = &action
	}
	for _, active := range c.state.active {
		s.PressedButtons = append(s.PressedButtons, active.token)
	}
	for key := range c.state.leases {
		s.HeldKeys = append(s.HeldKeys, key)
	}
	for key := range c.state.uncertain {
		s.RecoveryKeys = append(s.RecoveryKeys, key)
	}
	sort.Slice(s.HeldKeys, func(i, j int) bool { return s.HeldKeys[i] < s.HeldKeys[j] })
	sort.Slice(s.RecoveryKeys, func(i, j int) bool { return s.RecoveryKeys[i] < s.RecoveryKeys[j] })
	sort.Slice(s.PressedButtons, func(i, j int) bool {
		left, right := s.PressedButtons[i], s.PressedButtons[j]
		if left.pressID != right.pressID {
			return left.pressID < right.pressID
		}
		if left.device.id != right.device.id {
			return left.device.id < right.device.id
		}
		if left.device.generation != right.device.generation {
			return left.device.generation < right.device.generation
		}
		return left.buttonID < right.buttonID
	})
	return s
}
