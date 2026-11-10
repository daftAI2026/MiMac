// Package appinstall moves the packaged daily app from a read-only disk image
// into an Applications directory during its first launch from that image.
package appinstall

// Disposition tells the host whether normal startup may continue.
type Disposition uint8

const (
	// Continue means the current launch is already installed or is not eligible
	// for installation from a disk image.
	Continue Disposition = iota
	// Relaunch means the installed copy has started and the disk-image process
	// should exit normally.
	Relaunch
	// NeedsUserToQuit means the existing installed app declined or did not
	// complete a normal termination request. The host should ask the user to
	// quit it and retry; the installed bundle has not been changed.
	NeedsUserToQuit
)

// Result carries the exact bundle paths involved in a completed action or a
// request for the user to quit a running target.
type Result struct {
	Disposition Disposition
	SourcePath  string
	TargetPath  string
}
