package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Defer native menu tracking until the current render completes. Native Click
// commits directly on MyGo's main thread, even if the window becomes occluded.
func (s *shell) popupMenu(menu *mygo.Menu, bounds ui.Rect) {
	if s.menuOpen || s.window == nil || s.closing {
		return
	}
	s.menuOpen = true
	window := s.window
	go func() {
		mygo.RunOnMain(func() {
			defer func() { s.menuOpen = false }()
			if s.closing || s.window != window || window.IsDestroyed() {
				return
			}
			menu.PopupAt(window, int(bounds.X), int(bounds.Y+bounds.H))
		})
	}()
}
