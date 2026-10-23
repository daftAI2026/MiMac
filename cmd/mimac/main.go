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
	brand "mimac.local/mimac/Assets/Brand"
)

type hostReport struct {
	Ready            bool   `json:"host_ready"`
	TrayCreated      bool   `json:"tray_created"`
	WindowsCreated   int    `json:"windows_created"`
	WindowsClosed    int    `json:"windows_closed"`
	ClosedBeforeQuit bool   `json:"settings_closed_before_quit"`
	QuitCallbacks    int    `json:"quit_callbacks"`
	CaptureWritten   bool   `json:"capture_written"`
	NormalExit       bool   `json:"normal_exit"`
	Error            string `json:"error,omitempty"`
}

type shell struct {
	settingsState
	window  *mygo.Window
	tray    *mygo.Tray
	closing bool // Main-thread state, independent of framework dispatcher shutdown.
	cancel  context.CancelFunc
	report  hostReport
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
		Title: "MiMac", Width: 480, Height: 420, MinWidth: 420, MinHeight: 420,
		Content: ui.View(s.settings),
	})
	s.report.WindowsCreated++
	s.window.OnClosed(func() {
		s.window = nil
		s.report.WindowsClosed++
		s.report.ClosedBeforeQuit = !s.closing
	})
}

func (s *shell) ready(ctx context.Context, show bool, smoke time.Duration, capturePath string) {
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: "dev.mimac.app.status", Icon: brand.TrayPNG, IconIsTemplate: true, ToolTip: "MiMac"})
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
	if capturePath != "" {
		s.captureSettings(ctx, capturePath)
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
	capturePath := flag.String("host-capture", "", "capture this app settings content as PNG to this explicit path")
	reportPath := flag.String("host-report", "", "write host lifecycle metadata to this explicit path")
	flag.Parse()
	if *smoke != 0 && (*smoke < time.Second || *smoke > 30*time.Second) {
		fmt.Fprintln(os.Stderr, "smoke-duration must be 1s to 30s")
		os.Exit(2)
	}
	if *capturePath != "" && (!*show || (*smoke != 0 && *smoke < 2*time.Second)) {
		fmt.Fprintln(os.Stderr, "host-capture requires show-settings and at least 2s if smoke-duration is set")
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
	mygo.App.WhenReady(func() { s.ready(ctx, *show, *smoke, *capturePath) })
	if err := mygo.App.Run(); err != nil {
		s.report.Error = err.Error()
	}
	if *capturePath != "" && !s.report.CaptureWritten && s.report.Error == "" {
		s.report.Error = "settings capture did not complete"
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

// Capture only our own renderer through MyGo; no desktop recording API is used.
// The explicit diagnostic flag is off by default and never selects a path itself.
func (s *shell) captureSettings(ctx context.Context, path string) {
	go func() {
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		// Keep capture, explicit diagnostic write, and reporting in one main-thread
		// operation so shutdown cannot leave a background write after App.Run exits.
		mygo.RunOnMain(func() {
			if s.closing {
				return
			}
			if s.window == nil || s.window.IsDestroyed() {
				s.report.Error = "settings capture: window is unavailable"
				return
			}
			png, err := s.window.CapturePage()
			if err == nil {
				var file *os.File
				file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err == nil {
					_, err = file.Write(png)
					if closeErr := file.Close(); err == nil {
						err = closeErr
					}
				}
			}
			if err != nil {
				s.report.Error = fmt.Sprintf("settings capture: %v", err)
			} else {
				s.report.CaptureWritten = true
			}
		})
	}()
}
