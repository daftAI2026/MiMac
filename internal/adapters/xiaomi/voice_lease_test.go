package xiaomi

import (
	"errors"
	"reflect"
	"testing"
)

type fakeVoiceMappingPort struct {
	registryID uint64
	entries    []voiceMappingEntry
	reads      int
	writes     int
	readHook   func(read int, port *fakeVoiceMappingPort)
	writeErr   error
	afterWrite func([]voiceMappingEntry) []voiceMappingEntry
}

func (p *fakeVoiceMappingPort) RegistryID() uint64 { return p.registryID }

func (p *fakeVoiceMappingPort) Read() ([]voiceMappingEntry, error) {
	p.reads++
	if p.readHook != nil {
		p.readHook(p.reads, p)
	}
	return cloneVoiceMappingEntries(p.entries), nil
}

func (p *fakeVoiceMappingPort) Write(entries []voiceMappingEntry) error {
	p.writes++
	if p.writeErr != nil {
		return p.writeErr
	}
	p.entries = cloneVoiceMappingEntries(entries)
	if p.afterWrite != nil {
		p.entries = cloneVoiceMappingEntries(p.afterWrite(cloneVoiceMappingEntries(p.entries)))
	}
	return nil
}

type fakeVoiceMappingSnapshots struct {
	pending    bool
	registryID uint64
	original   []voiceMappingEntry
	saves      int
	clears     int
	saveErr    error
	clearErr   error
}

func (s *fakeVoiceMappingSnapshots) HasPending() bool { return s.pending }

func (s *fakeVoiceMappingSnapshots) Save(registryID uint64, original []voiceMappingEntry) error {
	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.pending = true
	s.registryID = registryID
	s.original = cloneVoiceMappingEntries(original)
	return nil
}

func (s *fakeVoiceMappingSnapshots) Clear() error {
	s.clears++
	if s.clearErr != nil {
		return s.clearErr
	}
	s.pending = false
	s.registryID = 0
	s.original = nil
	return nil
}

func TestVoiceMappingLeaseRestoresOriginalEntryAndPreservesExternalOrder(t *testing.T) {
	otherA := voiceMappingEntry{
		voiceMappingSourceKey:      uint64(0x70000004),
		voiceMappingDestinationKey: uint64(0x70000005),
		"FutureField":              map[string]any{"labels": []any{"alpha", []byte{1, 2}}, "tuple": [2]uint8{3, 4}},
	}
	voiceOriginal := voiceMappingEntry{
		voiceMappingSourceKey:      uint64(voiceMappingSource),
		voiceMappingDestinationKey: uint64(0x70000060),
		"FutureField":              map[string]any{"version": int32(9), "bytes": []byte{5, 6, 7}},
	}
	otherB := voiceMappingEntry{
		voiceMappingSourceKey:      uint64(0x70000008),
		voiceMappingDestinationKey: uint64(0x70000009),
		"VendorExtension":          []string{"kept", "as-is"},
	}
	baseline := []voiceMappingEntry{otherA, voiceOriginal, otherB}
	port := &fakeVoiceMappingPort{registryID: 42, entries: cloneVoiceMappingEntries(baseline)}
	snapshots := &fakeVoiceMappingSnapshots{}
	lease := newVoiceMappingLease(port, snapshots)

	if err := lease.acquire(); err != nil {
		t.Fatalf("acquire() error = %v", err)
	}
	if !snapshots.pending || snapshots.registryID != 42 || !voiceMappingEntriesEqual(snapshots.original, baseline) {
		t.Fatalf("snapshot after acquire = pending %v, registry %d, entries %#v", snapshots.pending, snapshots.registryID, snapshots.original)
	}
	installed := cloneVoiceMappingEntries(baseline)
	installed[1][voiceMappingDestinationKey] = voiceMappingNoEvent
	if !voiceMappingEntriesEqual(port.entries, installed) {
		t.Fatalf("installed mappings = %#v, want %#v", port.entries, installed)
	}

	changedA := cloneVoiceMappingEntry(otherA)
	changedA[voiceMappingDestinationKey] = uint64(0x7000000a)
	added := voiceMappingEntry{
		voiceMappingSourceKey:      uint64(0x70000010),
		voiceMappingDestinationKey: uint64(0x70000011),
		"Unknown":                  map[string]any{"kept": true},
	}
	changedB := cloneVoiceMappingEntry(otherB)
	changedB["VendorExtension"] = []string{"external", "update"}
	// An external reorder and unrelated edits survive; only our current voice
	// entry is replaced in place with the exact saved baseline entry.
	port.entries = []voiceMappingEntry{added, changedB, cloneVoiceMappingEntry(lease.owned), changedA}
	if err := lease.release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	want := []voiceMappingEntry{added, changedB, voiceOriginal, changedA}
	if !voiceMappingEntriesEqual(port.entries, want) {
		t.Fatalf("restored mappings = %#v, want %#v", port.entries, want)
	}
	if snapshots.pending {
		t.Fatal("snapshot remains after confirmed restore")
	}
	if port.writes != 2 {
		t.Fatalf("mapping writes = %d, want acquire and restore writes", port.writes)
	}
}

func TestVoiceMappingLeaseSameBaselineDoesNotWrite(t *testing.T) {
	baseline := []voiceMappingEntry{{
		voiceMappingSourceKey:      int64(voiceMappingSource),
		voiceMappingDestinationKey: int64(voiceMappingNoEvent),
		"FutureField":              []byte{8, 9},
	}}
	port := &fakeVoiceMappingPort{registryID: 73, entries: cloneVoiceMappingEntries(baseline)}
	snapshots := &fakeVoiceMappingSnapshots{}
	lease := newVoiceMappingLease(port, snapshots)
	if err := lease.acquire(); err != nil {
		t.Fatalf("acquire() error = %v", err)
	}
	if port.writes != 0 {
		t.Fatalf("acquire writes = %d, want no write for already-owned baseline", port.writes)
	}
	if err := lease.release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if port.writes != 0 {
		t.Fatalf("total writes = %d, want no mapping writes", port.writes)
	}
	if snapshots.pending {
		t.Fatal("snapshot remains after no-write lease release")
	}
}

func TestVoiceMappingLeaseRestoresNilBaseline(t *testing.T) {
	port := &fakeVoiceMappingPort{registryID: 74, entries: nil}
	snapshots := &fakeVoiceMappingSnapshots{}
	lease := newVoiceMappingLease(port, snapshots)
	if err := lease.acquire(); err != nil {
		t.Fatalf("acquire() error = %v", err)
	}
	if !snapshots.pending || snapshots.original == nil || len(snapshots.original) != 0 {
		t.Fatalf("empty baseline snapshot = pending %v entries %#v, want present empty slice", snapshots.pending, snapshots.original)
	}
	if err := lease.release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if len(port.entries) != 0 || snapshots.pending {
		t.Fatalf("release left entries=%#v pending=%v, want empty and cleared", port.entries, snapshots.pending)
	}
	if port.writes != 2 {
		t.Fatalf("mapping writes = %d, want install and restore", port.writes)
	}
}

func TestVoiceMappingLeaseWriteFailureRetainsBaselineAndSnapshot(t *testing.T) {
	writeFailure := errors.New("write failed")
	baseline := []voiceMappingEntry{{"UnrecognizedProperty": []byte{1, 2}}}
	port := &fakeVoiceMappingPort{registryID: 17, entries: cloneVoiceMappingEntries(baseline), writeErr: writeFailure}
	snapshots := &fakeVoiceMappingSnapshots{}
	lease := newVoiceMappingLease(port, snapshots)

	if err := lease.acquire(); !errors.Is(err, writeFailure) {
		t.Fatalf("acquire() error = %v, want wrapped write failure", err)
	}
	if !snapshots.pending || !voiceMappingEntriesEqual(snapshots.original, baseline) {
		t.Fatalf("failed acquire lost recovery snapshot: pending=%v original=%#v", snapshots.pending, snapshots.original)
	}
	port.writeErr = nil
	if err := lease.acquire(); !errors.Is(err, ErrVoiceMappingRecoveryNeeded) {
		t.Fatalf("second acquire error = %v, want recovery-required", err)
	}
	if snapshots.saves != 1 {
		t.Fatalf("snapshot saves = %d, want original baseline saved once", snapshots.saves)
	}
	if err := lease.release(); err != nil {
		t.Fatalf("release() after failed write error = %v", err)
	}
	if snapshots.pending {
		t.Fatal("snapshot remains after verified no-write rollback")
	}
	if !voiceMappingEntriesEqual(port.entries, baseline) {
		t.Fatalf("mappings after failed acquire = %#v, want original %#v", port.entries, baseline)
	}
}

func TestVoiceMappingLeaseDetectsConcurrentChangeAndDoesNotReplaceBaseline(t *testing.T) {
	baseline := []voiceMappingEntry{{
		voiceMappingSourceKey:      uint64(0x70000004),
		voiceMappingDestinationKey: uint64(0x70000005),
	}}
	external := []voiceMappingEntry{{
		voiceMappingSourceKey:      uint64(0x70000004),
		voiceMappingDestinationKey: uint64(0x70000008),
		"Extra":                    "external",
	}}
	port := &fakeVoiceMappingPort{registryID: 18, entries: cloneVoiceMappingEntries(baseline)}
	port.readHook = func(read int, port *fakeVoiceMappingPort) {
		if read == 2 {
			port.entries = cloneVoiceMappingEntries(external)
		}
	}
	snapshots := &fakeVoiceMappingSnapshots{}
	lease := newVoiceMappingLease(port, snapshots)

	if err := lease.acquire(); !errors.Is(err, ErrVoiceMappingChanged) {
		t.Fatalf("acquire() error = %v, want changed-before-write", err)
	}
	if !voiceMappingEntriesEqual(snapshots.original, baseline) {
		t.Fatalf("saved baseline = %#v, want first read %#v", snapshots.original, baseline)
	}
	if err := lease.acquire(); !errors.Is(err, ErrVoiceMappingRecoveryNeeded) {
		t.Fatalf("repeated acquire error = %v, want recovery-required", err)
	}
	if snapshots.saves != 1 || !voiceMappingEntriesEqual(snapshots.original, baseline) {
		t.Fatalf("repeated acquire changed baseline: saves=%d original=%#v", snapshots.saves, snapshots.original)
	}
	if port.writes != 0 {
		t.Fatalf("writes after concurrent conflict = %d, want 0", port.writes)
	}
	if err := lease.release(); err != nil {
		t.Fatalf("release() after no-write conflict error = %v", err)
	}
	if !voiceMappingEntriesEqual(port.entries, external) {
		t.Fatalf("release overwrote external mapping: %#v", port.entries)
	}
}

func TestVoiceMappingLeaseRestoreConflictRetainsSnapshot(t *testing.T) {
	port := &fakeVoiceMappingPort{registryID: 19}
	snapshots := &fakeVoiceMappingSnapshots{}
	lease := newVoiceMappingLease(port, snapshots)
	if err := lease.acquire(); err != nil {
		t.Fatalf("acquire() error = %v", err)
	}
	port.entries = []voiceMappingEntry{{
		voiceMappingSourceKey:      uint64(voiceMappingSource),
		voiceMappingDestinationKey: uint64(0x70000004),
		"External":                 true,
	}}
	before := cloneVoiceMappingEntries(port.entries)
	if err := lease.release(); !errors.Is(err, ErrVoiceMappingOwnership) {
		t.Fatalf("release() error = %v, want ownership conflict", err)
	}
	if !snapshots.pending {
		t.Fatal("ownership conflict cleared recovery snapshot")
	}
	if !voiceMappingEntriesEqual(port.entries, before) {
		t.Fatalf("ownership conflict changed mappings: got %#v want %#v", port.entries, before)
	}
}

func TestVoiceMappingLeaseReadbackMismatchRetainsSnapshot(t *testing.T) {
	port := &fakeVoiceMappingPort{registryID: 191}
	port.afterWrite = func(entries []voiceMappingEntry) []voiceMappingEntry {
		for _, entry := range entries {
			if source, ok := voiceMappingInteger(entry[voiceMappingSourceKey]); ok && source == voiceMappingSource {
				entry[voiceMappingDestinationKey] = uint64(0x70000004)
			}
		}
		return entries
	}
	snapshots := &fakeVoiceMappingSnapshots{}
	lease := newVoiceMappingLease(port, snapshots)
	if err := lease.acquire(); !errors.Is(err, ErrVoiceMappingReadback) {
		t.Fatalf("acquire() error = %v, want readback mismatch", err)
	}
	if !snapshots.pending || snapshots.original == nil || len(snapshots.original) != 0 {
		t.Fatalf("readback mismatch lost empty-baseline snapshot: pending=%v original=%#v", snapshots.pending, snapshots.original)
	}
	if err := lease.release(); !errors.Is(err, ErrVoiceMappingOwnership) {
		t.Fatalf("release() after unowned readback error = %v, want ownership conflict", err)
	}
	if !snapshots.pending {
		t.Fatal("unowned readback cleared recovery snapshot")
	}
}

func TestVoiceMappingLeaseSnapshotClearFailureRetriesWithoutRewrite(t *testing.T) {
	baseline := []voiceMappingEntry{{
		voiceMappingSourceKey:      uint64(voiceMappingSource),
		voiceMappingDestinationKey: uint64(0x70000060),
	}}
	port := &fakeVoiceMappingPort{registryID: 20, entries: cloneVoiceMappingEntries(baseline)}
	snapshots := &fakeVoiceMappingSnapshots{clearErr: errors.New("snapshot still busy")}
	lease := newVoiceMappingLease(port, snapshots)
	if err := lease.acquire(); err != nil {
		t.Fatalf("acquire() error = %v", err)
	}
	if err := lease.release(); err == nil {
		t.Fatal("release() succeeded while snapshot clear failed")
	}
	if !snapshots.pending {
		t.Fatal("failed snapshot clear lost recovery record")
	}
	if !voiceMappingEntriesEqual(port.entries, baseline) {
		t.Fatalf("first release did not restore baseline: %#v", port.entries)
	}
	if port.writes != 2 {
		t.Fatalf("writes before clear retry = %d, want one install and one restore", port.writes)
	}
	snapshots.clearErr = nil
	if err := lease.release(); err != nil {
		t.Fatalf("retry release() error = %v", err)
	}
	if port.writes != 2 {
		t.Fatalf("snapshot clear retry rewrote mappings; writes = %d", port.writes)
	}
	if snapshots.pending {
		t.Fatal("snapshot remains after successful retry")
	}
}

func TestVoiceMappingLeaseRejectsMalformedAndDuplicateSourcesBeforeSnapshot(t *testing.T) {
	tests := []struct {
		name    string
		entries []voiceMappingEntry
		wantErr error
	}{
		{
			name:    "malformed source",
			entries: []voiceMappingEntry{{voiceMappingSourceKey: "not-an-integer"}},
			wantErr: ErrVoiceMappingInvalidEntry,
		},
		{
			name: "duplicate voice source",
			entries: []voiceMappingEntry{
				{voiceMappingSourceKey: uint64(voiceMappingSource)},
				{voiceMappingSourceKey: uint64(voiceMappingSource)},
			},
			wantErr: ErrVoiceMappingDuplicateSource,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			port := &fakeVoiceMappingPort{registryID: 21, entries: cloneVoiceMappingEntries(test.entries)}
			snapshots := &fakeVoiceMappingSnapshots{}
			lease := newVoiceMappingLease(port, snapshots)
			if err := lease.acquire(); !errors.Is(err, test.wantErr) {
				t.Fatalf("acquire() error = %v, want %v", err, test.wantErr)
			}
			if snapshots.saves != 0 || snapshots.pending || port.writes != 0 {
				t.Fatalf("invalid baseline caused side effects: saves=%d pending=%v writes=%d", snapshots.saves, snapshots.pending, port.writes)
			}
		})
	}
}

func TestVoiceMappingValueEqualityIgnoresMapOrderButComparesArrays(t *testing.T) {
	left := voiceMappingEntry{"a": map[string]any{"first": int32(3), "second": []any{[]byte{1}, [2]uint8{2, 3}}}}
	right := voiceMappingEntry{"a": map[string]any{"second": []any{[]byte{1}, [2]uint8{2, 3}}, "first": uint64(3)}}
	if !voiceMappingEntryEqual(left, right) {
		t.Fatalf("equivalent mapping values compared unequal: %#v and %#v", left, right)
	}
	right["a"].(map[string]any)["second"] = []any{[]byte{1}, [2]uint8{3, 2}}
	if voiceMappingEntryEqual(left, right) {
		t.Fatal("array order was ignored")
	}
	clone := cloneVoiceMappingEntry(left)
	nested := left["a"].(map[string]any)
	nested["first"] = int32(99)
	nested["second"].([]any)[0].([]byte)[0] = 9
	nested["second"].([]any)[1] = [2]uint8{8, 8}
	if reflect.DeepEqual(clone, left) {
		t.Fatal("mutating original did not leave a distinct clone")
	}
	if got := clone["a"].(map[string]any)["first"]; got != int32(3) {
		t.Fatalf("cloned scalar changed after original mutation: %v", got)
	}
	if got := clone["a"].(map[string]any)["second"].([]any)[0].([]byte); !reflect.DeepEqual(got, []byte{1}) {
		t.Fatalf("cloned byte slice changed after original mutation: %v", got)
	}
	if got := clone["a"].(map[string]any)["second"].([]any)[1]; got != [2]uint8{2, 3} {
		t.Fatalf("cloned array changed after original mutation: %v", got)
	}
}
