package audiometer

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestNewValidatesSampleRateAndCompleteStopList(t *testing.T) {
	for _, sampleRate := range []int{8000, 16000} {
		nyquist := float32(sampleRate / 2)
		stops := []float32{200, 1200, nyquist}
		a, err := New(sampleRate, stops)
		if err != nil {
			t.Fatalf("New(%d, %v): %v", sampleRate, stops, err)
		}
		stops[0] = 300
		got, ok := a.Write(make([]int16, WindowSize))
		if !ok || got.SampleRate != sampleRate || got.Samples != WindowSize || got.Sequence != 1 || got.Count != 3 {
			t.Fatalf("spectrum metadata = %+v, ok=%v", got, ok)
		}
		if got.StopFrequenciesHz[0] != 200 || got.StopFrequenciesHz[1] != 1200 || got.StopFrequenciesHz[2] != nyquist {
			t.Fatalf("constructor did not copy complete stops: %v", got.StopFrequenciesHz[:got.Count])
		}
	}

	if _, err := New(4000, []float32{2000}); !errors.Is(err, ErrUnsupportedSampleRate) {
		t.Fatalf("unsupported rate error = %v", err)
	}
	if _, err := New(44100, []float32{22050}); !errors.Is(err, ErrUnsupportedSampleRate) {
		t.Fatalf("unsupported rate error = %v", err)
	}

	tooMany := make([]float32, MaxBands+1)
	for i := range tooMany {
		tooMany[i] = float32(i + 1)
	}
	for _, test := range []struct {
		name  string
		stops []float32
		want  error
	}{
		{"empty list", nil, ErrMissingNyquistStop},
		{"does not reach Nyquist", []float32{100, 200}, ErrMissingNyquistStop},
		{"too many bands", tooMany, ErrTooManyBands},
		{"duplicate", []float32{100, 100, 4000}, ErrInvalidStopFrequency},
		{"descending", []float32{200, 100, 4000}, ErrInvalidStopFrequency},
		{"negative", []float32{-1, 4000}, ErrInvalidStopFrequency},
		{"above Nyquist", []float32{4000.5}, ErrInvalidStopFrequency},
		{"NaN", []float32{float32(math.NaN()), 4000}, ErrInvalidStopFrequency},
		{"positive infinity", []float32{float32(math.Inf(1))}, ErrInvalidStopFrequency},
		{"negative infinity", []float32{float32(math.Inf(-1)), 4000}, ErrInvalidStopFrequency},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(8000, test.stops); !errors.Is(err, test.want) {
				t.Fatalf("New error = %v, want errors.Is(_, %v)", err, test.want)
			}
		})
	}

	// Zero is a legal lower boundary; the final explicit stop is Nyquist.
	a, err := New(8000, []float32{0, 4000})
	if err != nil {
		t.Fatalf("New with zero boundary: %v", err)
	}
	constant := make([]int16, WindowSize)
	for i := range constant {
		constant[i] = 8192
	}
	got, ok := a.Write(constant)
	if !ok || got.Count != 2 || got.StopFrequenciesHz[0] != 0 || got.StopFrequenciesHz[1] != 4000 {
		t.Fatalf("zero/Nyquist boundaries = %+v, ok=%v", got, ok)
	}
	if got.RMS[0] != 0 || math.Abs(float64(got.RMS[1])-.25) > 1e-6 {
		t.Fatalf("zero-width first band should be empty: RMS=%v", got.RMS[:got.Count])
	}

	maxStops := make([]float32, MaxBands)
	for i := 0; i < MaxBands-1; i++ {
		maxStops[i] = float32((i + 1) * 200)
	}
	maxStops[MaxBands-1] = 4000
	a, err = New(8000, maxStops)
	if err != nil {
		t.Fatalf("New with MaxBands stops: %v", err)
	}
	got, ok = a.Write(make([]int16, WindowSize))
	if !ok || got.Count != MaxBands {
		t.Fatalf("maximum band count = %d, ok=%v", got.Count, ok)
	}
}

func TestWindowPowerCompensationAndFullScale(t *testing.T) {
	for _, sampleRate := range []int{8000, 16000} {
		t.Run(rateName(sampleRate), func(t *testing.T) {
			a, err := New(sampleRate, []float32{float32(sampleRate / 2)})
			if err != nil {
				t.Fatal(err)
			}
			pcm := make([]int16, WindowSize)
			for i := range pcm {
				pcm[i] = 8192
			}
			got, ok := a.Write(pcm)
			if !ok {
				t.Fatal("complete frame did not publish")
			}
			if math.Abs(float64(got.RMS[0])-.25) > 1e-6 {
				t.Fatalf("constant half-scale RMS = %.9f, want 0.25", got.RMS[0])
			}
			for i := range pcm {
				pcm[i] = -32768
			}
			got, ok = a.Write(pcm)
			if !ok || got.Sequence != 2 || got.RMS[0] != 1 {
				t.Fatalf("negative full-scale RMS = %+v, ok=%v", got, ok)
			}
		})
	}
}

func TestFramesAreNonOverlapping(t *testing.T) {
	a, err := New(8000, []float32{4000})
	if err != nil {
		t.Fatal(err)
	}
	first := make([]int16, WindowSize)
	for i := range first {
		first[i] = 8192
	}
	got, ok := a.Write(first)
	if !ok || math.Abs(float64(got.RMS[0])-.25) > 1e-6 {
		t.Fatalf("first frame = %+v, ok=%v", got, ok)
	}
	got, ok = a.Write(make([]int16, WindowSize))
	if !ok || got.Sequence != 2 || got.RMS[0] != 0 {
		t.Fatalf("second frame overlapped prior samples: %+v, ok=%v", got, ok)
	}
}

func TestBinAtStopBelongsToBandAboveAndNyquistIsIncluded(t *testing.T) {
	a, err := New(8000, []float32{125, 4000})
	if err != nil {
		t.Fatal(err)
	}
	if a.binBand[7] != 0 || a.binBand[8] != 1 || a.binBand[256] != 1 {
		t.Fatalf("bin assignments around stop/Nyquist = [%d %d %d], want [0 1 1]", a.binBand[7], a.binBand[8], a.binBand[256])
	}
}

func TestCoherentToneFallsInConfiguredFrequencyBand(t *testing.T) {
	const (
		sampleRate = 8000
		bin        = 8
		amplitude  = 8192
	)
	a, err := New(sampleRate, []float32{100, 150, 4000})
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, WindowSize)
	for n := range pcm {
		pcm[n] = int16(math.Round(amplitude * math.Sin(2*math.Pi*float64(bin*n)/WindowSize)))
	}
	got, ok := a.Write(pcm)
	if !ok || got.Count != 3 {
		t.Fatalf("spectrum = %+v, ok=%v", got, ok)
	}
	if got.StopFrequenciesHz[:got.Count][0] != 100 || got.StopFrequenciesHz[:got.Count][1] != 150 || got.StopFrequenciesHz[:got.Count][2] != 4000 {
		t.Fatalf("band upper boundaries = %v", got.StopFrequenciesHz[:got.Count])
	}
	want := float64(amplitude) / 32768 / math.Sqrt2
	if math.Abs(float64(got.RMS[1])-want) > 0.00008 {
		t.Fatalf("tone band RMS = %.8f, want about %.8f", got.RMS[1], want)
	}
	if got.RMS[0] > 0.00008 || got.RMS[2] > 0.00008 {
		t.Fatalf("tone energy leaked outside its Hann-bin band: RMS=%v", got.RMS[:got.Count])
	}
}

func TestBandMeanSquaresAddToFullBandParsevalPower(t *testing.T) {
	pcm := patternedPCM(WindowSize)
	full, err := New(16000, []float32{8000})
	if err != nil {
		t.Fatal(err)
	}
	fullSpectrum, ok := full.Write(pcm)
	if !ok {
		t.Fatal("full-band spectrum missing")
	}
	partitioned, err := New(16000, []float32{200, 500, 1200, 2400, 4800, 8000})
	if err != nil {
		t.Fatal(err)
	}
	partitionedSpectrum, ok := partitioned.Write(pcm)
	if !ok {
		t.Fatal("partitioned spectrum missing")
	}
	var sumSquares float64
	for _, rms := range partitionedSpectrum.RMS[:partitionedSpectrum.Count] {
		sumSquares += float64(rms) * float64(rms)
	}
	fullSquares := float64(fullSpectrum.RMS[0]) * float64(fullSpectrum.RMS[0])
	if math.Abs(sumSquares-fullSquares) > 1e-7 {
		t.Fatalf("partitioned mean-square sum %.10g != full-band %.10g", sumSquares, fullSquares)
	}
}

func TestChunkingIsInvariantAndWriteProcessesMultipleWindows(t *testing.T) {
	const sampleRate = 16000
	pcm := patternedPCM(5*WindowSize + 103)
	original := append([]int16(nil), pcm...)
	stops := []float32{200, 500, 1200, 2400, 4800, 8000}
	fixed := collectWithChunkSizes(t, sampleRate, stops, pcm, []int{128})
	irregular := collectWithChunkSizes(t, sampleRate, stops, pcm, []int{1, 31, 7, 250, 173, 64, 19, 511, 2, 200})
	if len(fixed) != 5 || len(irregular) != 5 {
		t.Fatalf("published windows: fixed=%d irregular=%d, want 5 each", len(fixed), len(irregular))
	}
	for i := range fixed {
		if fixed[i] != irregular[i] {
			t.Fatalf("chunking changed spectrum %d:\n fixed=%+v\n irregular=%+v", i, fixed[i], irregular[i])
		}
	}
	if !reflect.DeepEqual(pcm, original) {
		t.Fatal("Write modified caller PCM")
	}

	a, err := New(sampleRate, stops)
	if err != nil {
		t.Fatal(err)
	}
	latest, ok := a.Write(pcm)
	if !ok || latest.Sequence != 5 || latest != fixed[4] {
		t.Fatalf("multi-window Write = %+v, ok=%v; want final spectrum %+v", latest, ok, fixed[4])
	}
	if _, ok := a.Write(make([]int16, WindowSize-103)); !ok {
		t.Fatal("buffered tail did not complete the next window")
	} else if a.sequence != 6 {
		t.Fatalf("sequence after multi-window write and tail = %d, want 6", a.sequence)
	}
}

func TestResetDiscardsPartialFrameAndSequence(t *testing.T) {
	a, err := New(8000, []float32{4000})
	if err != nil {
		t.Fatal(err)
	}
	preReset := make([]int16, 200)
	for i := range preReset {
		preReset[i] = 32767
	}
	if _, ok := a.Write(preReset); ok {
		t.Fatal("partial frame published")
	}
	a.Reset()
	if _, ok := a.Write(make([]int16, 312)); ok {
		t.Fatal("pre-reset samples survived Reset")
	}
	got, ok := a.Write(make([]int16, 200))
	if !ok || got.Sequence != 1 || got.RMS[0] != 0 {
		t.Fatalf("frame after Reset = %+v, ok=%v", got, ok)
	}
	if _, ok := a.Write(make([]int16, WindowSize)); !ok || a.sequence != 2 {
		t.Fatalf("second frame sequence = %d, ok=%v", a.sequence, ok)
	}
	a.Reset()
	got, ok = a.Write(make([]int16, WindowSize))
	if !ok || got.Sequence != 1 {
		t.Fatalf("sequence did not reset: %+v, ok=%v", got, ok)
	}
}

func TestWriteDoesNotAllocate(t *testing.T) {
	a, err := New(16000, []float32{200, 500, 1200, 2400, 4800, 8000})
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, WindowSize)
	allocs := testing.AllocsPerRun(1000, func() {
		_, _ = a.Write(pcm)
	})
	if allocs != 0 {
		t.Fatalf("full-frame Write allocated %.2f objects per call", allocs)
	}
}

func collectWithChunkSizes(t *testing.T, sampleRate int, stops []float32, pcm []int16, sizes []int) []Spectrum {
	t.Helper()
	a, err := New(sampleRate, stops)
	if err != nil {
		t.Fatal(err)
	}
	var spectra []Spectrum
	for offset, chunkIndex := 0, 0; offset < len(pcm); chunkIndex++ {
		size := sizes[chunkIndex%len(sizes)]
		end := min(offset+size, len(pcm))
		if got, ok := a.Write(pcm[offset:end]); ok {
			spectra = append(spectra, got)
		}
		offset = end
	}
	return spectra
}

func patternedPCM(count int) []int16 {
	pcm := make([]int16, count)
	for i := range pcm {
		pcm[i] = int16((i*7919)%65536 - 32768)
	}
	return pcm
}

func rateName(sampleRate int) string {
	if sampleRate == 8000 {
		return "8000Hz"
	}
	return "16000Hz"
}

var benchmarkSpectrum Spectrum

func BenchmarkWrite512(b *testing.B) {
	benchmarkWrite(b, WindowSize)
}

func BenchmarkWrite2048(b *testing.B) {
	benchmarkWrite(b, 4*WindowSize)
}

func benchmarkWrite(b *testing.B, samples int) {
	b.Helper()
	a, err := New(16000, []float32{200, 500, 1200, 2400, 4800, 8000})
	if err != nil {
		b.Fatal(err)
	}
	pcm := patternedPCM(samples)
	b.SetBytes(int64(len(pcm) * 2))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkSpectrum, _ = a.Write(pcm)
	}
}
