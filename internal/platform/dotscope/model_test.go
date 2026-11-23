package dotscope

import (
	"math"
	"testing"
)

func TestDefaultPlateAndDotGeometry(t *testing.T) {
	m := New()
	s := m.Snapshot()
	if s.Columns != 43 || s.Rows != 9 || len(s.Dots) != 43*9 {
		t.Fatalf("default grid = %d×%d (%d dots), want 43×9", s.Columns, s.Rows, len(s.Dots))
	}
	if s.Pitch != 2.6 || s.Pose.Height != 26 || m.Tuning().Width != 112 || m.Tuning().ProcessingWidth != 76 {
		t.Fatalf("default geometry = pitch %.2f, pose=%+v tuning=%+v", s.Pitch, s.Pose, m.Tuning())
	}
	if Loudness(0) != -120 {
		t.Fatalf("silence loudness = %v dBFS, want -120", Loudness(0))
	}
	if math.IsNaN(float64(Loudness(float32(math.NaN())))) || math.IsInf(float64(Loudness(float32(math.Inf(1)))), 0) {
		t.Fatal("invalid RMS should map to a finite silence floor")
	}
}

func TestTuningKeepsDotBufferWithinLimit(t *testing.T) {
	m := NewWithTuning(Tuning{Columns: 96, Rows: 15})
	s := m.Snapshot()
	if s.Columns != 96 || s.Rows != 10 || len(s.Dots) != 960 || len(s.Dots) > MaxDots {
		t.Fatalf("clamped layout = %d×%d (%d dots)", s.Columns, s.Rows, len(s.Dots))
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
	m := New()
	checkAt := make(map[int]golden, len(checks))
	for _, check := range checks {
		checkAt[check.step] = check
	}
	for i := 0; i < 720; i++ {
		stage := StageRecording
		if i >= 480 && i < 700 {
			stage = StageProcessing
		} else if i >= 700 {
			stage = StageDone
		}
		rms := float32(0)
		if i >= 100 {
			if i%120 < 60 {
				rms = .18
			} else {
				rms = .035
			}
		}
		m.Advance(step, Frame{Stage: stage, Visible: true, LoudnessDB: Loudness(rms)})
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

func TestSilenceDoesNotLiftAndHiddenModelSettles(t *testing.T) {
	m := New()
	frame := Frame{Stage: StageRecording, Visible: true, LoudnessDB: Loudness(0)}
	for range 240 {
		m.Advance(step, frame)
	}
	if level := m.Snapshot().Pose.Level; level > .002 {
		t.Fatalf("silence voice level = %.6f, want near zero", level)
	}
	frame.Visible = false
	for range 180 {
		m.Advance(1.0/60, frame)
	}
	if !m.Settled() {
		t.Fatalf("hidden opacity did not settle: %+v", m.Snapshot().Pose)
	}
}

func TestSnapshotsAndAdvanceDoNotAllocate(t *testing.T) {
	m := New()
	frame := Frame{Stage: StageRecording, Visible: true, LoudnessDB: Loudness(.12)}
	allocs := testing.AllocsPerRun(100, func() {
		m.Advance(1.0/120, frame)
		s := m.Snapshot()
		if len(s.Dots) == 0 {
			t.Fatal("empty dot buffer")
		}
	})
	if allocs != 0 {
		t.Fatalf("Advance + Snapshot allocated %.2f objects per frame", allocs)
	}
}

func near(got, want, tolerance float32) bool { return abs(got-want) <= tolerance }

func BenchmarkModelAdvance(b *testing.B) {
	m := New()
	frame := Frame{Stage: StageRecording, Visible: true, LoudnessDB: Loudness(.12)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Advance(1.0/120, frame)
	}
}
