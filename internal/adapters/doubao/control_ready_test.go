package doubao

import (
	"context"
	"errors"
	"testing"

	"mimac.local/mimac/internal/platform/runningapp"
)

func readyControlFixture(t *testing.T) (settingsCandidate, ControlSettings) {
	t.Helper()
	candidate := shortcutCandidate(t, map[string]any{
		"asrShortcutKeyCode": uint64(61), "asrShortcutModifierFlags": uint64(0), "asrShortcutKeyDisplay": "rightoption",
		"asrLongPressShortcutKeyCode": uint64(54), "asrLongPressShortcutModifierFlags": uint64(1048592), "asrLongPressShortcutKeyDisplay": "RightCommand",
	})
	return candidate, ControlSettings{
		VoiceShortcutEnabled: true, GlobalVoiceEnabled: false,
		SingleClick: ControlShortcutSettings{Configured: true, KeyCode: 61, ModifierFlags: 0},
		LongPress:   ControlShortcutSettings{Configured: true, KeyCode: 54, ModifierFlags: 1048592},
	}
}

func TestLiveControlSettingsMustMatchSupportedStoredShortcuts(t *testing.T) {
	candidate, settings := readyControlFixture(t)
	if err := matchLiveControlSettings(candidate, settings); err != nil {
		t.Fatal(err)
	}
	settings.LongPress.KeyCode = 53
	if err := matchLiveControlSettings(candidate, settings); !errors.Is(err, ErrControlSettingsChanged) {
		t.Fatalf("changed live shortcut accepted: %v", err)
	}
	if err := matchLiveControlSettings(settingsCandidate{}, ControlSettings{}); !errors.Is(err, ErrControlSettingsChanged) {
		t.Fatalf("no supported shortcuts accepted: %v", err)
	}
}

func TestControlReadinessDoesNotQueryAnUnlaunchedOrChangedTarget(t *testing.T) {
	for _, changed := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		deps := controlReadiness{
			checkIdentity: func() error { return nil },
			launch: func(context.Context) (int32, bool, error) {
				if changed {
					return 43, true, nil
				}
				cancel()
				return 42, false, nil
			},
			query: func(context.Context) (ControlSettings, error) {
				t.Fatal("query issued before target launch")
				return ControlSettings{}, nil
			},
		}
		err := prepareControl(ctx, 42, deps)
		cancel()
		want := error(context.Canceled)
		if changed {
			want = runningapp.ErrReadFailed
		}
		if !errors.Is(err, want) {
			t.Fatalf("readiness = %v, want %v", err, want)
		}
	}
}

func TestControlReadinessRechecksGenerationAndPropagatesServiceFailure(t *testing.T) {
	candidate, settings := readyControlFixture(t)
	stale := errors.New("same PID now has different start time")
	for _, queryFails := range []bool{false, true} {
		checks := 0
		deps := controlReadiness{
			checkIdentity: func() error {
				checks++
				if checks == 3 {
					return stale
				}
				return nil
			},
			launch: func(context.Context) (int32, bool, error) { return 42, true, nil },
			query: func(ctx context.Context) (ControlSettings, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("query lacks deadline")
				}
				if queryFails {
					return ControlSettings{}, ErrControlSettingsNotReady
				}
				return settings, nil
			},
			shortcuts: func(context.Context) (settingsCandidate, error) {
				if queryFails {
					t.Fatal("configuration read after service failure")
				}
				return candidate, nil
			},
		}
		err := prepareControl(context.Background(), 42, deps)
		want := stale
		if queryFails {
			want = ErrControlSettingsNotReady
		}
		if !errors.Is(err, want) {
			t.Fatalf("preparation failure = %v, want %v", err, want)
		}
	}
}

func TestControlReadinessSupportsTheExistingRequiredIMEContract(t *testing.T) {
	candidate, settings := readyControlFixture(t)
	err := prepareControl(context.Background(), 42, controlReadiness{
		checkIdentity: func() error { return nil },
		launch:        func(context.Context) (int32, bool, error) { return 42, true, nil },
		query:         func(context.Context) (ControlSettings, error) { return settings, nil },
		shortcuts:     func(context.Context) (settingsCandidate, error) { return candidate, nil },
	})
	if err != nil {
		t.Fatalf("globalVoiceEnabled=false incorrectly blocked current IME: %v", err)
	}
}

func TestControlReadinessCannotCompleteAfterCancellationDuringConfigRead(t *testing.T) {
	candidate, settings := readyControlFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := prepareControl(ctx, 42, controlReadiness{
		checkIdentity: func() error { return nil }, launch: func(context.Context) (int32, bool, error) { return 42, true, nil }, query: func(context.Context) (ControlSettings, error) { return settings, nil },
		shortcuts: func(context.Context) (settingsCandidate, error) { cancel(); return candidate, nil },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation completed: %v", err)
	}
}
