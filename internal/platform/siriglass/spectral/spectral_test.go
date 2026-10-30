package spectral

import (
	"math"
	"reflect"
	"testing"
)

func BenchmarkDisplayPCM320(b *testing.B) {
	a, err := New(Config{FrameLength: 128, SampleRateHz: 16000,
		StopsHz: []float32{200, 500, 1200, 2400, 4800, 8000}, SmoothingPreviousWeight: 0.5})
	if err != nil {
		b.Fatal(err)
	}
	pcm := make([]int16, 320)
	for i := range pcm {
		pcm[i] = int16(8192 * math.Sin(2*math.Pi*375*float64(i)/16000))
	}
	var raw, smoothed [6]float32
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := a.ProcessInt16(pcm); err != nil {
			b.Fatal(err)
		}
		if err := a.Get(raw[:], smoothed[:]); err != nil {
			b.Fatal(err)
		}
	}
}

func TestReferenceSpectrumFixtureAndRawDoesNotAdvanceSmoothing(t *testing.T) {
	a := mustNew(t, Config{
		FrameLength: 128, SampleRateHz: 16000,
		StopsHz: []float32{250, 1000, 8000}, SmoothingPreviousWeight: DefaultSmoothingPreviousWeight,
	})
	if got, want := a.bounds, []int{2, 8, 64}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exclusive boundaries=%v want=%v", got, want)
	}
	if math.Abs(float64(a.window[0]-0.0800000131)) > 2e-8 || math.Abs(float64(a.window[1]-0.0805541053)) > 3e-8 {
		t.Fatalf("periodic Hamming window starts [%g,%g]", a.window[0], a.window[1])
	}

	frame := fixtureCosine(128, 8, 0.25)
	original := append([]float32(nil), frame...)
	if err := a.Process(frame); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frame, original) {
		t.Fatal("Process modified caller samples")
	}
	for i, want := range []float32{0.0574999899, 0.13499999, 0.0575000197} {
		if math.Abs(float64(a.magnitudes[7+i]-want)) > 2e-8 {
			t.Errorf("magnitude bin %d=%g want %g", 7+i, a.magnitudes[7+i], want)
		}
	}

	raw := make([]float32, a.BandCount())
	out := make([]float32, a.BandCount())
	if err := a.Raw(raw); err != nil {
		t.Fatal(err)
	}
	if !allZero(a.outBands) {
		t.Fatalf("Raw advanced smoothed state: %v", a.outBands)
	}
	assertNear(t, "raw bands", raw, []float32{5.36494404e-9, 0.0575000197, 0.19250071}, 3e-8)

	if err := a.Get(raw, out); err != nil {
		t.Fatal(err)
	}
	assertNear(t, "first getter", out, []float32{2.68247202e-9, 0.0287500098, 0.0962503552}, 2e-8)
	first := append([]float32(nil), out...)
	if err := a.Raw(raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.outBands, first) {
		t.Fatalf("Raw changed prior smoothed state: previous=%v after=%v", first, a.outBands)
	}
	if err := a.Get(raw, out); err != nil {
		t.Fatal(err)
	}
	assertNear(t, "repeated getter", out, []float32{4.023708e-9, 0.043125, 0.14437553}, 3e-8)
}

func TestRecommendedBandBoundariesAt16k(t *testing.T) {
	a := mustNew(t, Config{
		FrameLength: 128, SampleRateHz: 16000,
		StopsHz:                 []float32{200, 500, 1200, 2400, 4800, 8000},
		SmoothingPreviousWeight: DefaultSmoothingPreviousWeight,
	})
	if got, want := a.bounds, []int{2, 4, 10, 20, 39, 64}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recommended profile boundaries=%v want=%v", got, want)
	}
}

func TestHugeFiniteStopsClampBeforeIntegerConversion(t *testing.T) {
	a := mustNew(t, Config{FrameLength: 128, SampleRateHz: 16000,
		StopsHz: []float32{8000, math.MaxFloat32}, SmoothingPreviousWeight: 0.5})
	if !reflect.DeepEqual(a.bounds, []int{64, 64}) {
		t.Fatalf("above-Nyquist stops did not clamp: %v", a.bounds)
	}
}

func TestSmoothingUsesPreviousOutputWeight(t *testing.T) {
	current := []float32{0.1, 0.4, 1.2}
	previous := []float32{0, 0.05, 0.2}
	currentBefore := append([]float32(nil), current...)
	previousBefore := append([]float32(nil), previous...)
	for _, weight := range []float32{0.25, 0.8} {
		out := make([]float32, len(current))
		if err := SmoothInto(current, previous, out, weight); err != nil {
			t.Fatal(err)
		}
		for i := range out {
			want := (1-weight)*current[i] + weight*previous[i]
			if math.Abs(float64(out[i]-want)) > 1e-7 {
				t.Errorf("s=%g output[%d]=%g want %g", weight, i, out[i], want)
			}
		}
	}
	for _, weight := range []float32{0.25, 0.8} {
		a := mustNew(t, Config{
			FrameLength: 128, SampleRateHz: 16000,
			StopsHz: []float32{250, 1000, 8000}, SmoothingPreviousWeight: weight,
		})
		if err := a.Process(fixtureCosine(128, 8, 0.25)); err != nil {
			t.Fatal(err)
		}
		raw := make([]float32, a.BandCount())
		out := make([]float32, a.BandCount())
		if err := a.Get(raw, out); err != nil {
			t.Fatal(err)
		}
		first := append([]float32(nil), out...)
		for i := range raw {
			want := (1 - weight) * raw[i]
			if math.Abs(float64(first[i]-want)) > 1e-7 {
				t.Errorf("Get s=%g first[%d]=%g want=%g", weight, i, first[i], want)
			}
		}
		if err := a.Get(raw, out); err != nil {
			t.Fatal(err)
		}
		for i := range raw {
			want := (1-weight)*raw[i] + weight*first[i]
			if math.Abs(float64(out[i]-want)) > 1e-7 {
				t.Errorf("Get s=%g repeated[%d]=%g want=%g", weight, i, out[i], want)
			}
		}
	}
	if !reflect.DeepEqual(current, currentBefore) || !reflect.DeepEqual(previous, previousBefore) {
		t.Fatal("SmoothInto mutated an input")
	}
}

func TestProcessShortAndLongBuffersAndReset(t *testing.T) {
	a := fixtureAnalyzer(t)
	frame := fixtureCosine(128, 8, 0.25)
	if err := a.Process(frame); err != nil {
		t.Fatal(err)
	}
	before := append([]float32(nil), a.magnitudes...)
	if err := a.Process(make([]float32, 127)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.magnitudes, before) {
		t.Fatal("short Process changed previous magnitudes")
	}
	long := make([]float32, 256)
	for i := range long[:128] {
		long[i] = float32(math.Sin(float64(i) * 0.91))
	}
	copy(long[128:], frame)
	if err := a.Process(long); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.magnitudes, before) {
		t.Fatal("long Process did not use the final N samples")
	}

	raw := make([]float32, a.BandCount())
	out := make([]float32, a.BandCount())
	if err := a.Get(raw, out); err != nil {
		t.Fatal(err)
	}
	previousOut := append([]float32(nil), out...)
	a.Reset()
	if !reflect.DeepEqual(a.magnitudes, before) {
		t.Fatal("Reset cleared FFT magnitudes")
	}
	if !allZero(a.rawBands) || !allZero(a.outBands) {
		t.Fatalf("Reset left band vectors: raw=%v smooth=%v", a.rawBands, a.outBands)
	}
	if err := a.Raw(raw); err != nil {
		t.Fatal(err)
	}
	assertNear(t, "raw after reset", raw, []float32{5.36494404e-9, 0.0575000197, 0.19250071}, 3e-8)
	if !allZero(a.outBands) {
		t.Fatalf("Raw after Reset advanced smoothing: %v (before reset %v)", a.outBands, previousOut)
	}
	if err := a.Get(raw, out); err != nil {
		t.Fatal(err)
	}
	assertNear(t, "getter after reset", out, []float32{2.68247202e-9, 0.0287500098, 0.0962503552}, 2e-8)
}

func TestSilenceAndInt16NormalizationDoNotMutateInput(t *testing.T) {
	a := mustNew(t, Config{
		FrameLength: 256, SampleRateHz: 8000,
		StopsHz: []float32{200, 800, 4000}, SmoothingPreviousWeight: 0.5,
	})
	pcm := make([]int16, a.FrameLength())
	if err := a.ProcessInt16(pcm); err != nil {
		t.Fatal(err)
	}
	raw := make([]float32, a.BandCount())
	out := make([]float32, a.BandCount())
	if err := a.Get(raw, out); err != nil {
		t.Fatal(err)
	}
	if !allZero(raw) || !allZero(out) {
		t.Fatalf("silence raw=%v output=%v", raw, out)
	}

	for i := range pcm {
		pcm[i] = int16(8192 * math.Sin(2*math.Pi*8*float64(i)/float64(len(pcm))))
	}
	pcmBefore := append([]int16(nil), pcm...)
	if err := a.ProcessInt16(pcm); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pcm, pcmBefore) {
		t.Fatal("ProcessInt16 modified caller PCM")
	}
	if err := a.Raw(raw); err != nil {
		t.Fatal(err)
	}
	if allZero(raw) {
		t.Fatal("nonzero int16 PCM produced no band energy")
	}

	floatFrame := make([]float32, len(pcm))
	for i, sample := range pcm {
		floatFrame[i] = float32(sample) / 32768
	}
	other := mustNew(t, Config{
		FrameLength: 256, SampleRateHz: 8000,
		StopsHz: []float32{200, 800, 4000}, SmoothingPreviousWeight: 0.5,
	})
	if err := other.Process(floatFrame); err != nil {
		t.Fatal(err)
	}
	floatRaw := make([]float32, len(raw))
	if err := other.Raw(floatRaw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw, floatRaw) {
		t.Fatalf("int16/32768 raw bands differ from normalized float: %v vs %v", raw, floatRaw)
	}
}

func TestOffBinMultitoneDCAndNyquistCases(t *testing.T) {
	const n = 256
	a := mustNew(t, Config{
		FrameLength: n, SampleRateHz: 8000,
		StopsHz: []float32{250, 750, 2000, 4000}, SmoothingPreviousWeight: 0.5,
	})
	frame := make([]float32, n)
	for i := range frame {
		x := float64(i)
		frame[i] = float32(0.08*math.Cos(2*math.Pi*4*x/n) + 0.16*math.Sin(2*math.Pi*20*x/n) + 0.04*math.Cos(2*math.Pi*8.5*x/n))
	}
	if err := a.Process(frame); err != nil {
		t.Fatal(err)
	}
	if a.magnitudes[8] == 0 || a.magnitudes[9] == 0 {
		t.Fatalf("off-bin tone did not spread to adjacent bins: %v", a.magnitudes[6:11])
	}
	raw := make([]float32, a.BandCount())
	if err := a.Raw(raw); err != nil {
		t.Fatal(err)
	}
	if !allPositive(raw) {
		t.Fatalf("multi-tone bands are not all positive: %v", raw)
	}

	constant := make([]float32, n)
	for i := range constant {
		constant[i] = 0.25
	}
	if err := a.Process(constant); err != nil {
		t.Fatal(err)
	}
	if a.magnitudes[0] != 0 || a.magnitudes[1] == 0 {
		t.Fatalf("DC packed bin/sidelobe: bin0=%g bin1=%g", a.magnitudes[0], a.magnitudes[1])
	}
	for i := range constant {
		if i&1 == 0 {
			constant[i] = 0.25
		} else {
			constant[i] = -0.25
		}
	}
	if err := a.Process(constant); err != nil {
		t.Fatal(err)
	}
	if a.magnitudes[0] != 0 || a.magnitudes[n/2-1] == 0 {
		t.Fatalf("Nyquist packed bin/edge: bin0=%g binN/2-1=%g", a.magnitudes[0], a.magnitudes[n/2-1])
	}
}

func TestSmoothingPreviousWeightLimits(t *testing.T) {
	for _, weight := range []float32{0, 0.99} {
		if _, err := New(Config{FrameLength: 128, SampleRateHz: 16000, StopsHz: []float32{8000}, SmoothingPreviousWeight: weight}); err != nil {
			t.Errorf("weight %g should be accepted: %v", weight, err)
		}
	}
	for _, weight := range []float32{0.999, 1, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		if _, err := New(Config{FrameLength: 128, SampleRateHz: 16000, StopsHz: []float32{8000}, SmoothingPreviousWeight: weight}); err != ErrInvalidSmoothing {
			t.Errorf("weight %g error=%v want ErrInvalidSmoothing", weight, err)
		}
	}
	if err := SmoothInto([]float32{1}, []float32{0}, make([]float32, 1), 1); err != ErrInvalidSmoothing {
		t.Fatalf("SmoothInto weight 1 error=%v want ErrInvalidSmoothing", err)
	}
}

func TestConfiguredBoundsAndAllocationLimits(t *testing.T) {
	stops := make([]float32, MaxBands)
	for i := range stops {
		stops[i] = float32((i + 1) * 100)
	}
	stops[len(stops)-1] = 8000
	if _, err := New(Config{FrameLength: MaxFrameLength, SampleRateHz: 16000, StopsHz: stops, SmoothingPreviousWeight: 0.5}); err != nil {
		t.Fatalf("maximum supported allocation config rejected: %v", err)
	}
	if _, err := New(Config{FrameLength: MaxFrameLength * 2, SampleRateHz: 16000, StopsHz: []float32{8000}, SmoothingPreviousWeight: 0.5}); err != ErrInvalidFrameLength {
		t.Fatalf("oversized frame error=%v want ErrInvalidFrameLength", err)
	}
	tooManyStops := make([]float32, MaxBands+1)
	for i := range tooManyStops {
		tooManyStops[i] = float32(i + 1)
	}
	if _, err := New(Config{FrameLength: 128, SampleRateHz: 16000, StopsHz: tooManyStops, SmoothingPreviousWeight: 0.5}); err != ErrTooManyBands {
		t.Fatalf("oversized bands error=%v want ErrTooManyBands", err)
	}
}

func TestProcessRawGetAndSmoothIntoHaveNoHotAllocations(t *testing.T) {
	a := fixtureAnalyzer(t)
	frame := fixtureCosine(128, 8, 0.25)
	raw := make([]float32, a.BandCount())
	out := make([]float32, a.BandCount())
	allocs := testing.AllocsPerRun(1000, func() {
		if err := a.Process(frame); err != nil {
			panic(err)
		}
		if err := a.Raw(raw); err != nil {
			panic(err)
		}
		if err := a.Get(raw, out); err != nil {
			panic(err)
		}
		if err := SmoothInto(raw, out, out, 0.5); err != nil {
			panic(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("hot processing allocs=%g want 0", allocs)
	}
}

func TestInvalidConfigurationAndBuffers(t *testing.T) {
	invalid := []struct {
		config Config
		want   error
	}{
		{Config{FrameLength: 128, SampleRateHz: 44100, StopsHz: []float32{8000}, SmoothingPreviousWeight: 0.5}, ErrUnsupportedSampleRate},
		{Config{FrameLength: 127, SampleRateHz: 16000, StopsHz: []float32{8000}, SmoothingPreviousWeight: 0.5}, ErrInvalidFrameLength},
		{Config{FrameLength: 0, SampleRateHz: 16000, StopsHz: []float32{8000}, SmoothingPreviousWeight: 0.5}, ErrInvalidFrameLength},
		{Config{FrameLength: 128, SampleRateHz: 16000, StopsHz: nil, SmoothingPreviousWeight: 0.5}, ErrInvalidStops},
		{Config{FrameLength: 128, SampleRateHz: 16000, StopsHz: []float32{100, 100}, SmoothingPreviousWeight: 0.5}, ErrInvalidStops},
		{Config{FrameLength: 128, SampleRateHz: 16000, StopsHz: []float32{float32(math.NaN())}, SmoothingPreviousWeight: 0.5}, ErrInvalidStops},
		{Config{FrameLength: 128, SampleRateHz: 16000, StopsHz: []float32{8000}, SmoothingPreviousWeight: float32(math.NaN())}, ErrInvalidSmoothing},
	}
	for _, test := range invalid {
		if _, err := New(test.config); err != test.want {
			t.Errorf("New(%+v) error=%v want %v", test.config, err, test.want)
		}
	}
	a := fixtureAnalyzer(t)
	if err := a.Raw(nil); err != ErrInvalidBuffer {
		t.Fatalf("Raw bad buffer error=%v", err)
	}
	if err := a.Get(nil, nil); err != ErrInvalidBuffer {
		t.Fatalf("Get bad buffers error=%v", err)
	}
	if err := SmoothInto(nil, nil, nil, 0.5); err != ErrInvalidBuffer {
		t.Fatalf("SmoothInto bad buffers error=%v", err)
	}
}

func fixtureAnalyzer(t *testing.T) *Analyzer {
	t.Helper()
	return mustNew(t, Config{
		FrameLength: 128, SampleRateHz: 16000,
		StopsHz: []float32{250, 1000, 8000}, SmoothingPreviousWeight: 0.5,
	})
}

func mustNew(t *testing.T, config Config) *Analyzer {
	t.Helper()
	a, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func fixtureCosine(n, bin int, amplitude float32) []float32 {
	const pi = float32(3.14159265358979323846)
	frame := make([]float32, n)
	for i := range frame {
		phase := float32(2) * pi
		phase *= float32(bin)
		phase *= float32(i)
		phase /= float32(n)
		frame[i] = amplitude * float32(math.Cos(float64(phase)))
	}
	return frame
}

func assertNear(t *testing.T, label string, got, want []float32, tolerance float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s len=%d want=%d", label, len(got), len(want))
	}
	for i := range got {
		if math.Abs(float64(got[i]-want[i])) > tolerance {
			t.Errorf("%s[%d]=%.10g want %.10g", label, i, got[i], want[i])
		}
	}
}

func allZero(values []float32) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
}

func allPositive(values []float32) bool {
	for _, value := range values {
		if value <= 0 {
			return false
		}
	}
	return true
}
