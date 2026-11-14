package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

const onboardingCompletionBadgeDelay = 450 * time.Millisecond

var (
	onboardingCompletionEntranceSpring = ui.SpringOptions{Duration: 600 * time.Millisecond, Bounce: .15}
	onboardingCompletionBadgeSpring    = ui.SpringOptions{Duration: 400 * time.Millisecond, Bounce: .5}
)

type onboardingCompletionEntrance struct {
	started  time.Time
	finished bool
}

// Presentation belongs to this mounted element. MyGo owns both spring
// integration and scheduling; no host timer or business state is introduced.
func onboardingCompletionMotion(c *ui.Context, element ui.Element) (entrance, badge float32) {
	state := ui.Local(element, "completion-entrance", func() onboardingCompletionEntrance { return onboardingCompletionEntrance{} })
	if state == nil {
		return 1, 1
	}
	if c.Preferences().ReduceMotion || state.finished {
		state.finished = true
		element.Jump("entrance", 1)
		element.Jump("badge", 1)
		return 1, 1
	}
	if state.started.IsZero() {
		state.started = c.Now()
		element.Jump("entrance", 0)
		element.Jump("badge", 0)
		c.AnimationFrame()
		return 0, 0
	}
	entrance = element.Spring("entrance", 1, onboardingCompletionEntranceSpring)
	if remaining := onboardingCompletionBadgeDelay - c.Now().Sub(state.started); remaining > 0 {
		c.After(remaining)
		badge = element.Jump("badge", 0)
	} else {
		badge = element.Spring("badge", 1, onboardingCompletionBadgeSpring)
	}
	state.finished = entrance == 1 && badge == 1
	return entrance, badge
}
