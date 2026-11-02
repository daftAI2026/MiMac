package xiaomi

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const (
	voiceMappingSourceKey      = "HIDKeyboardModifierMappingSrc"
	voiceMappingDestinationKey = "HIDKeyboardModifierMappingDst"
	voiceMappingSource         = uint64(0x000000070000003e)
	voiceMappingNoEvent        = uint64(0)
)

var (
	ErrVoiceMappingSnapshotPending = errors.New("xiaomi HID: a voice mapping recovery snapshot is already pending")
	ErrVoiceMappingRecoveryNeeded  = errors.New("xiaomi HID: voice mapping recovery must finish before reacquiring")
	ErrVoiceMappingChanged         = errors.New("xiaomi HID: user key mappings changed before lease write")
	ErrVoiceMappingReadback        = errors.New("xiaomi HID: user key mapping readback did not match")
	ErrVoiceMappingDuplicateSource = errors.New("xiaomi HID: duplicate voice source mapping")
	ErrVoiceMappingInvalidEntry    = errors.New("xiaomi HID: mapping entry has an invalid source value")
	ErrVoiceMappingOwnership       = errors.New("xiaomi HID: voice mapping ownership changed")
	ErrVoiceMappingLeaseDependency = errors.New("xiaomi HID: voice mapping lease requires a port and snapshot store")
	ErrVoiceMappingUnknownButton   = errors.New("xiaomi HID: mapping lease requested an unknown button")
	ErrVoiceMappingVoiceButton     = errors.New("xiaomi HID: voice button is already part of the reservation")
	ErrVoiceMappingSnapshotSources = errors.New("xiaomi HID: recovery store cannot persist mapping sources")
)

// voiceMappingEntry retains every property from the native mapping dictionary.
// The slice of entries is ordered; dictionary key order is intentionally not
// significant when comparing readback values.
type voiceMappingEntry map[string]any

type voiceMappingPort interface {
	RegistryID() uint64
	Read() ([]voiceMappingEntry, error)
	Write([]voiceMappingEntry) error
}

type voiceMappingSnapshotStore interface {
	HasPending() bool
	Save(registryID uint64, original []voiceMappingEntry) error
	Clear() error
}

type voiceMappingSnapshotSourceWriter interface {
	SaveWithSources(registryID uint64, sources []uint64, original []voiceMappingEntry) error
}

type voiceMappingLease struct {
	port          voiceMappingPort
	snapshots     voiceMappingSnapshotStore
	baseline      []voiceMappingEntry
	ownedBySource map[uint64]voiceMappingEntry
	sources       []uint64
	active        bool
}

func newVoiceMappingLease(port voiceMappingPort, snapshots voiceMappingSnapshotStore) *voiceMappingLease {
	return newVoiceMappingLeaseWithSources(port, snapshots, []uint64{voiceMappingSource})
}

func newVoiceMappingLeaseWithSources(port voiceMappingPort, snapshots voiceMappingSnapshotStore, sources []uint64) *voiceMappingLease {
	return &voiceMappingLease{port: port, snapshots: snapshots, sources: append([]uint64(nil), sources...)}
}

func (l *voiceMappingLease) acquire() error {
	if l == nil || l.port == nil || l.snapshots == nil {
		return ErrVoiceMappingLeaseDependency
	}
	if l.baseline != nil {
		if l.active {
			return nil
		}
		return ErrVoiceMappingRecoveryNeeded
	}
	if l.snapshots.HasPending() {
		return ErrVoiceMappingSnapshotPending
	}

	read, err := l.port.Read()
	if err != nil {
		return fmt.Errorf("read original user key mappings: %w", err)
	}
	baseline := cloneVoiceMappingEntries(read)
	sources := l.managedSources()
	if err := validateVoiceMappingSources(sources); err != nil {
		return err
	}
	desired, ownedBySource, err := installVoiceMappings(baseline, sources)
	if err != nil {
		return err
	}
	if err := saveVoiceMappingSnapshot(l.snapshots, l.port.RegistryID(), sources, cloneVoiceMappingEntries(baseline)); err != nil {
		// Snapshot creation can fail after the file itself was committed (for
		// example, while syncing its parent directory). Retain an in-memory owner
		// only when the on-disk record can be read back and matches this exact
		// lease. Otherwise release must keep the port/lock while the record is
		// pending instead of guessing which baseline it belongs to.
		l.rememberSnapshotAfterSaveError(l.port.RegistryID(), baseline, sources, ownedBySource)
		return fmt.Errorf("save original user key mappings: %w", err)
	}

	// Keep the original in memory as soon as its durable snapshot exists. Any
	// later error must be recoverable through release, never through a new read
	// that could replace the baseline.
	l.baseline = cloneVoiceMappingEntries(baseline)
	l.ownedBySource = cloneVoiceMappingEntriesBySource(ownedBySource)

	beforeWrite, err := l.port.Read()
	if err != nil {
		return fmt.Errorf("verify user key mappings before lease write: %w", err)
	}
	if !voiceMappingEntriesEqual(beforeWrite, baseline) {
		return ErrVoiceMappingChanged
	}
	if !voiceMappingEntriesEqual(desired, baseline) {
		if err := l.port.Write(cloneVoiceMappingEntries(desired)); err != nil {
			return fmt.Errorf("write voice mapping lease: %w", err)
		}
		readback, err := l.port.Read()
		if err != nil {
			return fmt.Errorf("read back voice mapping lease: %w", err)
		}
		if !voiceMappingEntriesEqual(readback, desired) {
			return ErrVoiceMappingReadback
		}
	}

	l.active = true
	return nil
}

func (l *voiceMappingLease) rememberSnapshotAfterSaveError(registryID uint64, baseline []voiceMappingEntry, sources []uint64, owned map[uint64]voiceMappingEntry) {
	if l == nil || l.snapshots == nil || !l.snapshots.HasPending() {
		return
	}
	reader, ok := l.snapshots.(voiceMappingSnapshotReader)
	if !ok {
		return
	}
	snapshot, err := reader.Load()
	if err != nil {
		return
	}
	snapshotSources, err := snapshot.managedSources()
	if err != nil || snapshot.RegistryID != registryID || snapshot.BootID != reader.currentBootID() ||
		!voiceMappingSourcesEqual(snapshotSources, sources) || !voiceMappingEntriesEqual(snapshot.Original, baseline) {
		return
	}
	l.baseline = cloneVoiceMappingEntries(baseline)
	l.ownedBySource = cloneVoiceMappingEntriesBySource(owned)
}

func saveVoiceMappingSnapshot(store voiceMappingSnapshotStore, registryID uint64, sources []uint64, original []voiceMappingEntry) error {
	if writer, ok := store.(voiceMappingSnapshotSourceWriter); ok {
		return writer.SaveWithSources(registryID, append([]uint64(nil), sources...), original)
	}
	if len(sources) == 1 && sources[0] == voiceMappingSource {
		return store.Save(registryID, original)
	}
	return ErrVoiceMappingSnapshotSources
}

func (l *voiceMappingLease) managedSources() []uint64 {
	if l == nil || len(l.sources) == 0 {
		return []uint64{voiceMappingSource}
	}
	return append([]uint64(nil), l.sources...)
}

func voiceMappingSourcesEqual(left, right []uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (l *voiceMappingLease) release() error {
	if l == nil || l.port == nil || l.snapshots == nil {
		return ErrVoiceMappingLeaseDependency
	}
	if l.baseline == nil {
		if l.snapshots.HasPending() {
			return ErrVoiceMappingSnapshotPending
		}
		return nil
	}

	current, err := l.port.Read()
	if err != nil {
		return fmt.Errorf("read user key mappings before lease restore: %w", err)
	}
	desired, err := restoreVoiceMappings(current, l.baseline, l.managedSources(), l.ownedBySource)
	if err != nil {
		return err
	}
	if !voiceMappingEntriesEqual(current, desired) {
		if err := l.port.Write(cloneVoiceMappingEntries(desired)); err != nil {
			return fmt.Errorf("restore original voice mapping: %w", err)
		}
		readback, err := l.port.Read()
		if err != nil {
			return fmt.Errorf("read back restored voice mapping: %w", err)
		}
		if !voiceMappingEntriesEqual(readback, desired) {
			return ErrVoiceMappingReadback
		}
	}

	// Do not forget the in-memory baseline until the durable recovery record is
	// confirmed cleared. A failed clear can then retry without another mapping
	// write or a new snapshot.
	if err := l.snapshots.Clear(); err != nil {
		return fmt.Errorf("clear restored voice mapping snapshot: %w", err)
	}
	l.baseline = nil
	l.ownedBySource = nil
	l.active = false
	return nil
}

func installVoiceMapping(baseline []voiceMappingEntry) ([]voiceMappingEntry, voiceMappingEntry, error) {
	desired, owned, err := installVoiceMappings(baseline, []uint64{voiceMappingSource})
	if err != nil {
		return nil, nil, err
	}
	return desired, cloneVoiceMappingEntry(owned[voiceMappingSource]), nil
}

func restoreVoiceMapping(current, baseline []voiceMappingEntry, owned voiceMappingEntry) ([]voiceMappingEntry, error) {
	return restoreVoiceMappings(current, baseline, []uint64{voiceMappingSource}, map[uint64]voiceMappingEntry{
		voiceMappingSource: cloneVoiceMappingEntry(owned),
	})
}

func installVoiceMappings(baseline []voiceMappingEntry, sources []uint64) ([]voiceMappingEntry, map[uint64]voiceMappingEntry, error) {
	if err := validateVoiceMappingSources(sources); err != nil {
		return nil, nil, err
	}
	desired := cloneVoiceMappingEntries(baseline)
	indexes, err := mappingIndexesForSources(baseline, sources)
	if err != nil {
		return nil, nil, err
	}
	owned := make(map[uint64]voiceMappingEntry, len(sources))
	for _, source := range sources {
		matches := indexes[source]
		if len(matches) > 1 {
			return nil, nil, ErrVoiceMappingDuplicateSource
		}
		if len(matches) == 0 {
			entry := voiceMappingEntry{
				voiceMappingSourceKey:      source,
				voiceMappingDestinationKey: voiceMappingNoEvent,
			}
			owned[source] = cloneVoiceMappingEntry(entry)
			desired = append(desired, cloneVoiceMappingEntry(entry))
			continue
		}

		index := matches[0]
		entry := cloneVoiceMappingEntry(baseline[index])
		if destination, ok := voiceMappingInteger(entry[voiceMappingDestinationKey]); !ok || destination != voiceMappingNoEvent {
			entry[voiceMappingDestinationKey] = voiceMappingNoEvent
			desired[index] = cloneVoiceMappingEntry(entry)
		}
		owned[source] = cloneVoiceMappingEntry(entry)
	}
	return desired, owned, nil
}

// restoreVoiceMappings validates every owned source before constructing a new
// property list. The caller writes only after the whole set has an unambiguous
// owner state, so a conflict on one button cannot partially restore another.
func restoreVoiceMappings(current, baseline []voiceMappingEntry, sources []uint64, owned map[uint64]voiceMappingEntry) ([]voiceMappingEntry, error) {
	if err := validateVoiceMappingSources(sources); err != nil {
		return nil, err
	}
	currentIndexes, err := mappingIndexesForSources(current, sources)
	if err != nil {
		return nil, err
	}
	baselineIndexes, err := mappingIndexesForSources(baseline, sources)
	if err != nil {
		return nil, err
	}
	type restoreOperation struct {
		source   uint64
		baseline voiceMappingEntry
		remove   bool
	}
	operations := make([]restoreOperation, 0, len(sources))
	for _, source := range sources {
		currentMatches := currentIndexes[source]
		baselineMatches := baselineIndexes[source]
		if len(baselineMatches) > 1 {
			return nil, ErrVoiceMappingDuplicateSource
		}
		if len(currentMatches) > 1 {
			return nil, ErrVoiceMappingOwnership
		}

		if len(currentMatches) == len(baselineMatches) {
			if len(currentMatches) == 0 || voiceMappingEntryEqual(current[currentMatches[0]], baseline[baselineMatches[0]]) {
				continue
			}
		}
		if len(currentMatches) != 1 || !voiceMappingEntryEqual(current[currentMatches[0]], owned[source]) {
			return nil, ErrVoiceMappingOwnership
		}
		operation := restoreOperation{source: source, remove: len(baselineMatches) == 0}
		if len(baselineMatches) != 0 {
			operation.baseline = cloneVoiceMappingEntry(baseline[baselineMatches[0]])
		}
		operations = append(operations, operation)
	}

	result := cloneVoiceMappingEntries(current)
	for _, operation := range operations {
		index := -1
		for i, entry := range result {
			source, present, sourceErr := voiceMappingSourceOf(entry)
			if sourceErr != nil {
				return nil, sourceErr
			}
			if present && source == operation.source {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, ErrVoiceMappingOwnership
		}
		if operation.remove {
			result = append(result[:index], result[index+1:]...)
		} else {
			result[index] = cloneVoiceMappingEntry(operation.baseline)
		}
	}
	return result, nil
}

func mappingIndexesForSources(entries []voiceMappingEntry, sources []uint64) (map[uint64][]int, error) {
	requested := make(map[uint64]struct{}, len(sources))
	for _, source := range sources {
		requested[source] = struct{}{}
	}
	indexes := make(map[uint64][]int, len(sources))
	for i, entry := range entries {
		source, present, err := voiceMappingSourceOf(entry)
		if err != nil {
			return nil, err
		}
		if _, ok := requested[source]; present && ok {
			indexes[source] = append(indexes[source], i)
		}
	}
	return indexes, nil
}

func findVoiceMapping(entries []voiceMappingEntry) ([]voiceMappingEntry, int, error) {
	indexes, err := mappingIndexesForSources(entries, []uint64{voiceMappingSource})
	if err != nil {
		return nil, -1, err
	}
	matches := indexes[voiceMappingSource]
	found := make([]voiceMappingEntry, 0, len(matches))
	for _, index := range matches {
		found = append(found, cloneVoiceMappingEntry(entries[index]))
	}
	if len(matches) == 0 {
		return found, -1, nil
	}
	return found, matches[0], nil
}

func voiceMappingSourceOf(entry voiceMappingEntry) (uint64, bool, error) {
	raw, present := entry[voiceMappingSourceKey]
	if !present {
		return 0, false, nil
	}
	source, ok := voiceMappingInteger(raw)
	if !ok {
		return 0, true, ErrVoiceMappingInvalidEntry
	}
	return source, true, nil
}

func voiceMappingSourcesForButtonIDs(buttonIDs []string) ([]uint64, error) {
	profile, err := DeviceProfile()
	if err != nil {
		return nil, err
	}
	sources := map[uint64]struct{}{voiceMappingSource: {}}
	seenButtons := make(map[string]struct{}, len(buttonIDs))
	for _, buttonID := range buttonIDs {
		if buttonID == VoiceButtonID {
			return nil, ErrVoiceMappingVoiceButton
		}
		if _, exists := seenButtons[buttonID]; exists {
			return nil, ErrVoiceMappingDuplicateSource
		}
		seenButtons[buttonID] = struct{}{}
		button, ok := profile.Button(buttonID)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrVoiceMappingUnknownButton, buttonID)
		}
		source, err := hidUsageSource(button.ID)
		if err != nil {
			return nil, err
		}
		if _, exists := sources[source]; exists {
			return nil, ErrVoiceMappingDuplicateSource
		}
		sources[source] = struct{}{}
	}
	result := make([]uint64, 0, len(sources))
	for source := range sources {
		result = append(result, source)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func hidUsageSource(buttonID string) (uint64, error) {
	parts := strings.Split(buttonID, ":")
	if len(parts) != 3 || parts[0] != "hid" || len(parts[1]) != 4 || len(parts[2]) != 4 {
		return 0, fmt.Errorf("%w: invalid HID button ID %q", ErrVoiceMappingUnknownButton, buttonID)
	}
	page, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid HID usage page in %q", ErrVoiceMappingUnknownButton, buttonID)
	}
	usage, err := strconv.ParseUint(parts[2], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid HID usage in %q", ErrVoiceMappingUnknownButton, buttonID)
	}
	return page<<32 | usage, nil
}

func validateVoiceMappingSources(sources []uint64) error {
	if len(sources) == 0 {
		return ErrVoiceMappingInvalidEntry
	}
	profile, err := DeviceProfile()
	if err != nil {
		return err
	}
	known := make(map[uint64]struct{}, len(profile.Buttons))
	for _, button := range profile.Buttons {
		source, err := hidUsageSource(button.ID)
		if err != nil {
			return err
		}
		known[source] = struct{}{}
	}
	previous := uint64(0)
	containsVoice := false
	for index, source := range sources {
		if _, ok := known[source]; !ok {
			return fmt.Errorf("%w: source %#x", ErrVoiceMappingUnknownButton, source)
		}
		if index != 0 && source <= previous {
			return ErrVoiceMappingDuplicateSource
		}
		previous = source
		containsVoice = containsVoice || source == voiceMappingSource
	}
	if !containsVoice {
		return ErrVoiceMappingVoiceButton
	}
	return nil
}

func voiceMappingInteger(value any) (uint64, bool) {
	if value == nil {
		return 0, false
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := v.Int()
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint(), true
	default:
		return 0, false
	}
}

func voiceMappingEntriesEqual(left, right []voiceMappingEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !voiceMappingEntryEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}

func voiceMappingEntryEqual(left, right voiceMappingEntry) bool {
	if (left == nil) != (right == nil) || len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || !voiceMappingValueEqual(value, other) {
			return false
		}
	}
	return true
}

func voiceMappingValueEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if leftInteger, ok := voiceMappingInteger(left); ok {
		rightInteger, rightOK := voiceMappingInteger(right)
		return rightOK && leftInteger == rightInteger
	}

	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if leftValue.Kind() == reflect.Map && rightValue.Kind() == reflect.Map {
		if leftValue.IsNil() != rightValue.IsNil() || leftValue.Len() != rightValue.Len() {
			return false
		}
		iter := leftValue.MapRange()
		for iter.Next() {
			key := iter.Key()
			if key.Type() != rightValue.Type().Key() {
				if !key.Type().ConvertibleTo(rightValue.Type().Key()) {
					return false
				}
				key = key.Convert(rightValue.Type().Key())
			}
			other := rightValue.MapIndex(key)
			if !other.IsValid() || !voiceMappingValueEqual(iter.Value().Interface(), other.Interface()) {
				return false
			}
		}
		return true
	}
	if (leftValue.Kind() == reflect.Slice || leftValue.Kind() == reflect.Array) &&
		(rightValue.Kind() == reflect.Slice || rightValue.Kind() == reflect.Array) {
		if leftValue.Kind() == reflect.Slice && rightValue.Kind() == reflect.Slice && leftValue.IsNil() != rightValue.IsNil() {
			return false
		}
		if leftValue.Len() != rightValue.Len() {
			return false
		}
		for i := 0; i < leftValue.Len(); i++ {
			if !voiceMappingValueEqual(leftValue.Index(i).Interface(), rightValue.Index(i).Interface()) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(left, right)
}

func cloneVoiceMappingEntries(entries []voiceMappingEntry) []voiceMappingEntry {
	clone := make([]voiceMappingEntry, len(entries))
	for i, entry := range entries {
		clone[i] = cloneVoiceMappingEntry(entry)
	}
	return clone
}

func cloneVoiceMappingEntry(entry voiceMappingEntry) voiceMappingEntry {
	if entry == nil {
		return nil
	}
	return cloneMappingValue(reflect.ValueOf(entry)).Interface().(voiceMappingEntry)
}

func cloneVoiceMappingEntriesBySource(entries map[uint64]voiceMappingEntry) map[uint64]voiceMappingEntry {
	if entries == nil {
		return nil
	}
	clone := make(map[uint64]voiceMappingEntry, len(entries))
	for source, entry := range entries {
		clone[source] = cloneVoiceMappingEntry(entry)
	}
	return clone
}

func cloneMappingValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.New(value.Type()).Elem()
		clone.Set(cloneMappingValue(value.Elem()))
		return clone
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			clone.SetMapIndex(iter.Key(), cloneMappingValue(iter.Value()))
		}
		return clone
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			clone.Index(i).Set(cloneMappingValue(value.Index(i)))
		}
		return clone
	case reflect.Array:
		clone := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			clone.Index(i).Set(cloneMappingValue(value.Index(i)))
		}
		return clone
	default:
		return value
	}
}
