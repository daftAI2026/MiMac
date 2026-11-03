//go:build darwin && arm64

package siriglass

import (
	"encoding/json"
	"github.com/ebitengine/purego"
	"os"
	"testing"
	"unsafe"
)

var benchmarkFrameBytes []byte

func BenchmarkRendererJSONFrame(b *testing.B) {
	meters := Meters{Count: 6, StopFrequenciesHz: [MaxMeterBands]float32{200, 500, 1200, 2400, 4800, 8000}}
	for i := range meters.Amplitudes {
		meters.Amplitudes[i] = float32(i) / 100
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		data, err := json.Marshal(rendererMeterParameters(uint64(i+1), true, -30, meters))
		if err != nil {
			b.Fatal(err)
		}
		benchmarkFrameBytes = data
	}
}

var benchmarkFrameRevision uint64

func BenchmarkRendererFlatFrame(b *testing.B) {
	r := &Renderer{handle: 1, api: &nativeAPI{updateFrame: func(_ uint64, revision uint64, _ int32, _ float32, _, _ unsafe.Pointer, _ int32) int32 {
		benchmarkFrameRevision = revision
		return 1
	}}}
	meters := Meters{Count: 6, StopFrequenciesHz: [MaxMeterBands]float32{200, 500, 1200, 2400, 4800, 8000}}
	for i := range meters.Amplitudes {
		meters.Amplitudes[i] = float32(i) / 100
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !r.submitFrame(uint64(i+1), true, -60, meters) {
			b.Fatal("frame refused")
		}
	}
}

func TestFlatFrameTransfersOnlyLiveBands(t *testing.T) {
	meters := Meters{Count: 2, Amplitudes: [MaxMeterBands]float32{1.25, 0.125}, StopFrequenciesHz: [MaxMeterBands]float32{200, 8000}}
	called := 0
	r := &Renderer{handle: 17}
	r.api = &nativeAPI{updateFrame: func(handle, revision uint64, visible int32, db float32, a, f unsafe.Pointer, count int32) int32 {
		called++
		if handle != 17 || revision != 19 || visible != 1 || db != -60 || count != 2 {
			t.Fatal("scalar frame fields changed")
		}
		if got := unsafe.Slice((*float32)(a), int(count)); got[0] != 1.25 || got[1] != 0.125 {
			t.Fatal("amplitudes changed")
		}
		if got := unsafe.Slice((*float32)(f), int(count)); got[0] != 200 || got[1] != 8000 {
			t.Fatal("frequencies changed")
		}
		return 0
	}}
	if r.submitFrame(19, true, -60, meters) || called != 1 {
		t.Fatal("native refusal lost")
	}
	r.api.updateFrame = func(_ uint64, _ uint64, visible int32, _ float32, a, f unsafe.Pointer, count int32) int32 {
		if visible != 0 || count != 0 || a != nil || f != nil {
			t.Fatal("empty frame retained buffers")
		}
		return 1
	}
	if !r.submitFrame(20, false, -60, Meters{}) {
		t.Fatal("empty frame refused")
	}
}

func TestFlatFramePreservesLegacyDisplayRange(t *testing.T) {
	for _, test := range []struct{ source, want float32 }{{-60, -40}, {-30, -25}, {-20, -20}} {
		r := &Renderer{handle: 1, api: &nativeAPI{updateFrame: func(_ uint64, _ uint64, visible int32, db float32, a, f unsafe.Pointer, count int32) int32 {
			if visible != 1 || db != test.want || count != 0 || a != nil || f != nil {
				t.Fatalf("source %g: got display=%g count=%d, want %g scalar", test.source, db, count, test.want)
			}
			return 1
		}}}
		if !r.submitFrame(1, true, test.source, Meters{}) {
			t.Fatal("frame refused")
		}
	}
}

// This opt-in benchmark includes purego's real call trampoline, but excludes
// Swift material composition and rendering. Build testdata/frame_ffi_fixture.c.
func BenchmarkRendererRealFFI(b *testing.B) {
	path := os.Getenv("MIMAC_FRAME_FFI_LIBRARY")
	if path == "" {
		b.Skip("set MIMAC_FRAME_FFI_LIBRARY to the C fixture library")
	}
	library, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		b.Fatal(err)
	}
	defer purego.Dlclose(library)
	var old func(uint64, unsafe.Pointer, int32) int32
	purego.RegisterLibFunc(&old, library, "frame_fixture_json")
	r := &Renderer{handle: 1, api: &nativeAPI{}}
	purego.RegisterLibFunc(&r.api.updateFrame, library, "frame_fixture_typed")
	meters := Meters{Count: 6, StopFrequenciesHz: [MaxMeterBands]float32{200, 500, 1200, 2400, 4800, 8000}}
	b.Run("JSON", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			data, err := json.Marshal(rendererMeterParameters(uint64(i+1), true, -60, meters))
			if err != nil {
				b.Fatal(err)
			}
			if old(1, unsafe.Pointer(&data[0]), int32(len(data))) != 1 {
				b.Fatal("old call refused")
			}
		}
	})
	b.Run("Typed", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if !r.submitFrame(uint64(i+1), true, -60, meters) {
				b.Fatal("typed call refused")
			}
		}
	})
}
