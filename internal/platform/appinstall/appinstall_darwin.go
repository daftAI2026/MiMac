//go:build darwin

package appinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"howett.net/plist"
)

const (
	dailyBundleID   = "dev.mimac.app"
	dailyBundleName = "MiMac"
	readOnlyMount   = 0x00000001
)

var (
	errBundleConflict  = errors.New("appinstall: another or invalid app occupies the install path")
	errInstallAccess   = errors.New("appinstall: no writable Applications location is available")
	errNeedsUserToQuit = errors.New("appinstall: installed MiMac did not quit normally; quit it and retry")
)

type bundleMetadata struct {
	Path       string
	BundleID   string
	BundleName string
	Executable string
	Version    string
	Build      string
}

type installIdentity struct {
	BundleID string
	Name     string
}

type diskImage struct {
	ImagePath    string
	Device       string
	VolumeDevice string
	MountPoint   string
}

type installPlan struct {
	Source       bundleMetadata
	SourcePID    int32
	TargetPath   string
	TargetExists bool
	Identity     installIdentity
	Image        diskImage
}

// MaybeInstall handles first launch from an attached, read-only DMG. It is a
// cheap no-op for ordinary installed launches and for the isolated Dev app.
func MaybeInstall(parent context.Context) (Result, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()

	source, ok, err := currentBundle()
	if err != nil || !ok || !isDailyBundle(source) {
		return Result{Disposition: Continue}, err
	}

	image, onImage, err := diskImageForBundle(ctx, source.Path)
	if err != nil {
		return Result{Disposition: Continue, SourcePath: source.Path}, fmt.Errorf("appinstall: inspect source volume: %w", err)
	}
	if !onImage {
		return Result{Disposition: Continue}, nil
	}

	target, exists, err := chooseTarget(source)
	if err != nil {
		return Result{Disposition: Continue, SourcePath: source.Path}, err
	}
	plan := installPlan{
		Source: source, SourcePID: int32(os.Getpid()), TargetPath: target, TargetExists: exists,
		Identity: installIdentity{BundleID: dailyBundleID, Name: dailyBundleName},
		Image:    image,
	}
	return executePlan(ctx, plan, systemInstallHooks())
}

func isDailyBundle(bundle bundleMetadata) bool {
	return bundle.BundleID == dailyBundleID && bundle.BundleName == dailyBundleName && filepath.Base(bundle.Path) == dailyBundleName+".app"
}

func currentBundle() (bundleMetadata, bool, error) {
	executable, err := os.Executable()
	if err != nil {
		return bundleMetadata{}, false, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return bundleMetadata{}, false, err
	}
	if filepath.Base(filepath.Dir(executable)) != "MacOS" || filepath.Base(filepath.Dir(filepath.Dir(executable))) != "Contents" {
		return bundleMetadata{}, false, nil
	}
	bundlePath := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	bundle, err := readBundle(bundlePath)
	if err != nil {
		return bundleMetadata{}, false, err
	}
	expectedExecutable := filepath.Join(bundlePath, "Contents", "MacOS", bundle.Executable)
	if filepath.Clean(expectedExecutable) != filepath.Clean(executable) {
		return bundleMetadata{}, false, nil
	}
	return bundle, true, nil
}

func readBundle(path string) (bundleMetadata, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return bundleMetadata{}, err
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return bundleMetadata{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return bundleMetadata{}, fmt.Errorf("appinstall: bundle path is not a real directory: %s", path)
	}
	infoPath := filepath.Join(path, "Contents", "Info.plist")
	infoStat, err := os.Lstat(infoPath)
	if err != nil {
		return bundleMetadata{}, err
	}
	if !infoStat.Mode().IsRegular() || infoStat.Mode()&os.ModeSymlink != 0 || infoStat.Size() <= 0 || infoStat.Size() > 4<<20 {
		return bundleMetadata{}, fmt.Errorf("appinstall: invalid Info.plist in %s", path)
	}
	data, err := os.ReadFile(infoPath)
	if err != nil {
		return bundleMetadata{}, err
	}
	var values map[string]interface{}
	if _, err := plist.Unmarshal(data, &values); err != nil {
		return bundleMetadata{}, fmt.Errorf("appinstall: decode Info.plist: %w", err)
	}
	stringValue := func(key string) string {
		value, _ := values[key].(string)
		return strings.TrimSpace(value)
	}
	metadata := bundleMetadata{
		Path: path, BundleID: stringValue("CFBundleIdentifier"),
		BundleName: stringValue("CFBundleName"), Executable: stringValue("CFBundleExecutable"),
		Version: stringValue("CFBundleShortVersionString"), Build: stringValue("CFBundleVersion"),
	}
	if metadata.BundleID == "" || metadata.BundleName == "" || !validBundleExecutable(metadata.Executable) {
		return bundleMetadata{}, fmt.Errorf("appinstall: incomplete bundle identity in %s", path)
	}
	return metadata, nil
}

func validBundleExecutable(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\\x00")
}

func diskImageForBundle(ctx context.Context, bundlePath string) (diskImage, bool, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(bundlePath, &stat); err != nil {
		return diskImage{}, false, err
	}
	if stat.Flags&readOnlyMount == 0 {
		return diskImage{}, false, nil
	}
	volumeDevice := cString(stat.Mntfromname[:])
	mountPoint := filepath.Clean(cString(stat.Mntonname[:]))
	if !validDiskDevice(volumeDevice) || !filepath.IsAbs(mountPoint) || !pathWithin(mountPoint, bundlePath) {
		return diskImage{}, false, nil
	}
	images, err := attachedImages(ctx)
	if err != nil {
		return diskImage{}, false, err
	}
	var match *diskImage
	for _, image := range images {
		if image.ImagePath == "" || !strings.EqualFold(filepath.Ext(image.ImagePath), ".dmg") || image.Writable {
			continue
		}
		for _, entity := range image.Entities {
			if entity.Device == volumeDevice && filepath.Clean(entity.MountPoint) == mountPoint {
				if match != nil {
					return diskImage{}, false, errors.New("multiple attached images match the app volume")
				}
				wholeDevice, ok := wholeDiskDevice(volumeDevice)
				if !ok {
					return diskImage{}, false, nil
				}
				match = &diskImage{ImagePath: image.ImagePath, Device: wholeDevice, VolumeDevice: volumeDevice, MountPoint: mountPoint}
			}
		}
	}
	if match == nil {
		return diskImage{}, false, nil
	}
	return *match, true, nil
}

type attachedImage struct {
	ImagePath string
	Writable  bool
	Entities  []imageEntity
}

type imageEntity struct {
	Device     string
	MountPoint string
}

func attachedImages(ctx context.Context) ([]attachedImage, error) {
	command := execCommandContext(ctx, "/usr/bin/hdiutil", "info", "-plist")
	data, err := command.Output()
	if err != nil {
		return nil, err
	}
	var root map[string]interface{}
	if _, err := plist.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("decode disk-image list: %w", err)
	}
	entries, _ := root["images"].([]interface{})
	result := make([]attachedImage, 0, len(entries))
	for _, raw := range entries {
		value, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		imagePath, _ := value["image-path"].(string)
		writable, ok := value["writeable"].(bool)
		if !ok {
			continue
		}
		image := attachedImage{ImagePath: imagePath, Writable: writable}
		entities, _ := value["system-entities"].([]interface{})
		for _, rawEntity := range entities {
			entity, ok := rawEntity.(map[string]interface{})
			if !ok {
				continue
			}
			device, _ := entity["dev-entry"].(string)
			mount, _ := entity["mount-point"].(string)
			image.Entities = append(image.Entities, imageEntity{Device: device, MountPoint: mount})
		}
		result = append(result, image)
	}
	return result, nil
}

func chooseTarget(source bundleMetadata) (string, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	locations := []struct{ directory, bundle string }{
		{directory: "/Applications", bundle: "/Applications/MiMac.app"},
		{directory: filepath.Join(home, "Applications"), bundle: filepath.Join(home, "Applications", "MiMac.app")},
	}
	type candidate struct {
		path   string
		exists bool
	}
	var existing []candidate
	for _, location := range locations {
		info, err := os.Lstat(location.bundle)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("%w: %s", errBundleConflict, location.bundle)
		}
		observed, err := readBundle(location.bundle)
		if err != nil || observed.BundleID != source.BundleID || observed.BundleName != source.BundleName {
			return "", false, fmt.Errorf("%w: %s", errBundleConflict, location.bundle)
		}
		existing = append(existing, candidate{path: location.bundle, exists: true})
	}
	if len(existing) > 1 {
		return "", false, fmt.Errorf("%w: more than one installed copy exists", errBundleConflict)
	}
	if len(existing) == 1 {
		return existing[0].path, true, nil
	}
	if writableDirectory(locations[0].directory) {
		return locations[0].bundle, false, nil
	}
	if !writableDirectory(home) {
		return "", false, errInstallAccess
	}
	userDir := locations[1].directory
	if err := ensureRealDirectory(userDir, 0755); err != nil {
		return "", false, err
	}
	if !writableDirectory(userDir) {
		return "", false, errInstallAccess
	}
	return locations[1].bundle, false, nil
}

func ensureRealDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("appinstall: unsafe Applications directory: %s", path)
	}
	return nil
}

func writableDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	return syscall.Access(path, 2) == nil
}

func pathWithin(directory, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(directory), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func cString(value []int8) string {
	bytes := make([]byte, 0, len(value))
	for _, item := range value {
		if item == 0 {
			break
		}
		bytes = append(bytes, byte(item))
	}
	return string(bytes)
}

func validDiskDevice(device string) bool {
	if !strings.HasPrefix(device, "/dev/disk") {
		return false
	}
	_, ok := wholeDiskDevice(device)
	return ok
}

func wholeDiskDevice(device string) (string, bool) {
	if !strings.HasPrefix(device, "/dev/disk") {
		return "", false
	}
	name := strings.TrimPrefix(device, "/dev/")
	if !strings.HasPrefix(name, "disk") {
		return "", false
	}
	digits := name[len("disk"):]
	partition := strings.IndexByte(digits, 's')
	if partition >= 0 {
		digits = digits[:partition]
	}
	if digits == "" {
		return "", false
	}
	for _, char := range digits {
		if char < '0' || char > '9' {
			return "", false
		}
	}
	if partition >= 0 {
		part := name[len("disk")+partition+1:]
		if part == "" {
			return "", false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return "", false
			}
		}
	}
	return "/dev/disk" + digits, true
}

// execCommandContext is replaceable by package tests without widening the
// production API or adding launch-time configuration switches.
var execCommandContext = exec.CommandContext
