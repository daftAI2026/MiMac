package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

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

type recoverySelectionObserver interface {
	CaptureCurrentSelection() (interruptedEditor, error)
	Close() error
}

type textoutputRecoveryObserver struct {
	observer textoutput.DictationSelectionObserver
}

func (o textoutputRecoveryObserver) CaptureCurrentSelection() (interruptedEditor, error) {
	return o.observer.CaptureCurrentSelection()
}

func (o textoutputRecoveryObserver) Close() error { return o.observer.Close() }

// A route publishes this tiny receipt only after its existing close checks
// establish that the captured input-method process has retired.
type editorRetirementReceipt struct{ confirmed atomic.Bool }

// An input method can die while its client still owns marked text. Core owns
// the audio/key recovery. Only the Doubao adapter renews the editor connection.
type doubaoEditorRecovery struct {
	mu                 sync.Mutex
	owner              *compatLiveAudioRoute
	origin             recoverySelectionObserver
	interrupted        interruptedEditor
	interruptedReceipt *editorRetirementReceipt
	dispatch           func(func())
	refresh            func() error
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
	d := r.editorRecovery
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.owner != r || d.origin == nil || r.editorRetirementReceipt == nil {
		d.mu.Unlock()
		return
	}
	origin, receipt := d.origin, r.editorRetirementReceipt
	d.mu.Unlock()

	// Save the same editor's current selection while its text field remains
	// available. A receiver identity error can be a transient read failure, so
	// this snapshot is not eligible for reconnect until route Close proves that
	// the captured input-method process retired.
	current, err := origin.CaptureCurrentSelection()

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.owner != r || d.origin == nil {
		if current != nil {
			_ = current.Close()
		}
		return
	}
	d.releaseInterrupted()
	if err == nil && current != nil {
		d.interrupted = current
		d.interruptedReceipt = receipt
	} else if current != nil {
		_ = current.Close()
	}
	d.releaseOrigin()
}

// The restarted process may not answer the control handshake until the
// editor reconnects. Do this before asking it to report readiness. TIS
// belongs to the host main thread. A normal prepared route never dispatches.
func (d *doubaoEditorRecovery) prepareReplacement(ctx context.Context, route *compatLiveAudioRoute) error {
	d.mu.Lock()
	target := d.interrupted
	receipt := d.interruptedReceipt
	d.mu.Unlock()
	if target == nil || receipt == nil || !receipt.confirmed.Load() {
		return nil
	}
	done := make(chan error, 1)
	d.dispatch(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.interrupted != target || d.interruptedReceipt != receipt || !receipt.confirmed.Load() {
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
	d.releaseInterrupted()
	d.owner = r
	d.origin = textoutputRecoveryObserver{observer: textoutput.ObserveDictationSelection()}
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
	d.interruptedReceipt = nil
}

// finishRoute drops an unconfirmed snapshot after a successful close. A
// confirmed receipt remains available for the next route's focus/input-source
// checks; recovery retains neither the old route nor its audio resources.
func (d *doubaoEditorRecovery) finishRoute(receipt *editorRetirementReceipt) {
	if d == nil || receipt == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.interrupted == nil || d.interruptedReceipt != receipt {
		return
	}
	if receipt.confirmed.Load() {
		return
	}
	d.releaseInterrupted()
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
