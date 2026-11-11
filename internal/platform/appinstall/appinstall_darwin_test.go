//go:build darwin

package appinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"howett.net/plist"
)

const fixtureBundleID = "dev.mimac.app.fixture"

const nativeFixtureOptIn = "MIMAC_APPINSTALL_NATIVE_FIXTURE"

const nativeFixtureSwiftSource = `import AppKit
import Foundation

@MainActor
final class FixtureDelegate: NSObject, NSApplicationDelegate {
    private let exitPath: String
    private let pidPath: String
    private let stoppedPath: String
    private let deadline = Date().addingTimeInterval(25)

    init(exitPath: String, pidPath: String, stoppedPath: String) {
        self.exitPath = exitPath
        self.pidPath = pidPath
        self.stoppedPath = stoppedPath
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        .terminateNow
    }

    func applicationWillTerminate(_ notification: Notification) {
        try? "stopped".write(toFile: stoppedPath, atomically: true, encoding: .utf8)
    }

    @objc func checkExit(_ timer: Timer) {
        if FileManager.default.fileExists(atPath: exitPath) || Date() >= deadline {
            timer.invalidate()
            NSApplication.shared.terminate(nil)
        }
    }

    func startExitTimer() {
        _ = Timer.scheduledTimer(timeInterval: 0.05, target: self,
                                 selector: #selector(checkExit(_:)), userInfo: nil, repeats: true)
        try? String(ProcessInfo.processInfo.processIdentifier).write(toFile: pidPath, atomically: true, encoding: .utf8)
    }
}

@main
@MainActor
struct FixtureMain {
    static func main() {
        let arguments = Array(CommandLine.arguments.dropFirst())
        guard arguments.count == 3 else { return }
        let delegate = FixtureDelegate(exitPath: arguments[0], pidPath: arguments[1], stoppedPath: arguments[2])
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory)
        app.delegate = delegate
        delegate.startExitTimer()
        app.run()
    }
}`

func TestMain(m *testing.M) {
	if os.Getenv(nativeFixtureOptIn) != "1" {
		os.Exit(m.Run())
	}
	mygo.App.SetName("MiMac Appinstall Native Test Host")
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	mygo.App.OnWindowAllClosed(func() {})
	testResult := 1
	mygo.App.WhenReady(func() {
		go func() {
			testResult = m.Run()
			mygo.RunOnMain(func() { mygo.App.Quit() })
		}()
	})
	if err := mygo.App.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "MyGo native appinstall test host:", err)
		testResult = 1
	}
	os.Exit(testResult)
}

func TestExecutePlanInstallsIntoEmptyApplicationsFixture(t *testing.T) {
	fixture := newInstallFixture(t, "2.1.0", "9", "new-release")
	hooks := fixtureHooks(fixture)

	result, err := executePlan(context.Background(), fixture.plan, hooks)
	if err != nil {
		t.Fatalf("executePlan: %v", err)
	}
	if result.Disposition != Relaunch {
		t.Fatalf("disposition = %v, want Relaunch", result.Disposition)
	}
	if got := readFixturePayload(t, fixture.plan.TargetPath); got != "new-release" {
		t.Fatalf("installed payload = %q, want new-release", got)
	}
	if got := strings.Join(fixture.events, ","); got != "copy,running,launch,wait-running,schedule-exit" {
		t.Fatalf("transaction events = %s, want copy, running, launch, wait-running, schedule-exit", got)
	}
	assertOnlyInstalledBundle(t, filepath.Dir(fixture.plan.TargetPath), fixture.plan.TargetPath)
}

func TestExecutePlanUpgradesStoppedInstalledFixture(t *testing.T) {
	fixture := newInstallFixture(t, "2.1.0", "9", "new-release")
	fixture.plan.TargetExists = true
	makeFixtureBundle(t, fixture.plan.TargetPath, "2.0.0", "8", "old-release")
	runningResults := []bool{true, false}
	hooks := fixtureHooks(fixture)
	hooks.running = func(ctx context.Context, bundleID, path string) (bool, error) {
		fixture.events = append(fixture.events, "running")
		assertExactTargetQuery(t, fixture.plan, bundleID, path)
		if len(runningResults) == 0 {
			return false, errors.New("unexpected extra running-state query")
		}
		result := runningResults[0]
		runningResults = runningResults[1:]
		return result, nil
	}
	hooks.terminate = func(_ context.Context, bundleID, path string) (bool, error) {
		fixture.events = append(fixture.events, "terminate")
		assertExactTargetQuery(t, fixture.plan, bundleID, path)
		return true, nil
	}
	hooks.waitStopped = func(_ context.Context, bundleID, path string) (bool, error) {
		fixture.events = append(fixture.events, "wait-stopped")
		assertExactTargetQuery(t, fixture.plan, bundleID, path)
		return true, nil
	}

	result, err := executePlan(context.Background(), fixture.plan, hooks)
	if err != nil {
		t.Fatalf("executePlan: %v", err)
	}
	if result.Disposition != Relaunch {
		t.Fatalf("disposition = %v, want Relaunch", result.Disposition)
	}
	if got := readFixturePayload(t, fixture.plan.TargetPath); got != "new-release" {
		t.Fatalf("upgraded payload = %q, want new-release", got)
	}
	if len(runningResults) != 0 {
		t.Fatalf("running-state queries left unused results: %v", runningResults)
	}
	assertEventOrder(t, fixture.events, "terminate", "wait-stopped", "launch", "wait-running", "schedule-exit")
	assertOnlyInstalledBundle(t, filepath.Dir(fixture.plan.TargetPath), fixture.plan.TargetPath)
}

func TestExecutePlanSameVersionAndBuildKeepsInstalledFixture(t *testing.T) {
	fixture := newInstallFixture(t, "2.1.0", "9", "same-release")
	fixture.plan.TargetExists = true
	makeFixtureBundle(t, fixture.plan.TargetPath, "2.1.0", "9", "same-release")
	hooks := fixtureHooks(fixture)

	result, err := executePlan(context.Background(), fixture.plan, hooks)
	if err != nil {
		t.Fatalf("executePlan: %v", err)
	}
	if result.Disposition != Relaunch {
		t.Fatalf("disposition = %v, want Relaunch", result.Disposition)
	}
	if fixture.copyCount != 0 {
		t.Fatalf("copy count = %d, want no copy for the identical build", fixture.copyCount)
	}
	if got := readFixturePayload(t, fixture.plan.TargetPath); got != "same-release" {
		t.Fatalf("installed payload = %q, want same-release", got)
	}
	assertEventOrder(t, fixture.events, "running", "launch", "wait-running", "schedule-exit")
}

func TestExecutePlanUsesBuildNumberWhenMarketingVersionMatches(t *testing.T) {
	fixture := newInstallFixture(t, "2.1.0", "10", "new-build")
	fixture.plan.TargetExists = true
	makeFixtureBundle(t, fixture.plan.TargetPath, "2.1.0", "9", "old-build")
	hooks := fixtureHooks(fixture)

	result, err := executePlan(context.Background(), fixture.plan, hooks)
	if err != nil {
		t.Fatalf("executePlan: %v", err)
	}
	if result.Disposition != Relaunch || fixture.copyCount != 1 {
		t.Fatalf("same-version build update: disposition=%v copyCount=%d, want Relaunch and one copy", result.Disposition, fixture.copyCount)
	}
	if got := readFixturePayload(t, fixture.plan.TargetPath); got != "new-build" {
		t.Fatalf("installed payload = %q, want new-build", got)
	}
}

func TestExecutePlanLaunchFailureRestoresPreviousBundle(t *testing.T) {
	fixture := newInstallFixture(t, "2.1.0", "9", "new-release")
	fixture.plan.TargetExists = true
	makeFixtureBundle(t, fixture.plan.TargetPath, "2.0.0", "8", "old-release")
	hooks := fixtureHooks(fixture)
	hooks.launch = func(_ context.Context, path string) error {
		fixture.events = append(fixture.events, "launch")
		if filepath.Clean(path) != filepath.Clean(fixture.plan.TargetPath) {
			t.Fatalf("launch path = %q, want %q", path, fixture.plan.TargetPath)
		}
		return errors.New("injected launch failure")
	}

	result, err := executePlan(context.Background(), fixture.plan, hooks)
	if err == nil || !strings.Contains(err.Error(), "injected launch failure") {
		t.Fatalf("executePlan error = %v, want injected launch failure", err)
	}
	if result.Disposition != Continue {
		t.Fatalf("disposition = %v, want Continue after rollback", result.Disposition)
	}
	if got := readFixturePayload(t, fixture.plan.TargetPath); got != "old-release" {
		t.Fatalf("restored payload = %q, want old-release", got)
	}
	if containsEvent(fixture.events, "schedule-exit") {
		t.Fatal("scheduled disk-image cleanup after launch failed")
	}
	assertOnlyInstalledBundle(t, filepath.Dir(fixture.plan.TargetPath), fixture.plan.TargetPath)
}

func TestExecutePlanPreservesOldBundleWhenRollbackCannotRestore(t *testing.T) {
	fixture := newInstallFixture(t, "2.1.0", "9", "new-release")
	fixture.plan.TargetExists = true
	makeFixtureBundle(t, fixture.plan.TargetPath, "2.0.0", "8", "old-release")
	hooks := fixtureHooks(fixture)
	verifyCalls := 0
	hooks.verify = func(_ context.Context, bundle bundleMetadata, identity installIdentity) (bundleProof, error) {
		verifyCalls++
		if verifyCalls == 5 { // source, target, staged, stopped target, promoted target
			return bundleProof{}, errors.New("injected installed verification failure")
		}
		return fixtureProof(bundle, identity)
	}
	hooks.remove = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(fixture.plan.TargetPath) {
			return errors.New("injected target cleanup failure")
		}
		return os.RemoveAll(path)
	}

	_, err := executePlan(context.Background(), fixture.plan, hooks)
	if err == nil || !strings.Contains(err.Error(), "injected installed verification failure") {
		t.Fatalf("executePlan error = %v, want injected installed verification failure", err)
	}
	entries, readErr := os.ReadDir(filepath.Dir(fixture.plan.TargetPath))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var preserved string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".mimac-install-") {
			candidate := filepath.Join(filepath.Dir(fixture.plan.TargetPath), entry.Name(), "Previous-MiMac.app")
			if _, statErr := os.Stat(candidate); statErr == nil {
				preserved = candidate
				break
			}
		}
	}
	if preserved == "" {
		t.Fatalf("rollback copy was not preserved; Applications entries: %v", entryNames(entries))
	}
	if got := readFixturePayload(t, preserved); got != "old-release" {
		t.Fatalf("preserved rollback payload = %q, want old-release", got)
	}
}

func TestExecutePlanDoesNotReplaceAppThatRefusesNormalTermination(t *testing.T) {
	fixture := newInstallFixture(t, "2.1.0", "9", "new-release")
	fixture.plan.TargetExists = true
	makeFixtureBundle(t, fixture.plan.TargetPath, "2.0.0", "8", "old-release")
	hooks := fixtureHooks(fixture)
	hooks.running = func(_ context.Context, bundleID, path string) (bool, error) {
		fixture.events = append(fixture.events, "running")
		assertExactTargetQuery(t, fixture.plan, bundleID, path)
		return true, nil
	}
	hooks.terminate = func(_ context.Context, bundleID, path string) (bool, error) {
		fixture.events = append(fixture.events, "terminate")
		assertExactTargetQuery(t, fixture.plan, bundleID, path)
		return false, nil
	}

	result, err := executePlan(context.Background(), fixture.plan, hooks)
	if err != nil {
		t.Fatalf("executePlan: %v", err)
	}
	if result.Disposition != NeedsUserToQuit {
		t.Fatalf("disposition = %v, want NeedsUserToQuit", result.Disposition)
	}
	if got := readFixturePayload(t, fixture.plan.TargetPath); got != "old-release" {
		t.Fatalf("target payload = %q, want old-release", got)
	}
	if containsEvent(fixture.events, "launch") || containsEvent(fixture.events, "schedule-exit") {
		t.Fatalf("refused termination proceeded with install: %v", fixture.events)
	}
	assertOnlyInstalledBundle(t, filepath.Dir(fixture.plan.TargetPath), fixture.plan.TargetPath)
}

func TestExecutePlanQueriesRunningStateWithExactTargetPath(t *testing.T) {
	fixture := newInstallFixture(t, "2.1.0", "9", "new-release")
	hooks := fixtureHooks(fixture)
	hooks.running = func(_ context.Context, bundleID, path string) (bool, error) {
		fixture.events = append(fixture.events, "running")
		assertExactTargetQuery(t, fixture.plan, bundleID, path)
		// A same-bundle-ID source process is intentionally not considered the
		// installed target because its canonical path differs.
		if filepath.Clean(path) == filepath.Clean(fixture.plan.Source.Path) {
			return true, nil
		}
		return false, nil
	}
	hooks.waitRunning = func(_ context.Context, bundleID, path string) (bool, error) {
		fixture.events = append(fixture.events, "wait-running")
		assertExactTargetQuery(t, fixture.plan, bundleID, path)
		return true, nil
	}

	result, err := executePlan(context.Background(), fixture.plan, hooks)
	if err != nil {
		t.Fatalf("executePlan: %v", err)
	}
	if result.Disposition != Relaunch {
		t.Fatalf("disposition = %v, want Relaunch", result.Disposition)
	}
	assertEventOrder(t, fixture.events, "running", "launch", "wait-running", "schedule-exit")
}

func TestAppinstallNativeFreshTransactionQueriesMissingTarget(t *testing.T) {
	if os.Getenv(nativeFixtureOptIn) != "1" {
		t.Skipf("set %s=1 for the native transaction query", nativeFixtureOptIn)
	}
	fixture := newInstallFixture(t, "2.1.0", "9", "new-release")
	hooks := fixtureHooks(fixture)
	hooks.running = nativeHostExactTargetRunning
	result, err := executePlan(context.Background(), fixture.plan, hooks)
	if err != nil {
		t.Fatalf("fresh transaction with native process query: %v", err)
	}
	if result.Disposition != Relaunch || readFixturePayload(t, fixture.plan.TargetPath) != "new-release" {
		t.Fatal("fresh installation did not finish")
	}
}

func TestAppinstallNativeMissingDestinationIsNotRunning(t *testing.T) {
	if os.Getenv(nativeFixtureOptIn) != "1" {
		t.Skipf("set %s=1 for the read-only native query", nativeFixtureOptIn)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "MiMac.app")
	running, err := nativeHostExactTargetRunning(context.Background(), fixtureBundleID, missing)
	if err != nil {
		t.Fatalf("fresh install destination: %v", err)
	}
	if running {
		t.Fatal("nonexistent fresh destination matched a running app")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("query created destination: %v", err)
	}
}

func TestAppinstallNativeExactIDAndPathQuery(t *testing.T) {
	if os.Getenv(nativeFixtureOptIn) != "1" {
		t.Skipf("set %s=1 to launch a self-owned temporary app fixture", nativeFixtureOptIn)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exitMarker := filepath.Join(root, "exit-helper")
	pidMarker := filepath.Join(root, "fixture.pid")
	stoppedMarker := filepath.Join(root, "fixture.stopped")
	appPath := filepath.Join(root, "MiMac Fixture.app")
	launchAttempted := false
	defer func() {
		_ = os.WriteFile(exitMarker, []byte("exit"), 0600)
		if launchAttempted {
			if err := waitForNativeFixtureFile(stoppedMarker, 8*time.Second); err != nil {
				t.Errorf("native AppKit fixture did not acknowledge shutdown: %v", err)
			}
			if err := waitForNativeFixtureState(fixtureBundleID, appPath, false, 8*time.Second); err != nil {
				t.Errorf("native AppKit fixture remained registered after shutdown: %v", err)
			}
		}
	}()

	otherPath := filepath.Join(root, "Other Fixture.app")
	executable := compileNativeFixtureExecutable(t, root)
	createNativeFixtureApp(t, appPath, fixtureBundleID, executable)
	createNativeFixtureApp(t, otherPath, fixtureBundleID, executable)

	commandContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, "/usr/bin/open", "-n", "-a", appPath, "--args",
		exitMarker, pidMarker, stoppedMarker)
	launchAttempted = true
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("open temporary app fixture: %v: %s", err, strings.TrimSpace(string(output)))
	}
	if err := waitForNativeFixtureFile(pidMarker, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitForNativeFixtureState(fixtureBundleID, appPath, true, 10*time.Second); err != nil {
		t.Fatalf("exactTargetRunning did not find the active AppKit fixture: %v", err)
	}
	if running, err := nativeHostExactTargetRunning(context.Background(), fixtureBundleID, otherPath); err != nil {
		t.Fatalf("query same identity at alternate path: %v", err)
	} else if running {
		t.Fatal("same-ID app running at a different bundle path matched the query")
	}
	if running, err := nativeHostExactTargetRunning(context.Background(), fixtureBundleID+".other", appPath); err != nil {
		t.Fatalf("query alternate identity at exact path: %v", err)
	} else if running {
		t.Fatal("different bundle ID at the exact path matched the query")
	}
	aliasPath := filepath.Join(root, "MiMac Fixture Alias.app")
	if err := os.Symlink(appPath, aliasPath); err != nil {
		t.Fatal(err)
	}
	if running, err := nativeHostExactTargetRunning(context.Background(), fixtureBundleID, aliasPath); err != nil {
		t.Fatalf("query exact fixture through symlink alias: %v", err)
	} else if !running {
		t.Fatal("same fixture through its symlink alias was not recognized as running")
	}
	terminated, err := nativeHostTerminateExactTarget(context.Background(), fixtureBundleID, appPath)
	if err != nil {
		t.Fatalf("terminate exact fixture through NSRunningApplication: %v", err)
	}
	if !terminated {
		t.Fatal("NSRunningApplication.terminate returned false for the exact fixture")
	}
	if err := waitForNativeFixtureFile(stoppedMarker, 8*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitForNativeFixtureState(fixtureBundleID, appPath, false, 8*time.Second); err != nil {
		t.Fatal(err)
	}
}

func compileNativeFixtureExecutable(t *testing.T, root string) string {
	t.Helper()
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		t.Skipf("opt-in native fixture requires swiftc: %v", err)
	}
	sourcePath := filepath.Join(root, "FixtureApp.swift")
	if err := os.WriteFile(sourcePath, []byte(nativeFixtureSwiftSource), 0600); err != nil {
		t.Fatal(err)
	}
	moduleCache := filepath.Join(root, "swift-module-cache")
	if err := os.Mkdir(moduleCache, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "AppInstallFixture")
	commandContext, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, swiftc, "-parse-as-library", "-module-cache-path", moduleCache, "-o", executable, sourcePath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("compile temporary AppKit fixture: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return executable
}

func createNativeFixtureApp(t *testing.T, path, bundleID, sourceExecutable string) {
	t.Helper()
	contents := filepath.Join(path, "Contents")
	macOS := filepath.Join(contents, "MacOS")
	if err := os.MkdirAll(macOS, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sourceExecutable)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(macOS, "AppInstallFixture")
	if err := os.WriteFile(executable, data, 0755); err != nil {
		t.Fatal(err)
	}
	info := map[string]interface{}{
		"CFBundleIdentifier":  bundleID,
		"CFBundleName":        "MiMac Installer Fixture",
		"CFBundleExecutable":  "AppInstallFixture",
		"CFBundlePackageType": "APPL",
		"CFBundleVersion":     "1",
		"LSUIElement":         true,
	}
	plistData, err := plist.Marshal(info, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), plistData, 0644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/usr/bin/codesign", "--force", "--deep", "--sign", "-", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("ad-hoc sign temporary fixture: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func waitForNativeFixtureFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", path)
}

func waitForNativeFixtureState(bundleID, path string, want bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		got, err := nativeHostExactTargetRunning(context.Background(), bundleID, path)
		if err == nil && got == want {
			return nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	got, err := nativeHostExactTargetRunning(context.Background(), bundleID, path)
	if err != nil {
		lastErr = err
	}
	return fmt.Errorf("native fixture running state = %v, err=%v, want %v (last query error: %v)", got, err, want, lastErr)
}

func nativeHostExactTargetRunning(ctx context.Context, bundleID, path string) (bool, error) {
	var running bool
	var queryErr error
	mygo.RunOnMain(func() {
		if !nativeHostOnMainThread() {
			queryErr = fmt.Errorf("MyGo RunOnMain callback is not NSThread main thread")
			return
		}
		running, queryErr = exactTargetRunning(ctx, bundleID, path)
	})
	return running, queryErr
}

func nativeHostTerminateExactTarget(ctx context.Context, bundleID, path string) (bool, error) {
	var terminated bool
	var terminateErr error
	mygo.RunOnMain(func() {
		if !nativeHostOnMainThread() {
			terminateErr = fmt.Errorf("MyGo RunOnMain callback is not NSThread main thread")
			return
		}
		terminated, terminateErr = terminateExactTarget(ctx, bundleID, path)
	})
	return terminated, terminateErr
}

func nativeHostOnMainThread() bool {
	threadClass := objc.GetClass("NSThread")
	return threadClass != 0 && objc.Send[bool](objc.ID(threadClass), installerSelector("isMainThread"))
}

type installFixture struct {
	plan      installPlan
	events    []string
	copyCount int
}

func newInstallFixture(t *testing.T, version, build, payload string) *installFixture {
	t.Helper()
	root := t.TempDir()
	mountPoint := filepath.Join(root, "Mounted Installer")
	applications := filepath.Join(root, "Applications")
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(applications, 0755); err != nil {
		t.Fatal(err)
	}
	source := makeFixtureBundle(t, filepath.Join(mountPoint, "MiMac.app"), version, build, payload)
	return &installFixture{plan: installPlan{
		Source: source, SourcePID: int32(os.Getpid()),
		TargetPath: filepath.Join(applications, "MiMac.app"),
		Identity:   installIdentity{BundleID: fixtureBundleID, Name: "MiMac"},
		Image: diskImage{
			ImagePath: filepath.Join(root, "MiMac-fixture.dmg"), Device: "/dev/disk999",
			VolumeDevice: "/dev/disk999s1", MountPoint: mountPoint,
		},
	}}
}

func makeFixtureBundle(t *testing.T, path, version, build, payload string) bundleMetadata {
	t.Helper()
	contents := filepath.Join(path, "Contents")
	macOS := filepath.Join(contents, "MacOS")
	if err := os.MkdirAll(macOS, 0755); err != nil {
		t.Fatal(err)
	}
	info := map[string]interface{}{
		"CFBundleIdentifier":         fixtureBundleID,
		"CFBundleName":               "MiMac",
		"CFBundleExecutable":         "MiMac",
		"CFBundleShortVersionString": version,
		"CFBundleVersion":            build,
	}
	data, err := plist.Marshal(info, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macOS, "MiMac"), []byte(payload), 0755); err != nil {
		t.Fatal(err)
	}
	bundle, err := readBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func fixtureHooks(fixture *installFixture) installHooks {
	return installHooks{
		verify: func(_ context.Context, bundle bundleMetadata, identity installIdentity) (bundleProof, error) {
			return fixtureProof(bundle, identity)
		},
		copyBundle: func(ctx context.Context, source, destination string) error {
			fixture.copyCount++
			fixture.events = append(fixture.events, "copy")
			return copyBundle(ctx, source, destination) // Exercise the real ditto copy in this temp fixture.
		},
		running: func(_ context.Context, bundleID, path string) (bool, error) {
			fixture.events = append(fixture.events, "running")
			if bundleID != fixture.plan.Identity.BundleID || filepath.Clean(path) != filepath.Clean(fixture.plan.TargetPath) {
				return false, fmt.Errorf("running query got (%s, %s), want (%s, %s)", bundleID, path, fixture.plan.Identity.BundleID, fixture.plan.TargetPath)
			}
			return false, nil
		},
		terminate: func(_ context.Context, bundleID, path string) (bool, error) {
			fixture.events = append(fixture.events, "terminate")
			if bundleID != fixture.plan.Identity.BundleID || filepath.Clean(path) != filepath.Clean(fixture.plan.TargetPath) {
				return false, fmt.Errorf("terminate query got (%s, %s), want (%s, %s)", bundleID, path, fixture.plan.Identity.BundleID, fixture.plan.TargetPath)
			}
			return true, nil
		},
		waitStopped: func(_ context.Context, bundleID, path string) (bool, error) {
			fixture.events = append(fixture.events, "wait-stopped")
			if bundleID != fixture.plan.Identity.BundleID || filepath.Clean(path) != filepath.Clean(fixture.plan.TargetPath) {
				return false, fmt.Errorf("wait-stopped query got (%s, %s), want (%s, %s)", bundleID, path, fixture.plan.Identity.BundleID, fixture.plan.TargetPath)
			}
			return true, nil
		},
		launch: func(_ context.Context, path string) error {
			fixture.events = append(fixture.events, "launch")
			if filepath.Clean(path) != filepath.Clean(fixture.plan.TargetPath) {
				return fmt.Errorf("launch got %s, want %s", path, fixture.plan.TargetPath)
			}
			return nil
		},
		waitRunning: func(_ context.Context, bundleID, path string) (bool, error) {
			fixture.events = append(fixture.events, "wait-running")
			if bundleID != fixture.plan.Identity.BundleID || filepath.Clean(path) != filepath.Clean(fixture.plan.TargetPath) {
				return false, fmt.Errorf("wait-running got (%s, %s), want (%s, %s)", bundleID, path, fixture.plan.Identity.BundleID, fixture.plan.TargetPath)
			}
			return true, nil
		},
		scheduleExit: func(_ context.Context, plan installPlan) error {
			fixture.events = append(fixture.events, "schedule-exit")
			if filepath.Clean(plan.Source.Path) != filepath.Clean(fixture.plan.Source.Path) {
				return errors.New("cleanup received a different source")
			}
			return nil
		},
		remove: os.RemoveAll,
	}
}

func fixtureProof(bundle bundleMetadata, identity installIdentity) (bundleProof, error) {
	if !matchesInstallIdentity(bundle, identity) {
		return bundleProof{}, errors.New("fixture bundle identity mismatch")
	}
	executablePath := filepath.Join(bundle.Path, "Contents", "MacOS", bundle.Executable)
	executable, err := os.ReadFile(executablePath)
	if err != nil {
		return bundleProof{}, err
	}
	data := []byte(bundle.Version + "\x00" + bundle.Build + "\x00")
	data = append(data, executable...)
	codeDirectory := sha256.Sum256(data)
	executableHash := sha256.Sum256(executable)
	return bundleProof{
		TeamIdentifier: "fixture-team",
		CodeDirectory:  hex.EncodeToString(codeDirectory[:]),
		ExecutableSHA:  hex.EncodeToString(executableHash[:]),
	}, nil
}

func assertExactTargetQuery(t *testing.T, plan installPlan, bundleID, path string) {
	t.Helper()
	if bundleID != plan.Identity.BundleID || filepath.Clean(path) != filepath.Clean(plan.TargetPath) {
		t.Fatalf("running query = (%s, %s), want exact target (%s, %s)", bundleID, path, plan.Identity.BundleID, plan.TargetPath)
	}
}

func readFixturePayload(t *testing.T, bundlePath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bundlePath, "Contents", "MacOS", "MiMac"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertOnlyInstalledBundle(t *testing.T, applications, target string) {
	t.Helper()
	entries, err := os.ReadDir(applications)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || filepath.Clean(filepath.Join(applications, entries[0].Name())) != filepath.Clean(target) {
		t.Fatalf("unexpected Applications fixture contents: %v", entryNames(entries))
	}
}

func assertEventOrder(t *testing.T, events []string, expected ...string) {
	t.Helper()
	position := 0
	for _, event := range events {
		if position < len(expected) && event == expected[position] {
			position++
		}
	}
	if position != len(expected) {
		t.Fatalf("events %v do not contain required order %v", events, expected)
	}
}

func containsEvent(events []string, expected string) bool {
	for _, event := range events {
		if event == expected {
			return true
		}
	}
	return false
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
