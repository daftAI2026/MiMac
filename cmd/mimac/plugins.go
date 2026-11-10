package main

import (
	"errors"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/pluginrt"
)

const (
	corePlugin   = "bridge"
	outputPlugin = "key-output"
	xiaomiPlugin = "xiaomi-hid"
)

type hidMonitor interface {
	Close() error
	Stats() xiaomi.MonitorStats
}

type startHIDMonitor func(func(xiaomi.MonitorEvent)) (hidMonitor, error)

// This is the composition root, analogous to Magpie's built-in plugin list.
// It alone joins the runtime, core and concrete device adapter. MyGo owns the
// thread and loop; the plugin runtime adds no event loop or background polling.
func (s *shell) setupPlugins() error {
	s.remoteState.postMain = mygo.RunOnMain
	return s.setupPluginsWith(func(onEvent func(xiaomi.MonitorEvent)) (hidMonitor, error) {
		start := xiaomi.StartMonitor
		if s.powerState.resuming {
			start = xiaomi.StartMonitorWithoutPrompt
		}
		monitor, err := start(onEvent)
		if err != nil {
			return nil, err
		}
		return monitor, nil
	})
}

func (s *shell) setupPluginsWith(start startHIDMonitor) error {
	profile, err := xiaomi.DeviceProfile()
	if err != nil {
		return err
	}
	s.deviceProfiles = []devicePlugin{{ModuleID: xiaomiPlugin, VoiceButtonID: xiaomi.VoiceButtonID, Profile: profile, Artwork: xiaomi.Artwork, Discover: func() (deviceDiscovery, error) {
		devices, err := xiaomi.DiscoverHID()
		result := deviceDiscovery{Count: len(devices)}
		if len(devices) == 1 {
			result.Supported = devices[0].KnownReportLayout
		}
		return result, err
	}}}
	s.selectedProfile = profile.ID
	s.audioOutput = newAudioOutputOwner()
	manifests := []pluginrt.Manifest{
		pluginrt.Manifest{ID: corePlugin, Start: func() (pluginrt.StopFunc, error) {
			s.coordinator = newDeviceCoordinator()
			return func() error {
				err := s.coordinator.disconnect()
				s.report.Core = s.coordinator.report()
				return err
			}, nil
		}},
		pluginrt.Manifest{ID: outputPlugin, Requires: []string{corePlugin}, Start: func() (pluginrt.StopFunc, error) {
			if err := s.requireDeviceAccess(); err != nil {
				return func() error { return nil }, err
			}
			sink, err := keyoutput.Open(keyoutput.GlobalTarget)
			if err != nil {
				return func() error { return nil }, err
			}
			s.output = sink
			err = s.coordinator.attachOutput(sink)
			return func() error {
				err := s.coordinator.detachOutput()
				if err == nil {
					s.output = nil
				}
				return err
			}, err
		}},
		pluginrt.Manifest{ID: xiaomiPlugin, Requires: []string{corePlugin}, Start: func() (pluginrt.StopFunc, error) {
			if err := s.requireDeviceAccess(); err != nil {
				return func() error { return nil }, err
			}
			monitor, err := start(s.handleDeviceEvent)
			if err != nil {
				// StartMonitor cleans up partial opens itself; a failure to do so
				// is explicit ErrMonitorRestartRequired and locks its native slot.
				return func() error {
					if errors.Is(err, xiaomi.ErrMonitorRestartRequired) {
						return err
					}
					return nil
				}, err
			}
			s.monitor = monitor
			s.report.MonitorStarts++
			return func() error {
				coreErr := s.coordinator.disconnect()
				s.report.Core = s.coordinator.report()
				nativeErr := monitor.Close()
				s.report.MonitorSamples = append(s.report.MonitorSamples, monitor.Stats())
				s.report.MonitorStopAttempts++
				if nativeErr == nil {
					s.monitor = nil
				}
				return errors.Join(nativeErr, coreErr)
			}, nil
		}},
		pluginrt.Manifest{ID: remotePlugin, Requires: []string{corePlugin, xiaomiPlugin}, Start: func() (pluginrt.StopFunc, error) {
			stop, err := s.remoteStart()
			return pluginrt.StopFunc(stop), err
		}},
		pluginrt.Manifest{ID: audioOutputPlugin, Requires: []string{corePlugin}, Start: func() (pluginrt.StopFunc, error) {
			// Enable only registers the adapter. A session must supply its
			// verified device UID and PCM rate before any AudioQueue is opened.
			err := s.audioOutput.enable(openSystemAudioOutput)
			return s.audioOutput.disable, err
		}},
	}
	manifests = append(manifests, s.audioSessionManifest())
	manifests = append(manifests, s.voiceToolManifests()...)
	runtime, err := pluginrt.New(manifests...)
	if err != nil {
		return err
	}
	s.plugins = runtime
	return runtime.Enable(corePlugin)
}
