package main

import (
	"context"
	"fmt"
	"os"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/platform/appinstall"
)

type appInstallState struct {
	installing         bool
	installCancel      context.CancelFunc
	installQuitPending bool
}

// installFromDiskImage keeps copying and process waits off MyGo's event loop.
// The host starts its plugins only after this startup gate has completed.
func (s *shell) installFromDiskImage(ctx context.Context, continueStartup func()) {
	if s.development || !mygo.App.IsPackaged() {
		continueStartup()
		return
	}
	installContext, cancel := context.WithCancel(ctx)
	s.installing, s.installCancel = true, cancel
	go func() {
		result, err := appinstall.MaybeInstall(installContext)
		cancel()
		mygo.RunOnMain(func() {
			s.installing, s.installCancel = false, nil
			if s.installQuitPending || result.Disposition == appinstall.Relaunch {
				if err != nil {
					fmt.Fprintln(os.Stderr, "installation:", err)
				}
				mygo.App.Quit()
				return
			}
			if err == nil && result.Disposition == appinstall.Continue {
				continueStartup()
				return
			}
			message, detail := "请先退出正在运行的 MiMac", "退出后点重试，继续安装。"
			if err != nil {
				fmt.Fprintln(os.Stderr, "installation:", err)
				message, detail = "未能完成安装", "安装未完成，请重试。原有设置会保留。"
			}
			answer, dialogErr := mygo.Dialog.Message(mygo.MessageOptions{
				Type: mygo.MessageInfo, Title: "安装 MiMac", Message: message,
				Detail: detail, Buttons: []string{"重试", "关闭"}, DefaultButton: 0, CancelButton: 1,
			})
			if dialogErr != nil || answer.Button != 0 {
				mygo.App.Quit()
				return
			}
			s.installFromDiskImage(ctx, continueStartup)
		})
	}()
}

// Give the transaction a chance to restore a moved bundle before exiting.
func (s *shell) deferInstallQuit(preventDefault func()) bool {
	if !s.installing {
		return false
	}
	preventDefault()
	s.installQuitPending = true
	if s.installCancel != nil {
		s.installCancel()
	}
	return true
}
