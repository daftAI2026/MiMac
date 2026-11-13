package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOnboardingRecordRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onboarding.json")
	started := onboardingRecord{Version: onboardingRecordVersion, Started: true}
	if err := writeOnboardingRecord(path, started); err != nil {
		t.Fatal(err)
	}
	got, exists, err := readOnboardingRecord(path)
	if err != nil || !exists || got != started {
		t.Fatalf("read started record = %+v, exists=%t, err=%v", got, exists, err)
	}
	completed := onboardingRecord{Version: onboardingRecordVersion, Started: true, Completed: true}
	if err := writeOnboardingRecord(path, completed); err != nil {
		t.Fatal(err)
	}
	got, exists, err = readOnboardingRecord(path)
	if err != nil || !exists || got != completed {
		t.Fatalf("read completed record = %+v, exists=%t, err=%v", got, exists, err)
	}
}

func TestReadOnboardingRecordRejectsCorruptAndUnknownFields(t *testing.T) {
	for name, contents := range map[string]string{
		"malformed":      "{",
		"wrong version":  `{"version":2,"started":true}`,
		"unknown field":  `{"version":1,"started":true,"completed":false,"device":"remote"}`,
		"invalid state":  `{"version":1,"started":false,"completed":true}`,
		"trailing value": `{"version":1,"started":true,"completed":false} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "onboarding.json")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			_, exists, err := readOnboardingRecord(path)
			if err == nil || !exists {
				t.Fatalf("read corrupt record: exists=%t err=%v", exists, err)
			}
		})
	}
}

func TestShouldPresentOnboardingSeparatesNewAndExistingInstallations(t *testing.T) {
	started := onboardingRecord{Version: onboardingRecordVersion, Started: true}
	completed := onboardingRecord{Version: onboardingRecordVersion, Started: true, Completed: true}
	for _, tc := range []struct {
		name     string
		record   onboardingRecord
		exists   bool
		legacy   bool
		readErr  bool
		wantShow bool
	}{
		{name: "new install", wantShow: true},
		{name: "old install without marker", legacy: true},
		{name: "incomplete run resumes despite later settings", record: started, exists: true, legacy: true, wantShow: true},
		{name: "completed run stays closed", record: completed, exists: true, wantShow: false},
		{name: "damaged marker on new install is shown for recovery", exists: true, readErr: true, wantShow: true},
		{name: "damaged marker on old install is not forced", exists: true, legacy: true, readErr: true, wantShow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var readErr error
			if tc.readErr {
				readErr = os.ErrInvalid
			}
			got := shouldPresentOnboarding(tc.record, tc.exists, tc.legacy, readErr)
			if got != tc.wantShow {
				t.Fatalf("shouldPresentOnboarding = %t, want %t", got, tc.wantShow)
			}
		})
	}
}

func TestOnboardingRecordForOpenOnlyPersistsAutomaticFirstRun(t *testing.T) {
	completed := onboardingRecord{Version: onboardingRecordVersion, Started: true, Completed: true}
	if got, write := onboardingRecordForOpen(completed, true, false); write || got != completed {
		t.Fatalf("explicit reopen changed completed marker: %+v write=%t", got, write)
	}
	if got, write := onboardingRecordForOpen(onboardingRecord{}, false, false); write || got != (onboardingRecord{}) {
		t.Fatalf("explicit diagnostic created marker: %+v write=%t", got, write)
	}
	wantStarted := onboardingRecord{Version: onboardingRecordVersion, Started: true, Step: int(onboardingStepWelcome)}
	if got, write := onboardingRecordForOpen(onboardingRecord{}, false, true); !write || got != wantStarted {
		t.Fatalf("automatic first run = %+v write=%t, want %+v and a write", got, write, wantStarted)
	}
	started := onboardingRecord{Version: onboardingRecordVersion, Started: true}
	if got, write := onboardingRecordForOpen(started, true, true); write || got != started {
		t.Fatalf("resume rewrote existing marker: %+v write=%t", got, write)
	}
}

func TestOnboardingAdjacentStepClampsAtEnds(t *testing.T) {
	if got := onboardingAdjacentStep(onboardingStepWelcome, -1); got != onboardingStepWelcome {
		t.Fatalf("previous from first step = %d", got)
	}
	if got := onboardingAdjacentStep(onboardingStepPermissions, 1); got != onboardingStepPermissions {
		t.Fatalf("next from last step = %d", got)
	}
	if got := onboardingAdjacentStep(onboardingStepDevice, 1); got != onboardingStepVoice {
		t.Fatalf("next from first step = %d", got)
	}
}
