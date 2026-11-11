//go:build darwin

package permissionguide

import (
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

const dropOverlayFadeDuration = 0.18

// ConfigureDropOverlay configures an existing MyGo NSWindow for the drop veil.
// MyGo retains window ownership and controls when it is shown or closed.
func ConfigureDropOverlay(window *mygo.Window) {
	if window == nil {
		return
	}

	mygo.RunOnMain(func() {
		if loadAppKitFramework() != nil {
			return
		}
		handle := window.NativeHandle()
		if handle == 0 {
			return
		}

		nativeWindow := objc.ID(handle)
		nativeWindow.Send(objc.RegisterName("setLevel:"), int64(4))
		nativeWindow.Send(objc.RegisterName("setHidesOnDeactivate:"), false)
		nativeWindow.Send(objc.RegisterName("setOpaque:"), false)
		nativeWindow.Send(objc.RegisterName("setHasShadow:"), false)
		nativeWindow.Send(objc.RegisterName("setIgnoresMouseEvents:"), true)
		nativeWindow.Send(objc.RegisterName("setAccessibilityElement:"), false)
		nativeWindow.Send(objc.RegisterName("setAlphaValue:"), float64(0))
	})
}

// FadeDropOverlay animates an existing MyGo NSWindow with AppKit's default
// NSAnimationContext timing curve. It does not order out, activate, focus, or
// close the window; the caller owns those actions and any stale-completion
// generation checks.
func FadeDropOverlay(window *mygo.Window, visible bool, done func()) {
	mygo.RunOnMain(func() {
		finish := func() {
			if done != nil {
				done()
			}
		}
		if window == nil || loadAppKitFramework() != nil {
			finish()
			return
		}
		handle := window.NativeHandle()
		contextClass := objc.GetClass("NSAnimationContext")
		if handle == 0 || contextClass == 0 {
			finish()
			return
		}

		alpha := float64(0)
		if visible {
			alpha = 1
		}
		nativeWindow := objc.ID(handle)
		changes := objc.NewBlock(func(_ objc.Block, context objc.ID) {
			context.Send(objc.RegisterName("setDuration:"), dropOverlayFadeDuration)
			nativeWindow.Send(objc.RegisterName("animator")).Send(
				objc.RegisterName("setAlphaValue:"), alpha,
			)
		})
		completion := objc.NewBlock(func(_ objc.Block) {
			finish()
		})

		// runAnimationGroup:completionHandler: consumes the synchronous changes
		// block and retains its completion handler until the group finishes.
		objc.ID(contextClass).Send(
			objc.RegisterName("runAnimationGroup:completionHandler:"),
			changes,
			completion,
		)
		completion.Release()
		changes.Release()
	})
}
