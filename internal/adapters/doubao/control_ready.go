package doubao

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"mimac.local/mimac/internal/platform/processidentity"
	"mimac.local/mimac/internal/platform/runningapp"
	"mimac.local/mimac/internal/voicetools"
)

var ErrControlSettingsChanged = errors.New("doubao live shortcut settings differ from the supported configuration")

// PrepareControl prepares only the target's keyboard-control service. It sends
// no key or microphone-selection request, and opens no input stream. Run this
// on the preparation worker while the host main run loop continues to run.
func PrepareControl(ctx context.Context, locator voicetools.Locator, expectedPID int32) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if expectedPID <= 0 {
		return runningapp.ErrInvalidTarget
	}
	bundle, state, err := LocateApplication(ctx, locator)
	if err != nil {
		return err
	}
	if state != voicetools.Ready {
		return ErrControlSettingsNotReady
	}
	executable := filepath.Join(bundle.Path, "Contents", "MacOS", "DoubaoIme")
	identity, err := processidentity.Capture(expectedPID, executable)
	if err != nil {
		return err
	}
	return prepareControl(ctx, expectedPID, controlReadiness{
		checkIdentity: identity.Check,
		launch: func(ctx context.Context) (int32, bool, error) {
			return runningapp.FinishedLaunching(ctx, bundleIdentifier, bundle.Path, executable)
		},
		query: QueryControlSettings,
		shortcuts: func(ctx context.Context) (settingsCandidate, error) {
			return readDoubaoMMKV(ctx, doubaoMMKVPath(locator.Home))
		},
	})
}

type controlReadiness struct {
	checkIdentity func() error
	launch        func(context.Context) (int32, bool, error)
	query         func(context.Context) (ControlSettings, error)
	shortcuts     func(context.Context) (settingsCandidate, error)
}

func prepareControl(parent context.Context, expectedPID int32, deps controlReadiness) error {
	ctx, cancel := context.WithTimeout(parent, 6*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := deps.checkIdentity(); err != nil {
		return err
	}
	var ticker *time.Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	for {
		pid, finished, err := deps.launch(ctx)
		if err != nil {
			return err
		}
		if pid != expectedPID {
			return runningapp.ErrReadFailed
		}
		if finished {
			break
		}
		if ticker == nil {
			ticker = time.NewTicker(25 * time.Millisecond)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	if err := deps.checkIdentity(); err != nil {
		return err
	}
	queryCtx, cancelQuery := context.WithTimeout(ctx, time.Second)
	settings, err := deps.query(queryCtx)
	cancelQuery()
	if err != nil {
		return fmt.Errorf("doubao control service: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	candidate, err := deps.shortcuts(ctx)
	if err != nil {
		return err
	}
	if err := matchLiveControlSettings(candidate, settings); err != nil {
		return err
	}
	if err := deps.checkIdentity(); err != nil {
		return err
	}
	return ctx.Err()
}

func matchLiveControlSettings(candidate settingsCandidate, settings ControlSettings) error {
	live := [2]ControlShortcutSettings{settings.SingleClick, settings.LongPress}
	matched := false
	for index, contract := range verifiedShortcutContracts {
		if index >= len(live) {
			return ErrControlSettingsChanged
		}
		action := normalizeAction(candidate, contract)
		if len(action.Candidates) != 1 || !action.Candidates[0].Supported {
			continue
		}
		if !live[index].Configured || live[index].KeyCode != int64(contract.keyCode) || live[index].ModifierFlags != int64(contract.modifierFlags) {
			return ErrControlSettingsChanged
		}
		matched = true
	}
	if !matched {
		return ErrControlSettingsChanged
	}
	// The booleans remain the target's reported fields. In particular, false
	// globalVoiceEnabled does not override the existing required-TIS contract.
	return nil
}
