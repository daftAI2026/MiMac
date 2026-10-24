package xiaomi

import (
	"errors"
	"fmt"
	"reflect"
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

type voiceMappingLease struct {
	port      voiceMappingPort
	snapshots voiceMappingSnapshotStore
	baseline  []voiceMappingEntry
	owned     voiceMappingEntry
	active    bool
}

func newVoiceMappingLease(port voiceMappingPort, snapshots voiceMappingSnapshotStore) *voiceMappingLease {
	return &voiceMappingLease{port: port, snapshots: snapshots}
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
	desired, owned, err := installVoiceMapping(baseline)
	if err != nil {
		return err
	}
	if err := l.snapshots.Save(l.port.RegistryID(), cloneVoiceMappingEntries(baseline)); err != nil {
		return fmt.Errorf("save original user key mappings: %w", err)
	}

	// Keep the original in memory as soon as its durable snapshot exists. Any
	// later error must be recoverable through release, never through a new read
	// that could replace the baseline.
	l.baseline = cloneVoiceMappingEntries(baseline)
	l.owned = cloneVoiceMappingEntry(owned)

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

func (l *voiceMappingLease) release() error {
	if l == nil || l.port == nil || l.snapshots == nil {
		return ErrVoiceMappingLeaseDependency
	}
	if l.baseline == nil {
		return nil
	}

	current, err := l.port.Read()
	if err != nil {
		return fmt.Errorf("read user key mappings before lease restore: %w", err)
	}
	desired, err := restoreVoiceMapping(current, l.baseline, l.owned)
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
	l.owned = nil
	l.active = false
	return nil
}

func installVoiceMapping(baseline []voiceMappingEntry) ([]voiceMappingEntry, voiceMappingEntry, error) {
	desired := cloneVoiceMappingEntries(baseline)
	voiceIndex := -1
	for i, entry := range baseline {
		source, present, err := voiceMappingSourceOf(entry)
		if err != nil {
			return nil, nil, err
		}
		if present && source == voiceMappingSource {
			if voiceIndex != -1 {
				return nil, nil, ErrVoiceMappingDuplicateSource
			}
			voiceIndex = i
		}
	}

	if voiceIndex == -1 {
		owned := voiceMappingEntry{
			voiceMappingSourceKey:      voiceMappingSource,
			voiceMappingDestinationKey: voiceMappingNoEvent,
		}
		desired = append(desired, cloneVoiceMappingEntry(owned))
		return desired, owned, nil
	}

	owned := cloneVoiceMappingEntry(baseline[voiceIndex])
	if destination, ok := voiceMappingInteger(owned[voiceMappingDestinationKey]); ok && destination == voiceMappingNoEvent {
		return desired, owned, nil
	}
	owned[voiceMappingDestinationKey] = voiceMappingNoEvent
	desired[voiceIndex] = cloneVoiceMappingEntry(owned)
	return desired, owned, nil
}

func restoreVoiceMapping(current, baseline []voiceMappingEntry, owned voiceMappingEntry) ([]voiceMappingEntry, error) {
	currentVoice, currentIndex, err := findVoiceMapping(current)
	if err != nil {
		return nil, err
	}
	baselineVoice, _, err := findVoiceMapping(baseline)
	if err != nil {
		return nil, err
	}

	if voiceMappingEntriesEqual(currentVoice, baselineVoice) {
		return cloneVoiceMappingEntries(current), nil
	}
	if len(currentVoice) != 1 || !voiceMappingEntryEqual(currentVoice[0], owned) {
		return nil, ErrVoiceMappingOwnership
	}

	result := cloneVoiceMappingEntries(current)
	if len(baselineVoice) == 0 {
		return append(result[:currentIndex], result[currentIndex+1:]...), nil
	}
	result[currentIndex] = cloneVoiceMappingEntry(baselineVoice[0])
	return result, nil
}

func findVoiceMapping(entries []voiceMappingEntry) ([]voiceMappingEntry, int, error) {
	var found []voiceMappingEntry
	index := -1
	for i, entry := range entries {
		source, present, err := voiceMappingSourceOf(entry)
		if err != nil {
			return nil, -1, err
		}
		if present && source == voiceMappingSource {
			found = append(found, cloneVoiceMappingEntry(entry))
			if index == -1 {
				index = i
			}
		}
	}
	return found, index, nil
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
