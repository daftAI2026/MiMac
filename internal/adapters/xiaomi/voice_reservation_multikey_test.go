package xiaomi

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

const (
	multiKeyUpSource   = uint64(0x0000000700000052)
	multiKeyDownSource = uint64(0x0000000700000051)
)

var multiKeyButtonIDs = []string{"hid:0007:0052", "hid:0007:0051"}

func multiKeyBaseline() []voiceMappingEntry {
	return []voiceMappingEntry{
		{
			voiceMappingSourceKey:      voiceMappingSource,
			voiceMappingDestinationKey: uint64(0x0000000700000060),
			"VendorVoice":              []byte{1, 2},
		},
		{
			voiceMappingSourceKey:      multiKeyUpSource,
			voiceMappingDestinationKey: uint64(0x0000000700000051),
			"VendorUp":                 "keep",
		},
		{
			voiceMappingSourceKey:      multiKeyDownSource,
			voiceMappingDestinationKey: uint64(0x0000000700000050),
			"VendorDown":               "keep",
		},
	}
}

func TestVoiceMappingV2SnapshotRejectsInvalidSourceSets(t *testing.T) {
	tests := []struct {
		name    string
		sources []uint64
	}{
		{name: "empty"},
		{name: "missing F5", sources: []uint64{multiKeyUpSource}},
		{name: "unsorted", sources: []uint64{multiKeyUpSource, voiceMappingSource}},
		{name: "duplicate", sources: []uint64{voiceMappingSource, voiceMappingSource}},
		{name: "unknown", sources: []uint64{voiceMappingSource, 0x000000070000ffff}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fileVoiceMappingSnapshots{
				path:   filepath.Join(t.TempDir(), "invalid-v2.plist"),
				bootID: "boot-a",
			}
			// Write a raw v2 record to exercise validation on recovery, even
			// though the production writer rejects these source sets up front.
			if err := store.save(voiceMappingSnapshot{
				Version: 2, RegistryID: 42, Sources: test.sources,
				Original: []voiceMappingEntry{},
			}); err != nil {
				t.Fatalf("write invalid v2 fixture: %v", err)
			}
			if _, err := store.Load(); err == nil {
				t.Fatalf("Load accepted invalid v2 sources %#v", test.sources)
			}
		})
	}
}

func TestVoiceKeyReservationCloseWithMultiKeyConflictDoesNotPartiallyRestore(t *testing.T) {
	reservation, port, snapshots, unlocks := newUpdateTestReservation(t, multiKeyBaseline())
	if err := updateReservationKeys(t, reservation, multiKeyButtonIDs); err != nil {
		t.Fatalf("UpdateKeys(Up, Down): %v", err)
	}

	// Simulate an external edit to only Down while F5 and Up are still owned.
	for _, entry := range port.entries {
		source, present, err := voiceMappingSourceOf(entry)
		if err != nil {
			t.Fatalf("read mapping source: %v", err)
		}
		if present && source == multiKeyDownSource {
			entry[voiceMappingDestinationKey] = uint64(0x0000000700000042)
			entry["ExternalEdit"] = true
		}
	}
	beforeClose := cloneVoiceMappingEntries(port.entries)
	writesBeforeClose := port.writeCalls

	if err := reservation.Close(); !errors.Is(err, ErrVoiceMappingOwnership) {
		t.Fatalf("Close() error = %v, want ownership conflict", err)
	}
	if port.writeCalls != writesBeforeClose {
		t.Fatalf("Close wrote a partial restore: writes %d -> %d", writesBeforeClose, port.writeCalls)
	}
	if !voiceMappingEntriesEqual(port.entries, beforeClose) {
		t.Fatalf("Close changed mappings despite conflict: got %#v, want %#v", port.entries, beforeClose)
	}
	if !snapshots.pending || port.closes != 0 || *unlocks != 0 {
		t.Fatalf("conflicted Close discarded owner: pending=%t closes=%d unlocks=%d", snapshots.pending, port.closes, *unlocks)
	}
}

func TestVoiceKeyReservationMultiKeyServiceGoneValidatesSnapshotSources(t *testing.T) {
	for _, test := range []struct {
		name           string
		replaceSources []uint64
		wantOwnership  bool
	}{
		{name: "matching sources"},
		{name: "different sources", replaceSources: []uint64{voiceMappingSource}, wantOwnership: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			port := &disconnectAwareVoiceMappingPort{closingVoiceMappingPort: closingVoiceMappingPort{
				fakeVoiceMappingPort: fakeVoiceMappingPort{
					registryID: 42,
					entries:    cloneVoiceMappingEntries(multiKeyBaseline()),
				},
			}, gone: true}
			snapshots := &fakeVoiceMappingSnapshots{}
			lease := newVoiceMappingLease(port, snapshots)
			if err := lease.acquire(); err != nil {
				t.Fatalf("acquire F5 lease: %v", err)
			}
			reservation := &VoiceKeyReservation{lease: lease, port: port}
			unlocks := 0
			reservation.unlock = func() error { unlocks++; return nil }
			if err := reservation.UpdateKeys(multiKeyButtonIDs); err != nil {
				t.Fatalf("UpdateKeys(Up, Down): %v", err)
			}
			wantSources, err := voiceMappingSourcesForButtonIDs(multiKeyButtonIDs)
			if err != nil {
				t.Fatalf("resolve expected sources: %v", err)
			}
			if !reflect.DeepEqual(snapshots.sources, wantSources) {
				t.Fatalf("saved sources = %#v, want %#v", snapshots.sources, wantSources)
			}
			if test.replaceSources != nil {
				snapshots.sources = append([]uint64(nil), test.replaceSources...)
			}
			port.disconnected = true
			clearsBeforeClose := snapshots.clears

			err = reservation.Close()
			if test.wantOwnership {
				if !errors.Is(err, ErrVoiceMappingOwnership) {
					t.Fatalf("Close() error = %v, want snapshot ownership refusal", err)
				}
				if !snapshots.pending || snapshots.clears != clearsBeforeClose || port.closes != 0 || unlocks != 0 {
					t.Fatalf("mismatched-source cleanup discarded state: pending=%t clears=%d closes=%d unlocks=%d",
						snapshots.pending, snapshots.clears, port.closes, unlocks)
				}
				return
			}
			if err != nil {
				t.Fatalf("Close() with matching source snapshot: %v", err)
			}
			if snapshots.pending || snapshots.clears != clearsBeforeClose+1 || port.closes != 1 || unlocks != 1 {
				t.Fatalf("matching-source cleanup: pending=%t clears=%d closes=%d unlocks=%d",
					snapshots.pending, snapshots.clears, port.closes, unlocks)
			}
		})
	}
}

func TestVoiceKeyReservationUpdateKeysSameSetDoesNotWriteAgain(t *testing.T) {
	reservation, port, snapshots, _ := newUpdateTestReservation(t, multiKeyBaseline())
	if err := updateReservationKeys(t, reservation, multiKeyButtonIDs); err != nil {
		t.Fatalf("UpdateKeys(Up, Down): %v", err)
	}
	writes := port.writeCalls
	saves := snapshots.saves
	clears := snapshots.clears

	if err := updateReservationKeys(t, reservation, []string{"hid:0007:0051", "hid:0007:0052"}); err != nil {
		t.Fatalf("UpdateKeys(Down, Up): %v", err)
	}
	if port.writeCalls != writes || snapshots.saves != saves || snapshots.clears != clears {
		t.Fatalf("same-set update repeated lease work: writes %d->%d saves %d->%d clears %d->%d",
			writes, port.writeCalls, saves, snapshots.saves, clears, snapshots.clears)
	}
}

func TestVoiceKeyReservationUpdateKeysSameSetDetectsExternalChange(t *testing.T) {
	reservation, port, snapshots, _ := newUpdateTestReservation(t, multiKeyBaseline())
	if err := reservation.UpdateKeys(multiKeyButtonIDs); err != nil {
		t.Fatal(err)
	}
	for _, entry := range port.entries {
		source, _, _ := voiceMappingSourceOf(entry)
		if source == multiKeyUpSource {
			entry[voiceMappingDestinationKey] = uint64(0x0000000700000004)
		}
	}
	writes := port.writeCalls
	if err := reservation.UpdateKeys(multiKeyButtonIDs); !errors.Is(err, ErrVoiceMappingOwnership) {
		t.Fatalf("same-set update ignored externally changed mapping: %v", err)
	}
	if port.writeCalls != writes || !snapshots.pending {
		t.Fatal("external mapping was overwritten or recovery owner lost")
	}
}

type saveThenErrorVoiceMappingSnapshots struct {
	*fakeVoiceMappingSnapshots
	writeErr error
}

func (s *saveThenErrorVoiceMappingSnapshots) SaveWithSources(registryID uint64, sources []uint64, original []voiceMappingEntry) error {
	if err := s.fakeVoiceMappingSnapshots.SaveWithSources(registryID, sources, original); err != nil {
		return err
	}
	return s.writeErr
}

func TestVoiceMappingLeaseRetainsOwnerWhenCommittedSnapshotSaveReturnsError(t *testing.T) {
	saveErr := errors.New("snapshot directory sync failed after file commit")
	snapshots := &saveThenErrorVoiceMappingSnapshots{
		fakeVoiceMappingSnapshots: &fakeVoiceMappingSnapshots{},
		writeErr:                  saveErr,
	}
	port := &closingVoiceMappingPort{fakeVoiceMappingPort: fakeVoiceMappingPort{
		registryID: 42,
		entries:    cloneVoiceMappingEntries(multiKeyBaseline()),
	}}
	lease := newVoiceMappingLeaseWithSources(port, snapshots, []uint64{voiceMappingSource, multiKeyDownSource, multiKeyUpSource})

	if err := lease.acquire(); !errors.Is(err, saveErr) {
		t.Fatalf("acquire() error = %v, want committed-snapshot error", err)
	}
	if lease.baseline == nil || len(lease.ownedBySource) != 3 || !snapshots.HasPending() {
		t.Fatalf("committed snapshot lost in-memory owner: baseline=%#v owned=%d pending=%t",
			lease.baseline, len(lease.ownedBySource), snapshots.HasPending())
	}
	if port.writes != 0 {
		t.Fatalf("acquire wrote mappings after snapshot save failed: writes=%d", port.writes)
	}

	// An owner retained from the committed record can safely clear it on retry;
	// without that owner release would stop at ErrVoiceMappingSnapshotPending.
	if err := lease.release(); err != nil {
		t.Fatalf("release retained owner: %v", err)
	}
	if snapshots.HasPending() || snapshots.clears != 1 {
		t.Fatalf("retry did not clear committed snapshot: pending=%t clears=%d", snapshots.HasPending(), snapshots.clears)
	}
}
