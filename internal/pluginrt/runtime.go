// Package pluginrt provides a small, compile-time plugin lifecycle runtime.
// Calls are expected to be serialized by the host; the runtime creates no
// goroutines and deliberately has no platform-specific dependencies.
package pluginrt

import (
	"errors"
	"fmt"
	"strings"
)

// StopFunc cleans up a module Start attempt, including a partially failed one.
// Start must return a non-nil StopFunc on every path. If a failed Start has
// already cleaned up everything, it must return a no-op StopFunc; nil means the
// runtime cannot establish that the module's resources are clean.
type StopFunc func() error

// Manifest describes one statically registered module and its dependencies.
// Start must always return a non-nil StopFunc, including when it returns an
// error. A successful Start with nil StopFunc is a contract failure and faults
// the module while retaining its dependencies.
type Manifest struct {
	ID       string
	Requires []string
	Start    func() (StopFunc, error)
}

var (
	// ErrRuntimeClosed indicates that no modules can be enabled after Close.
	ErrRuntimeClosed = errors.New("plugin runtime is closed")
	// ErrRuntimeBusy indicates a mutating method was called from a lifecycle callback.
	ErrRuntimeBusy = errors.New("plugin runtime is busy in a lifecycle callback")
	// ErrUnknownModule indicates that an ID was not part of the fixed manifest set.
	ErrUnknownModule = errors.New("unknown plugin module")
	// ErrModuleFaulted indicates that cleanup may have left module resources active.
	ErrModuleFaulted = errors.New("plugin module is faulted")
	// ErrNilStopFunc indicates that Start did not provide the required cleanup function.
	ErrNilStopFunc = errors.New("plugin Start returned a nil StopFunc")
)

type moduleState uint8

const (
	stateInactive moduleState = iota
	stateActive
	stateFaulted
)

type module struct {
	manifest Manifest
	state    moduleState
	stop     StopFunc
	stopOnce bool
	faultErr error
}

// Runtime owns the lifecycle state for a fixed set of manifests. Its methods
// must be called serially by the host, typically on the main thread.
type Runtime struct {
	modules       map[string]*module
	order         []string // dependency-first, stable relative to manifest order
	closed        bool
	busy          bool
	cleanupErrors []error
}

// New validates and registers a fixed set of modules. It does not start them.
func New(manifests ...Manifest) (*Runtime, error) {
	r := &Runtime{modules: make(map[string]*module, len(manifests))}
	inputOrder := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		if strings.TrimSpace(manifest.ID) == "" || manifest.ID != strings.TrimSpace(manifest.ID) {
			return nil, fmt.Errorf("plugin module ID must be non-empty and have no surrounding whitespace: %q", manifest.ID)
		}
		if _, exists := r.modules[manifest.ID]; exists {
			return nil, fmt.Errorf("duplicate plugin module ID %q", manifest.ID)
		}
		if manifest.Start == nil {
			return nil, fmt.Errorf("plugin module %q has no Start function", manifest.ID)
		}
		manifest.Requires = append([]string(nil), manifest.Requires...)
		r.modules[manifest.ID] = &module{manifest: manifest}
		inputOrder = append(inputOrder, manifest.ID)
	}

	for _, id := range inputOrder {
		seen := make(map[string]struct{}, len(r.modules[id].manifest.Requires))
		for _, dependency := range r.modules[id].manifest.Requires {
			if strings.TrimSpace(dependency) == "" || dependency != strings.TrimSpace(dependency) {
				return nil, fmt.Errorf("plugin module %q has invalid dependency ID %q", id, dependency)
			}
			if dependency == id {
				return nil, fmt.Errorf("plugin module %q depends on itself", id)
			}
			if _, exists := seen[dependency]; exists {
				return nil, fmt.Errorf("plugin module %q repeats dependency %q", id, dependency)
			}
			seen[dependency] = struct{}{}
			if _, exists := r.modules[dependency]; !exists {
				return nil, fmt.Errorf("plugin module %q requires missing module %q", id, dependency)
			}
		}
	}

	marks := make(map[string]uint8, len(r.modules))
	var visit func(string) error
	visit = func(id string) error {
		switch marks[id] {
		case 1:
			return fmt.Errorf("plugin dependency cycle includes %q", id)
		case 2:
			return nil
		}
		marks[id] = 1
		for _, dependency := range r.modules[id].manifest.Requires {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		marks[id] = 2
		r.order = append(r.order, id)
		return nil
	}
	for _, id := range inputOrder {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Active reports whether a module is active, including a faulted module whose
// failed cleanup may have left resources in use.
func (r *Runtime) Active(id string) bool {
	if r == nil {
		return false
	}
	m := r.modules[id]
	return m != nil && m.state != stateInactive
}

// Failure returns the retained cleanup or lifecycle-contract failure for id.
// Active alone cannot distinguish a healthy module from resources retained
// after failed cleanup. This query never retries cleanup or changes state.
// A nil runtime or unknown module has no recorded failure and returns nil.
func (r *Runtime) Failure(id string) error {
	if r == nil {
		return nil
	}
	m := r.modules[id]
	if m == nil {
		return nil
	}
	return m.faultErr
}

// Enable starts id and any missing dependencies in dependency order. Modules
// already active before this call are left alone if a later Start fails.
func (r *Runtime) Enable(id string) error {
	if r.busy {
		return ErrRuntimeBusy
	}
	if r.closed {
		return ErrRuntimeClosed
	}
	if _, exists := r.modules[id]; !exists {
		return fmt.Errorf("%w: %s", ErrUnknownModule, id)
	}
	r.busy = true
	defer func() { r.busy = false }()

	closure := r.dependencyOrder(id)
	for _, current := range closure {
		m := r.modules[current]
		if m.state == stateFaulted {
			return errors.Join(ErrModuleFaulted, fmt.Errorf("module %q: %w", current, m.faultErr))
		}
	}

	started := make([]string, 0, len(closure))
	for _, current := range closure {
		m := r.modules[current]
		if m.state == stateActive {
			continue
		}
		stop, err := m.manifest.Start()
		if err != nil {
			startErr := fmt.Errorf("start module %q: %w", current, err)
			failureCleanupErr := r.rollbackFailedStart(current, stop, startErr)
			rollbackErr := r.rollbackStarted(started)
			return errors.Join(startErr, failureCleanupErr, rollbackErr)
		}
		if stop == nil {
			contractErr := r.faultWithoutStop(current, nil)
			rollbackErr := r.rollbackStarted(started)
			return errors.Join(contractErr, rollbackErr)
		}
		m.state = stateActive
		m.stop = stop
		m.stopOnce = false
		m.faultErr = nil
		started = append(started, current)
	}
	return nil
}

// Disable stops id only when no active module depends on it. It never disables
// dependents on the caller's behalf.
func (r *Runtime) Disable(id string) error {
	if r.busy {
		return ErrRuntimeBusy
	}
	if r.closed {
		return ErrRuntimeClosed
	}
	m, exists := r.modules[id]
	if !exists {
		return fmt.Errorf("%w: %s", ErrUnknownModule, id)
	}
	r.busy = true
	defer func() { r.busy = false }()
	if m.state == stateInactive {
		return nil
	}
	if m.state == stateFaulted {
		return errors.Join(ErrModuleFaulted, fmt.Errorf("module %q: %w", id, m.faultErr))
	}
	if dependent := r.activeDependent(id); dependent != "" {
		return fmt.Errorf("cannot disable module %q while active module %q depends on it", id, dependent)
	}
	return r.stopModule(id)
}

// Close stops all modules in reverse dependency order. A failed stop is never
// retried; its dependencies remain active because they may still be in use.
// Independent branches continue to stop. Repeated calls return accumulated
// cleanup errors without invoking Stop again.
func (r *Runtime) Close() error {
	if r.busy {
		return ErrRuntimeBusy
	}
	if r.closed {
		return errors.Join(r.cleanupErrors...)
	}
	r.busy = true
	defer func() { r.busy = false }()
	r.closed = true

	for i := len(r.order) - 1; i >= 0; i-- {
		id := r.order[i]
		m := r.modules[id]
		if m.state != stateActive || r.activeDependent(id) != "" {
			continue
		}
		_ = r.stopModule(id)
	}
	return errors.Join(r.cleanupErrors...)
}

func (r *Runtime) dependencyOrder(root string) []string {
	set := make(map[string]struct{})
	for _, id := range r.order {
		if id == root || r.requiresTransitively(root, id) {
			set[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for _, id := range r.order {
		if _, included := set[id]; included {
			out = append(out, id)
		}
	}
	return out
}

func (r *Runtime) requiresTransitively(moduleID, target string) bool {
	for _, dependency := range r.modules[moduleID].manifest.Requires {
		if dependency == target || r.requiresTransitively(dependency, target) {
			return true
		}
	}
	return false
}

// activeDependent returns a deterministic active transitive dependent of id.
func (r *Runtime) activeDependent(id string) string {
	for _, candidate := range r.order {
		if candidate == id || r.modules[candidate].state == stateInactive {
			continue
		}
		if r.requiresTransitively(candidate, id) {
			return candidate
		}
	}
	return ""
}

func (r *Runtime) rollbackFailedStart(id string, stop StopFunc, startErr error) error {
	m := r.modules[id]
	if stop == nil {
		return r.faultWithoutStop(id, startErr)
	}

	// Treat the failed module as active while cleanup runs so a failed cleanup
	// pins its dependencies and is visible through Active.
	m.state = stateActive
	m.stop = stop
	m.stopOnce = true
	if err := stop(); err != nil {
		cleanupErr := fmt.Errorf("rollback failed Start for module %q: %w", id, err)
		m.state = stateFaulted
		m.faultErr = cleanupErr
		r.cleanupErrors = append(r.cleanupErrors, cleanupErr)
		return cleanupErr
	}
	m.state = stateInactive
	m.stop = nil
	m.stopOnce = false
	m.faultErr = nil
	return nil
}

func (r *Runtime) faultWithoutStop(id string, cause error) error {
	contractErr := fmt.Errorf("%w: module %q", ErrNilStopFunc, id)
	if cause != nil {
		contractErr = errors.Join(contractErr, cause)
	}
	m := r.modules[id]
	m.state = stateFaulted
	m.stop = nil
	m.stopOnce = true
	m.faultErr = contractErr
	r.cleanupErrors = append(r.cleanupErrors, contractErr)
	return contractErr
}

func (r *Runtime) rollbackStarted(started []string) error {
	var errs []error
	for i := len(started) - 1; i >= 0; i-- {
		id := started[i]
		m := r.modules[id]
		if m.state != stateActive || r.activeDependent(id) != "" {
			continue
		}
		if err := r.stopModule(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Runtime) stopModule(id string) error {
	m := r.modules[id]
	if m.state == stateInactive {
		return nil
	}
	if m.state == stateFaulted {
		return errors.Join(ErrModuleFaulted, fmt.Errorf("module %q: %w", id, m.faultErr))
	}
	if m.stopOnce {
		return errors.Join(ErrModuleFaulted, fmt.Errorf("module %q Stop has already been called", id))
	}
	m.stopOnce = true
	if m.stop == nil {
		m.state = stateInactive
		m.stopOnce = false
		return nil
	}
	if err := m.stop(); err != nil {
		cleanupErr := fmt.Errorf("stop module %q: %w", id, err)
		m.state = stateFaulted
		m.faultErr = cleanupErr
		r.cleanupErrors = append(r.cleanupErrors, cleanupErr)
		return cleanupErr
	}
	m.state = stateInactive
	m.stop = nil
	m.stopOnce = false
	m.faultErr = nil
	return nil
}
