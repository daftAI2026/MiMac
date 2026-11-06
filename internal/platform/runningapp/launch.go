package runningapp

import "context"

// FinishedLaunching reads the native NSApplication launch-completion flag for
// the unique, path-verified application. It does not launch or activate it.
// A completed launch is not proof that any application-specific service or
// audio stream is ready. The host's main run loop must be running.
// As with Find, callers must retain and recheck a processidentity lease;
// matching PID numbers alone do not identify the same process generation.
func FinishedLaunching(ctx context.Context, bundleID, appPath, executablePath string) (int32, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	identity, err := validateTarget(bundleID, appPath, executablePath)
	if err != nil {
		return 0, false, err
	}
	return launchSnapshot(ctx, func(ctx context.Context) (int32, error) {
		return findNative(ctx, identity)
	}, finishedLaunchingNative)
}

func launchSnapshot(ctx context.Context, find func(context.Context) (int32, error), read func(context.Context, int32) (bool, error)) (int32, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	pid, err := find(ctx)
	if err != nil {
		return 0, false, err
	}
	finished, err := read(ctx, pid)
	if err != nil {
		return 0, false, err
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	current, err := find(ctx)
	if err != nil {
		return 0, false, err
	}
	if current != pid {
		return 0, false, ErrReadFailed
	}
	return pid, finished, nil
}
