//go:build darwin

package processidentity

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	procPIDTBSDInfo   = 3
	procBSDInfoSize   = 136
	procBSDStartSecAt = 120
	procBSDStartUSAt  = 128
	procPathMaxSize   = 4096
)

// procBSDInfo matches struct proc_bsdinfo in the active macOS SDK's
// sys/proc_info.h (MAXCOMLEN is 16). Keep the size and timestamp offsets
// compile-time checked against the ABI values verified from that header.
type procBSDInfo struct {
	flags, status, xstatus, pid, ppid  uint32
	uid, gid, ruid, rgid, svuid, svgid uint32
	reserved                           uint32
	comm                               [16]byte
	name                               [32]byte
	nfiles, pgid, pjobc                uint32
	ttyDevice, ttyProcessGroup         uint32
	nice                               int32
	startSeconds, startMicroseconds    uint64
}

var (
	_ [procBSDInfoSize - int(unsafe.Sizeof(procBSDInfo{}))]byte
	_ [int(unsafe.Sizeof(procBSDInfo{})) - procBSDInfoSize]byte
	_ [procBSDStartSecAt - int(unsafe.Offsetof(procBSDInfo{}.startSeconds))]byte
	_ [int(unsafe.Offsetof(procBSDInfo{}.startSeconds)) - procBSDStartSecAt]byte
	_ [procBSDStartUSAt - int(unsafe.Offsetof(procBSDInfo{}.startMicroseconds))]byte
	_ [int(unsafe.Offsetof(procBSDInfo{}.startMicroseconds)) - procBSDStartUSAt]byte
)

type processNativeAPI struct {
	procPIDInfo func(pid int32, flavor int32, arg uint64, buffer *procBSDInfo, bufferSize int32) int32
	procPIDPath func(pid int32, buffer *byte, bufferSize uint32) int32
}

var processNative struct {
	sync.Once
	api processNativeAPI
	err error
}

func nativeProcessReader() (processReader, error) {
	processNative.Do(loadProcessNativeAPI)
	if processNative.err != nil {
		return nil, fmt.Errorf("process identity: load libSystem process APIs: %w", processNative.err)
	}
	return readNativeSnapshot, nil
}

func loadProcessNativeAPI() {
	libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		processNative.err = err
		return
	}
	api := processNativeAPI{}
	for _, binding := range []struct {
		name string
		dst  any
	}{{"proc_pidinfo", &api.procPIDInfo}, {"proc_pidpath", &api.procPIDPath}} {
		address, lookupErr := purego.Dlsym(libSystem, binding.name)
		if lookupErr != nil {
			_ = purego.Dlclose(libSystem)
			processNative.err = lookupErr
			return
		}
		purego.RegisterFunc(binding.dst, address)
	}
	// The registered function pointers remain valid for the process lifetime.
	processNative.api = api
}

func readNativeSnapshot(pid int32) (snapshot, error) {
	before, err := readBSDInfo(pid)
	if err != nil {
		return snapshot{}, err
	}
	path, err := readExecutablePath(pid)
	if err != nil {
		return snapshot{}, err
	}
	after, err := readBSDInfo(pid)
	if err != nil {
		return snapshot{}, err
	}
	if before.startSeconds != after.startSeconds || before.startMicroseconds != after.startMicroseconds {
		return snapshot{}, errors.New("PID changed while reading process identity")
	}
	return snapshot{
		startedAt:  startTime{seconds: before.startSeconds, microseconds: before.startMicroseconds},
		executable: path,
	}, nil
}

func readBSDInfo(pid int32) (procBSDInfo, error) {
	var info procBSDInfo
	count := processNative.api.procPIDInfo(pid, procPIDTBSDInfo, 0, &info, procBSDInfoSize)
	if count != procBSDInfoSize {
		return procBSDInfo{}, fmt.Errorf("proc_pidinfo returned %d bytes", count)
	}
	if info.pid != uint32(pid) {
		return procBSDInfo{}, fmt.Errorf("proc_pidinfo returned PID %d for requested PID %d", info.pid, pid)
	}
	return info, nil
}

func readExecutablePath(pid int32) (string, error) {
	var buffer [procPathMaxSize]byte
	count := processNative.api.procPIDPath(pid, &buffer[0], uint32(len(buffer)))
	if count <= 0 || count > int32(len(buffer)) {
		return "", fmt.Errorf("proc_pidpath returned %d bytes", count)
	}
	end := bytes.IndexByte(buffer[:], 0)
	if end <= 0 {
		return "", errors.New("proc_pidpath returned an empty or unterminated path")
	}
	return string(buffer[:end]), nil
}
