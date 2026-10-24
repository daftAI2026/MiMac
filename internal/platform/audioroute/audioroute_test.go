package audioroute

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"testing"
)

type fakePropertyKey struct {
	object    uint32
	address   propertyAddress
	qualifier string
}

type fakePropertyAPI struct {
	data          map[fakePropertyKey][]byte
	dataErr       map[fakePropertyKey]error
	sizeOverride  map[fakePropertyKey]uint32
	hasOverride   map[fakePropertyKey]bool
	deviceIDs     map[string]uint32
	deviceIDErr   map[string]error
	uids          map[uint32]string
	bundleIDs     map[uint32]string
	uidErr        map[uint32]error
	bundleIDErr   map[uint32]error
	propertyReads []fakePropertyKey
	uidReads      []uint32
}

func newFakePropertyAPI() *fakePropertyAPI {
	return &fakePropertyAPI{
		data:         make(map[fakePropertyKey][]byte),
		dataErr:      make(map[fakePropertyKey]error),
		sizeOverride: make(map[fakePropertyKey]uint32),
		hasOverride:  make(map[fakePropertyKey]bool),
		deviceIDs:    make(map[string]uint32),
		deviceIDErr:  make(map[string]error),
		uids:         make(map[uint32]string),
		bundleIDs:    make(map[uint32]string),
		uidErr:       make(map[uint32]error),
		bundleIDErr:  make(map[uint32]error),
	}
}

func (f *fakePropertyAPI) HasProperty(objectID uint32, address propertyAddress) bool {
	key := fakePropertyKey{object: objectID, address: address}
	if value, exists := f.hasOverride[key]; exists {
		return value
	}
	_, exists := f.data[key]
	return exists
}

func (f *fakePropertyAPI) PropertyDataSize(objectID uint32, address propertyAddress, qualifier []byte) (uint32, error) {
	key := makeFakeKey(objectID, address, qualifier)
	if err := f.dataErr[key]; err != nil {
		return 0, err
	}
	if size, ok := f.sizeOverride[key]; ok {
		return size, nil
	}
	data, exists := f.data[key]
	if !exists {
		return 0, fmt.Errorf("missing property %#v", key)
	}
	return uint32(len(data)), nil
}

func (f *fakePropertyAPI) PropertyData(objectID uint32, address propertyAddress, qualifier []byte, capacity uint32) ([]byte, error) {
	key := makeFakeKey(objectID, address, qualifier)
	f.propertyReads = append(f.propertyReads, key)
	if err := f.dataErr[key]; err != nil {
		return nil, err
	}
	data, exists := f.data[key]
	if !exists {
		return nil, fmt.Errorf("missing property %#v", key)
	}
	return append([]byte(nil), data...), nil
}

func (f *fakePropertyAPI) DeviceUID(objectID uint32) (string, error) {
	f.uidReads = append(f.uidReads, objectID)
	if err := f.uidErr[objectID]; err != nil {
		return "", err
	}
	uid, exists := f.uids[objectID]
	if !exists {
		return "", errors.New("missing UID")
	}
	return uid, nil
}

func (f *fakePropertyAPI) DeviceIDForUID(uid string) (uint32, error) {
	if err := f.deviceIDErr[uid]; err != nil {
		return 0, err
	}
	return f.deviceIDs[uid], nil
}

func (f *fakePropertyAPI) ProcessBundleID(objectID uint32) (string, error) {
	if err := f.bundleIDErr[objectID]; err != nil {
		return "", err
	}
	id, exists := f.bundleIDs[objectID]
	if !exists {
		return "", errors.New("missing bundle ID")
	}
	return id, nil
}

func makeFakeKey(objectID uint32, address propertyAddress, qualifier []byte) fakePropertyKey {
	return fakePropertyKey{object: objectID, address: address, qualifier: string(qualifier)}
}

func putFakeProperty(f *fakePropertyAPI, objectID uint32, address propertyAddress, qualifier []byte, data []byte) {
	f.data[makeFakeKey(objectID, address, qualifier)] = append([]byte(nil), data...)
}

func u32(value uint32) []byte {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, value)
	return data
}

func objectIDsData(ids ...uint32) []byte {
	data := make([]byte, len(ids)*4)
	for index, id := range ids {
		binary.LittleEndian.PutUint32(data[index*4:index*4+4], id)
	}
	return data
}

func bufferListData(channels ...uint32) []byte {
	data := make([]byte, 8+len(channels)*16)
	binary.LittleEndian.PutUint32(data[:4], uint32(len(channels)))
	for index, channelCount := range channels {
		offset := 8 + index*16
		binary.LittleEndian.PutUint32(data[offset:offset+4], channelCount)
	}
	return data
}

func TestDefaultInputUIDAndNoFallbackOnFailure(t *testing.T) {
	fake := newFakePropertyAPI()
	putFakeProperty(fake, systemObjectID, propertyAddress{selector: selectorDefaultInput, scope: scopeGlobal}, nil, u32(12))
	fake.uids[12] = "device://default"
	inspector := newInspector(fake)

	got, err := inspector.DefaultInputDeviceUID()
	if err != nil || got != "device://default" {
		t.Fatalf("DefaultInputDeviceUID() = %q, %v", got, err)
	}

	fake.dataErr[makeFakeKey(systemObjectID, propertyAddress{selector: selectorDefaultInput, scope: scopeGlobal}, nil)] = errors.New("HAL unavailable")
	fake.uidReads = nil
	if _, err := inspector.DefaultInputDeviceUID(); err == nil {
		t.Fatal("DefaultInputDeviceUID() hid a Core Audio read error")
	}
	if len(fake.uidReads) != 0 {
		t.Fatalf("default lookup read another device as fallback: %v", fake.uidReads)
	}
}

func TestDefaultInputUnknownAndMalformedData(t *testing.T) {
	fake := newFakePropertyAPI()
	address := propertyAddress{selector: selectorDefaultInput, scope: scopeGlobal}
	putFakeProperty(fake, systemObjectID, address, nil, u32(unknownObject))
	if _, err := newInspector(fake).DefaultInputDeviceUID(); !errors.Is(err, ErrNoDefaultInput) {
		t.Fatalf("unknown default error = %v, want ErrNoDefaultInput", err)
	}
	putFakeProperty(fake, systemObjectID, address, nil, []byte{1, 2})
	if _, err := newInspector(fake).DefaultInputDeviceUID(); !errors.Is(err, ErrMalformedData) {
		t.Fatalf("malformed default error = %v, want ErrMalformedData", err)
	}
}

func TestDeviceByUIDExactMatchAndChannelCounts(t *testing.T) {
	fake := newFakePropertyAPI()
	fake.deviceIDs["wanted-uid"] = 3
	putFakeProperty(fake, 3, propertyAddress{selector: selectorStreamConfig, scope: scopeInput}, nil, bufferListData(1, 2))
	putFakeProperty(fake, 3, propertyAddress{selector: selectorStreamConfig, scope: scopeOutput}, nil, bufferListData(2))

	got, err := newInspector(fake).DeviceByUID("wanted-uid")
	if err != nil || got != (DeviceCapabilities{Exists: true, InputChannels: 3, OutputChannels: 2}) {
		t.Fatalf("DeviceByUID() = %+v, %v", got, err)
	}

	got, err = newInspector(fake).DeviceByUID("not-present")
	if err != nil || got.Exists {
		t.Fatalf("missing UID result = %+v, %v", got, err)
	}
}

func TestDeviceByUIDUnsupportedStreamScopeHasZeroChannels(t *testing.T) {
	fake := newFakePropertyAPI()
	fake.deviceIDs["output-only"] = 7
	putFakeProperty(fake, 7, propertyAddress{selector: selectorStreamConfig, scope: scopeOutput}, nil, bufferListData(2))
	fake.hasOverride[fakePropertyKey{object: 7, address: propertyAddress{selector: selectorStreamConfig, scope: scopeInput}}] = false

	got, err := newInspector(fake).DeviceByUID("output-only")
	if err != nil || got != (DeviceCapabilities{Exists: true, OutputChannels: 2}) {
		t.Fatalf("DeviceByUID() = %+v, %v", got, err)
	}
}

func TestDeviceByUIDUnknownAndTranslationErrorsStayExplicit(t *testing.T) {
	fake := newFakePropertyAPI()
	inspector := newInspector(fake)
	got, err := inspector.DeviceByUID("missing")
	if err != nil || got.Exists {
		t.Fatalf("unknown UID result = %+v, %v", got, err)
	}
	fake.deviceIDErr["configured"] = errors.New("Core Audio translation failed")
	if _, err := inspector.DeviceByUID("configured"); err == nil {
		t.Fatal("UID translation failure was reported as device absence")
	}
}

func TestProcessInputForPIDUsesExactPIDQualifier(t *testing.T) {
	fake := newFakePropertyAPI()
	pid := int32(0x01020304)
	qualifier := []byte{4, 3, 2, 1}
	putFakeProperty(fake, systemObjectID, propertyAddress{selector: selectorTranslatePID, scope: scopeGlobal}, qualifier, u32(90))
	putFakeProperty(fake, 90, propertyAddress{selector: selectorProcessRunning, scope: scopeGlobal}, nil, u32(1))
	putFakeProperty(fake, 90, propertyAddress{selector: selectorProcessDevices, scope: scopeInput}, nil, objectIDsData(21, 22))
	fake.uids[21] = "input-one"
	fake.uids[22] = "input-two"

	got, err := newInspector(fake).ProcessInput(pid)
	if err != nil || !got.Found || got.PID != pid || !got.RunningInput || len(got.InputDeviceUIDs) != 2 || got.InputDeviceUIDs[0] != "input-one" || got.InputDeviceUIDs[1] != "input-two" {
		t.Fatalf("ProcessInput() = %+v, %v", got, err)
	}
	if len(fake.propertyReads) < 1 || fake.propertyReads[0].qualifier != string(qualifier) {
		t.Fatalf("PID qualifier was not passed exactly: %+v", fake.propertyReads)
	}
}

func TestProcessInputMissingProcessAndInvalidPID(t *testing.T) {
	fake := newFakePropertyAPI()
	qualifier := []byte{8, 0, 0, 0}
	putFakeProperty(fake, systemObjectID, propertyAddress{selector: selectorTranslatePID, scope: scopeGlobal}, qualifier, u32(unknownObject))
	got, err := newInspector(fake).ProcessInput(8)
	if err != nil || got.Found || got.PID != 8 {
		t.Fatalf("missing process result = %+v, %v", got, err)
	}
	if _, err := newInspector(fake).ProcessInput(0); !errors.Is(err, ErrInvalidPID) {
		t.Fatalf("invalid PID error = %v", err)
	}
}

func TestInputsForBundleReturnsAllMatchesWithoutPickingOne(t *testing.T) {
	fake := newFakePropertyAPI()
	putFakeProperty(fake, systemObjectID, propertyAddress{selector: selectorProcessList, scope: scopeGlobal}, nil, objectIDsData(31, 32, 33))
	fake.bundleIDs[31] = "com.example.target"
	fake.bundleIDs[32] = "com.other"
	fake.bundleIDs[33] = "com.example.target"
	putFakeProperty(fake, 31, propertyAddress{selector: selectorProcessPID, scope: scopeGlobal}, nil, u32(501))
	putFakeProperty(fake, 31, propertyAddress{selector: selectorProcessRunning, scope: scopeGlobal}, nil, u32(0))
	putFakeProperty(fake, 31, propertyAddress{selector: selectorProcessDevices, scope: scopeInput}, nil, objectIDsData())
	putFakeProperty(fake, 33, propertyAddress{selector: selectorProcessPID, scope: scopeGlobal}, nil, u32(502))
	putFakeProperty(fake, 33, propertyAddress{selector: selectorProcessRunning, scope: scopeGlobal}, nil, u32(1))
	putFakeProperty(fake, 33, propertyAddress{selector: selectorProcessDevices, scope: scopeInput}, nil, objectIDsData(44))
	fake.uids[44] = "configured-input"

	got, err := newInspector(fake).InputsForBundle("com.example.target")
	if err != nil || len(got) != 2 {
		t.Fatalf("InputsForBundle() = %+v, %v", got, err)
	}
	if got[0].PID != 501 || got[0].RunningInput || got[0].InputDeviceUIDs == nil || len(got[0].InputDeviceUIDs) != 0 {
		t.Fatalf("first process route = %+v", got[0])
	}
	if got[1].PID != 502 || !got[1].RunningInput || len(got[1].InputDeviceUIDs) != 1 || got[1].InputDeviceUIDs[0] != "configured-input" {
		t.Fatalf("second process route = %+v", got[1])
	}
}

func TestInputsForBundleNoMatchesAndFailurePropagates(t *testing.T) {
	fake := newFakePropertyAPI()
	putFakeProperty(fake, systemObjectID, propertyAddress{selector: selectorProcessList, scope: scopeGlobal}, nil, objectIDsData(71))
	fake.bundleIDs[71] = "other"
	got, err := newInspector(fake).InputsForBundle("target")
	if err != nil || len(got) != 0 {
		t.Fatalf("no-match result = %+v, %v", got, err)
	}
	fake.bundleIDErr[71] = errors.New("property read denied")
	if _, err := newInspector(fake).InputsForBundle("target"); err == nil {
		t.Fatal("bundle property read error was hidden")
	}
}

func TestPropertyReadBoundsAndMalformedLists(t *testing.T) {
	fake := newFakePropertyAPI()
	address := propertyAddress{selector: selectorProcessList, scope: scopeGlobal}
	key := makeFakeKey(systemObjectID, address, nil)
	fake.sizeOverride[key] = maxPropertyBytes + 1
	if _, err := newInspector(fake).readProperty(systemObjectID, address, nil); !errors.Is(err, ErrMalformedData) {
		t.Fatalf("oversized property error = %v", err)
	}
	if _, err := objectIDs([]byte{1, 2, 3}, "fake IDs"); !errors.Is(err, ErrMalformedData) {
		t.Fatalf("misaligned object ID error = %v", err)
	}
	if _, err := parseAudioBufferListChannels([]byte{1, 0, 0, 0}); !errors.Is(err, ErrMalformedData) {
		t.Fatalf("truncated buffer list error = %v", err)
	}
	if _, err := parseAudioBufferListChannels([]byte{0, 0, 0, 0}); !errors.Is(err, ErrMalformedData) {
		t.Fatalf("missing buffer list alignment error = %v", err)
	}
}

func TestParseAudioBufferListOnlyReadsChannelFields(t *testing.T) {
	data := bufferListData(1, 3)
	for index := 0; index < 2; index++ {
		offset := 8 + index*16
		binary.LittleEndian.PutUint32(data[offset+4:offset+8], 0xffffffff)
		binary.LittleEndian.PutUint64(data[offset+8:offset+16], 0xffffffffffffffff)
	}
	got, err := parseAudioBufferListChannels(data)
	if err != nil || got != 4 {
		t.Fatalf("parseAudioBufferListChannels() = %d, %v", got, err)
	}
	if got, err := parseAudioBufferListChannels(bufferListData()); err != nil || got != 0 {
		t.Fatalf("empty buffer list = %d, %v", got, err)
	}
}

func TestNewUnsupportedPlatformStub(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Darwin has the native Core Audio implementation")
	}
	if _, err := New(); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("New() error = %v, want ErrUnsupportedPlatform", err)
	}
}

func TestDefaultOutputUIDIsIndependentOfInput(t *testing.T) {
	fake := newFakePropertyAPI()
	input := propertyAddress{selector: selectorDefaultInput, scope: scopeGlobal}
	output := propertyAddress{selector: selectorDefaultOutput, scope: scopeGlobal}
	putFakeProperty(fake, systemObjectID, input, nil, u32(12))
	putFakeProperty(fake, systemObjectID, output, nil, u32(24))
	fake.uids[12], fake.uids[24] = "tap-input", "playback-output"
	inspector := newInspector(fake)
	got, err := inspector.DefaultOutputDeviceUID()
	if err != nil || got != "playback-output" {
		t.Fatalf("output = %q, %v", got, err)
	}
	if len(fake.propertyReads) != 1 || fake.propertyReads[0].address != output {
		t.Fatalf("unexpected reads: %#v", fake.propertyReads)
	}
	// A later route change is reread, never served from a stale cached UID.
	putFakeProperty(fake, systemObjectID, output, nil, u32(25))
	fake.uids[25] = "new-output"
	if got, err := inspector.DefaultOutputDeviceUID(); err != nil || got != "new-output" {
		t.Fatalf("updated output = %q, %v", got, err)
	}
	// Missing output must not silently select the still-valid input.
	putFakeProperty(fake, systemObjectID, output, nil, u32(0))
	if _, err := inspector.DefaultOutputDeviceUID(); !errors.Is(err, ErrNoDefaultOutput) {
		t.Fatalf("missing output = %v", err)
	}
	failure := errors.New("output query failed")
	fake.dataErr[makeFakeKey(systemObjectID, output, nil)] = failure
	if _, err := inspector.DefaultOutputDeviceUID(); !errors.Is(err, failure) {
		t.Fatalf("output error = %v", err)
	}
}
