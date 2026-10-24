// Package audioroute reads Core Audio device and process routing metadata.
// It never opens an audio stream or reads sample data.
package audioroute

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var (
	ErrUnsupportedPlatform = errors.New("audioroute: Core Audio is available only on macOS")
	ErrNoDefaultInput      = errors.New("audioroute: no default input device")
	ErrInvalidPID          = errors.New("audioroute: PID must be positive")
	ErrMalformedData       = errors.New("audioroute: malformed Core Audio property data")
)

const (
	maxPropertyBytes = 1 << 20
	maxDeviceObjects = maxPropertyBytes / 4

	systemObjectID uint32 = 1
	unknownObject  uint32 = 0

	selectorDefaultInput    uint32 = 0x64496e20 // 'dIn '
	selectorTranslateUID    uint32 = 0x75696464 // 'uidd'
	selectorTranslatePID    uint32 = 0x69643270 // 'id2p'
	selectorProcessList     uint32 = 0x70727323 // 'prs#'
	selectorDeviceUID       uint32 = 0x75696420 // 'uid '
	selectorStreamConfig    uint32 = 0x736c6179 // 'slay'
	selectorProcessDevices  uint32 = 0x70647623 // 'pdv#'
	selectorProcessRunning  uint32 = 0x70697269 // 'piri'
	selectorProcessPID      uint32 = 0x70706964 // 'ppid'
	scopeGlobal             uint32 = 0x676c6f62 // 'glob'
	scopeInput              uint32 = 0x696e7074 // 'inpt'
	scopeOutput             uint32 = 0x6f757470 // 'outp'
	selectorProcessBundleID uint32 = 0x70626964 // 'pbid'
)

type propertyAddress struct {
	selector uint32
	scope    uint32
	element  uint32
}

// propertyAPI is deliberately read-only. Darwin backs it with AudioObjectGetPropertyData;
// tests use a fake implementation without opening any audio device.
type propertyAPI interface {
	HasProperty(objectID uint32, address propertyAddress) bool
	PropertyDataSize(objectID uint32, address propertyAddress, qualifier []byte) (uint32, error)
	PropertyData(objectID uint32, address propertyAddress, qualifier []byte, capacity uint32) ([]byte, error)
	DeviceIDForUID(uid string) (uint32, error)
	DeviceUID(objectID uint32) (string, error)
	ProcessBundleID(objectID uint32) (string, error)
}

// Inspector performs independent read-only Core Audio queries. It does not cache
// device or process state between calls.
type Inspector struct {
	api propertyAPI
}

// New creates a Core Audio inspector on macOS.
func New() (*Inspector, error) {
	api, err := newNativeAPI()
	if err != nil {
		return nil, err
	}
	return &Inspector{api: api}, nil
}

func newInspector(api propertyAPI) *Inspector { return &Inspector{api: api} }

// DeviceCapabilities reports exact UID presence and the device's total input
// and output channel counts. A UID lookup error is returned as an error rather
// than being reported as absence.
type DeviceCapabilities struct {
	Exists         bool
	InputChannels  uint32
	OutputChannels uint32
}

// ProcessInput describes a single Core Audio process object. InputDeviceUIDs
// are the process object's current input-device list; RunningInput is the
// separate Core Audio active-input flag.
type ProcessInput struct {
	Found           bool
	PID             int32
	RunningInput    bool
	InputDeviceUIDs []string
}

// DefaultInputDeviceUID returns the current default input device's persistent
// UID. Errors are not replaced with a search result or another default.
func (i *Inspector) DefaultInputDeviceUID() (string, error) {
	data, err := i.readProperty(systemObjectID, propertyAddress{selector: selectorDefaultInput, scope: scopeGlobal}, nil)
	if err != nil {
		return "", fmt.Errorf("audioroute: read default input device: %w", err)
	}
	deviceID, err := scalar(data, "default input device")
	if err != nil {
		return "", err
	}
	if deviceID == unknownObject {
		return "", ErrNoDefaultInput
	}
	uid, err := i.api.DeviceUID(deviceID)
	if err != nil {
		return "", fmt.Errorf("audioroute: read default input UID: %w", err)
	}
	if uid == "" {
		return "", fmt.Errorf("%w: empty default input UID", ErrMalformedData)
	}
	return uid, nil
}

// DeviceByUID reports whether Core Audio resolves uid to a device and, when
// present, its channel counts. The direct UID translation also covers hidden
// devices, which are omitted from the normal device list.
func (i *Inspector) DeviceByUID(uid string) (DeviceCapabilities, error) {
	if uid == "" {
		return DeviceCapabilities{}, fmt.Errorf("%w: empty device UID", ErrMalformedData)
	}
	deviceID, err := i.api.DeviceIDForUID(uid)
	if err != nil {
		return DeviceCapabilities{}, fmt.Errorf("audioroute: translate device UID: %w", err)
	}
	if deviceID == unknownObject {
		return DeviceCapabilities{}, nil
	}
	input, err := i.channelCount(deviceID, scopeInput)
	if err != nil {
		return DeviceCapabilities{}, fmt.Errorf("audioroute: read input channels: %w", err)
	}
	output, err := i.channelCount(deviceID, scopeOutput)
	if err != nil {
		return DeviceCapabilities{}, fmt.Errorf("audioroute: read output channels: %w", err)
	}
	return DeviceCapabilities{Exists: true, InputChannels: input, OutputChannels: output}, nil
}

// ProcessInput returns current input routing for pid. A missing Core Audio
// process object is represented by Found=false; it is not inferred from a
// failed property read.
func (i *Inspector) ProcessInput(pid int32) (ProcessInput, error) {
	if pid <= 0 {
		return ProcessInput{}, ErrInvalidPID
	}
	return i.processInputForPID(pid)
}

// InputsForBundle returns a snapshot for every Core Audio process object with
// an exact bundle ID match. An empty slice means no matching process object is
// currently connected to Core Audio. It does not launch or enumerate apps.
func (i *Inspector) InputsForBundle(bundleID string) ([]ProcessInput, error) {
	if bundleID == "" {
		return nil, fmt.Errorf("%w: empty bundle ID", ErrMalformedData)
	}
	data, err := i.readProperty(systemObjectID, propertyAddress{selector: selectorProcessList, scope: scopeGlobal}, nil)
	if err != nil {
		return nil, fmt.Errorf("audioroute: read process object list: %w", err)
	}
	ids, err := objectIDs(data, "process object list")
	if err != nil {
		return nil, err
	}
	var matches []ProcessInput
	for _, objectID := range ids {
		candidate, err := i.api.ProcessBundleID(objectID)
		if err != nil {
			return nil, fmt.Errorf("audioroute: read bundle ID for process object %d: %w", objectID, err)
		}
		if candidate != bundleID {
			continue
		}
		pidData, err := i.readProperty(objectID, propertyAddress{selector: selectorProcessPID, scope: scopeGlobal}, nil)
		if err != nil {
			return nil, fmt.Errorf("audioroute: read PID for process object %d: %w", objectID, err)
		}
		pidValue, err := scalar(pidData, "process PID")
		if err != nil || pidValue == 0 || pidValue > uint32(^uint32(0)>>1) {
			return nil, fmt.Errorf("%w: invalid process PID", ErrMalformedData)
		}
		snapshot, err := i.processInputForObject(objectID, int32(pidValue))
		if err != nil {
			return nil, err
		}
		matches = append(matches, snapshot)
	}
	return matches, nil
}

func (i *Inspector) processInputForPID(pid int32) (ProcessInput, error) {
	qualifier := make([]byte, 4)
	binary.LittleEndian.PutUint32(qualifier, uint32(pid))
	data, err := i.readProperty(systemObjectID, propertyAddress{selector: selectorTranslatePID, scope: scopeGlobal}, qualifier)
	if err != nil {
		return ProcessInput{}, fmt.Errorf("audioroute: translate PID: %w", err)
	}
	processID, err := scalar(data, "translated process object")
	if err != nil {
		return ProcessInput{}, err
	}
	if processID == unknownObject {
		return ProcessInput{PID: pid}, nil
	}
	return i.processInputForObject(processID, pid)
}

func (i *Inspector) processInputForObject(objectID uint32, pid int32) (ProcessInput, error) {
	runningData, err := i.readProperty(objectID, propertyAddress{selector: selectorProcessRunning, scope: scopeGlobal}, nil)
	if err != nil {
		return ProcessInput{}, fmt.Errorf("audioroute: read process input state: %w", err)
	}
	runningValue, err := scalar(runningData, "process input state")
	if err != nil || runningValue > 1 {
		return ProcessInput{}, fmt.Errorf("%w: invalid process input state", ErrMalformedData)
	}
	deviceData, err := i.readProperty(objectID, propertyAddress{selector: selectorProcessDevices, scope: scopeInput}, nil)
	if err != nil {
		return ProcessInput{}, fmt.Errorf("audioroute: read process input devices: %w", err)
	}
	deviceIDs, err := objectIDs(deviceData, "process input devices")
	if err != nil {
		return ProcessInput{}, err
	}
	uids := make([]string, 0, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		uid, err := i.api.DeviceUID(deviceID)
		if err != nil {
			return ProcessInput{}, fmt.Errorf("audioroute: read UID for process input device %d: %w", deviceID, err)
		}
		if uid == "" {
			return ProcessInput{}, fmt.Errorf("%w: empty process input device UID", ErrMalformedData)
		}
		uids = append(uids, uid)
	}
	return ProcessInput{Found: true, PID: pid, RunningInput: runningValue == 1, InputDeviceUIDs: uids}, nil
}

func (i *Inspector) channelCount(deviceID, scope uint32) (uint32, error) {
	address := propertyAddress{selector: selectorStreamConfig, scope: scope}
	if !i.api.HasProperty(deviceID, address) {
		return 0, nil
	}
	data, err := i.readProperty(deviceID, address, nil)
	if err != nil {
		return 0, err
	}
	return parseAudioBufferListChannels(data)
}

func (i *Inspector) readProperty(objectID uint32, address propertyAddress, qualifier []byte) ([]byte, error) {
	size, err := i.api.PropertyDataSize(objectID, address, qualifier)
	if err != nil {
		return nil, err
	}
	if size > maxPropertyBytes {
		return nil, fmt.Errorf("%w: property size %d exceeds limit", ErrMalformedData, size)
	}
	if size == 0 {
		return []byte{}, nil
	}
	data, err := i.api.PropertyData(objectID, address, qualifier, size)
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) > uint64(size) {
		return nil, fmt.Errorf("%w: returned %d bytes for %d-byte property", ErrMalformedData, len(data), size)
	}
	return data, nil
}

func scalar(data []byte, name string) (uint32, error) {
	if len(data) != 4 {
		return 0, fmt.Errorf("%w: %s has %d bytes, want 4", ErrMalformedData, name, len(data))
	}
	return binary.LittleEndian.Uint32(data), nil
}

func objectIDs(data []byte, name string) ([]uint32, error) {
	if len(data)%4 != 0 || len(data)/4 > maxDeviceObjects {
		return nil, fmt.Errorf("%w: invalid %s byte length %d", ErrMalformedData, name, len(data))
	}
	ids := make([]uint32, len(data)/4)
	for index := range ids {
		ids[index] = binary.LittleEndian.Uint32(data[index*4 : index*4+4])
	}
	return ids, nil
}

// AudioBufferList starts with UInt32 mNumberBuffers. On 64-bit macOS the
// following AudioBuffer array is 8-byte aligned; each AudioBuffer is 16 bytes.
// We inspect only mNumberChannels and never follow mData.
func parseAudioBufferListChannels(data []byte) (uint32, error) {
	const (
		bufferListArrayOffset = 8
		audioBufferSize       = 16
		maxBuffers            = maxPropertyBytes / audioBufferSize
	)
	if len(data) < 4 {
		return 0, fmt.Errorf("%w: truncated AudioBufferList header", ErrMalformedData)
	}
	if len(data) < bufferListArrayOffset {
		return 0, fmt.Errorf("%w: truncated AudioBufferList alignment", ErrMalformedData)
	}
	count := binary.LittleEndian.Uint32(data[:4])
	if count > maxBuffers {
		return 0, fmt.Errorf("%w: AudioBufferList count %d exceeds limit", ErrMalformedData, count)
	}
	if count == 0 {
		return 0, nil
	}
	need := uint64(bufferListArrayOffset) + uint64(count)*audioBufferSize
	if need > uint64(len(data)) {
		return 0, fmt.Errorf("%w: truncated AudioBufferList buffers", ErrMalformedData)
	}
	var channels uint64
	for index := uint32(0); index < count; index++ {
		offset := bufferListArrayOffset + int(index)*audioBufferSize
		channels += uint64(binary.LittleEndian.Uint32(data[offset : offset+4]))
		if channels > uint64(^uint32(0)) {
			return 0, fmt.Errorf("%w: channel count overflow", ErrMalformedData)
		}
	}
	return uint32(channels), nil
}
