package main

import "testing"

func TestSettingsHistoryBranchesAndIgnoresRepeatedSelection(t *testing.T) {
	s := &shell{}
	s.navigate(pageDevice)
	s.navigate(pageGeneral)
	s.navigate(pageGeneral)
	if len(s.backPages) != 2 {
		t.Fatalf("history duplicates: %v", s.backPages)
	}
	s.navigateHistory(-1)
	if s.page != pageDevice || len(s.forwardPages) != 1 {
		t.Fatal("back did not restore previous page")
	}
	s.navigateHistory(1)
	if s.page != pageGeneral || len(s.forwardPages) != 0 {
		t.Fatal("forward did not restore next page")
	}
	s.navigateHistory(-1)
	s.navigate(pagePlugins)
	if len(s.forwardPages) != 0 {
		t.Fatal("new destination retained stale forward branch")
	}
	for i := 0; i < 40; i++ {
		s.navigate(pageDevice)
		s.navigate(pageMappings)
	}
	if len(s.backPages) != navigationHistoryLimit {
		t.Fatal("history is not bounded")
	}
}
