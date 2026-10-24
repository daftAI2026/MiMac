package xiaomi

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/bridge"
)

func mustDevice(t *testing.T, id string, generation uint64) bridge.Device {
	t.Helper()
	device, err := bridge.NewDevice(id, generation)
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func mustNormalizer(t *testing.T, device bridge.Device) *Normalizer {
	t.Helper()
	normalizer, err := NewNormalizer(device)
	if err != nil {
		t.Fatal(err)
	}
	return normalizer
}

func mustToken(t *testing.T, device bridge.Device, physicalID string, pressID uint64) bridge.PressToken {
	t.Helper()
	token, err := bridge.NewPressToken(device, physicalID, pressID)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func usagePayload(usages ...uint16) []byte {
	if len(usages) > 3 {
		panic("usagePayload accepts at most three slots")
	}
	payload := make([]byte, keyPayloadLen)
	for slot, usage := range usages {
		binary.LittleEndian.PutUint16(payload[slot*2:], usage)
	}
	return payload
}

func requireEdges(t *testing.T, got, want []Edge) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edges = %#v, want %#v", got, want)
	}
}

func TestZeroValueNormalizerIsRejected(t *testing.T) {
	var normalizer Normalizer
	for _, test := range []struct {
		name     string
		reportID byte
		payload  []byte
	}{
		{name: "empty keys", reportID: reportIDKeys, payload: usagePayload()},
		{name: "pressed key", reportID: reportIDKeys, payload: usagePayload(0x0006)},
		{name: "opaque report", reportID: reportID6, payload: make([]byte, opaquePayloadLen)},
	} {
		t.Run(test.name, func(t *testing.T) {
			edges, err := normalizer.HandleReport(test.reportID, test.payload)
			if !errors.Is(err, ErrInvalidNormalizer) || len(edges) != 0 {
				t.Fatalf("zero-value normalizer returned edges=%#v err=%v; want ErrInvalidNormalizer", edges, err)
			}
		})
	}
}

func TestReport1EmitsShortPressAndUniqueMonotonicTokens(t *testing.T) {
	device := mustDevice(t, "hid:2717:32b8", 1)
	normalizer := mustNormalizer(t, device)

	down, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	first := mustToken(t, device, "hid:0007:0006", 1)
	requireEdges(t, down, []Edge{{Kind: EdgeDown, Press: first}})

	repeated, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, repeated, nil)

	up, err := normalizer.HandleReport(reportIDKeys, usagePayload())
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, up, []Edge{{Kind: EdgeUp, Press: first}})

	nextDown, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	second := mustToken(t, device, "hid:0007:0006", 2)
	requireEdges(t, nextDown, []Edge{{Kind: EdgeDown, Press: second}})
	if first == second {
		t.Fatal("separate presses reused the same token")
	}
}

func TestPressIDExhaustionRejectsMultiPressReportAtomically(t *testing.T) {
	device := mustDevice(t, "hid:2717:32b8", 1)
	normalizer := mustNormalizer(t, device)
	initial, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	oldPress := initial[0].Press
	maxUint64 := ^uint64(0)
	normalizer.nextPressID = maxUint64 - 1

	if edges, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0009, 0x0007, 0x0008)); !errors.Is(err, ErrPressIDExhausted) || len(edges) != 0 {
		t.Fatalf("overflowing report returned edges=%#v err=%v", edges, err)
	}
	stillHeld, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, stillHeld, nil)
	oldUp, err := normalizer.HandleReport(reportIDKeys, usagePayload())
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, oldUp, []Edge{{Kind: EdgeUp, Press: oldPress}})

	lastTwo, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0008, 0x0007))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, lastTwo, []Edge{
		{Kind: EdgeDown, Press: mustToken(t, device, "hid:0007:0007", maxUint64-1)},
		{Kind: EdgeDown, Press: mustToken(t, device, "hid:0007:0008", maxUint64)},
	})

	if edges, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0009)); !errors.Is(err, ErrPressIDExhausted) || len(edges) != 0 {
		t.Fatalf("post-exhaustion transition returned edges=%#v err=%v", edges, err)
	}
	stillHeld, err = normalizer.HandleReport(reportIDKeys, usagePayload(0x0007, 0x0008))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, stillHeld, nil)
	lastUps, err := normalizer.HandleReport(reportIDKeys, usagePayload())
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, lastUps, []Edge{
		{Kind: EdgeUp, Press: mustToken(t, device, "hid:0007:0007", maxUint64-1)},
		{Kind: EdgeUp, Press: mustToken(t, device, "hid:0007:0008", maxUint64)},
	})
}

func TestReportSetDiffIsOrderIndependentAndDeduplicatesSlots(t *testing.T) {
	device := mustDevice(t, "hid:2717:32b8", 4)
	normalizer := mustNormalizer(t, device)

	initial, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0009, 0x0006, 0x0007))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, initial, []Edge{
		{Kind: EdgeDown, Press: mustToken(t, device, "hid:0007:0006", 1)},
		{Kind: EdgeDown, Press: mustToken(t, device, "hid:0007:0007", 2)},
		{Kind: EdgeDown, Press: mustToken(t, device, "hid:0007:0009", 3)},
	})

	for _, slots := range [][]uint16{{0x0007, 0x0006, 0x0009}, {0x0009, 0x0007, 0x0006}, {0x0006, 0x0009, 0x0007}} {
		got, err := normalizer.HandleReport(reportIDKeys, usagePayload(slots...))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("reordered or duplicate slots produced false edges: %#v", got)
		}
	}

	duplicates, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006, 0x0006, 0x0006))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, duplicates, []Edge{
		{Kind: EdgeUp, Press: mustToken(t, device, "hid:0007:0007", 2)},
		{Kind: EdgeUp, Press: mustToken(t, device, "hid:0007:0009", 3)},
	})

	changed, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006, 0x0006))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, changed, nil)

	allReleased, err := normalizer.HandleReport(reportIDKeys, usagePayload())
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, allReleased, []Edge{
		{Kind: EdgeUp, Press: mustToken(t, device, "hid:0007:0006", 1)},
	})

	duplicateDown, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0008, 0x0008, 0x0008))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, duplicateDown, []Edge{
		{Kind: EdgeDown, Press: mustToken(t, device, "hid:0007:0008", 4)},
	})

	switched, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0007))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, switched, []Edge{
		{Kind: EdgeUp, Press: mustToken(t, device, "hid:0007:0008", 4)},
		{Kind: EdgeDown, Press: mustToken(t, device, "hid:0007:0007", 5)},
	})
}

func TestMalformedReportPreservesHeldStateAndDoesNotConsumePressID(t *testing.T) {
	device := mustDevice(t, "hid:2717:32b8", 1)
	normalizer := mustNormalizer(t, device)
	firstDown, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	first := firstDown[0].Press

	malformed := [][]byte{
		usagePayload(0x0006)[:5],
		usagePayload(0x0006, 0x0007, 0x0001),
		usagePayload(0x0006, 0x0007, 0x0002),
		usagePayload(0x0006, 0x0007, 0x0003),
		usagePayload(0x0006, 0x0007, 0x00ff),
		usagePayload(0x0006, 0x0007, 0x0100),
	}
	for _, payload := range malformed {
		if edges, err := normalizer.HandleReport(reportIDKeys, payload); !errors.Is(err, ErrMalformedReport) || len(edges) != 0 {
			t.Fatalf("malformed payload %x returned edges=%#v err=%v", payload, edges, err)
		}
	}
	if edges, err := normalizer.HandleReport(reportID6, make([]byte, opaquePayloadLen-1)); !errors.Is(err, ErrMalformedReport) || len(edges) != 0 {
		t.Fatalf("bad opaque report length returned edges=%#v err=%v", edges, err)
	}
	if edges, err := normalizer.HandleReport(0x09, nil); !errors.Is(err, ErrUnsupportedReportID) || len(edges) != 0 {
		t.Fatalf("unknown report ID returned edges=%#v err=%v", edges, err)
	}

	stillHeld, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006, 0x0006))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, stillHeld, nil)
	up, err := normalizer.HandleReport(reportIDKeys, usagePayload())
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, up, []Edge{{Kind: EdgeUp, Press: first}})
	nextDown, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0008))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, nextDown, []Edge{{Kind: EdgeDown, Press: mustToken(t, device, "hid:0007:0008", 2)}})
}

func TestOpaqueReportsAreIgnoredWithoutReleasingButtons(t *testing.T) {
	device := mustDevice(t, "hid:2717:32b8", 1)
	normalizer := mustNormalizer(t, device)
	down, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	press := down[0].Press

	for _, reportID := range []byte{reportID6, reportID7, reportID8} {
		payload := make([]byte, opaquePayloadLen)
		for i := range payload {
			payload[i] = byte(i + int(reportID))
		}
		if edges, err := normalizer.HandleReport(reportID, payload); err != nil || len(edges) != 0 {
			t.Fatalf("opaque report %d returned edges=%#v err=%v", reportID, edges, err)
		}
	}

	stillHeld, err := normalizer.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, stillHeld, nil)
	up, err := normalizer.HandleReport(reportIDKeys, usagePayload())
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, up, []Edge{{Kind: EdgeUp, Press: press}})
}

func TestNativeReportRemovesLeadingReportID(t *testing.T) {
	device := mustDevice(t, "hid:2717:32b8", 1)
	normalizer := mustNormalizer(t, device)
	raw := append([]byte{reportIDKeys}, usagePayload(0x0006)...)
	edges, err := normalizer.HandleNativeReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	requireEdges(t, edges, []Edge{{
		Kind:  EdgeDown,
		Press: mustToken(t, device, "hid:0007:0006", 1),
	}})

	if got, err := normalizer.HandleNativeReport(nil); !errors.Is(err, ErrMalformedReport) || len(got) != 0 {
		t.Fatalf("empty native report returned edges=%#v err=%v", got, err)
	}
}

func TestRealRC003ReportFixtureFrom20261023(t *testing.T) {
	// Selected raw reports from two 2026-10-23 real-device RC003 HID captures.
	// The volume pair is from the separate follow-up capture. The bytes verify
	// report parsing and usage preservation without reading private evidence at
	// test time or validating application actions.
	fixture := []string{
		"01000052000000", "01000000000000",
		"01000051000000", "01000000000000",
		"01000050000000", "01000000000000",
		"0100004f000000", "01000000000000",
		"01000028000000", "01000000000000",
		"010000f1000000", "01000000000000",
		"0100004a000000", "01000000000000",
		"01000065000000", "01000000000000",
		"0100003e000000", "01000000000000",
		"0100003e000000", "01000000000000",
		"01000035000000", "01000000000000",
		"01000066000000", "01000000000000",
		// First Volume Down press/release, then first Volume Up press/release
		// in the separate capture's observed order.
		"01000081000000", "01000000000000",
		"01000080000000", "01000000000000",
	}
	usages := []uint16{0x0052, 0x0051, 0x0050, 0x004f, 0x0028, 0x00f1, 0x004a, 0x0065, 0x003e, 0x003e, 0x0035, 0x0066, 0x0081, 0x0080}
	if len(fixture) != 28 || len(usages) != 14 {
		t.Fatalf("fixture size = %d reports and %d presses", len(fixture), len(usages))
	}

	device := mustDevice(t, "hid:2717:32b8", 1)
	normalizer := mustNormalizer(t, device)
	seenPressIDs := make(map[uint64]struct{}, 14)
	var active bridge.PressToken
	haveActive := false
	downCount, upCount := 0, 0

	for i, encoded := range fixture {
		raw, err := hex.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode fixture report %d: %v", i+1, err)
		}
		if len(raw) != 7 || raw[0] != reportIDKeys {
			t.Fatalf("fixture report %d = %x; want seven raw bytes including report ID 1", i+1, raw)
		}
		edges, err := normalizer.HandleNativeReport(raw)
		if err != nil {
			t.Fatalf("normalize fixture report %d (%s): %v", i+1, encoded, err)
		}
		if len(edges) != 1 {
			t.Fatalf("fixture report %d returned %d edges, want 1: %#v", i+1, len(edges), edges)
		}

		edge := edges[0]
		usage := usages[i/2]
		physicalID := "hid:0007:" + hex.EncodeToString([]byte{byte(usage >> 8), byte(usage)})
		if edge.Press.PhysicalButtonID() != physicalID {
			t.Fatalf("fixture report %d physical ID = %q, want %q", i+1, edge.Press.PhysicalButtonID(), physicalID)
		}
		if edge.Press.Device() != device {
			t.Fatalf("fixture report %d device token = %#v, want %#v", i+1, edge.Press.Device(), device)
		}

		if i%2 == 0 {
			if edge.Kind != EdgeDown {
				t.Fatalf("fixture report %d edge kind = %d, want down", i+1, edge.Kind)
			}
			if haveActive {
				t.Fatalf("fixture report %d began before the prior usage was released", i+1)
			}
			pressID := edge.Press.PressID()
			if _, exists := seenPressIDs[pressID]; exists {
				t.Fatalf("fixture reused press ID %d", pressID)
			}
			seenPressIDs[pressID] = struct{}{}
			active, haveActive = edge.Press, true
			downCount++
		} else {
			if edge.Kind != EdgeUp {
				t.Fatalf("fixture report %d edge kind = %d, want up", i+1, edge.Kind)
			}
			if !haveActive || edge.Press != active {
				t.Fatalf("fixture report %d release token = %#v, active token = %#v", i+1, edge.Press, active)
			}
			haveActive = false
			upCount++
		}
	}

	if haveActive || downCount != 14 || upCount != 14 || len(seenPressIDs) != 14 {
		t.Fatalf("fixture totals: down=%d up=%d uniquePressIDs=%d active=%v", downCount, upCount, len(seenPressIDs), haveActive)
	}
}

func TestCoreIntegrationRetiresOldGenerationAndRejectsLateReports(t *testing.T) {
	binding, err := bridge.NewBinding(bridge.PolicyExactEdge, bridge.Key("voice"))
	if err != nil {
		t.Fatal(err)
	}
	core, err := bridge.NewCore(binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(bridge.ScopeAvailable(true)); err != nil {
		t.Fatal(err)
	}
	device1 := mustDevice(t, "hid:2717:32b8", 1)
	if _, err := core.Step(bridge.DeviceConnected(device1)); err != nil {
		t.Fatal(err)
	}
	normalizer1 := mustNormalizer(t, device1)
	down1, err := normalizer1.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	oldPress := down1[0].Press
	effects, err := core.Step(bridge.OrdinaryBegan(oldPress, bridge.Key("space")))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bridgeKeyTransitions(effects), []string{"down:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first generation down transitions = %v, want %v", got, want)
	}

	normalizer1.Retire()
	effects, err = core.Step(bridge.DeviceDisconnected(device1))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bridgeKeyTransitions(effects), []string{"up:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("disconnect transitions = %v, want %v", got, want)
	}
	if edges, err := normalizer1.HandleReport(reportIDKeys, usagePayload()); !errors.Is(err, ErrRetired) || len(edges) != 0 {
		t.Fatalf("retired normalizer returned edges=%#v err=%v", edges, err)
	}

	device2 := mustDevice(t, device1.ID(), 2)
	if _, err := core.Step(bridge.DeviceConnected(device2)); err != nil {
		t.Fatal(err)
	}
	normalizer2 := mustNormalizer(t, device2)
	down2, err := normalizer2.HandleReport(reportIDKeys, usagePayload(0x0006))
	if err != nil {
		t.Fatal(err)
	}
	newPress := down2[0].Press
	if newPress == oldPress || newPress.Device().Generation() != 2 || newPress.PressID() != 1 {
		t.Fatalf("reconnect token = %#v, old token = %#v", newPress, oldPress)
	}
	effects, err = core.Step(bridge.OrdinaryBegan(newPress, bridge.Key("space")))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bridgeKeyTransitions(effects), []string{"down:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second generation down transitions = %v, want %v", got, want)
	}

	effects, err = core.Step(bridge.OrdinaryEnded(oldPress))
	if err != nil {
		t.Fatal(err)
	}
	if len(bridgeKeyTransitions(effects)) != 0 || !reflect.DeepEqual(core.Snapshot().HeldKeys, []bridge.Key{"space"}) {
		t.Fatalf("old-generation release affected new press: effects=%#v snapshot=%#v", effects, core.Snapshot())
	}

	up2, err := normalizer2.HandleReport(reportIDKeys, usagePayload())
	if err != nil {
		t.Fatal(err)
	}
	if len(up2) != 1 || up2[0].Kind != EdgeUp || up2[0].Press != newPress {
		t.Fatalf("new generation release = %#v", up2)
	}
	effects, err = core.Step(bridge.OrdinaryEnded(up2[0].Press))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bridgeKeyTransitions(effects), []string{"up:space"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second generation release transitions = %v, want %v", got, want)
	}
}

func bridgeKeyTransitions(effects []bridge.Effect) []string {
	var transitions []string
	for _, effect := range effects {
		switch e := effect.(type) {
		case bridge.KeyDownEffect:
			transitions = append(transitions, "down:"+string(e.Key))
		case bridge.KeyUpEffect:
			transitions = append(transitions, "up:"+string(e.Key))
		}
	}
	return transitions
}
