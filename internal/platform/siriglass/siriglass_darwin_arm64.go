//go:build darwin && arm64

package siriglass

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
)

const (
	nativeLibraryName = "libSiriGlassBridge.dylib"
	nativeABIVersion  = 1
	// Captured ChatInputView.listeningOrbSize, separate from the host slot and
	// the native renderer's 1.04 phase expansion.
	glassDiameter = 100.0
	attachWidth   = 160.0
	attachHeight  = 160.0
	initialDB     = -60.0
	// The host meter already floors source levels at -60 dBFS. The legacy
	// renderer saturates at -20 dB, so its useful display range ends there.
	sourceMeterFloorDBFS   float32 = -60.0
	sourceMeterCeilingDBFS float32 = -20.0
	nativeDisplayFloorDB   float32 = -40.0
	nativeDisplayCeilingDB float32 = -20.0
)

type glassParameters struct {
	SchemaVersion           int     `json:"schemaVersion"`
	Revision                uint64  `json:"revision"`
	Kind                    int     `json:"kind"`
	Personality             int     `json:"personality"`
	Variant                 string  `json:"variant"`
	AudioPowerDB            float32 `json:"audioPowerDB"`
	Diameter                float64 `json:"diameter"`
	Visible                 bool    `json:"visible"`
	Content                 string  `json:"content"`
	Layers                  int     `json:"layers"`
	ReferencePhaseAnimation bool    `json:"referencePhaseAnimation"`
}

func rendererParameters(revision uint64, visible bool, db float32) glassParameters {
	displayDB := float32(initialDB)
	if visible {
		displayDB = nativeDisplayDBFromSourceDBFS(db)
	}
	return glassParameters{
		SchemaVersion:           1,
		Revision:                revision,
		Kind:                    3,
		Personality:             1,
		Variant:                 "listening",
		AudioPowerDB:            displayDB,
		Diameter:                glassDiameter,
		Visible:                 visible,
		Content:                 "clear",
		Layers:                  3,
		ReferencePhaseAnimation: true,
	}
}

// nativeDisplayDBFromSourceDBFS maps the host's visible meter range into the
// legacy scalar renderer's responsive range. It affects only presentation;
// Runtime.InputLevel and the PCM sent to the voice tool keep their source dBFS.
func nativeDisplayDBFromSourceDBFS(sourceDBFS float32) float32 {
	if sourceDBFS < sourceMeterFloorDBFS {
		sourceDBFS = sourceMeterFloorDBFS
	} else if sourceDBFS > sourceMeterCeilingDBFS {
		sourceDBFS = sourceMeterCeilingDBFS
	}

	sourceRangeDB := sourceMeterCeilingDBFS - sourceMeterFloorDBFS
	nativeRangeDB := nativeDisplayCeilingDB - nativeDisplayFloorDB
	position := (sourceDBFS - sourceMeterFloorDBFS) / sourceRangeDB
	return nativeDisplayFloorDB + position*nativeRangeDB
}

type nativeAPI struct {
	library   uintptr // Deliberately retained until process exit.
	version   func() uint32
	create    func(unsafe.Pointer, int32) uint64
	attach    func(uint64, uintptr, float64, float64, float64, float64, int32) int32
	update    func(uint64, unsafe.Pointer, int32) int32
	destroy   func(uint64) int32
	lastError func(unsafe.Pointer, int32) int32
}

var (
	systemCheckOnce sync.Once
	systemCheckErr  error
	nativeOnce      sync.Once
	native          *nativeAPI
	nativeErr       error
	loadedLibrary   uintptr // Retains a successfully opened image even if symbol binding fails.
)

// Renderer owns one attached native SwiftUI view. Calls are serialized on
// MyGo's main thread. If Open returns both a renderer and an error, attach
// failed and its first destroy attempt also failed; the caller must retain the
// renderer and retry Close.
type Renderer struct {
	api      *nativeAPI
	handle   uint64
	revision uint64
	visible  bool
	db       float32
	closing  bool
	closed   bool
}

// Open checks the exact supported host before loading any native image, then
// creates and attaches the original three-layer glass renderer.
func Open(window uintptr) (*Renderer, error) {
	if window == 0 {
		return nil, errors.New("siriglass: invalid MyGo window handle")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nil, ErrUnsupported
	}
	if err := checkSupportedSystem(); err != nil {
		return nil, err
	}

	var ran bool
	var renderer *Renderer
	var resultErr error
	mygo.RunOnMain(func() {
		ran = true
		api, err := loadNativeAPI()
		if err != nil {
			resultErr = err
			return
		}

		parameters := rendererParameters(1, false, initialDB)
		data, err := json.Marshal(parameters)
		if err != nil {
			resultErr = fmt.Errorf("siriglass: encode initial parameters: %w", err)
			return
		}
		handle := api.create(unsafe.Pointer(&data[0]), int32(len(data)))
		runtime.KeepAlive(data)
		if handle == 0 {
			resultErr = fmt.Errorf("siriglass: create renderer: %s", api.errorText())
			return
		}

		candidate := &Renderer{api: api, handle: handle, revision: 1, db: initialDB}
		if api.attach(handle, window, 0, 0, attachWidth, attachHeight, 0) != 1 {
			attachErr := fmt.Errorf("siriglass: attach renderer: %s", api.errorText())
			if api.destroy(handle) == 1 {
				candidate.handle = 0
				candidate.closed = true
				resultErr = attachErr
				return
			}
			candidate.closing = true
			resultErr = errors.Join(attachErr, fmt.Errorf("siriglass: destroy after attach failure: %s", api.errorText()))
			renderer = candidate
			return
		}
		renderer = candidate
	})
	if !ran {
		return nil, errors.New("siriglass: MyGo main loop is unavailable")
	}
	return renderer, resultErr
}

// Update changes visibility and the legacy scalar dB input. Hidden frames are
// canonicalized to the reference floor so meter changes cannot refresh a
// hidden renderer. Identical native inputs are deduplicated.
func (r *Renderer) Update(visible bool, db float32) error {
	if r == nil {
		return errors.New("siriglass: nil renderer")
	}
	if math.IsNaN(float64(db)) || math.IsInf(float64(db), 0) || db < -160 || db > 0 {
		return errors.New("siriglass: audioPowerDB must be finite and between -160 and 0 dB")
	}
	if !visible {
		db = initialDB
	}

	var ran bool
	var resultErr error
	mygo.RunOnMain(func() {
		ran = true
		if r.closed || r.closing || r.handle == 0 || r.api == nil {
			resultErr = errors.New("siriglass: renderer is closed or awaiting close retry")
			return
		}
		if r.visible == visible && r.db == db {
			return
		}
		if r.revision == ^uint64(0) {
			resultErr = errors.New("siriglass: renderer revision exhausted")
			return
		}
		nextRevision := r.revision + 1
		parameters := rendererParameters(nextRevision, visible, db)
		data, err := json.Marshal(parameters)
		if err != nil {
			resultErr = fmt.Errorf("siriglass: encode update parameters: %w", err)
			return
		}
		if r.api.update(r.handle, unsafe.Pointer(&data[0]), int32(len(data))) != 1 {
			runtime.KeepAlive(data)
			resultErr = fmt.Errorf("siriglass: update renderer: %s", r.api.errorText())
			return
		}
		runtime.KeepAlive(data)
		r.revision, r.visible, r.db = nextRevision, visible, db
	})
	if !ran {
		return errors.New("siriglass: MyGo main loop is unavailable")
	}
	return resultErr
}

// Close destroys the attached view. A native failure leaves its handle intact
// so the owner can retry; a successful close is idempotent.
func (r *Renderer) Close() error {
	if r == nil {
		return nil
	}
	var ran bool
	var resultErr error
	mygo.RunOnMain(func() {
		ran = true
		if r.closed || r.handle == 0 {
			r.closed = true
			return
		}
		if r.api == nil {
			resultErr = errors.New("siriglass: renderer API is unavailable; native handle retained")
			r.closing = true
			return
		}
		r.closing = true
		if r.api.destroy(r.handle) != 1 {
			resultErr = fmt.Errorf("siriglass: destroy renderer: %s", r.api.errorText())
			return
		}
		r.handle = 0
		r.closed = true
	})
	if !ran {
		return errors.New("siriglass: MyGo main loop is unavailable; native handle retained")
	}
	return resultErr
}

func checkSupportedSystem() error {
	systemCheckOnce.Do(func() {
		productVersion, err := syscall.Sysctl("kern.osproductversion")
		if err != nil {
			systemCheckErr = fmt.Errorf("siriglass: read macOS product version: %w", err)
			return
		}
		buildVersion, err := syscall.Sysctl("kern.osversion")
		if err != nil {
			systemCheckErr = fmt.Errorf("siriglass: read macOS build version: %w", err)
			return
		}
		productVersion = strings.TrimSpace(productVersion)
		buildVersion = strings.TrimSpace(buildVersion)
		if productVersion != "27.0.1" || buildVersion != "26A434" {
			systemCheckErr = fmt.Errorf("siriglass: private renderer requires macOS 27.0.1 build 26A434 (running %s build %s)", productVersion, buildVersion)
		}
	})
	return systemCheckErr
}

func loadNativeAPI() (*nativeAPI, error) {
	nativeOnce.Do(func() {
		resourceDir, err := mygo.App.Path(mygo.PathResources)
		if err != nil {
			nativeErr = fmt.Errorf("siriglass: locate MyGo resources: %w", err)
			return
		}
		libraryPath := filepath.Join(resourceDir, nativeLibraryName)
		library, err := purego.Dlopen(libraryPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeErr = fmt.Errorf("siriglass: load %s: %w", libraryPath, err)
			return
		}
		loadedLibrary = library
		api := &nativeAPI{library: library}
		bindings := []struct {
			name   string
			target any
		}{
			{"mimac_siri_glass_abi_version", &api.version},
			{"mimac_siri_glass_create", &api.create},
			{"mimac_siri_glass_attach_rect_with_clip", &api.attach},
			{"mimac_siri_glass_update", &api.update},
			{"mimac_siri_glass_destroy", &api.destroy},
			{"mimac_siri_glass_last_error", &api.lastError},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(library, binding.name)
			if err != nil {
				nativeErr = fmt.Errorf("siriglass: resolve %s: %w", binding.name, err)
				return
			}
			purego.RegisterFunc(binding.target, address)
		}
		if got := api.version(); got != nativeABIVersion {
			nativeErr = fmt.Errorf("siriglass: unsupported native ABI version %d", got)
			return
		}
		native = api
	})
	if nativeErr != nil {
		return nil, nativeErr
	}
	if native == nil || loadedLibrary == 0 {
		return nil, errors.New("siriglass: native library is unavailable")
	}
	return native, nil
}

func (api *nativeAPI) errorText() string {
	buffer := make([]byte, 2048)
	n := api.lastError(unsafe.Pointer(&buffer[0]), int32(len(buffer)))
	if n <= 0 || int(n) >= len(buffer) {
		return "native call refused without a bounded error message"
	}
	return string(buffer[:n])
}
