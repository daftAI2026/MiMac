package runningapp

import (
	"errors"
	"testing"
)

var testTarget = target{
	bundleID:       "com.tencent.inputmethod.wetype",
	bundlePath:     "/Library/Input Methods/WeType.app",
	executablePath: "/Library/Input Methods/WeType.app/Contents/MacOS/WeType",
}

func TestUniquePIDReturnsSoleExactApplication(t *testing.T) {
	applications := []applicationSnapshot{
		{bundleID: "com.example.other", bundlePath: "/Applications/Other.app", executablePath: "/Applications/Other.app/Contents/MacOS/Other", pid: 10},
		{bundleID: testTarget.bundleID, bundlePath: testTarget.bundlePath, executablePath: testTarget.executablePath, pid: 42},
	}
	pid, err := uniquePID(testTarget, applications)
	if err != nil || pid != 42 {
		t.Fatalf("uniquePID() = %d, %v; want PID 42", pid, err)
	}
}

func TestUniquePIDIgnoresExitedApplication(t *testing.T) {
	applications := []applicationSnapshot{
		{bundleID: testTarget.bundleID, bundlePath: "/Users/test/WeType.app", executablePath: "/Users/test/WeType.app/Contents/MacOS/WeType", pid: 41, terminated: true},
	}
	if pid, err := uniquePID(testTarget, applications); pid != 0 || !errors.Is(err, ErrNotRunning) {
		t.Fatalf("uniquePID() = %d, %v; want no running target", pid, err)
	}
}

func TestUniquePIDRejectsAnyActiveSameBundleAtDifferentPath(t *testing.T) {
	applications := []applicationSnapshot{
		{bundleID: testTarget.bundleID, bundlePath: testTarget.bundlePath, executablePath: testTarget.executablePath, pid: 42},
		{bundleID: testTarget.bundleID, bundlePath: "/Users/test/WeType.app", executablePath: "/Users/test/WeType.app/Contents/MacOS/WeType", pid: 43},
	}
	if pid, err := uniquePID(testTarget, applications); pid != 0 || !errors.Is(err, ErrUnexpectedPath) {
		t.Fatalf("uniquePID() = %d, %v; want ErrUnexpectedPath", pid, err)
	}
}

func TestUniquePIDRejectsMultipleExactApplicationProcesses(t *testing.T) {
	applications := []applicationSnapshot{
		{bundleID: testTarget.bundleID, bundlePath: testTarget.bundlePath, executablePath: testTarget.executablePath, pid: 42},
		{bundleID: testTarget.bundleID, bundlePath: testTarget.bundlePath, executablePath: testTarget.executablePath, pid: 43},
	}
	if pid, err := uniquePID(testTarget, applications); pid != 0 || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("uniquePID() = %d, %v; want ErrAmbiguous", pid, err)
	}
}

func TestUniquePIDRejectsInvalidProcessSnapshot(t *testing.T) {
	applications := []applicationSnapshot{
		{bundleID: testTarget.bundleID, bundlePath: testTarget.bundlePath, executablePath: testTarget.executablePath, pid: 0},
	}
	if pid, err := uniquePID(testTarget, applications); pid != 0 || !errors.Is(err, ErrReadFailed) {
		t.Fatalf("uniquePID() = %d, %v; want ErrReadFailed", pid, err)
	}
}

func TestValidateTargetRequiresFullPathsWithinBundle(t *testing.T) {
	valid, err := validateTarget(testTarget.bundleID, testTarget.bundlePath, testTarget.executablePath)
	if err != nil || valid != testTarget {
		t.Fatalf("validateTarget() = %+v, %v; want %+v", valid, err, testTarget)
	}
	for _, test := range []struct {
		name, bundleID, bundlePath, executablePath string
	}{
		{name: "empty bundle ID", bundleID: "", bundlePath: testTarget.bundlePath, executablePath: testTarget.executablePath},
		{name: "relative bundle", bundleID: testTarget.bundleID, bundlePath: "WeType.app", executablePath: testTarget.executablePath},
		{name: "executable outside bundle", bundleID: testTarget.bundleID, bundlePath: testTarget.bundlePath, executablePath: "/Applications/Other.app/Contents/MacOS/Other"},
		{name: "NUL bundle ID", bundleID: "com.example\x00other", bundlePath: testTarget.bundlePath, executablePath: testTarget.executablePath},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateTarget(test.bundleID, test.bundlePath, test.executablePath); !errors.Is(err, ErrInvalidTarget) {
				t.Fatalf("validateTarget() error = %v, want ErrInvalidTarget", err)
			}
		})
	}
}
