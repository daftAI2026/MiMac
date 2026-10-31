// MiMac uses MyGo as its desktop host. Device and audio adapters are activated
// separately; starting the menu bar host does not request capture permissions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"mimac.local/mimac/internal/bridge"
	"os"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	brand "mimac.local/mimac/Assets/Brand"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/platform/navigation"
	nativeprefs "mimac.local/mimac/internal/platform/preferences"
	"mimac.local/mimac/internal/platform/typography"
	"mimac.local/mimac/internal/pluginrt"
)

type hostReport struct {
	Ready               bool                  `json:"host_ready"`
	TrayCreated         bool                  `json:"tray_created"`
	WindowsCreated      int                   `json:"windows_created"`
	WindowsClosed       int                   `json:"windows_closed"`
	ClosedBeforeQuit    bool                  `json:"settings_closed_before_quit"`
	QuitCallbacks       int                   `json:"quit_callbacks"`
	CaptureWritten      bool                  `json:"capture_written"`
	NormalExit          bool                  `json:"normal_exit"`
	Error               string                `json:"error,omitempty"`
	MonitorStarts       int                   `json:"monitor_starts,omitempty"`
	MonitorStopAttempts int                   `json:"monitor_stop_attempts,omitempty"`
	MonitorStates       map[string]int        `json:"monitor_states,omitempty"`
	ButtonDowns         map[string]int        `json:"button_downs,omitempty"`
	ButtonUps           map[string]int        `json:"button_ups,omitempty"`
	LastButtonAt        string                `json:"last_button_at,omitempty"`
	MonitorSamples      []xiaomi.MonitorStats `json:"monitor_samples,omitempty"`
	Core                coreReport            `json:"core"`
	VoiceOverlayError   string                `json:"voice_overlay_error,omitempty"`
	AudioError          string                `json:"audio_error,omitempty"`
	AudioCleanupFailed  bool                  `json:"audio_cleanup_failed,omitempty"`
}

type shell struct {
	settingsState
	preferencesState
	voiceToolsState
	remoteState
	voiceOverlay                     voiceOverlayState
	readyIndicator                   readyIndicatorState
	powerState                       powerState
	liveAudio                        liveAudioState
	window                           *mygo.Window
	navigation                       *navigation.Control
	generalControls                  *nativeprefs.Control
	tray                             *mygo.Tray
	trayState                        trayState
	closing                          bool // Main-thread state, independent of framework dispatcher shutdown.
	cancel                           context.CancelFunc
	monitor                          hidMonitor // Main-thread device adapter, survives settings closure.
	plugins                          *pluginrt.Runtime
	coordinator                      *deviceCoordinator
	output                           keySink
	audioOutput                      *audioOutputOwner
	audioSession                     *audioSessionHost
	audioSource                      audioSourceFactory
	audioResources                   audiosession.Prepare
	audioResourcesReady              func() bool
	audioResourcesBeforeStart        func(context.Context) error
	audioResourcesClose              func(context.Context) error
	audioResourcesDeliver            func(bridge.SessionToken, string) error
	audioPost                        func(func())
	audioQuitPending, audioQuitReady bool
	deviceProfiles                   []devicePlugin
	selectedProfile                  string
	report                           hostReport
}

func (s *shell) showSettings() {
	if s.closing {
		return
	}
	if s.page == pageGeneral {
		s.preferencesState.loginLoaded = false
	}
	if s.window != nil {
		s.syncGeneralControls()
		s.window.Invalidate()
		s.window.Show()
		return
	}
	s.window = mygo.NewWindow(mygo.WindowOptions{
		Title: "MiMac", Width: windowWidth, Height: windowHeight, MinWidth: windowMinWidth, MinHeight: windowMinHeight,
		TitleBarStyle:        mygo.TitleBarHidden,
		TrafficLightPosition: &mygo.Point{X: trafficLightInset, Y: trafficLightInset},
		Content:              ui.View(s.settings),
	})
	var navigationErr error
	s.navigation, navigationErr = navigation.New(s.window.NativeHandle(), float64(sidebarWidth), float64(toolbarInset), s.navigateHistory)
	if navigationErr != nil {
		s.report.Error = fmt.Sprintf("navigation: %v", navigationErr)
	}
	s.syncNavigation()
	s.syncGeneralControls()
	s.report.WindowsCreated++
	if !s.deviceReadBusy && s.monitor == nil && !s.deviceNeedsPermission && !s.deviceNeedsRestart {
		s.detectDevice()
	}
	s.window.OnClosed(func() {
		s.clearReadyIndicator()
		s.closeGeneralControls()
		if s.navigation != nil {
			s.navigation.Close()
			s.navigation = nil
		}
		s.releaseSettingsAssets()
		s.endShortcutRecording()
		s.window = nil
		s.report.WindowsClosed++
		s.report.ClosedBeforeQuit = !s.closing
	})
}

func (s *shell) ready(ctx context.Context, show bool, smoke time.Duration, capturePath string) {
	systemTextSize = typography.SystemSize()
	if err := s.setupPlugins(); err != nil {
		s.report.Error = fmt.Sprintf("plugins: %v", err)
		mygo.App.Quit()
		return
	}
	s.voiceOverlay.open = func(read func() voiceOverlayFrame, failed func(error)) (voiceOverlayView, error) {
		view, err := newVoiceOverlayView(read, failed)
		if native, ok := view.(*mygoVoiceOverlay); ok {
			native.trace = s.voiceOverlay.meter.trace
		}
		return view, err
	}
	s.watchPower()
	s.loadMappings()
	s.loadVoiceTools()
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: "dev.mimac.app.status", Icon: brand.TrayPNG, IconIsTemplate: true, ToolTip: "MiMac"})
	if err != nil {
		s.report.Error = err.Error()
		mygo.App.Quit()
		return
	}
	s.tray = tray
	s.report.Ready, s.report.TrayCreated = true, true
	s.updateTrayMenu()
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
	voiceReportPath := flag.String("voice-overlay-report", "", "write bounded frequency/display metadata on exit; excludes PCM and text")
	appearance := flag.String("host-theme", "system", "app appearance for visual checks: system, light, or dark")
	audioBackend := flag.String("audio-backend", "", "optional audio backend: mimac-compat; never installs a component")
	flag.Parse()
	if err := validateAudioBackend(*audioBackend); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *appearance != "system" && *appearance != "light" && *appearance != "dark" {
		fmt.Fprintln(os.Stderr, "host-theme must be system, light, or dark")
		os.Exit(2)
	}
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
	s := &shell{cancel: cancel, liveAudio: liveAudioState{backend: *audioBackend}}
	trace, traceFile, err := openVoiceOverlayTrace(*voiceReportPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "voice overlay report:", err)
		os.Exit(2)
	}
	s.voiceOverlay.meter.trace = trace
	mygo.App.SetName("MiMac")
	s.loadPreferences()
	s.applySavedAppearance()
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "host-theme" {
			mygo.Theme.SetSource(mygo.ThemeSource(*appearance))
		}
	})
	mygo.Theme.OnUpdated(func() {
		s.syncVoiceOverlay()
		if s.generalControls != nil {
			s.syncGeneralControls()
		}
	})
	mygo.App.OnActivate(func(_ bool) {
		if s.report.Ready && !s.closing {
			s.showSettings()
			if s.window != nil {
				s.window.Focus()
			}
		}
	})
	// A settings window is disposable; closing it must leave the menu bar host running.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnBeforeQuit(func(event *mygo.QuitEvent) {
		s.closeVoiceOverlay()
		s.cancelPowerResume()
		if s.deferAudioQuit(event.PreventDefault, mygo.RunOnMain, func() {
			if s.audioQuitReady && !s.report.AudioCleanupFailed && s.audioSession != nil {
				s.remoteState.cleanedAudio = s.audioSession.runtime
			}
			mygo.App.Quit()
		}) {
			return
		}
		s.closing = true
		s.clearReadyIndicator()
		s.unwatchPower()
		s.closeGeneralControls()
		if s.navigation != nil {
			s.navigation.Close()
			s.navigation = nil
		}
		s.cancel()
		if s.plugins != nil {
			if err := s.plugins.Close(); err != nil {
				s.report.Error = fmt.Sprintf("plugin shutdown: %v", err)
			}
		}
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		s.closeVoiceOverlay()
		s.report.QuitCallbacks++
		if s.tray != nil {
			s.tray.Destroy()
		}
	})
	mygo.App.WhenReady(func() { s.ready(ctx, *show, *smoke, *capturePath) })
	if err := mygo.App.Run(); err != nil {
		s.report.Error = err.Error()
	}
	if traceFile != nil {
		writeErr := trace.write(traceFile)
		closeErr := traceFile.Close()
		if writeErr != nil || closeErr != nil {
			s.report.Error = fmt.Sprintf("voice overlay report: write=%v close=%v", writeErr, closeErr)
		}
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
