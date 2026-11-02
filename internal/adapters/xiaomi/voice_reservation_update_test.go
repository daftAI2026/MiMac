package xiaomi

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"howett.net/plist"
)

const updateTestUpSource uint64 = 0x0000000700000052

// The type assertion keeps this red-phase test source-compatible before the
// reservation API gains UpdateKeys.
type reservationKeyUpdater interface {
	UpdateKeys(buttonIDs []string) error
}

func updateReservationKeys(t *testing.T, reservation *VoiceKeyReservation, buttonIDs []string) error {
	t.Helper()
	updater, ok := any(reservation).(reservationKeyUpdater)
	if !ok {
		t.Fatalf("%T does not implement UpdateKeys([]string) error", reservation)
		return errors.New("UpdateKeys unavailable")
	}
	return updater.UpdateKeys(buttonIDs)
}

type updateTestPort struct {
	closingVoiceMappingPort
	writeCalls   int
	failWriteAt  int
	failWriteErr error
}

func (p *updateTestPort) Write(entries []voiceMappingEntry) error {
	p.writeCalls++
	if p.writeCalls == p.failWriteAt {
		return p.failWriteErr
	}
	return p.closingVoiceMappingPort.fakeVoiceMappingPort.Write(entries)
}

func newUpdateTestReservation(t *testing.T, baseline []voiceMappingEntry) (*VoiceKeyReservation, *updateTestPort, *fakeVoiceMappingSnapshots, *int) {
	t.Helper()
	port := &updateTestPort{closingVoiceMappingPort: closingVoiceMappingPort{
		fakeVoiceMappingPort: fakeVoiceMappingPort{
			registryID: 42,
			entries:    cloneVoiceMappingEntries(baseline),
		},
	}}
	snapshots := &fakeVoiceMappingSnapshots{}
	lease := newVoiceMappingLease(port, snapshots)
	if err := lease.acquire(); err != nil {
		t.Fatalf("initial voice lease acquire: %v", err)
	}
	unlocks := 0
	reservation := &VoiceKeyReservation{
		lease:  lease,
		port:   port,
		unlock: func() error { unlocks++; return nil },
	}
	return reservation, port, snapshots, &unlocks
}

func mappingEntryForTest(entries []voiceMappingEntry, source uint64) (voiceMappingEntry, bool) {
	for _, entry := range entries {
		value, present, err := voiceMappingSourceOf(entry)
		if err == nil && present && value == source {
			return entry, true
		}
	}
	return nil, false
}

func requireMappingDestination(t *testing.T, entries []voiceMappingEntry, source, destination uint64) {
	t.Helper()
	entry, ok := mappingEntryForTest(entries, source)
	if !ok {
		t.Fatalf("mapping source %#x is missing from %#v", source, entries)
	}
	got, ok := voiceMappingInteger(entry[voiceMappingDestinationKey])
	if !ok || got != destination {
		t.Fatalf("mapping source %#x destination = %#x, want %#x", source, got, destination)
	}
}

func TestVoiceKeyReservationUpdateKeysReplacesSetAndPreservesUnrelatedEntries(t *testing.T) {
	voiceOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      voiceMappingSource,
		voiceMappingDestinationKey: uint64(0x0000000700000060),
		"VendorVoice":              map[string]any{"keep": []byte{1, 2}},
	}
	upOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      updateTestUpSource,
		voiceMappingDestinationKey: uint64(0x0000000700000051),
		"VendorUp":                 []string{"original"},
	}
	otherOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      uint64(0x0000000700000004),
		voiceMappingDestinationKey: uint64(0x0000000700000005),
		"Future":                   true,
	}
	baseline := []voiceMappingEntry{voiceOriginal, upOriginal, otherOriginal}
	reservation, port, snapshots, unlocks := newUpdateTestReservation(t, baseline)

	if err := updateReservationKeys(t, reservation, []string{"hid:0007:0052"}); err != nil {
		t.Fatalf("UpdateKeys(Up): %v", err)
	}
	requireMappingDestination(t, port.entries, voiceMappingSource, voiceMappingNoEvent)
	requireMappingDestination(t, port.entries, updateTestUpSource, voiceMappingNoEvent)
	if !snapshots.pending || port.closes != 0 || *unlocks != 0 {
		t.Fatalf("updated lease lost ownership: pending=%t closes=%d unlocks=%d", snapshots.pending, port.closes, *unlocks)
	}
	if snapshots.saves != 2 || snapshots.clears != 1 {
		t.Fatalf("update snapshot transaction saves=%d clears=%d, want 2/1", snapshots.saves, snapshots.clears)
	}

	// Unrelated edits and order changes during the lease survive its release.
	otherChanged := cloneVoiceMappingEntry(otherOriginal)
	otherChanged["Future"] = []string{"external"}
	externalAdded := voiceMappingEntry{
		voiceMappingSourceKey:      uint64(0x0000000700000010),
		voiceMappingDestinationKey: uint64(0x0000000700000011),
	}
	upOwned := cloneVoiceMappingEntry(upOriginal)
	upOwned[voiceMappingDestinationKey] = voiceMappingNoEvent
	voiceOwned := cloneVoiceMappingEntry(voiceOriginal)
	voiceOwned[voiceMappingDestinationKey] = voiceMappingNoEvent
	port.entries = []voiceMappingEntry{externalAdded, otherChanged, upOwned, voiceOwned}

	if err := reservation.Close(); err != nil {
		t.Fatalf("Close after UpdateKeys: %v", err)
	}
	want := []voiceMappingEntry{externalAdded, otherChanged, upOriginal, voiceOriginal}
	if !voiceMappingEntriesEqual(port.entries, want) {
		t.Fatalf("restored mappings = %#v, want %#v", port.entries, want)
	}
	if snapshots.pending || port.closes != 1 || *unlocks != 1 {
		t.Fatalf("successful Close state: pending=%t closes=%d unlocks=%d", snapshots.pending, port.closes, *unlocks)
	}
}

func TestVoiceKeyReservationUpdateKeysEmptyKeepsOnlyVoiceSuppressed(t *testing.T) {
	voiceOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      voiceMappingSource,
		voiceMappingDestinationKey: uint64(0x0000000700000060),
	}
	upOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      updateTestUpSource,
		voiceMappingDestinationKey: uint64(0x0000000700000051),
	}
	reservation, port, snapshots, unlocks := newUpdateTestReservation(t, []voiceMappingEntry{voiceOriginal, upOriginal})

	if err := updateReservationKeys(t, reservation, []string{"hid:0007:0052"}); err != nil {
		t.Fatalf("UpdateKeys(Up): %v", err)
	}
	if err := updateReservationKeys(t, reservation, nil); err != nil {
		t.Fatalf("UpdateKeys(empty): %v", err)
	}
	requireMappingDestination(t, port.entries, voiceMappingSource, voiceMappingNoEvent)
	requireMappingDestination(t, port.entries, updateTestUpSource, 0x0000000700000051)
	if !snapshots.pending || port.closes != 0 || *unlocks != 0 {
		t.Fatalf("empty update released the reservation: pending=%t closes=%d unlocks=%d", snapshots.pending, port.closes, *unlocks)
	}
	if err := reservation.Close(); err != nil {
		t.Fatalf("Close after empty update: %v", err)
	}
	if !voiceMappingEntriesEqual(port.entries, []voiceMappingEntry{voiceOriginal, upOriginal}) || snapshots.pending {
		t.Fatalf("final release did not restore baseline: entries=%#v pending=%t", port.entries, snapshots.pending)
	}
}

func TestVoiceKeyReservationUpdateKeysRejectsUnknownBeforeChangingLease(t *testing.T) {
	voiceOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      voiceMappingSource,
		voiceMappingDestinationKey: uint64(0x0000000700000060),
	}
	reservation, port, snapshots, unlocks := newUpdateTestReservation(t, []voiceMappingEntry{voiceOriginal})
	before := cloneVoiceMappingEntries(port.entries)
	writesBefore, savesBefore, clearsBefore := port.writeCalls, snapshots.saves, snapshots.clears

	if err := updateReservationKeys(t, reservation, []string{"hid:0007:ffff"}); err == nil {
		t.Fatal("UpdateKeys accepted a button outside the known profile")
	}
	if !voiceMappingEntriesEqual(port.entries, before) || port.writeCalls != writesBefore ||
		snapshots.saves != savesBefore || snapshots.clears != clearsBefore || !snapshots.pending ||
		port.closes != 0 || *unlocks != 0 {
		t.Fatalf("invalid target changed the active lease: entries=%#v writes=%d saves=%d clears=%d pending=%t closes=%d unlocks=%d",
			port.entries, port.writeCalls, snapshots.saves, snapshots.clears, snapshots.pending, port.closes, *unlocks)
	}
}

func TestVoiceKeyReservationUpdateKeysRetainsOldOwnerOnRestoreConflict(t *testing.T) {
	voiceOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      voiceMappingSource,
		voiceMappingDestinationKey: uint64(0x0000000700000060),
	}
	reservation, port, snapshots, unlocks := newUpdateTestReservation(t, []voiceMappingEntry{voiceOriginal})
	external := []voiceMappingEntry{{
		voiceMappingSourceKey:      voiceMappingSource,
		voiceMappingDestinationKey: uint64(0x0000000700000004),
		"External":                 true,
	}}
	port.entries = cloneVoiceMappingEntries(external)

	if err := updateReservationKeys(t, reservation, []string{"hid:0007:0052"}); !errors.Is(err, ErrVoiceMappingOwnership) {
		t.Fatalf("UpdateKeys error = %v, want ownership conflict", err)
	}
	if !voiceMappingEntriesEqual(port.entries, external) || !snapshots.pending ||
		port.closes != 0 || *unlocks != 0 {
		t.Fatalf("conflict lost owner or overwrote user mapping: entries=%#v pending=%t closes=%d unlocks=%d",
			port.entries, snapshots.pending, port.closes, *unlocks)
	}
}

func TestVoiceKeyReservationUpdateKeysRetainsNewOwnerAfterAcquireWriteFailure(t *testing.T) {
	writeFailure := errors.New("new composite mapping write failed")
	voiceOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      voiceMappingSource,
		voiceMappingDestinationKey: uint64(0x0000000700000060),
	}
	upOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      updateTestUpSource,
		voiceMappingDestinationKey: uint64(0x0000000700000051),
	}
	reservation, port, snapshots, unlocks := newUpdateTestReservation(t, []voiceMappingEntry{voiceOriginal, upOriginal})
	port.failWriteAt = port.writeCalls + 2 // Old lease restore succeeds; the new set write fails.
	port.failWriteErr = writeFailure

	if err := updateReservationKeys(t, reservation, []string{"hid:0007:0052"}); !errors.Is(err, writeFailure) {
		t.Fatalf("UpdateKeys error = %v, want new-write failure", err)
	}
	if !snapshots.pending || port.closes != 0 || *unlocks != 0 ||
		!voiceMappingEntriesEqual(port.entries, []voiceMappingEntry{voiceOriginal, upOriginal}) {
		t.Fatalf("failed update did not retain recovery owner: entries=%#v pending=%t closes=%d unlocks=%d",
			port.entries, snapshots.pending, port.closes, *unlocks)
	}

	// The host can keep output disabled and close/retry the still-owned lease.
	port.failWriteAt = 0
	port.failWriteErr = nil
	if err := reservation.Close(); err != nil {
		t.Fatalf("Close after failed update: %v", err)
	}
	if snapshots.pending || port.closes != 1 || *unlocks != 1 {
		t.Fatalf("failed-update cleanup: pending=%t closes=%d unlocks=%d", snapshots.pending, port.closes, *unlocks)
	}
}

func TestVoiceKeyReservationUpdateKeysPersistsV2SourcesAndRecoversOnlyThoseSources(t *testing.T) {
	voiceOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      voiceMappingSource,
		voiceMappingDestinationKey: uint64(0x0000000700000060),
	}
	upOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      updateTestUpSource,
		voiceMappingDestinationKey: uint64(0x0000000700000051),
		"VendorUp":                 []byte{4, 5},
	}
	otherOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      uint64(0x0000000700000004),
		voiceMappingDestinationKey: uint64(0x0000000700000005),
		"Untouched":                true,
	}
	baseline := []voiceMappingEntry{voiceOriginal, upOriginal, otherOriginal}
	path := filepath.Join(t.TempDir(), "mapping-recovery.plist")
	store := &fileVoiceMappingSnapshots{path: path, bootID: "boot-a"}
	port := &updateTestPort{closingVoiceMappingPort: closingVoiceMappingPort{
		fakeVoiceMappingPort: fakeVoiceMappingPort{registryID: 42, entries: cloneVoiceMappingEntries(baseline)},
	}}
	lease := newVoiceMappingLease(port, store)
	if err := lease.acquire(); err != nil {
		t.Fatalf("initial F5 lease: %v", err)
	}
	reservation := &VoiceKeyReservation{lease: lease, port: port}
	if err := updateReservationKeys(t, reservation, []string{"hid:0007:0052"}); err != nil {
		t.Fatalf("UpdateKeys(Up): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read v2 recovery snapshot: %v", err)
	}
	var record map[string]any
	if _, err := plist.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode recovery snapshot: %v", err)
	}
	version, ok := voiceMappingInteger(record["version"])
	if !ok || version != 2 {
		t.Fatalf("recovery version = %v, want v2", record["version"])
	}
	rawSources := reflect.ValueOf(record["sources"])
	if !rawSources.IsValid() || (rawSources.Kind() != reflect.Slice && rawSources.Kind() != reflect.Array) {
		t.Fatalf("v2 sources field is missing or invalid: %#v", record["sources"])
	}
	gotSources := make([]uint64, 0, rawSources.Len())
	for index := 0; index < rawSources.Len(); index++ {
		source, ok := voiceMappingInteger(rawSources.Index(index).Interface())
		if !ok {
			t.Fatalf("v2 source %d is not an integer: %#v", index, rawSources.Index(index).Interface())
		}
		gotSources = append(gotSources, source)
	}
	wantSources := []uint64{voiceMappingSource, updateTestUpSource}
	if !reflect.DeepEqual(gotSources, wantSources) {
		t.Fatalf("v2 sources = %#v, want canonical %#v", gotSources, wantSources)
	}

	// Simulate process recovery with the same boot and exact event-service ID.
	recoveredEntries := cloneVoiceMappingEntries(port.entries)
	otherCurrent := cloneVoiceMappingEntry(otherOriginal)
	otherCurrent["Untouched"] = "external update"
	externalAdded := voiceMappingEntry{
		voiceMappingSourceKey:      uint64(0x0000000700000010),
		voiceMappingDestinationKey: uint64(0x0000000700000011),
	}
	for index := range recoveredEntries {
		if source, present, _ := voiceMappingSourceOf(recoveredEntries[index]); present && source == otherOriginal[voiceMappingSourceKey].(uint64) {
			recoveredEntries[index] = otherCurrent
		}
	}
	recoveredEntries = append([]voiceMappingEntry{externalAdded}, recoveredEntries...)
	recoveryPort := &fakeVoiceMappingPort{registryID: 42, entries: recoveredEntries}
	if err := recoverVoiceMapping(recoveryPort, store); err != nil {
		t.Fatalf("recover v2 mapping lease: %v", err)
	}
	wantRecovered := []voiceMappingEntry{externalAdded, voiceOriginal, upOriginal, otherCurrent}
	if !voiceMappingEntriesEqual(recoveryPort.entries, wantRecovered) {
		t.Fatalf("v2 recovery mappings = %#v, want %#v", recoveryPort.entries, wantRecovered)
	}
	if store.HasPending() {
		t.Fatal("v2 snapshot remains after confirmed recovery")
	}
}

func TestVoiceMappingRecoveryV1StillRestoresOnlyF5(t *testing.T) {
	voiceOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      voiceMappingSource,
		voiceMappingDestinationKey: uint64(0x0000000700000060),
	}
	upOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      updateTestUpSource,
		voiceMappingDestinationKey: uint64(0x0000000700000051),
	}
	baseline := []voiceMappingEntry{voiceOriginal, upOriginal}
	path := filepath.Join(t.TempDir(), "legacy-recovery.plist")
	store := &fileVoiceMappingSnapshots{path: path, bootID: "boot-a"}
	if err := store.Save(42, baseline); err != nil {
		t.Fatalf("save legacy v1 snapshot: %v", err)
	}
	current := cloneVoiceMappingEntries(baseline)
	current[0][voiceMappingDestinationKey] = voiceMappingNoEvent
	port := &fakeVoiceMappingPort{registryID: 42, entries: current}
	if err := recoverVoiceMapping(port, store); err != nil {
		t.Fatalf("recover v1 snapshot: %v", err)
	}
	if !voiceMappingEntriesEqual(port.entries, baseline) || store.HasPending() {
		t.Fatalf("legacy v1 recovery = entries %#v pending=%t, want original entries and cleared snapshot", port.entries, store.HasPending())
	}
}
