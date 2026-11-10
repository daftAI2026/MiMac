//go:build darwin

package appinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var codeDirectoryPattern = regexp.MustCompile(`(?m)^CDHash=([0-9a-fA-F]+)$`)

func systemInstallHooks() installHooks {
	return installHooks{
		verify:       verifyBundle,
		copyBundle:   copyBundle,
		running:      exactTargetRunning,
		terminate:    terminateExactTarget,
		waitStopped:  waitForTargetState(false),
		launch:       launchTarget,
		waitRunning:  waitForTargetState(true),
		scheduleExit: scheduleDetach,
		remove:       os.RemoveAll,
	}
}

func verifyBundle(ctx context.Context, bundle bundleMetadata, identity installIdentity) (bundleProof, error) {
	if !matchesInstallIdentity(bundle, identity) {
		return bundleProof{}, fmt.Errorf("bundle identity mismatch: %s", bundle.Path)
	}
	actual, err := readBundle(bundle.Path)
	if err != nil {
		return bundleProof{}, err
	}
	if actual.BundleID != bundle.BundleID || actual.Executable != bundle.Executable {
		return bundleProof{}, errors.New("bundle metadata changed during verification")
	}
	executable := filepath.Join(bundle.Path, "Contents", "MacOS", bundle.Executable)
	info, err := os.Lstat(executable)
	if err != nil {
		return bundleProof{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return bundleProof{}, errors.New("bundle executable is not a regular file")
	}
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "--deep", bundle.Path).Run(); err != nil {
		return bundleProof{}, fmt.Errorf("codesign verification failed: %w", err)
	}
	display := exec.CommandContext(ctx, "/usr/bin/codesign", "--display", "--verbose=4", bundle.Path)
	output, err := display.CombinedOutput()
	if err != nil {
		return bundleProof{}, fmt.Errorf("read code signature: %w", err)
	}
	text := string(output)
	identifier := signatureValue(text, `(?m)^Identifier=(.*)$`)
	team := signatureValue(text, `(?m)^TeamIdentifier=(.*)$`)
	codeDirectory := signatureValue(text, codeDirectoryPattern.String())
	if identifier != identity.BundleID || codeDirectory == "" || !isHex(codeDirectory) {
		return bundleProof{}, errors.New("code signature does not match the app bundle identity")
	}
	if strings.EqualFold(team, "not set") {
		team = ""
	}
	hash, err := sha256FileContext(ctx, executable)
	if err != nil {
		return bundleProof{}, err
	}
	return bundleProof{TeamIdentifier: team, CodeDirectory: strings.ToLower(codeDirectory), ExecutableSHA: hash}, nil
}

func signatureValue(text, pattern string) string {
	match := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func isHex(value string) bool {
	if value == "" {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sha256File(path string) (string, error) {
	return sha256FileContext(context.Background(), path)
}

func sha256FileContext(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("not a regular file")
		}
		return "", err
	}
	hash := sha256.New()
	buffer := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyBundle(ctx context.Context, source, destination string) error {
	command := exec.CommandContext(ctx, "/usr/bin/ditto", "--rsrc", "--extattr", "--acl", source, destination)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ditto: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func launchTarget(ctx context.Context, path string) error {
	command := exec.CommandContext(ctx, "/usr/bin/open", "-n", "-a", path)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("open: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func waitForTargetState(wantRunning bool) func(context.Context, string, string) (bool, error) {
	return func(ctx context.Context, bundleID, path string) (bool, error) {
		deadline := time.NewTimer(transactionWait)
		defer deadline.Stop()
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		for {
			running, err := exactTargetRunning(ctx, bundleID, path)
			if err != nil {
				return false, err
			}
			if running == wantRunning {
				return true, nil
			}
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-deadline.C:
				return false, nil
			case <-ticker.C:
			}
		}
	}
}

func scheduleDetach(ctx context.Context, plan installPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scriptPath, err := ensureDetachScript()
	if err != nil {
		return err
	}
	command := exec.Command("/usr/bin/osascript", "-l", "JavaScript", scriptPath,
		fmt.Sprint(plan.SourcePID), plan.Identity.BundleID, plan.Source.Path,
		plan.Image.ImagePath, plan.Image.Device, plan.Image.VolumeDevice, plan.Image.MountPoint)
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	command.Stdin, command.Stdout, command.Stderr = devNull, devNull, devNull
	if err := command.Start(); err != nil {
		return fmt.Errorf("start bounded disk-image cleanup helper: %w", err)
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("release disk-image cleanup helper: %w", err)
	}
	return nil
}

func ensureDetachScript() (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	cacheRoot, err = filepath.EvalSymlinks(cacheRoot)
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	directory := filepath.Join(cacheRoot, "MiMac", "appinstall")
	if err := ensureRealDirectory(filepath.Dir(directory), 0700); err != nil {
		return "", err
	}
	if err := ensureRealDirectory(directory, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return "", err
	}
	content := []byte(detachScript)
	digest := sha256.Sum256(content)
	name := "detach-" + hex.EncodeToString(digest[:]) + ".js"
	path := filepath.Join(directory, name)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("cached cleanup helper is not a regular file")
		}
		have, err := sha256File(path)
		if err != nil || have != hex.EncodeToString(digest[:]) {
			return "", errors.New("cached cleanup helper failed integrity check")
		}
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

const detachScript = `ObjC.import('AppKit');
ObjC.import('Foundation');
function run(argv) {
  if (argv.length !== 7) throw new Error('invalid cleanup arguments');
  var pid = Number(argv[0]);
  var bundleID = argv[1];
  var sourcePath = argv[2];
  var imagePath = argv[3];
  var wholeDevice = argv[4];
  var volumeDevice = argv[5];
  var mountPoint = argv[6];
  if (!Number.isInteger(pid) || pid <= 0 || !/^dev\.mimac\.app$/.test(bundleID) ||
      !/^\/dev\/disk[0-9]+$/.test(wholeDevice) || !/^\/dev\/disk[0-9]+s[0-9]+$/.test(volumeDevice) ||
      volumeDevice.replace(/s[0-9]+$/, '') !== wholeDevice)
    throw new Error('invalid cleanup identity');
  var app = Application.currentApplication();
  app.includeStandardAdditions = true;
  var deadline = Date.now() + 180000;
  while (Date.now() < deadline) {
    var running = $.NSRunningApplication.runningApplicationWithProcessIdentifier(pid);
    if (running == null) break;
    var observedID = running.bundleIdentifier;
    var bundleURL = running.bundleURL;
    if (observedID == null || bundleURL == null) break;
    observedID = ObjC.unwrap(observedID);
    var observedPath = ObjC.unwrap(bundleURL.path);
    if (observedID !== bundleID || observedPath !== sourcePath) break;
    delay(0.25);
  }
  if (Date.now() >= deadline) throw new Error('source app did not exit before cleanup deadline');
  var output = app.doShellScript('/usr/bin/hdiutil info -plist');
  var nsOutput = $.NSString.stringWithString(output);
  var data = nsOutput.dataUsingEncoding($.NSUTF8StringEncoding);
  var format = Ref();
  var parseError = Ref();
  var root = $.NSPropertyListSerialization.propertyListWithDataOptionsFormatError(
    data, $.NSPropertyListImmutable, format, parseError);
  if (root === null || root === undefined) throw new Error('cannot parse attached disk images');
  var images = ObjC.deepUnwrap(root).images || [];
  var match = false;
  images.forEach(function(image) {
    if (image['image-path'] !== imagePath || image.writeable !== false) return;
    (image['system-entities'] || []).forEach(function(entity) {
      if (entity['dev-entry'] === volumeDevice && entity['mount-point'] === mountPoint) match = true;
    });
  });
  if (!match) throw new Error('source image no longer matches the mounted device');
  app.doShellScript('/usr/bin/hdiutil detach ' + volumeDevice);
}
`
