package doubao

import (
	"context"
	"errors"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

const (
	fixtureShortVersion = "1.0.2"
	fixtureBuildVersion = "1"
)

func TestDoubaoCapabilitiesDoNotDependOnReleaseVersion(t *testing.T) {
	requireDarwin(t)
	for _, release := range []struct{ version, build string }{
		{"1.0.2", "1"}, {"1.0.3", "1"}, {"24.8-preview", "502"},
	} {
		t.Run(release.version, func(t *testing.T) {
			locator, _ := doubaoFixtureLocator(t, bundleIdentifier, release.version, release.build)
			writeMMKVFixture(t, doubaoMMKVPath(locator.Home), []mmkvEntry{{
				key: []byte("d"), value: keyedArchiveFixture(t, map[string]interface{}{
					"asrShortcutKeyCode": uint64(61), "asrShortcutModifierFlags": uint64(0), "asrShortcutKeyDisplay": "rightoption",
					// An unknown hold action must not disable the recognized tap action.
					"asrLongPressShortcutKeyCode": uint64(12), "asrLongPressShortcutModifierFlags": uint64(2048), "asrLongPressShortcutKeyDisplay": "unknown",
					"selectedMicrophoneId": "fixture-input",
				}),
			}})
			ctx := context.Background()
			snapshot := Discover(ctx, locator)
			if snapshot.State != voicetools.Ready || snapshot.Version != formatBundleVersion(release.version, release.build) || len(snapshot.Actions) != 2 {
				t.Fatalf("configuration discovery = %+v", snapshot)
			}
			if tap := snapshot.Actions[0]; len(tap.Candidates) != 1 || !tap.Candidates[0].Supported || tap.Control == nil {
				t.Fatalf("recognized tap action unavailable: %+v", tap)
			}
			if hold := snapshot.Actions[1]; len(hold.Candidates) != 1 || hold.Candidates[0].Supported || hold.Control != nil {
				t.Fatalf("unknown hold action became executable: %+v", hold)
			}
			selection, err := ReadMicrophoneSelection(ctx, locator)
			if err != nil || selection.Automatic || selection.DeviceUID != "fixture-input" {
				t.Fatalf("microphone configuration = %+v, %v", selection, err)
			}
			posts := 0
			err = requestMicrophoneSelection(ctx, locator, selection.SelectedMicrophoneID, verifyMicrophoneRequestTarget,
				func(_ context.Context, request MicrophoneSelectionRequest) error {
					posts++
					if request.UserInfo()[selectedMicrophoneUserInfoKey] != "fixture-input" {
						t.Fatalf("selection payload = %v", request.UserInfo())
					}
					return nil
				})
			if err != nil || posts != 1 {
				t.Fatalf("identity-checked request: posts=%d, err=%v", posts, err)
			}
		})
	}
}

func TestDoubaoCapabilitiesStillRequireApplicationIdentity(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, "not.doubao", "24.8-preview", "502")
	if got := Discover(context.Background(), locator); got.State != voicetools.Unsupported {
		t.Fatalf("foreign application discovery = %+v", got)
	}
	if _, err := ReadMicrophoneSelection(context.Background(), locator); !errors.Is(err, ErrMicrophoneSelectionUnsupported) {
		t.Fatalf("foreign application read = %v", err)
	}
	posts := 0
	err := requestMicrophoneSelection(context.Background(), locator, nil, verifyMicrophoneRequestTarget,
		func(context.Context, MicrophoneSelectionRequest) error { posts++; return nil })
	if !errors.Is(err, ErrMicrophoneSelectionUnsupported) || posts != 0 {
		t.Fatalf("foreign application request: posts=%d, err=%v", posts, err)
	}
}
