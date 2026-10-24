package xiaomi

import (
	"reflect"
	"testing"

	"mimac.local/mimac/internal/bridge"
)

func TestMonitorCloseIsSynchronousAndIdempotent(t *testing.T) {
	calls := 0
	monitor := &Monitor{closeFn: func() error {
		calls++
		return nil
	}}
	if err := monitor.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := monitor.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("native cleanup calls = %d, want 1", calls)
	}
}

func TestMonitorStatsRemainReadableAfterClose(t *testing.T) {
	stats := &monitorStats{}
	stats.nativeReports.Store(4)
	stats.rejectedSender.Store(2)
	stats.otherReports.Store(1)
	stats.reportErrors.Store(3)
	monitor := &Monitor{stats: stats, closeFn: func() error { return nil }}
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
	want := MonitorStats{NativeReports: 4, RejectedSender: 2, OtherReports: 1, ReportErrors: 3}
	if got := monitor.Stats(); got != want {
		t.Fatalf("Stats() after Close() = %#v, want %#v", got, want)
	}
}

func TestDisconnectEdgesReleaseHeldPressesInStableOrder(t *testing.T) {
	device, err := bridge.NewDevice("xiaomi-hid:2717:32b8", 42)
	if err != nil {
		t.Fatal(err)
	}
	late, err := bridge.NewPressToken(device, "hid:0007:0065", 9)
	if err != nil {
		t.Fatal(err)
	}
	early, err := bridge.NewPressToken(device, "hid:0007:003e", 4)
	if err != nil {
		t.Fatal(err)
	}
	held := map[bridge.PressToken]struct{}{
		late:  {},
		early: {},
	}
	got := disconnectEdges(held)
	want := []Edge{
		{Kind: EdgeUp, Press: early},
		{Kind: EdgeUp, Press: late},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("disconnectEdges() = %#v, want %#v", got, want)
	}
}

func TestDispatchMonitorEventCopiesEdges(t *testing.T) {
	device, err := bridge.NewDevice("xiaomi-hid:2717:32b8", 7)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "hid:0007:0052", 1)
	if err != nil {
		t.Fatal(err)
	}
	input := []Edge{{Kind: EdgeDown, Press: press}}
	dispatchMonitorEvent(func(event MonitorEvent) {
		event.Edges[0] = Edge{}
	}, MonitorEvent{State: MonitorStateConnected, Edges: input})
	if input[0].Kind != EdgeDown || input[0].Press != press {
		t.Fatalf("dispatch mutated caller's edge slice: %#v", input)
	}
}
