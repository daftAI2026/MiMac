package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestCompletionEntranceDelaysBadgeAndStops(t *testing.T) {
	var entrance, badge float32
	v := ui.NewTester(func(c *ui.Context) {
		e := ui.Box(c).Key("artwork").Size(128, 128)
		entrance, badge = onboardingCompletionMotion(c, e)
	}, 200, 200)
	if entrance != 0 || badge != 0 {
		t.Fatalf("unexpected first frame: %g %g", entrance, badge)
	}
	v.Advance(300 * time.Millisecond)
	if entrance <= .5 || badge != 0 {
		t.Fatalf("badge appeared before the icon: %g %g", entrance, badge)
	}
	v.Advance(400 * time.Millisecond)
	if badge <= .5 {
		t.Fatalf("badge did not follow its delayed spring: %g", badge)
	}
	v.Advance(2 * time.Second)
	if entrance != 1 || badge != 1 || v.FramePending() {
		t.Fatalf("one-time entrance kept scheduling: %g %g pending=%v", entrance, badge, v.FramePending())
	}
	v.SetPreferences(ui.Preferences{ReduceMotion: true})
	v.Frame()
	if entrance != 1 || badge != 1 || v.FramePending() {
		t.Fatal("reduced motion did not preserve a static completion signal")
	}
}
