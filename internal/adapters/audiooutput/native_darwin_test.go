//go:build darwin

package audiooutput

import (
	"reflect"
	"testing"
	"unsafe"
)

type fakeAudioQueueAPI struct {
	api                                    *audioQueueAPI
	queue                                  *darwinNativeQueue
	storage                                [bufferCount][]int16
	buffers                                [bufferCount]*audioQueueBuffer
	allocations, failAllocation, disposals int
	disposeStatus                          int32
	returnedRefs                           []uintptr
	format                                 audioStreamBasicDescription
	boundUID                               uintptr
	routeSame                              bool
	converter                              uint32
}

func newFakeAudioQueueAPI(t *testing.T) *fakeAudioQueueAPI {
	t.Helper()
	f := &fakeAudioQueueAPI{failAllocation: -1, routeSame: true}
	f.api = &audioQueueAPI{
		newOutput: func(format *audioStreamBasicDescription, cb, user, runloop, mode uintptr, flags uint32, out *uintptr) int32 {
			f.format = *format
			*out = 901
			return 0
		},
		allocateBuffer: func(q uintptr, size uint32, out **audioQueueBuffer) int32 {
			i := f.allocations
			f.allocations++
			if i == f.failAllocation {
				return -1
			}
			f.storage[i] = make([]int16, int(size)/2)
			f.buffers[i] = &audioQueueBuffer{audioDataBytesCapacity: size, audioData: unsafe.Pointer(&f.storage[i][0])}
			*out = f.buffers[i]
			return 0
		},
		enqueueBuffer: func(uintptr, *audioQueueBuffer, uint32, uintptr) int32 { return 0 },
		start:         func(uintptr, uintptr) int32 { return 0 },
		stop:          func(uintptr, bool) int32 { return 0 },
		dispose:       func(uintptr, bool) int32 { f.disposals++; return f.disposeStatus },
		setProperty: func(q uintptr, property uint32, value unsafe.Pointer, size uint32) int32 {
			if property != audioQueuePropertyCurrentDevice || size != 8 {
				t.Fatalf("set property=%x size=%d", property, size)
			}
			f.boundUID = *(*uintptr)(value)
			return 0
		},
		getProperty: func(q uintptr, property uint32, value unsafe.Pointer, size *uint32) int32 {
			switch property {
			case audioQueuePropertyCurrentDevice:
				*(*uintptr)(value) = 2
				*size = 8
			case audioQueuePropertyConverterError:
				*(*uint32)(value) = f.converter
				*size = 4
			case audioQueuePropertyIsRunning:
				*(*uint32)(value) = 0
				*size = 4
			default:
				t.Fatalf("unexpected property=%x", property)
			}
			return 0
		},
		cfStringCreate: func(uintptr, *byte, uint32) uintptr { return 1 },
		cfEqual: func(a, b uintptr) uint8 {
			if a != 2 || b != 1 {
				t.Fatalf("CFEqual=%d,%d", a, b)
			}
			if f.routeSame {
				return 1
			}
			return 0
		},
		cfRelease: func(ref uintptr) { f.returnedRefs = append(f.returnedRefs, ref) },
	}
	return f
}

func TestNativeFormatCopiesShortBuffersAndRetiresCallbackToken(t *testing.T) {
	f := newFakeAudioQueueAPI(t)
	var slots []int
	q, err := openNativeWithAPI(Config{DeviceUID: "fake", SampleRate: 8000}, func(slot int) { slots = append(slots, slot) }, f.api)
	if err != nil {
		t.Fatal(err)
	}
	d := q.(*darwinNativeQueue)
	defer d.Dispose()
	if f.boundUID != 1 || f.format.sampleRate != 8000 || f.format.channelsPerFrame != 1 || f.format.bitsPerChannel != 16 || f.format.formatFlags != 12 {
		t.Fatalf("format=%+v UID=%d", f.format, f.boundUID)
	}
	pcm := []int16{1, -200, 32767, -32768}
	if err = d.Enqueue(0, pcm); err != nil {
		t.Fatal(err)
	}
	pcm[0] = 999
	if !reflect.DeepEqual(f.storage[0][:4], []int16{1, -200, 32767, -32768}) || f.buffers[0].audioDataByteSize != 8 {
		t.Fatal("native copy or byte size")
	}
	oldToken, oldQueue := d.token, d.queue
	nativeAudioQueueBufferCallback(oldToken, oldQueue, f.buffers[0])
	nativeAudioQueueBufferCallback(oldToken, oldQueue, f.buffers[0])
	if !reflect.DeepEqual(slots, []int{0}) {
		t.Fatal(slots)
	}
	if err = d.Dispose(); err != nil {
		t.Fatal(err)
	}
	nativeAudioQueueBufferCallback(oldToken, oldQueue, f.buffers[0])
	if !reflect.DeepEqual(slots, []int{0}) {
		t.Fatal("stale callback escaped")
	}
	if (*nativeQueueRegistry.snapshot.Load())[oldToken] != nil {
		t.Fatal("registry retained disposed owner")
	}
	if f.returnedRefs[len(f.returnedRefs)-1] != 1 {
		t.Fatal("owned UID not released")
	}
}

func TestNativePartialAllocationCleanupAndRetry(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "released", true: "retained"}[fails], func(t *testing.T) {
			f := newFakeAudioQueueAPI(t)
			f.failAllocation = 2
			if fails {
				f.disposeStatus = -2
			}
			q, err := openNativeWithAPI(Config{DeviceUID: "fake", SampleRate: 16000}, func(int) {}, f.api)
			if err == nil || f.disposals != 1 {
				t.Fatalf("q=%v err=%v disposals=%d", q, err, f.disposals)
			}
			if !fails {
				if q != nil {
					t.Fatal("successfully cleaned partial queue retained")
				}
				return
			}
			if q == nil {
				t.Fatal("failed cleanup lost queue")
			}
			d := q.(*darwinNativeQueue)
			if (*nativeQueueRegistry.snapshot.Load())[d.token] != d {
				t.Fatal("failed Dispose retired callback owner")
			}
			f.disposeStatus = 0
			if err = q.Dispose(); err != nil {
				t.Fatal(err)
			}
			if (*nativeQueueRegistry.snapshot.Load())[d.token] != nil {
				t.Fatal("cleanup retry retained owner")
			}
		})
	}
}

func TestNativeRouteAndConverterErrors(t *testing.T) {
	f := newFakeAudioQueueAPI(t)
	q, err := openNativeWithAPI(Config{DeviceUID: "fake", SampleRate: 16000}, func(int) {}, f.api)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Dispose()
	f.routeSame = false
	if _, err = q.Check(); err == nil {
		t.Fatal("route change ignored")
	}
	f.routeSame = true
	f.converter = 1
	if _, err = q.Check(); err == nil {
		t.Fatal("converter error ignored")
	}
	for _, ref := range f.returnedRefs {
		if ref != 2 {
			t.Fatalf("wrong readback ownership: %v", f.returnedRefs)
		}
	}
}

func TestNativeAudioQueueABI(t *testing.T) {
	var b audioQueueBuffer
	if unsafe.Sizeof(audioStreamBasicDescription{}) != 40 || unsafe.Sizeof(b) != 56 || unsafe.Offsetof(b.audioData) != 8 || unsafe.Offsetof(b.audioDataByteSize) != 16 || unsafe.Offsetof(b.packetDescs) != 40 {
		t.Fatal("AudioQueue ABI mismatch")
	}
}
