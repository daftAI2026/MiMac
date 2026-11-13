package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOnboardingUnfinishedStepSurvivesRecordRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onboarding.json")
	if err := os.WriteFile(path, []byte("{\"version\":1,\"started\":true,\"completed\":false}"), 0600); err != nil {
		t.Fatal(err)
	}
	record, exists, err := readOnboardingRecord(path)
	if err != nil || !exists || onboardingInitialStep(record, true) != onboardingStepDevice {
		t.Fatalf("legacy first step: %+v %t %v", record, exists, err)
	}
	next, write := onboardingRecordForStep(record, onboardingStepPermissions, true)
	if !write {
		t.Fatal("unfinished automatic journey did not save its current step")
	}
	if err := writeOnboardingRecord(path, next); err != nil {
		t.Fatal(err)
	}
	restored, _, err := readOnboardingRecord(path)
	if err != nil || onboardingInitialStep(restored, true) != onboardingStepPermissions {
		t.Fatalf("restored wrong step: %+v %v", restored, err)
	}
	if _, write := onboardingRecordForStep(restored, onboardingStepPermissions, true); write {
		t.Fatal("unchanged step would rewrite progress")
	}
	if onboardingInitialStep(restored, false) != onboardingStepWelcome {
		t.Fatal("explicit review did not start at its first page")
	}
}

func TestOnboardingReviewPreservesCompletedMarker(t *testing.T) {
	record := onboardingRecord{Version: onboardingRecordVersion, Started: true, Completed: true}
	for _, automatic := range []bool{false, true} {
		next, write := onboardingRecordForStep(record, onboardingStepPermissions, automatic)
		if write || next != record {
			t.Fatalf("completed marker changed during review: %+v %t", next, write)
		}
	}
	started := onboardingRecord{Version: onboardingRecordVersion, Started: true}
	if next, write := onboardingRecordForStep(started, onboardingStepVoice, false); write || next != started {
		t.Fatal("explicit display wrote automatic progress")
	}
}

func TestOnboardingInvalidStepIsNotPersisted(t *testing.T) {
	for _, step := range []int{-2, int(onboardingStepCount), 99} {
		if validOnboardingRecord(onboardingRecord{Version: onboardingRecordVersion, Started: true, Step: step}) {
			t.Fatalf("invalid step accepted: %d", step)
		}
	}
}
