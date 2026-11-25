package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/egoist/mygo"

	"mimac.local/mimac/internal/adapters/doubao"
	"mimac.local/mimac/internal/adapters/textoutput"
	"mimac.local/mimac/internal/platform/inputsource"
	"mimac.local/mimac/internal/platform/processidentity"
)

type interruptedEditor interface {
	CheckFocus() error
	Close() error
}

// An input method can die while its client still owns marked text. Core owns
// the audio/key recovery. Only the Doubao adapter renews the editor connection.
type doubaoEditorRecovery struct {
	mu          sync.Mutex
	owner       *compatLiveAudioRoute
	origin      *textoutput.DictationTarget
	interrupted interruptedEditor
	dispatch    func(func())
	refresh     func() error
}

func newDoubaoEditorRecovery() *doubaoEditorRecovery {
	return &doubaoEditorRecovery{dispatch: mygo.RunOnMain, refresh: func() error {
		return reconnectInputSource(doubao.InputSourceID, inputsource.CurrentID, inputsource.ASCIILayoutID, inputsource.SelectEnabledID)
	}}
}

func (r *compatLiveAudioRoute) recordEditorOnExit(fault error) {
	if !errors.Is(fault, processidentity.ErrReadFailed) && !errors.Is(fault, processidentity.ErrIdentityChanged) && !errors.Is(fault, errCompatRouteTargetMissing) {
		return
	}
	if alive, err := r.capturedTargetAlive(); err != nil || alive {
		return
	}
	d := r.editorRecovery
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.owner != r || d.origin == nil {
		return
	}
	d.releaseInterrupted()
	if current, err := d.origin.CaptureCurrentSelection(); err == nil {
		d.interrupted = current
	}
	d.releaseOrigin()
}

// The restarted process may not answer the control handshake until the
// editor reconnects. Do this before asking it to report readiness. TIS
// belongs to the host main thread. A normal prepared route never dispatches.
func (d *doubaoEditorRecovery) prepareReplacement(ctx context.Context, route *compatLiveAudioRoute) error {
	d.mu.Lock()
	target := d.interrupted
	d.mu.Unlock()
	if target == nil {
		return nil
	}
	done := make(chan error, 1)
	d.dispatch(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.interrupted != target {
			done <- nil
			return
		}
		if err := route.verifyCapturedTargetIdle(ctx); err != nil {
			done <- err
			return
		}
		if target.CheckFocus() == nil {
			if err := ctx.Err(); err != nil {
				done <- err
				return
			}
			if err := d.refresh(); err != nil {
				done <- err
				return
			}
		}
		d.releaseInterrupted()
		done <- nil
	})
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *doubaoEditorRecovery) begin(r *compatLiveAudioRoute) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.releaseOrigin()
	d.owner, d.origin = r, textoutput.BeginDictation()
}

func (d *doubaoEditorRecovery) releaseOrigin() {
	if d.origin != nil {
		_ = d.origin.Close()
		d.owner, d.origin = nil, nil
	}
}

func (d *doubaoEditorRecovery) releaseRoute(r *compatLiveAudioRoute) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.owner == r {
		d.releaseOrigin()
	}
}

func (d *doubaoEditorRecovery) releaseInterrupted() {
	if d.interrupted != nil {
		_ = d.interrupted.Close()
		d.interrupted = nil
	}
}

func (d *doubaoEditorRecovery) clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.releaseOrigin()
	d.releaseInterrupted()
}

// Called on the host main thread, before the next recording. Never change a
// different user-selected source or send an editing command to unmark text.
func reconnectInputSource(expected string, current, layout func() (string, error), selectID func(string) error) error {
	id, err := current()
	if err != nil || id != expected {
		return err
	}
	alternate, err := layout()
	if err != nil {
		return err
	}
	if alternate == "" || alternate == expected {
		return errors.New("no distinct keyboard layout for input-method recovery")
	}
	if err := selectID(alternate); err != nil {
		return err
	}
	id, err = current()
	if err != nil {
		// A failed read is ambiguous. Restore only if a fresh read still
		// identifies the temporary layout this operation selected.
		observed, readErr := current()
		if readErr == nil && observed == alternate {
			return errors.Join(err, selectID(expected))
		}
		return errors.Join(fmt.Errorf("inspect selected keyboard layout: %w", err), readErr)
	}
	if id != alternate {
		if id == expected {
			return errors.New("input-method disconnect was not observed")
		}
		return nil // A new user selection takes precedence.
	}
	if err := selectID(expected); err != nil {
		// Retry only while the temporary layout installed by this operation still owns selection.
		if owned, checkErr := current(); checkErr == nil && owned == alternate {
			if restoreErr := selectID(expected); restoreErr != nil {
				return errors.Join(err, restoreErr)
			}
		} else {
			return err
		}
	}
	id, err = current()
	if err != nil {
		return err
	}
	if id != expected {
		return errors.New("input method was not reselected")
	}
	return nil
}
