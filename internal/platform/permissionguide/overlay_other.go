//go:build !darwin

package permissionguide

import "github.com/egoist/mygo"

func ConfigureDropOverlay(window *mygo.Window) {}
func FadeDropOverlay(window *mygo.Window, visible bool, done func()) {
	if done != nil {
		done()
	}
}
