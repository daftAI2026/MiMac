//go:build darwin

package chatgpt

import (
	"runtime"
	"testing"

	"github.com/ebitengine/purego/objc"
)

func TestTargetFileURLIdentityIgnoresBaseRepresentation(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := loadEnvironmentNativeAPI(); err != nil {
		t.Fatal(err)
	}
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(envSel("alloc")).Send(envSel("init"))
	defer pool.Send(envSel("drain"))
	urls := objc.ID(objc.GetClass("NSURL"))
	base := urls.Send(envSel("URLWithString:"), envString("file:///Applications/Example.app/"))
	relative := urls.Send(envSel("URLWithString:relativeToURL:"), envString("Contents/MacOS/Example"), base)
	absolute := urls.Send(envSel("fileURLWithPath:"), envString("/Applications/Example.app/Contents/MacOS/Example"))
	other := urls.Send(envSel("fileURLWithPath:"), envString("/Applications/Other.app/Contents/MacOS/Other"))
	network := urls.Send(envSel("URLWithString:"), envString("https://example.invalid/Applications/Example.app/Contents/MacOS/Example"))
	if !envURLsEqual(relative, absolute) {
		t.Fatal("same executable with a relative-base URL did not match")
	}
	if envURLsEqual(absolute, other) || envURLsEqual(absolute, network) || envURLsEqual(0, absolute) {
		t.Fatal("different or non-file URL matched target executable")
	}
}
