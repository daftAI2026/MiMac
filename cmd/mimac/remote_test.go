package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/pluginrt"
)

type remoteTestMonitor struct {
	closeCalls int
	closeErr   error
	callback   func(xiaomi.MonitorEvent)
	closed     bool
}

func (m *remoteTestMonitor) Close() error {
	m.closeCalls++
	if m.closeErr != nil {
		return m.closeErr
	}
	m.closed = true
	return nil
}

func (*remoteTestMonitor) Stats() xiaomi.MonitorStats { return xiaomi.MonitorStats{} }

func (m *remoteTestMonitor) emit(event xiaomi.MonitorEvent) {
	m.callback(event)
}

type remoteTestReservation struct {
	closeCalls int
	closeErr   error
	events     *[]string
	label      string
}

func (r *remoteTestReservation) Close() error {
	r.closeCalls++
	if r.events != nil {
		*r.events = append(*r.events, r.label)
	}
	return r.closeErr
}

func setupRemoteTest(t *testing.T, onStart func(func(xiaomi.MonitorEvent), *remoteTestMonitor) error) (*shell, *remoteTestMonitor) {
	t.Helper()
	s := &shell{}
	monitor := &remoteTestMonitor{}
	err := s.setupPluginsWith(func(onEvent func(xiaomi.MonitorEvent)) (hidMonitor, error) {
		monitor.callback = onEvent
		if err := onStart(onEvent, monitor); err != nil {
			return nil, err
		}
		return monitor, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.remoteState.snapshotPath = t.TempDir() + "/voice-mapping-recovery.plist"
	return s, monitor
}

func connectedEvent(t *testing.T, generation uint64) xiaomi.MonitorEvent {
	t.Helper()
	device, err := bridge.NewDevice("remote-test", generation)
	if err != nil {
		t.Fatal(err)
	}
	return xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device}
}

func disconnectedEvent(t *testing.T, generation uint64) xiaomi.MonitorEvent {
	t.Helper()
	device, err := bridge.NewDevice("remote-test", generation)
	if err != nil {
		t.Fatal(err)
	}
	return xiaomi.MonitorEvent{State: xiaomi.MonitorStateDisconnected, Device: device}
}

func nextRemoteDispatch(t *testing.T, queue <-chan func()) {
	t.Helper()
	remoteDispatch(t, queue)()
}

func remoteDispatch(t *testing.T, queue <-chan func()) func() {
	t.Helper()
	select {
	case fn := <-queue:
		return fn
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for main dispatcher task")
		return nil
	}
}

func TestToggleRemoteReservesEvenWithoutVoiceTargetAndNeverStartsAudio(t *testing.T) {
	reservation := &remoteTestReservation{}
	s, monitor := setupRemoteTest(t, func(onEvent func(xiaomi.MonitorEvent), _ *remoteTestMonitor) error {
		onEvent(connectedEvent(t, 1)) // StartMonitor may synchronously report a live device.
		return nil
	})
	reserveCalls := 0
	s.remoteState.reserve = func(path string) (remoteVoiceReservation, error) {
		reserveCalls++
		if path != s.remoteState.snapshotPath {
			t.Fatalf("snapshot path = %q, want user config path %q", path, s.remoteState.snapshotPath)
		}
		return reservation, nil
	}

	s.toggleRemote()
	if !s.remoteEnabled() || !s.remoteHasReservation() {
		t.Fatal("remote takeover did not retain the synchronously connected voice key")
	}
	if reserveCalls != 1 || monitor.closeCalls != 0 {
		t.Fatalf("reserve calls=%d monitor closes=%d, want one reserve and a live monitor", reserveCalls, monitor.closeCalls)
	}
	activeTools := 0
	for _, tool := range s.tools {
		if tool.active {
			activeTools++
		}
	}
	if s.audioSession != nil || activeTools != 0 {
		t.Fatal("enabling remote auto-started audio or a target")
	}
	if got := s.remoteLabel(); got != "请启用语音插件" {
		t.Fatalf("remoteLabel() = %q, want setup-needed status", got)
	}
	if s.remoteTone() != toneNeutral {
		t.Fatal("pending setup must not advertise a ready green indicator")
	}
	if !s.remoteBusy() && s.remoteButtonLabel() != "停用遥控器" {
		t.Fatalf("remote button = %q, want stop action", s.remoteButtonLabel())
	}

	s.outputNeedsPermission = true
	s.outputStatus = "过期的权限提示"
	s.toggleRemote()
	if s.remoteEnabled() || s.remoteState.reservation != nil || reservation.closeCalls != 1 || !monitor.closed {
		t.Fatalf("stop left remote state active=%v reservation=%v closes=%d monitorClosed=%v", s.remoteEnabled(), s.remoteState.reservation != nil, reservation.closeCalls, monitor.closed)
	}
	if s.outputNeedsPermission || s.outputStatus != "" {
		t.Fatal("successful stop retained stale output permission state")
	}
}

func TestRemoteWaitsForDeviceWithoutShowingPermanentPreparingState(t *testing.T) {
	s, _ := setupRemoteTest(t, func(func(xiaomi.MonitorEvent), *remoteTestMonitor) error { return nil })
	s.deviceNeedsPermission = true
	s.toggleRemote()
	if !s.remoteEnabled() || s.deviceNeedsPermission {
		t.Fatal("new explicit enable did not clear the prior input permission result")
	}
	if got := s.remoteLabel(); got != "等待遥控器" {
		t.Fatalf("remoteLabel() without a connected device = %q, want waiting status", got)
	}
}

func TestRemoteConnectionEventsReserveOnlyAfterMainDispatchAndFenceGeneration(t *testing.T) {
	s, monitor := setupRemoteTest(t, func(func(xiaomi.MonitorEvent), *remoteTestMonitor) error {
		return nil
	})
	queue := make(chan func(), 8)
	s.remoteState.postMain = func(fn func()) { queue <- fn }
	first := &remoteTestReservation{}
	second := &remoteTestReservation{}
	reserveCalls := 0
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		if reserveCalls == 1 {
			return first, nil
		}
		return second, nil
	}

	s.toggleRemote()
	monitor.emit(connectedEvent(t, 1))
	if reserveCalls != 0 {
		t.Fatal("voice mapping was reserved inside the native callback")
	}
	nextRemoteDispatch(t, queue)
	if !s.remoteHasReservation() || s.remoteState.reservationGeneration != 1 {
		t.Fatal("deferred reserve did not bind to current generation 1")
	}

	monitor.emit(disconnectedEvent(t, 1))
	if first.closeCalls != 0 {
		t.Fatal("reservation restore ran inside the disconnect callback")
	}
	nextRemoteDispatch(t, queue)
	if first.closeCalls != 1 || s.remoteState.reservation != nil {
		t.Fatal("disconnect cleanup did not release the old generation lease")
	}

	monitor.emit(connectedEvent(t, 2))
	nextRemoteDispatch(t, queue)
	if !s.remoteHasReservation() || s.remoteState.reservationGeneration != 2 || reserveCalls != 2 {
		t.Fatal("reconnect did not acquire only the new generation lease")
	}
}

func TestRemoteStopWaitsForAudioCleanupBeforeReservationAndMonitor(t *testing.T) {
	var order []string
	reservation := &remoteTestReservation{events: &order, label: "reservation"}
	s, monitor := setupRemoteTest(t, func(onEvent func(xiaomi.MonitorEvent), _ *remoteTestMonitor) error {
		onEvent(connectedEvent(t, 3))
		return nil
	})
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) { return reservation, nil }
	s.toggleRemote()

	queue := make(chan func(), 2)
	s.remoteState.postMain = func(fn func()) { queue <- fn }
	host := &audioSessionHost{cancel: func() {}}
	s.audioSession = host
	s.remoteState.closeAudio = func(context.Context, *audioSessionHost) error {
		order = append(order, "audio-cleanup")
		return nil
	}
	s.toggleRemote()
	if reservation.closeCalls != 0 || monitor.closeCalls != 0 || !s.remoteBusy() {
		t.Fatal("stop released remote resources before async audio cleanup")
	}
	nextRemoteDispatch(t, queue)
	if s.remoteEnabled() || s.remoteState.reservation != nil || reservation.closeCalls != 1 || !monitor.closed {
		t.Fatalf("stop did not finish: enabled=%v reservation=%v closes=%d monitorClosed=%v", s.remoteEnabled(), s.remoteState.reservation != nil, reservation.closeCalls, monitor.closed)
	}
	if len(order) != 2 || order[0] != "audio-cleanup" || order[1] != "reservation" {
		t.Fatalf("cleanup order = %v, want audio then reservation; monitor closes after reservation", order)
	}
}

func TestRemoteStopFailureRetainsReservationAndEnabledModules(t *testing.T) {
	reservation := &remoteTestReservation{}
	s, monitor := setupRemoteTest(t, func(onEvent func(xiaomi.MonitorEvent), _ *remoteTestMonitor) error {
		onEvent(connectedEvent(t, 4))
		return nil
	})
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) { return reservation, nil }
	s.toggleRemote()

	queue := make(chan func(), 2)
	s.remoteState.postMain = func(fn func()) { queue <- fn }
	s.audioSession = &audioSessionHost{cancel: func() {}}
	cleanupErr := errors.New("audio close timed out")
	s.remoteState.closeAudio = func(context.Context, *audioSessionHost) error { return cleanupErr }
	s.toggleRemote()
	nextRemoteDispatch(t, queue)
	if !s.remoteEnabled() || s.remoteState.reservation != reservation || reservation.closeCalls != 0 || monitor.closeCalls != 0 {
		t.Fatal("failed cleanup falsely stopped or restored the remote lease")
	}
	if !errors.Is(s.remoteState.err, cleanupErr) || !s.report.AudioCleanupFailed {
		t.Fatalf("cleanup failure was not retained for status: err=%v report=%+v", s.remoteState.err, s.report)
	}
}

func TestRemoteReservationValidityFollowsCoreGeneration(t *testing.T) {
	reservation := &remoteTestReservation{}
	s, monitor := setupRemoteTest(t, func(onEvent func(xiaomi.MonitorEvent), _ *remoteTestMonitor) error {
		onEvent(connectedEvent(t, 5))
		return nil
	})
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) { return reservation, nil }
	s.toggleRemote()
	if !s.remoteHasReservation() {
		t.Fatal("current connection reservation was not recognized")
	}
	monitor.emit(disconnectedEvent(t, 5))
	if s.remoteHasReservation() {
		t.Fatal("reservation remained valid after Core retired its generation")
	}
}

func TestRemoteQueuedReserveFencesRetiredGenerationAndQuit(t *testing.T) {
	s, monitor := setupRemoteTest(t, func(func(xiaomi.MonitorEvent), *remoteTestMonitor) error {
		return nil
	})
	queue := make(chan func(), 8)
	s.remoteState.postMain = func(fn func()) { queue <- fn }
	reserveCalls := 0
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		return &remoteTestReservation{}, nil
	}
	s.toggleRemote()

	monitor.emit(connectedEvent(t, 10))
	retired := remoteDispatch(t, queue)
	monitor.emit(disconnectedEvent(t, 10))
	monitor.emit(connectedEvent(t, 11))
	current := remoteDispatch(t, queue)
	retired()
	if reserveCalls != 0 {
		t.Fatal("queued reservation for retired generation reached the native port")
	}
	current()
	if reserveCalls != 1 || s.remoteState.reservationGeneration != 11 {
		t.Fatalf("current-generation reservation calls=%d generation=%d", reserveCalls, s.remoteState.reservationGeneration)
	}

	// Simulate a queued current-generation callback that has not reached MyGo
	// yet. Quit owns cleanup now, so that callback must not reopen the lease.
	s.remoteState.reservation = nil
	s.remoteState.reservationGeneration = 0
	s.queueRemoteReserve(11)
	s.closing = true
	nextRemoteDispatch(t, queue)
	if reserveCalls != 1 || s.remoteState.reservation != nil {
		t.Fatal("queued callback reacquired the voice mapping after quit began")
	}
}

func TestClosingFencesPendingRemoteStopAndFinishCallbacks(t *testing.T) {
	reservation := &remoteTestReservation{}
	s, monitor := setupRemoteTest(t, func(onEvent func(xiaomi.MonitorEvent), _ *remoteTestMonitor) error {
		onEvent(connectedEvent(t, 15))
		return nil
	})
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) { return reservation, nil }
	s.toggleRemote()
	if !s.remoteHasReservation() {
		t.Fatal("test setup did not acquire a reservation")
	}
	closeCalls := 0
	s.remoteState.closeAudio = func(context.Context, *audioSessionHost) error {
		closeCalls++
		return nil
	}
	s.audioSession = &audioSessionHost{cancel: func() {}}
	s.closing = true

	s.beginRemoteStop()
	s.beginRemoteDisconnectCleanup(15)
	s.finishRemoteStop(s.remoteState.cleanupSequence, s.audioSession, nil, nil)
	s.finishRemoteDisconnect(s.remoteState.cleanupSequence, 15, s.audioSession, nil)
	if closeCalls != 0 || s.remoteState.stopPending || s.remoteState.cleanupInFlight {
		t.Fatalf("quit fence started async cleanup: closeCalls=%d stop=%v cleanup=%v", closeCalls, s.remoteState.stopPending, s.remoteState.cleanupInFlight)
	}
	if reservation.closeCalls != 0 || s.remoteState.reservation != reservation || monitor.closeCalls != 0 || !s.remoteEnabled() {
		t.Fatal("late remote finish callback mutated plugin or mapping state after quit began")
	}
}

func TestRemotePendingAudioStopIsNonblockingAndKeepsReservation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	runner, err := audiosession.New(func(context.Context, bridge.SessionToken) (audiosession.Resources, error) {
		close(entered)
		<-release // Deliberately ignore cancellation while fake Prepare is pending.
		return audiosession.Resources{}, errors.New("test preparation released")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	binding, err := bridge.NewBinding(bridge.PolicyAfterDrain, "space")
	if err != nil {
		t.Fatal(err)
	}
	core, err := bridge.NewCore(binding)
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("pending-audio-close", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.RouteAvailable(true), bridge.ScopeAvailable(true)} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := core.Step(bridge.VoiceBegan(press))
	if err != nil {
		t.Fatal(err)
	}
	var prepare bridge.PrepareSourceEffect
	for _, effect := range effects {
		if value, ok := effect.(bridge.PrepareSourceEffect); ok {
			prepare = value
		}
	}
	if prepare.Token.ID() == 0 {
		t.Fatalf("VoiceBegan did not request audio preparation: %#v", effects)
	}
	if err := runner.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fake preparation did not start")
	}

	reservation := &remoteTestReservation{}
	s := &shell{
		audioSession: &audioSessionHost{runtime: runner},
		remoteState:  remoteState{reservation: reservation},
	}
	finished := make(chan error, 1)
	go func() { finished <- s.stopRemotePlugin() }()
	select {
	case err := <-finished:
		if !errors.Is(err, audiosession.ErrCleanupPending) {
			t.Fatalf("plugin Stop error = %v, want nonblocking pending cleanup", err)
		}
	case <-time.After(250 * time.Millisecond):
		close(release)
		<-finished
		t.Fatal("plugin Stop blocked on unfinished audio preparation")
	}
	if reservation.closeCalls != 0 || s.remoteState.reservation != reservation {
		t.Fatal("plugin Stop released the mapping before audio cleanup completed")
	}

	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Close(ctx); err != nil {
		t.Fatalf("finish fake audio cleanup: %v", err)
	}
}

func TestRemoteDisconnectRequiresConfirmedOutputCloseBeforeRestoringMapping(t *testing.T) {
	var order []string
	reservation := &remoteTestReservation{events: &order, label: "reservation"}
	outputErr := errors.New("synthesized key release failed")
	s := &shell{
		coordinator: newDeviceCoordinator(),
		remoteState: remoteState{
			reservation:           reservation,
			reservationGeneration: 12,
			cleanupInFlight:       true,
			cleanupSequence:       1,
		},
	}
	runtime, err := remoteTestPluginRuntime(func() error {
		order = append(order, "output-close")
		return outputErr
	})
	if err != nil {
		t.Fatal(err)
	}
	s.plugins = runtime
	reserveCalls := 0
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		reserveCalls++
		return &remoteTestReservation{}, nil
	}

	s.finishRemoteDisconnect(1, 12, nil, nil)
	if len(order) != 1 || order[0] != "output-close" || reservation.closeCalls != 0 {
		t.Fatalf("disconnect cleanup order=%v reservation closes=%d, want output close only", order, reservation.closeCalls)
	}
	if s.remoteState.reservation != reservation || !errors.Is(s.remoteState.err, outputErr) || reserveCalls != 0 {
		t.Fatal("failed output cleanup released the old lease or acquired a new generation")
	}
	if !runtime.Active(outputPlugin) || !runtime.Active(remotePlugin) {
		t.Fatal("failed output stop did not retain the dependent remote runtime")
	}
}

func TestRemoteDisconnectClosesOutputBeforeRestoringMapping(t *testing.T) {
	var order []string
	reservation := &remoteTestReservation{events: &order, label: "reservation"}
	s := &shell{
		coordinator: newDeviceCoordinator(),
		remoteState: remoteState{
			reservation:           reservation,
			reservationGeneration: 13,
			cleanupInFlight:       true,
			cleanupSequence:       1,
		},
	}
	runtime, err := remoteTestPluginRuntime(func() error {
		order = append(order, "output-close")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.plugins = runtime
	s.finishRemoteDisconnect(1, 13, nil, nil)
	if len(order) != 2 || order[0] != "output-close" || order[1] != "reservation" {
		t.Fatalf("disconnect cleanup order=%v, want output close then mapping restore", order)
	}
	if s.remoteState.reservation != nil || reservation.closeCalls != 1 {
		t.Fatal("successful output close did not release the old voice mapping")
	}
}

func remoteTestPluginRuntime(outputStop func() error) (*pluginrt.Runtime, error) {
	noop := func() (pluginrt.StopFunc, error) { return func() error { return nil }, nil }
	runtime, err := pluginrt.New(
		pluginrt.Manifest{ID: corePlugin, Start: noop},
		pluginrt.Manifest{ID: outputPlugin, Requires: []string{corePlugin}, Start: func() (pluginrt.StopFunc, error) {
			return outputStop, nil
		}},
		pluginrt.Manifest{ID: xiaomiPlugin, Requires: []string{corePlugin}, Start: noop},
		pluginrt.Manifest{ID: remotePlugin, Requires: []string{corePlugin, xiaomiPlugin}, Start: noop},
	)
	if err != nil {
		return nil, err
	}
	if err := runtime.Enable(remotePlugin); err != nil {
		return nil, err
	}
	if err := runtime.Enable(outputPlugin); err != nil {
		return nil, err
	}
	return runtime, nil
}

func TestRemoteReadyIndicatorRequiresVoiceAndLiveOutput(t *testing.T) {
	s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
	s.deviceProfiles[0].VoiceButtonID = toolShortcutsTestButton
	s.coordinator = newDeviceCoordinator()
	s.remoteState.reservation = &remoteTestReservation{}
	s.remoteState.reservationGeneration = 1
	event := connectedEvent(t, 1)
	if err := s.coordinator.step(bridge.DeviceConnected(event.Device)); err != nil {
		t.Fatal(err)
	}
	start := func() (pluginrt.StopFunc, error) { return func() error { return nil }, nil }
	var err error
	s.plugins, err = pluginrt.New(pluginrt.Manifest{ID: remotePlugin, Start: start}, pluginrt.Manifest{ID: outputPlugin, Start: start})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.plugins.Enable(remotePlugin); err != nil {
		t.Fatal(err)
	}
	check := func(label string, tone statusTone) {
		t.Helper()
		gotLabel, gotTone := s.remoteStatus()
		if gotLabel != label || gotTone != tone {
			t.Fatalf("status = %q/%v, want %q/%v", gotLabel, gotTone, label, tone)
		}
	}
	check("输出未就绪", toneWarning)
	if err = s.plugins.Enable(outputPlugin); err != nil {
		t.Fatal(err)
	}
	check("输出未就绪", toneWarning) // Active module without an attached sink is insufficient.
	s.coordinator.sink = &toolShortcutsTestSink{}
	check("已启用", toneActive)
	s.outputNeedsPermission = true
	check("需要辅助功能权限", toneWarning)
	s.outputNeedsPermission = false
	s.outputNeedsRestart = true
	check("恢复失败", toneError)
	s.outputNeedsRestart = false
	s.remoteState.stopPending = true
	check("正在停用", toneNeutral)
	s.remoteState.stopPending = false
	s.tools[0].snapshot.Actions[0].Control = nil
	check("请检查语音配置", toneNeutral)
}

func TestRemoteSetupPromptNamesNextStep(t *testing.T) {
	s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
	check := func(want string) {
		t.Helper()
		if label := s.remoteSetupPrompt(); label != want {
			t.Fatalf("prompt %q, want %q", label, want)
		}
	}
	s.tools[0].active = false
	check("请启用语音插件")
	s.tools[0].active = true
	s.currentVoiceTool = ""
	check("请选择语音工具")
	s.currentVoiceTool = s.tools[0].id
	check("请检查语音配置")
	s.tools[0].busy = true
	check("正在读取配置")
}
