package dotscope

import (
	"errors"
	"fmt"
	"unsafe"
)

// The ABI uses fixed-width fields and caller-owned memory. No Go memory is
// retained after a native call. A model is owned by one UI thread.
type nativeFrame struct {
	Pose          Pose
	Columns, Rows uint32
	Pitch         float32
	Dots          [MaxDots][2]float32
}

type nativeAPI struct {
	version                    func() uint32
	create                     func() uintptr
	destroy                    func(uintptr)
	start                      func(uintptr, float64, uint8)
	meter                      func(uintptr, float32)
	submit, done, fail, cancel func(uintptr, uint64, float64)
	step                       func(uintptr, float64, float32, uint8, *nativeFrame, uintptr) int32
}

type Model struct {
	api    nativeAPI
	handle uintptr
	unload func()
	frame  nativeFrame
}

func Open(path string) (*Model, error) {
	m := &Model{}
	unload, err := bindNative(path, &m.api)
	if err != nil {
		return nil, err
	}
	if v := m.api.version(); v != 1 {
		unload()
		return nil, fmt.Errorf("voice dots: unsupported native ABI %d", v)
	}
	m.handle = m.api.create()
	if m.handle == 0 {
		unload()
		return nil, errors.New("voice dots: native model creation failed")
	}
	m.unload = unload
	return m, nil
}

func (m *Model) Close() {
	if m.handle != 0 {
		m.api.destroy(m.handle)
		m.handle = 0
		m.unload()
	}
}

func flag(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func (m *Model) Start(seconds float64, reduce bool) { m.api.start(m.handle, seconds, flag(reduce)) }
func (m *Model) Meter(rms float32)                  { m.api.meter(m.handle, rms) }
func (m *Model) Submit(id uint64, seconds float64)  { m.api.submit(m.handle, id, seconds) }
func (m *Model) Done(id uint64, seconds float64)    { m.api.done(m.handle, id, seconds) }
func (m *Model) Fail(id uint64, seconds float64)    { m.api.fail(m.handle, id, seconds) }
func (m *Model) Cancel(id uint64, seconds float64)  { m.api.cancel(m.handle, id, seconds) }

// Step returns whether the panel should request another frame.
func (m *Model) Step(seconds float64, dt float32, reduce bool) bool {
	result := m.api.step(m.handle, seconds, dt, flag(reduce), &m.frame, unsafe.Sizeof(m.frame))
	return result == 1
}

func (m *Model) Snapshot() Snapshot {
	f := &m.frame
	count := uint64(f.Columns) * uint64(f.Rows)
	if count > MaxDots {
		return Snapshot{}
	}
	return Snapshot{Pose: f.Pose, Columns: int(f.Columns), Rows: int(f.Rows), Pitch: f.Pitch, Dots: f.Dots[:count]}
}
