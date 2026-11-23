package dotscope

import (
	"math"
	"mimac.local/mimac/internal/testutil/dotfixture"
	"testing"
	"unsafe"
)

func testModel(t testing.TB) *Model {
	t.Helper()
	m, err := Open(dotfixture.Library(t, "../../.."))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func TestNativeFrameLayoutAndDefaultPlate(t *testing.T) {
	var f nativeFrame
	if unsafe.Sizeof(f) != 8264 || unsafe.Offsetof(f.Dots) != 72 {
		t.Fatalf("native frame layout %d/%d", unsafe.Sizeof(f), unsafe.Offsetof(f.Dots))
	}
	m := testModel(t)
	m.Start(0, false)
	if !m.Step(.02, .02, false) {
		t.Fatal("recording hidden")
	}
	s := m.Snapshot()
	if s.Columns != 43 || s.Rows != 9 || len(s.Dots) != 387 || s.Pitch != 2.6 || s.Pose.Height != 26 {
		t.Fatalf("incorrect geometry %d x %d, pitch %f, height %f", s.Columns, s.Rows, s.Pitch, s.Pose.Height)
	}
}
func TestNumericalSnapshotsMatchReferenceModel(t *testing.T) {
	// These values were recorded by stepping the source model at 240 Hz with
	// the same RMS input, default tuning, and stage transitions below.
	indices := []int{20, 21, 150, 193, 194, 236}
	type golden struct {
		step               int
		pose               Pose
		powerSum, freshSum float32
		dots               [][2]float32
	}
	checks := []golden{
		{step: 119, pose: Pose{Width: 112.00447, Height: 26, Opacity: .99967504, Scale: 1.00000513, Softness: .00097463, Glow: .09160118, Grid: 1, Live: .59710997, Level: .90753752}, powerSum: 40.98088455, freshSum: 379.46487427, dots: [][2]float32{{.01000899, 1}, {.01146752, 1}, {.72545570, 1}, {.42679697, .37236997}, {.45989591, .32011378}, {.85367799, 1}}},
		{step: 479, pose: Pose{Width: 112.00000763, Height: 26, Opacity: .99999976, Scale: .99999988, Glow: .76525587, Grid: 1, Live: .97365206, Level: .51418906}, powerSum: 142.97959900, freshSum: 311.92059326, dots: [][2]float32{{.09392405, 1}, {.08141935, 1}, {.51716471, .17136192}, {.51026952, .19246225}, {.53044045, .19387658}, {.94564641, .72661775}}},
		{step: 599, pose: Pose{Width: 75.99803925, Height: 26, Opacity: .99999976, Scale: .99999988, Cool: .99967504, Glow: .12343027, Grid: 1, Live: .39227483, Level: .00266295}, powerSum: 83.59760284, freshSum: 150.53706360, dots: [][2]float32{{.00413501, 1}, {.00360334, 1}, {.34690547, .51824820}, {.52907836, 1}, {.63587534, 1}, {.33783406, .52783853}}},
		{step: 719, pose: Pose{Width: 76.00000763, Height: 26, Opacity: .99999976, Scale: .99999988, Cool: .99999976, Glow: .03185775, Grid: 1, Live: .15804359, Level: .00001379, Done: 1}, powerSum: 58.80035400, freshSum: 202.10992432, dots: [][2]float32{{.01130122, 1}, {.00394702, 1}, {.09845573, .33349165}, {.90882379, .17845818}, {.74886930, .40507752}, {.09701824, .33866161}}},
	}
	m := testModel(t)
	m.Start(0, false)
	checkAt := make(map[int]golden, len(checks))
	for _, check := range checks {
		checkAt[check.step] = check
	}
	for i := 0; i < 720; i++ {
		seconds := float64(i) / 240
		if i == 480 {
			m.Submit(1, seconds)
		}
		if i == 700 {
			m.Done(1, seconds)
		}
		rms := float32(0)
		if i >= 100 {
			if i%120 < 60 {
				rms = .18
			} else {
				rms = .035
			}
		}
		if i < 480 {
			m.Meter(rms)
		}
		m.Step(seconds, 1.0/240, false)
		check, ok := checkAt[i]
		if !ok {
			continue
		}
		s := m.Snapshot()
		var powerSum, freshSum float32
		for _, dot := range s.Dots {
			powerSum += dot[0]
			freshSum += dot[1]
		}
		if !near(powerSum, check.powerSum, .1) || !near(freshSum, check.freshSum, .25) {
			t.Errorf("step %d field sums = %.6f/%.6f, want %.6f/%.6f", i, powerSum, freshSum, check.powerSum, check.freshSum)
		}
		for _, field := range []struct {
			name      string
			got, want float32
		}{
			{"width", s.Pose.Width, check.pose.Width}, {"height", s.Pose.Height, check.pose.Height},
			{"opacity", s.Pose.Opacity, check.pose.Opacity}, {"scale", s.Pose.Scale, check.pose.Scale},
			{"softness", s.Pose.Softness, check.pose.Softness}, {"cool", s.Pose.Cool, check.pose.Cool},
			{"glow", s.Pose.Glow, check.pose.Glow}, {"grid", s.Pose.Grid, check.pose.Grid},
			{"live", s.Pose.Live, check.pose.Live}, {"level", s.Pose.Level, check.pose.Level},
			{"done", s.Pose.Done, check.pose.Done},
		} {
			if !near(field.got, field.want, .001) {
				t.Errorf("step %d pose.%s = %.8f, want %.8f", i, field.name, field.got, field.want)
			}
		}
		for n, index := range indices {
			for channel := 0; channel < 2; channel++ {
				if !near(s.Dots[index][channel], check.dots[n][channel], .001) {
					t.Errorf("step %d dot[%d][%d] = %.8f, want %.8f", i, index, channel, s.Dots[index][channel], check.dots[n][channel])
				}
			}
		}
	}
}

func TestSilenceAndIdleRetirement(t *testing.T) {
	m := testModel(t)
	m.Start(0, false)
	for i := range 240 {
		m.Meter(0)
		m.Step(float64(i)/240, 1.0/240, false)
	}
	if level := m.Snapshot().Pose.Level; level > .002 {
		t.Fatalf("silent level %f", level)
	}
	m.Cancel(0, 1)
	for i := range 180 {
		if !m.Step(1+float64(i)/60, 1.0/60, false) {
			return
		}
	}
	t.Fatal("cancel did not settle")
}

func near(got, want, tolerance float32) bool {
	return math.Abs(float64(got-want)) <= float64(tolerance)
}

func BenchmarkNativeVoiceFrame(b *testing.B) {
	m := testModel(b)
	m.Start(0, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Meter(.12)
		m.Step(float64(i)/60, 1.0/60, false)
		_ = m.Snapshot()
	}
}
