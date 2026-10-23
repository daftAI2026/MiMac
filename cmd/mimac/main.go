// MiMac uses MyGo as its desktop host. Device and audio adapters are activated
// separately; starting the menu bar host does not request capture permissions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type hostReport struct {
	Ready            bool   `json:"host_ready"`
	TrayCreated      bool   `json:"tray_created"`
	WindowsCreated   int    `json:"windows_created"`
	WindowsClosed    int    `json:"windows_closed"`
	ClosedBeforeQuit bool   `json:"settings_closed_before_quit"`
	QuitCallbacks    int    `json:"quit_callbacks"`
	NormalExit       bool   `json:"normal_exit"`
	Error            string `json:"error,omitempty"`
}

type shell struct {
	window  *mygo.Window
	tray    *mygo.Tray
	closing bool // Main-thread state, independent of framework dispatcher shutdown.
	cancel  context.CancelFunc
	report  hostReport
}

func (s *shell) settings(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(24).Gap(18).Children(func() {
		ui.Text(c, "MiMac").FontSize(28).Bold()
		ui.Text(c, "用遥控器，连接你使用的语音工具。").FontSize(14).TextColor(t.TextMuted)
		ui.Divider(c)
		ui.Column(c).Gap(12).Children(func() {
			ui.Text(c, "遥控器　尚未连接").FontSize(16)
			ui.Text(c, "语音工具　尚未选择").FontSize(16)
			ui.Text(c, "音频通道　尚未建立").FontSize(16)
		})
		ui.Spacer(c)
		ui.Row(c).Gap(10).Children(func() {
			ui.Text(c, "小米 Pro 2").FontSize(12).TextColor(t.TextMuted)
			ui.Spacer(c)
			ui.Button(c, "关闭窗口").OnClick(func() { s.window.Close() })
		})
	})
}

func (s *shell) showSettings() {
	if s.closing {
		return
	}
	if s.window != nil {
		s.window.Show()
		return
	}
	s.window = mygo.NewWindow(mygo.WindowOptions{
		Title: "MiMac", Width: 460, Height: 330,
		Content: ui.View(s.settings),
	})
	s.report.WindowsCreated++
	s.window.OnClosed(func() {
		s.window = nil
		s.report.WindowsClosed++
		s.report.ClosedBeforeQuit = !s.closing
	})
}

func (s *shell) ready(ctx context.Context, show bool, smoke time.Duration) {
	tray, err := mygo.NewTray(mygo.TrayOptions{Title: "Mi", ToolTip: "MiMac"})
	if err != nil {
		s.report.Error = err.Error()
		mygo.App.Quit()
		return
	}
	s.tray = tray
	s.report.Ready, s.report.TrayCreated = true, true
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) { s.showSettings() }},
		mygo.Separator(),
		{Label: "退出 MiMac", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
	tray.OnClick(s.showSettings)
	tray.OnRightClick(func() {
		if s.closing {
			return
		}
		tray.SetMenu(menu)
		defer tray.SetMenu(nil)
		tray.PopUpMenu()
	})
	if show {
		s.showSettings()
	}
	if smoke > 0 {
		go func() {
			first := smoke
			if show {
				first = smoke / 2
			}
			timer := time.NewTimer(first)
			defer timer.Stop()
			if show {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					mygo.RunOnMain(func() {
						if !s.closing && s.window != nil {
							s.window.Close()
						}
					})
				}
				timer.Reset(smoke - first)
			}
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				mygo.RunOnMain(func() {
					if !s.closing {
						mygo.App.Quit()
					}
				})
			}
		}()
	}
}

func main() {
	show := flag.Bool("show-settings", false, "open the settings window on launch")
	smoke := flag.Duration("smoke-duration", 0, "quit after a bounded host smoke run (1s to 30s)")
	reportPath := flag.String("host-report", "", "write host lifecycle metadata to this explicit path")
	flag.Parse()
	if *smoke != 0 && (*smoke < time.Second || *smoke > 30*time.Second) {
		fmt.Fprintln(os.Stderr, "smoke-duration must be 1s to 30s")
		os.Exit(2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &shell{cancel: cancel}
	mygo.App.SetName("MiMac")
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	// A settings window is disposable; closing it must leave the menu bar host running.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { s.closing = true; s.cancel() })
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		s.report.QuitCallbacks++
		if s.tray != nil {
			s.tray.Destroy()
		}
	})
	mygo.App.WhenReady(func() { s.ready(ctx, *show, *smoke) })
	if err := mygo.App.Run(); err != nil {
		s.report.Error = err.Error()
	}
	s.report.NormalExit = s.report.Error == "" && s.report.QuitCallbacks == 1
	if *reportPath != "" {
		data, err := json.MarshalIndent(s.report, "", "  ")
		if err == nil {
			err = os.WriteFile(*reportPath, append(data, '\n'), 0600)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if !s.report.NormalExit {
		os.Exit(1)
	}
}
