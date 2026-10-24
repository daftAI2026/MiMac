package bridge

import "fmt"

type ActionScope string

const (
	ActionGlobal ActionScope = "global"
	ActionApp    ActionScope = "app"
)

type ActionTrigger string

const (
	ActionTap  ActionTrigger = "tap"
	ActionHold ActionTrigger = "hold"
)

type TargetRequest string

const (
	RequestNone   TargetRequest = "none"
	RequestStart  TargetRequest = "start"
	RequestToggle TargetRequest = "toggle"
	RequestFinish TargetRequest = "finish"
	RequestCancel TargetRequest = "cancel"
)

// ActionSpec freezes the tool action identity and its resolved control
// contract. Revision identifies the configuration snapshot used at press.
type ActionSpec struct {
	ToolID         string
	ActionID       string
	CandidateRaw   string
	Revision       uint64
	Scope          ActionScope
	Trigger        ActionTrigger
	Request        TargetRequest
	ReleaseRequest TargetRequest
}

// ActionPlan is immutable outside this package. Keys returns a copy and Spec
// returns the value snapshot used when the plan was constructed.
type ActionPlan struct {
	spec ActionSpec
	keys []Key
}

func NewActionPlan(spec ActionSpec, keys ...Key) (ActionPlan, error) {
	plan := ActionPlan{spec: spec, keys: cloneKeys(keys)}
	if err := plan.validate(); err != nil {
		return ActionPlan{}, err
	}
	return plan, nil
}

func (p ActionPlan) Spec() ActionSpec { return p.spec }

func (p ActionPlan) Keys() []Key { return cloneKeys(p.keys) }

func (p ActionPlan) clone() ActionPlan {
	return ActionPlan{spec: p.spec, keys: cloneKeys(p.keys)}
}

func (p ActionPlan) validate() error {
	spec := p.spec
	if !validKey(Key(spec.ToolID)) || !validKey(Key(spec.ActionID)) || !validKey(Key(spec.CandidateRaw)) || spec.Revision == 0 {
		return fmt.Errorf("%w: invalid action identity or revision", ErrInvalidBinding)
	}
	if spec.Scope != ActionGlobal && spec.Scope != ActionApp {
		return fmt.Errorf("%w: invalid action scope", ErrInvalidBinding)
	}
	if !validTargetRequest(spec.Request) || !validTargetRequest(spec.ReleaseRequest) {
		return fmt.Errorf("%w: invalid action request", ErrInvalidBinding)
	}
	switch spec.Trigger {
	case ActionTap:
		if spec.Request == RequestNone || spec.ReleaseRequest != RequestNone {
			return fmt.Errorf("%w: tap action requires one request and no release request", ErrInvalidBinding)
		}
	case ActionHold:
		if spec.Request != RequestStart || (spec.ReleaseRequest != RequestNone && spec.ReleaseRequest != RequestFinish) {
			return fmt.Errorf("%w: hold action requires start and optional finish", ErrInvalidBinding)
		}
	default:
		return fmt.Errorf("%w: invalid action trigger", ErrInvalidBinding)
	}
	return validateKeys(p.keys, ErrInvalidBinding)
}

func validTargetRequest(request TargetRequest) bool {
	switch request {
	case RequestNone, RequestStart, RequestToggle, RequestFinish, RequestCancel:
		return true
	default:
		return false
	}
}

const (
	actionStatusActive      = "active"
	actionStatusReleased    = "released"
	actionStatusInterrupted = "interrupted"
	actionOutcomeUnknown    = "unknown"
)

// ActionSnapshot reports the local request and cleanup state. TargetOutcome
// is always unknown because key output does not acknowledge target behavior.
type ActionSnapshot struct {
	Press         PressToken
	Plan          ActionPlan
	Status        string
	Request       TargetRequest
	TargetOutcome string
}

func (s ActionSnapshot) clone() ActionSnapshot {
	s.Plan = s.Plan.clone()
	return s
}
