package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/platform/runningapp"
)

const developmentAppName = "MiMac Dev"

var errDevelopmentInputDisabled = errors.New("开发版未接管设备，请在日用版测试")

// MyGo supplies the packaged name, including its separate development identity.
// Unpackaged command-line execution retains the established production path.
func initializeHostIdentity() {
	if !mygo.App.IsPackaged() {
		mygo.App.SetName("MiMac")
	}
}

func hostDataPath(filename string) (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filename), nil
}

// This host policy protects daily use during UI/configuration development.
// It is not a second device or speech state machine. Explicit device testing
// is serialized with the daily app by the development launch entrypoint.
func (s *shell) requireDeviceAccess() error {
	if s.isolatedDevelopment {
		return errDevelopmentInputDisabled
	}
	if s.development {
		return checkDevelopmentDeviceAccess()
	}
	return nil
}

func checkDevelopmentDeviceAccess() error {
	active, err := runningapp.Exists(context.Background(), "dev.mimac.app")
	if err != nil {
		return fmt.Errorf("无法确认日用版是否已退出：%w", err)
	}
	if active {
		return errors.New("请先退出日用版 MiMac，再开始开发版设备测试")
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	recovery := filepath.Join(base, "MiMac", "voice-mapping-recovery.plist")
	_, err = os.Stat(recovery)
	if err == nil {
		return errors.New("请先在日用版 MiMac 恢复遥控器映射，再开始开发版设备测试")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("无法确认日用版设备已释放：%w", err)
	}
	return nil
}
