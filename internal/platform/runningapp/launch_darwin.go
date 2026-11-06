//go:build darwin

package runningapp

import (
	"context"
	"runtime"

	"github.com/ebitengine/purego/objc"
)

func finishedLaunchingNative(ctx context.Context, pid int32) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := loadRunningApplicationAPI(); err != nil {
		return false, err
	}
	poolClass, appClass := objc.GetClass("NSAutoreleasePool"), objc.GetClass("NSRunningApplication")
	if poolClass == 0 || appClass == 0 {
		return false, ErrReadFailed
	}
	pool := objc.ID(poolClass).Send(runningApplicationSelector("alloc")).Send(runningApplicationSelector("init"))
	if pool == 0 {
		return false, ErrReadFailed
	}
	defer pool.Send(runningApplicationSelector("drain"))
	app := objc.Send[objc.ID](objc.ID(appClass), runningApplicationSelector("runningApplicationWithProcessIdentifier:"), pid)
	if app == 0 {
		return false, ErrNotRunning
	}
	for _, selector := range []string{"isTerminated", "processIdentifier", "isFinishedLaunching"} {
		if !runningApplicationResponds(app, selector) {
			return false, ErrReadFailed
		}
	}
	if objc.Send[bool](app, runningApplicationSelector("isTerminated")) || objc.Send[int32](app, runningApplicationSelector("processIdentifier")) != pid {
		return false, ErrNotRunning
	}
	finished := objc.Send[bool](app, runningApplicationSelector("isFinishedLaunching"))
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if objc.Send[bool](app, runningApplicationSelector("isTerminated")) {
		return false, ErrNotRunning
	}
	return finished, nil
}
