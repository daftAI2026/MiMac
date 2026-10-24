//go:build darwin

package audiooutput

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	cfStringEncodingUTF8 uint32 = 0x08000100

	audioFormatLinearPCM             uint32 = 0x6c70636d // 'lpcm'
	audioFormatFlagIsSignedInteger   uint32 = 1 << 2
	audioFormatFlagIsPacked          uint32 = 1 << 3
	audioQueuePropertyIsRunning      uint32 = 0x6171726e // 'aqrn'
	audioQueuePropertyCurrentDevice  uint32 = 0x61716364 // 'aqcd'
	audioQueuePropertyConverterError uint32 = 0x71637665 // 'qcve'
)

type audioStreamBasicDescription struct {
	sampleRate       float64
	formatID         uint32
	formatFlags      uint32
	bytesPerPacket   uint32
	framesPerPacket  uint32
	bytesPerFrame    uint32
	channelsPerFrame uint32
	bitsPerChannel   uint32
	reserved         uint32
}

type audioQueueBuffer struct {
	audioDataBytesCapacity uint32
	audioData              unsafe.Pointer
	audioDataByteSize      uint32
	userData               uintptr
	packetDescCapacity     uint32
	packetDescs            uintptr
	packetDescCount        uint32
}

type audioQueueAPI struct {
	newOutput      func(*audioStreamBasicDescription, uintptr, uintptr, uintptr, uintptr, uint32, *uintptr) int32
	allocateBuffer func(uintptr, uint32, **audioQueueBuffer) int32
	enqueueBuffer  func(uintptr, *audioQueueBuffer, uint32, uintptr) int32
	prime          func(uintptr, uint32, *uint32) int32
	start          func(uintptr, uintptr) int32
	pause          func(uintptr) int32
	stop           func(uintptr, bool) int32
	dispose        func(uintptr, bool) int32
	setProperty    func(uintptr, uint32, unsafe.Pointer, uint32) int32
	getProperty    func(uintptr, uint32, unsafe.Pointer, *uint32) int32
	cfStringCreate func(uintptr, *byte, uint32) uintptr
	cfEqual        func(uintptr, uintptr) uint8
	cfRelease      func(uintptr)
	callback       uintptr
}

var nativeAudioQueueRuntime struct {
	once sync.Once
	api  *audioQueueAPI
	err  error
}

func loadAudioQueueAPI() (*audioQueueAPI, error) {
	nativeAudioQueueRuntime.once.Do(func() {
		audioToolbox, err := purego.Dlopen("/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeAudioQueueRuntime.err = fmt.Errorf("audiooutput: load AudioToolbox: %w", err)
			return
		}
		coreFoundation, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeAudioQueueRuntime.err = fmt.Errorf("audiooutput: load CoreFoundation: %w", err)
			return
		}

		a := &audioQueueAPI{}
		bindings := []struct {
			library uintptr
			dst     any
			symbol  string
		}{
			{audioToolbox, &a.newOutput, "AudioQueueNewOutput"},
			{audioToolbox, &a.allocateBuffer, "AudioQueueAllocateBuffer"},
			{audioToolbox, &a.enqueueBuffer, "AudioQueueEnqueueBuffer"},
			{audioToolbox, &a.prime, "AudioQueuePrime"},
			{audioToolbox, &a.start, "AudioQueueStart"},
			{audioToolbox, &a.pause, "AudioQueuePause"},
			{audioToolbox, &a.stop, "AudioQueueStop"},
			{audioToolbox, &a.dispose, "AudioQueueDispose"},
			{audioToolbox, &a.setProperty, "AudioQueueSetProperty"},
			{audioToolbox, &a.getProperty, "AudioQueueGetProperty"},
			{coreFoundation, &a.cfStringCreate, "CFStringCreateWithCString"},
			{coreFoundation, &a.cfEqual, "CFEqual"},
			{coreFoundation, &a.cfRelease, "CFRelease"},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(binding.library, binding.symbol)
			if err != nil {
				nativeAudioQueueRuntime.err = fmt.Errorf("audiooutput: resolve %s: %w", binding.symbol, err)
				return
			}
			purego.RegisterFunc(binding.dst, address)
		}
		a.callback = purego.NewCallback(nativeAudioQueueBufferCallback)
		if a.callback == 0 {
			nativeAudioQueueRuntime.err = errors.New("audiooutput: create AudioQueue callback failed")
			return
		}
		nativeAudioQueueRuntime.api = a
	})
	return nativeAudioQueueRuntime.api, nativeAudioQueueRuntime.err
}

type queueRegistrySnapshot map[uintptr]*darwinNativeQueue

var nativeQueueRegistry = struct {
	mu       sync.Mutex
	snapshot atomic.Pointer[queueRegistrySnapshot]
	next     uintptr
}{next: 1}

func registerNativeQueue(queue *darwinNativeQueue) (uintptr, error) {
	nativeQueueRegistry.mu.Lock()
	defer nativeQueueRegistry.mu.Unlock()
	if nativeQueueRegistry.next == 0 {
		return 0, errors.New("audiooutput: callback token space exhausted")
	}
	token := nativeQueueRegistry.next
	if token == ^uintptr(0) {
		nativeQueueRegistry.next = 0
	} else {
		nativeQueueRegistry.next++
	}
	updated := make(queueRegistrySnapshot)
	if current := nativeQueueRegistry.snapshot.Load(); current != nil {
		for key, value := range *current {
			updated[key] = value
		}
	}
	updated[token] = queue
	nativeQueueRegistry.snapshot.Store(&updated)
	return token, nil
}

func unregisterNativeQueue(token uintptr) {
	if token == 0 {
		return
	}
	nativeQueueRegistry.mu.Lock()
	defer nativeQueueRegistry.mu.Unlock()
	updated := make(queueRegistrySnapshot)
	if current := nativeQueueRegistry.snapshot.Load(); current != nil {
		for key, value := range *current {
			if key != token {
				updated[key] = value
			}
		}
	}
	nativeQueueRegistry.snapshot.Store(&updated)
}

func nativeAudioQueueBufferCallback(user uintptr, queue uintptr, buffer *audioQueueBuffer) {
	if user == 0 || queue == 0 || buffer == nil {
		return
	}
	snapshot := nativeQueueRegistry.snapshot.Load()
	if snapshot == nil {
		return
	}
	d := (*snapshot)[user]
	if d == nil || d.queue != queue || d.disposing.Load() {
		return
	}
	slot := -1
	for i := 0; i < bufferCount; i++ {
		if d.buffers[i] == buffer {
			slot = i
			break
		}
	}
	if slot >= 0 && d.queued[slot].CompareAndSwap(true, false) && d.returnBuffer != nil {
		d.returnBuffer(slot)
	}
}

type darwinNativeQueue struct {
	mu sync.Mutex

	api             *audioQueueAPI
	queue           uintptr
	token           uintptr
	uidRef          uintptr
	framesPerBuffer int
	buffers         [bufferCount]*audioQueueBuffer
	queued          [bufferCount]atomic.Bool
	returnBuffer    func(slot int)
	primeAttempted  bool
	startAttempted  bool
	stopRequested   bool
	disposing       atomic.Bool
	disposed        atomic.Bool
}

func openNativeQueue(config Config, returnBuffer func(slot int)) (nativeQueue, error) {
	if config.DeviceUID == "" || strings.IndexByte(config.DeviceUID, 0) >= 0 {
		return nil, errors.New("audiooutput: device UID is empty or contains NUL")
	}
	if config.SampleRate != 8000 && config.SampleRate != 16000 {
		return nil, fmt.Errorf("audiooutput: unsupported sample rate %d", config.SampleRate)
	}
	if returnBuffer == nil {
		return nil, errors.New("audiooutput: nil buffer-return callback")
	}
	api, err := loadAudioQueueAPI()
	if err != nil {
		return nil, err
	}
	return openNativeWithAPI(config, returnBuffer, api)
}

// openNativeWithAPI keeps the construction path testable with a fake native API.
// It is called only after the public config and callback have been validated.
func openNativeWithAPI(config Config, returnBuffer func(slot int), api *audioQueueAPI) (nativeQueue, error) {
	framesPerBuffer := config.SampleRate / 50
	queue := &darwinNativeQueue{
		api:             api,
		framesPerBuffer: framesPerBuffer,
		returnBuffer:    returnBuffer,
	}
	token, err := registerNativeQueue(queue)
	if err != nil {
		return nil, err
	}
	queue.token = token

	uidBytes := append([]byte(config.DeviceUID), 0)
	queue.uidRef = api.cfStringCreate(0, &uidBytes[0], cfStringEncodingUTF8)
	runtime.KeepAlive(uidBytes)
	if queue.uidRef == 0 {
		unregisterNativeQueue(queue.token)
		return nil, errors.New("audiooutput: create device UID CFString failed")
	}

	format := audioStreamBasicDescription{
		sampleRate:       float64(config.SampleRate),
		formatID:         audioFormatLinearPCM,
		formatFlags:      audioFormatFlagIsSignedInteger | audioFormatFlagIsPacked,
		bytesPerPacket:   2,
		framesPerPacket:  1,
		bytesPerFrame:    2,
		channelsPerFrame: 1,
		bitsPerChannel:   16,
	}
	status := api.newOutput(&format, api.callback, token, 0, 0, 0, &queue.queue)
	runtime.KeepAlive(format)
	if status != 0 || queue.queue == 0 {
		cause := fmt.Errorf("audiooutput: AudioQueueNewOutput status=%d queue=%#x", status, queue.queue)
		return failNativeQueueOpen(queue, cause)
	}

	uid := queue.uidRef
	status = api.setProperty(queue.queue, audioQueuePropertyCurrentDevice, unsafe.Pointer(&uid), uint32(unsafe.Sizeof(uid)))
	runtime.KeepAlive(queue)
	if status != 0 {
		return failNativeQueueOpen(queue, fmt.Errorf("audiooutput: AudioQueueSetProperty(CurrentDevice) status=%d", status))
	}
	if err := queue.verifyUIDLocked(); err != nil {
		return failNativeQueueOpen(queue, err)
	}

	bufferBytes := uint32(framesPerBuffer * 2)
	for slot := 0; slot < bufferCount; slot++ {
		status = api.allocateBuffer(queue.queue, bufferBytes, &queue.buffers[slot])
		if status != 0 || queue.buffers[slot] == nil || queue.buffers[slot].audioData == nil ||
			queue.buffers[slot].audioDataBytesCapacity < bufferBytes {
			return failNativeQueueOpen(queue, fmt.Errorf("audiooutput: AudioQueueAllocateBuffer slot=%d status=%d", slot, status))
		}
	}
	if running, err := queue.checkLocked(); err != nil {
		return failNativeQueueOpen(queue, err)
	} else if running {
		return failNativeQueueOpen(queue, errors.New("audiooutput: new queue unexpectedly running"))
	}
	return queue, nil
}

func failNativeQueueOpen(queue *darwinNativeQueue, cause error) (nativeQueue, error) {
	if queue.queue == 0 {
		queue.retireWithoutNativeQueue()
		return nil, cause
	}
	queue.mu.Lock()
	err := queue.disposeLocked()
	queue.mu.Unlock()
	if err != nil {
		return queue, errors.Join(cause, fmt.Errorf("audiooutput: partial-open cleanup: %w", err))
	}
	return nil, cause
}

func (d *darwinNativeQueue) retireWithoutNativeQueue() {
	unregisterNativeQueue(d.token)
	d.token = 0
	if d.uidRef != 0 {
		d.api.cfRelease(d.uidRef)
		d.uidRef = 0
	}
	d.disposed.Store(true)
}

func (d *darwinNativeQueue) Enqueue(slot int, pcm []int16) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disposed.Load() || d.disposing.Load() || d.queue == 0 {
		return errors.New("audiooutput: queue is disposed")
	}
	if d.stopRequested {
		return errors.New("audiooutput: queue is stopping")
	}
	if slot < 0 || slot >= bufferCount {
		return fmt.Errorf("audiooutput: invalid buffer slot %d", slot)
	}
	if len(pcm) == 0 || len(pcm) > d.framesPerBuffer {
		return fmt.Errorf("audiooutput: slot %d accepts 1..%d frames, got %d", slot, d.framesPerBuffer, len(pcm))
	}
	buffer := d.buffers[slot]
	if buffer == nil || buffer.audioData == nil || buffer.audioDataBytesCapacity < uint32(len(pcm)*2) {
		return fmt.Errorf("audiooutput: slot %d has no suitable native buffer", slot)
	}
	if !d.queued[slot].CompareAndSwap(false, true) {
		return fmt.Errorf("audiooutput: slot %d is already enqueued", slot)
	}
	copy(unsafe.Slice((*int16)(buffer.audioData), len(pcm)), pcm)
	buffer.audioDataByteSize = uint32(len(pcm) * 2)
	status := d.api.enqueueBuffer(d.queue, buffer, 0, 0)
	runtime.KeepAlive(pcm)
	if status != 0 {
		d.queued[slot].Store(false)
		return fmt.Errorf("audiooutput: AudioQueueEnqueueBuffer slot=%d status=%d", slot, status)
	}
	return nil
}

func (d *darwinNativeQueue) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disposed.Load() || d.disposing.Load() || d.queue == 0 {
		return errors.New("audiooutput: queue is disposed")
	}
	if d.stopRequested {
		return errors.New("audiooutput: queue is stopping")
	}
	d.startAttempted = true
	status := d.api.start(d.queue, 0)
	if status != 0 {
		return fmt.Errorf("audiooutput: AudioQueueStart status=%d", status)
	}
	return nil
}

// Prime asks AudioQueue to decode all currently enqueued buffers before first
// start. This must happen on a fresh queue and is intentionally one-shot.
func (d *darwinNativeQueue) Prime() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disposed.Load() || d.disposing.Load() || d.queue == 0 {
		return errors.New("audiooutput: queue is disposed")
	}
	if d.stopRequested {
		return errors.New("audiooutput: queue is stopping")
	}
	if d.primeAttempted || d.startAttempted {
		return errors.New("audiooutput: AudioQueuePrime requires a fresh queue")
	}
	d.primeAttempted = true
	var preparedFrames uint32
	status := d.api.prime(d.queue, 0, &preparedFrames)
	runtime.KeepAlive(&preparedFrames)
	if status != 0 {
		return fmt.Errorf("audiooutput: AudioQueuePrime status=%d", status)
	}
	if preparedFrames == 0 {
		return errors.New("audiooutput: AudioQueuePrime prepared no frames")
	}
	return nil
}

// Pause preserves queued buffers and converter state so a later Start resumes
// the same AudioQueue. IsRunning is not used as an indication of PCM playout.
func (d *darwinNativeQueue) Pause() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disposed.Load() || d.disposing.Load() || d.queue == 0 {
		return errors.New("audiooutput: queue is disposed")
	}
	if !d.startAttempted {
		return errors.New("audiooutput: AudioQueuePause requires a started queue")
	}
	status := d.api.pause(d.queue)
	if status != 0 {
		return fmt.Errorf("audiooutput: AudioQueuePause status=%d", status)
	}
	return nil
}

func (d *darwinNativeQueue) Stop(immediate bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disposed.Load() || d.queue == 0 {
		return nil
	}
	status := d.api.stop(d.queue, immediate)
	if status != 0 {
		return fmt.Errorf("audiooutput: AudioQueueStop(immediate=%t) status=%d", immediate, status)
	}
	d.stopRequested = true
	return nil
}

func (d *darwinNativeQueue) Check() (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.checkLocked()
}

func (d *darwinNativeQueue) checkLocked() (bool, error) {
	if d.disposed.Load() || d.queue == 0 {
		return false, errors.New("audiooutput: queue is disposed")
	}
	if err := d.verifyUIDLocked(); err != nil {
		return false, err
	}
	var converterError uint32
	size := uint32(unsafe.Sizeof(converterError))
	status := d.api.getProperty(d.queue, audioQueuePropertyConverterError, unsafe.Pointer(&converterError), &size)
	if status != 0 {
		return false, fmt.Errorf("audiooutput: AudioQueueGetProperty(ConverterError) status=%d", status)
	}
	if converterError != 0 {
		return false, fmt.Errorf("audiooutput: converter error %d", int32(converterError))
	}
	var runningValue uint32
	size = uint32(unsafe.Sizeof(runningValue))
	status = d.api.getProperty(d.queue, audioQueuePropertyIsRunning, unsafe.Pointer(&runningValue), &size)
	if status != 0 {
		return false, fmt.Errorf("audiooutput: AudioQueueGetProperty(IsRunning) status=%d", status)
	}
	running := runningValue != 0
	return running, nil
}

func (d *darwinNativeQueue) verifyUIDLocked() error {
	var currentRef uintptr
	size := uint32(unsafe.Sizeof(currentRef))
	status := d.api.getProperty(d.queue, audioQueuePropertyCurrentDevice, unsafe.Pointer(&currentRef), &size)
	if status != 0 {
		return fmt.Errorf("audiooutput: AudioQueueGetProperty(CurrentDevice) status=%d", status)
	}
	if currentRef == 0 || size != uint32(unsafe.Sizeof(currentRef)) {
		if currentRef != 0 {
			d.api.cfRelease(currentRef)
		}
		return errors.New("audiooutput: AudioQueue returned an invalid CurrentDevice CFString")
	}
	// SDK AudioQueue.h:271 declares CurrentDevice as CFStringRef; AudioQueueGetProperty
	// is declared at lines 1437-1440. Apple documents returned CF objects as duplicated
	// (+1 owned), so release this readback here.
	defer d.api.cfRelease(currentRef)
	if d.api.cfEqual(currentRef, d.uidRef) == 0 {
		return errors.New("audiooutput: queue route changed")
	}
	return nil
}

func (d *darwinNativeQueue) Dispose() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disposed.Load() {
		return nil
	}
	return d.disposeLocked()
}

func (d *darwinNativeQueue) disposeLocked() error {
	if d.queue == 0 {
		d.retireWithoutNativeQueue()
		return nil
	}
	d.disposing.Store(true)
	status := d.api.dispose(d.queue, true)
	if status != 0 {
		d.disposing.Store(false)
		return fmt.Errorf("audiooutput: AudioQueueDispose(immediate=true) status=%d", status)
	}
	unregisterNativeQueue(d.token)
	d.token = 0
	if d.uidRef != 0 {
		d.api.cfRelease(d.uidRef)
		d.uidRef = 0
	}
	d.disposed.Store(true)
	d.stopRequested = true
	d.queue = 0
	return nil
}
