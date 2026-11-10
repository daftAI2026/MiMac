//go:build darwin

package appinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type bundleProof struct {
	TeamIdentifier string
	CodeDirectory  string
	ExecutableSHA  string
}

// installHooks isolate external effects so package tests can exercise the
// copy/verify/rollback transaction using temporary application bundles.
type installHooks struct {
	verify       func(context.Context, bundleMetadata, installIdentity) (bundleProof, error)
	copyBundle   func(context.Context, string, string) error
	running      func(context.Context, string, string) (bool, error)
	terminate    func(context.Context, string, string) (bool, error)
	waitStopped  func(context.Context, string, string) (bool, error)
	launch       func(context.Context, string) error
	waitRunning  func(context.Context, string, string) (bool, error)
	scheduleExit func(context.Context, installPlan) error
	remove       func(string) error
}

func executePlan(ctx context.Context, plan installPlan, hooks installHooks) (Result, error) {
	result := Result{Disposition: Continue, SourcePath: plan.Source.Path, TargetPath: plan.TargetPath}
	if err := validateInstallPlan(plan); err != nil {
		return result, err
	}
	if err := requireInstallHooks(hooks); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	sourceProof, err := hooks.verify(ctx, plan.Source, plan.Identity)
	if err != nil {
		return result, fmt.Errorf("appinstall: verify source app: %w", err)
	}
	if sourceProof.ExecutableSHA == "" || sourceProof.CodeDirectory == "" {
		return result, errors.New("appinstall: source signature or executable identity is incomplete")
	}

	target, targetErr := readBundle(plan.TargetPath)
	if errors.Is(targetErr, os.ErrNotExist) {
		targetErr = nil
	} else if targetErr == nil {
		if !matchesInstallIdentity(target, plan.Identity) {
			return result, fmt.Errorf("%w: %s", errBundleConflict, plan.TargetPath)
		}
	} else {
		return result, fmt.Errorf("appinstall: inspect existing target: %w", targetErr)
	}
	targetExists := targetErr == nil && target.Path != ""
	if targetExists != plan.TargetExists {
		return result, errors.New("appinstall: install target changed during inspection")
	}

	var targetProof bundleProof
	if targetExists {
		targetProof, err = hooks.verify(ctx, target, plan.Identity)
		if err != nil {
			return result, fmt.Errorf("appinstall: verify installed target: %w", err)
		}
		if targetProof.ExecutableSHA == "" || targetProof.CodeDirectory == "" || targetProof.TeamIdentifier != sourceProof.TeamIdentifier {
			return result, fmt.Errorf("%w: installed app signing identity differs", errBundleConflict)
		}
		if installedBuildIsNewer(target, plan.Source) || proofEqual(targetProof, sourceProof) {
			return launchExisting(ctx, plan, hooks, result)
		}
	}

	parent := filepath.Dir(plan.TargetPath)
	if err := ensureRealDirectory(parent, 0755); err != nil {
		return result, fmt.Errorf("appinstall: prepare Applications directory: %w", err)
	}
	if !writableDirectory(parent) {
		return result, fmt.Errorf("%w: %s", errInstallAccess, parent)
	}
	stageRoot, err := os.MkdirTemp(parent, ".mimac-install-")
	if err != nil {
		return result, fmt.Errorf("appinstall: create staging directory: %w", err)
	}
	preserveStage := false
	defer func() {
		if !preserveStage {
			_ = hooks.remove(stageRoot)
		}
	}()
	if err := os.Chmod(stageRoot, 0700); err != nil {
		return result, err
	}
	stageBundle := filepath.Join(stageRoot, plan.Identity.Name+".app")
	if err := hooks.copyBundle(ctx, plan.Source.Path, stageBundle); err != nil {
		return result, fmt.Errorf("appinstall: copy app to staging: %w", err)
	}
	staged, err := readBundle(stageBundle)
	if err != nil {
		return result, fmt.Errorf("appinstall: read staged app identity: %w", err)
	}
	if !matchesInstallIdentity(staged, plan.Identity) {
		return result, fmt.Errorf("appinstall: staged app identity changed")
	}
	stageProof, err := hooks.verify(ctx, staged, plan.Identity)
	if err != nil {
		return result, fmt.Errorf("appinstall: verify staged app: %w", err)
	}
	if !proofEqual(stageProof, sourceProof) {
		return result, errors.New("appinstall: staged signature or executable differs from source")
	}

	oldBundle := filepath.Join(stageRoot, "Previous-"+plan.Identity.Name+".app")
	rollback := func(cause error) error {
		rollbackErr := rollbackInstall(plan.TargetPath, oldBundle, targetExists, hooks, cause)
		if targetExists {
			if _, statErr := os.Lstat(oldBundle); statErr == nil {
				preserveStage = true
			}
		}
		return rollbackErr
	}
	if targetExists {
		if err := stopExactTarget(ctx, plan, hooks); err != nil {
			if errors.Is(err, errNeedsUserToQuit) {
				result.Disposition = NeedsUserToQuit
				return result, nil
			}
			return result, err
		}
		// Re-read the old target after it has stopped. Never move a changed or
		// newly active app out from under another process.
		current, err := readBundle(plan.TargetPath)
		if err != nil {
			return result, fmt.Errorf("appinstall: recheck target before replacement: %w", err)
		}
		currentProof, err := hooks.verify(ctx, current, plan.Identity)
		if err != nil || !proofEqual(currentProof, targetProof) {
			if err == nil {
				err = errors.New("signature or executable hash changed")
			}
			return result, fmt.Errorf("appinstall: installed app changed while replacement was prepared: %w", err)
		}
		active, err := hooks.running(ctx, plan.Identity.BundleID, plan.TargetPath)
		if err != nil {
			return result, fmt.Errorf("appinstall: recheck target process: %w", err)
		}
		if active {
			result.Disposition = NeedsUserToQuit
			return result, nil
		}
		if err := os.Rename(plan.TargetPath, oldBundle); err != nil {
			return result, fmt.Errorf("appinstall: preserve old app before update: %w", err)
		}
		if err := ctx.Err(); err != nil {
			if restoreErr := os.Rename(oldBundle, plan.TargetPath); restoreErr != nil {
				preserveStage = true
				return result, fmt.Errorf("%w; restore old app after cancellation: %v", err, restoreErr)
			}
			return result, err
		}
	} else {
		if _, err := os.Lstat(plan.TargetPath); err == nil {
			return result, fmt.Errorf("%w: install target appeared during staging", errBundleConflict)
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("appinstall: recheck empty target path: %w", err)
		}
		active, err := hooks.running(ctx, plan.Identity.BundleID, plan.TargetPath)
		if err != nil {
			return result, fmt.Errorf("appinstall: check target process before first install: %w", err)
		}
		if active {
			result.Disposition = NeedsUserToQuit
			return result, nil
		}
	}
	if err := os.Rename(stageBundle, plan.TargetPath); err != nil {
		if targetExists {
			if restoreErr := os.Rename(oldBundle, plan.TargetPath); restoreErr != nil {
				preserveStage = true
				return result, fmt.Errorf("appinstall: install staged app: %v; restore old app: %w", err, restoreErr)
			}
		}
		return result, fmt.Errorf("appinstall: install staged app: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, rollback(err)
	}
	installed, err := readBundle(plan.TargetPath)
	if err == nil {
		var installedProof bundleProof
		installedProof, err = hooks.verify(ctx, installed, plan.Identity)
		if err == nil && !proofEqual(installedProof, sourceProof) {
			err = errors.New("installed signature or executable differs from source")
		}
	}
	if err != nil {
		return result, rollback(fmt.Errorf("appinstall: verify installed app: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return result, rollback(err)
	}
	if err := hooks.launch(ctx, plan.TargetPath); err != nil {
		return result, rollback(fmt.Errorf("appinstall: launch installed app: %w", err))
	}
	started, err := hooks.waitRunning(ctx, plan.Identity.BundleID, plan.TargetPath)
	if err != nil || !started {
		// The source app has the same bundle ID while this function runs. The
		// hook only accepts the destination's canonical path.
		active, activeErr := hooks.running(ctx, plan.Identity.BundleID, plan.TargetPath)
		if activeErr == nil && active {
		} else if activeErr != nil {
			preserveStage = targetExists
			return result, fmt.Errorf("appinstall: cannot determine whether installed app is running; preserved rollback copy: %w", activeErr)
		} else {
			if err == nil {
				err = errors.New("launch did not produce a running process at the destination")
			}
			return result, rollback(fmt.Errorf("appinstall: installed app did not become active: %w", err))
		}
	}
	if err := hooks.scheduleExit(context.Background(), plan); err != nil {
		result.Disposition = Relaunch
		return result, fmt.Errorf("appinstall: installed app is running but disk-image cleanup could not start: %w", err)
	}
	if targetExists {
		// The previous bundle has served as a rollback copy until the new path
		// was observed running. It contains no user configuration.
		_ = hooks.remove(oldBundle)
	}
	result.Disposition = Relaunch
	return result, nil
}

func launchExisting(ctx context.Context, plan installPlan, hooks installHooks, result Result) (Result, error) {
	active, err := hooks.running(ctx, plan.Identity.BundleID, plan.TargetPath)
	if err != nil {
		return result, fmt.Errorf("appinstall: inspect installed process: %w", err)
	}
	if !active {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := hooks.launch(ctx, plan.TargetPath); err != nil {
			return result, fmt.Errorf("appinstall: launch installed app: %w", err)
		}
		active, err = hooks.waitRunning(ctx, plan.Identity.BundleID, plan.TargetPath)
		if err != nil || !active {
			if err == nil {
				err = errors.New("launch did not produce a running process at the destination")
			}
			return result, fmt.Errorf("appinstall: installed app did not become active: %w", err)
		}
	}
	result.Disposition = Relaunch
	if err := hooks.scheduleExit(context.Background(), plan); err != nil {
		return result, fmt.Errorf("appinstall: installed app is running but disk-image cleanup could not start: %w", err)
	}
	return result, nil
}

func stopExactTarget(ctx context.Context, plan installPlan, hooks installHooks) error {
	active, err := hooks.running(ctx, plan.Identity.BundleID, plan.TargetPath)
	if err != nil {
		return fmt.Errorf("appinstall: inspect installed process: %w", err)
	}
	if !active {
		return nil
	}
	accepted, err := hooks.terminate(ctx, plan.Identity.BundleID, plan.TargetPath)
	if err != nil {
		return fmt.Errorf("appinstall: request normal app termination: %w", err)
	}
	if !accepted {
		return errNeedsUserToQuit
	}
	stopped, err := hooks.waitStopped(ctx, plan.Identity.BundleID, plan.TargetPath)
	if err != nil {
		return fmt.Errorf("appinstall: wait for normal app termination: %w", err)
	}
	if !stopped {
		return errNeedsUserToQuit
	}
	return nil
}

func rollbackInstall(target, old string, hadOld bool, hooks installHooks, cause error) error {
	removeErr := hooks.remove(target)
	if hadOld {
		restoreErr := os.Rename(old, target)
		if removeErr != nil || restoreErr != nil {
			return fmt.Errorf("%w; rollback remove=%v restore=%v", cause, removeErr, restoreErr)
		}
		return cause
	}
	if removeErr != nil {
		return fmt.Errorf("%w; remove incomplete installation: %v", cause, removeErr)
	}
	return cause
}

func validateInstallPlan(plan installPlan) error {
	if !filepath.IsAbs(plan.Source.Path) || !filepath.IsAbs(plan.TargetPath) || filepath.Clean(plan.Source.Path) == filepath.Clean(plan.TargetPath) {
		return errors.New("appinstall: invalid source or target path")
	}
	wholeDevice, wholeDeviceOK := wholeDiskDevice(plan.Image.VolumeDevice)
	if !matchesInstallIdentity(plan.Source, plan.Identity) || plan.SourcePID <= 0 || !validDiskDevice(plan.Image.VolumeDevice) ||
		!validDiskDevice(plan.Image.Device) || !wholeDeviceOK || wholeDevice != plan.Image.Device ||
		!filepath.IsAbs(plan.Image.ImagePath) || !strings.EqualFold(filepath.Ext(plan.Image.ImagePath), ".dmg") ||
		!filepath.IsAbs(plan.Image.MountPoint) || !pathWithin(plan.Image.MountPoint, plan.Source.Path) {
		return errors.New("appinstall: incomplete installation plan")
	}
	return nil
}

func requireInstallHooks(hooks installHooks) error {
	if hooks.verify == nil || hooks.copyBundle == nil || hooks.running == nil || hooks.terminate == nil || hooks.waitStopped == nil ||
		hooks.launch == nil || hooks.waitRunning == nil || hooks.scheduleExit == nil || hooks.remove == nil {
		return errors.New("appinstall: incomplete execution hooks")
	}
	return nil
}

func installedBuildIsNewer(target, source bundleMetadata) bool {
	version := compareVersions(target.Version, source.Version)
	if version != 0 {
		return version > 0
	}
	return compareVersions(target.Build, source.Build) > 0
}

func matchesInstallIdentity(bundle bundleMetadata, identity installIdentity) bool {
	return bundle.BundleID == identity.BundleID && bundle.BundleName == identity.Name && filepath.Base(bundle.Path) == identity.Name+".app"
}

func proofEqual(left, right bundleProof) bool {
	return left.TeamIdentifier == right.TeamIdentifier && left.CodeDirectory == right.CodeDirectory && left.ExecutableSHA == right.ExecutableSHA
}

func compareVersions(left, right string) int {
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < len(leftParts) || index < len(rightParts); index++ {
		var a, b uint64
		if index < len(leftParts) {
			if leftParts[index] != "" {
				parsed, err := strconv.ParseUint(leftParts[index], 10, 64)
				if err != nil {
					return 0
				}
				a = parsed
			}
		}
		if index < len(rightParts) {
			if rightParts[index] != "" {
				parsed, err := strconv.ParseUint(rightParts[index], 10, 64)
				if err != nil {
					return 0
				}
				b = parsed
			}
		}
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
	}
	return 0
}

var transactionWait = 20 * time.Second
