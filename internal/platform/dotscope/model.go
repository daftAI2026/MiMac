// Package dotscope implements the deterministic point-field voice animation.
//
// Model advances on a fixed 240 Hz clock and keeps its dot buffer in row-major
// order. Snapshot returns a read-only view of that buffer, valid until the next
// Advance, Tune, or Restart call. The model owns no native or external resources.
package dotscope

import (
	"math"
)

const (
	MaxColumns  = 96
	MaxRows     = 15
	MaxDots     = 1024
	PlateHeight = float32(26)

	step              = float32(1.0 / 240.0)
	historyLength     = 512
	samplesPerColumn  = 3
	startWidth        = float32(30)
	enterDuration     = float32(0.42)
	wipeDuration      = float32(0.3)
	passInset         = float32(9)
	phosphorHead      = float32(0.15)
	breathPeriod      = float32(5.5)
	hiddenScale       = float32(0.9)
	hiddenSoftness    = float32(3)
	workHelix         = float32(2.6)
	flowSpacing       = float32(0.7)
	carrierRest       = float32(0.14)
	ringBirth         = float32(0.15)
	beamFade          = float32(0.14)
	criticalTolerance = float32(1e-4)
)

var (
	traceSpeeds  = [...]float32{0.8, 1.25, 1.85, 2.6}
	traceBase    = [...]float32{0.55, 0.42, 0.3, 0.2}
	traceSpatial = [...]float32{1.4, 2.4, 3.8, 5.6}
)

const tau = float32(2 * math.Pi)
const pi = float32(math.Pi)

// Processing selects the motion used while the voice tool is processing.
type Processing uint8

const (
	ProcessingSweep Processing = iota
	ProcessingFlow
	ProcessingPulse
)

// Tuning contains the parameters used by the upstream dot-field simulation.
// Values outside the supported ranges are replaced or clamped by NewWithTuning
// and Tune, matching the source model's knob ranges.
type Tuning struct {
	Columns         float32
	Rows            float32
	Spacing         float32
	Width           float32
	ProcessingWidth float32
	Height          float32
	BraidSpeed      float32
	HelixCycles     float32
	Gain            float32
	Response        float32
	Release         float32
	HistorySpeed    float32
	Phosphor        float32
	Glow            float32
	ShimmerPass     float32
	Shimmer         float32
	Transition      float32
	Bounce          float32
	AutoLevel       bool
	AutoRelease     float32
	NoiseFloor      float32
	Processing      Processing
}

// DefaultTuning reproduces the model's default 112 by 26 point plate and grid.
func DefaultTuning() Tuning {
	return Tuning{
		Columns: 43, Rows: 9, Spacing: 2.6,
		Width: 112, ProcessingWidth: 76, Height: PlateHeight,
		BraidSpeed: 1, HelixCycles: 1.6, Gain: 1,
		Response: 6.5, Release: 0.15, HistorySpeed: 150,
		Phosphor: 0.6, Glow: 1, ShimmerPass: 0.55, Shimmer: 1,
		Transition: 0.3, Bounce: 0.15, AutoLevel: true,
		AutoRelease: 3, NoiseFloor: -60, Processing: ProcessingFlow,
	}
}

func (t Tuning) clamped() Tuning {
	d := DefaultTuning()
	t.Columns = tuneWhole(t.Columns, d.Columns, 11, MaxColumns)
	t.Rows = tuneWhole(t.Rows, d.Rows, 3, MaxRows)
	t.Spacing = tune(t.Spacing, d.Spacing, 1.6, 4)
	t.Width = tune(t.Width, d.Width, 40, 280)
	t.ProcessingWidth = tune(t.ProcessingWidth, d.ProcessingWidth, 26, 280)
	t.Height = tune(t.Height, d.Height, 14, 40)
	t.BraidSpeed = tune(t.BraidSpeed, d.BraidSpeed, 0, 3)
	t.HelixCycles = tune(t.HelixCycles, d.HelixCycles, 0, 4)
	t.Gain = tune(t.Gain, d.Gain, 0.25, 3)
	t.Response = tune(t.Response, d.Response, 1, 16)
	t.Release = tune(t.Release, d.Release, 0.03, 0.6)
	t.HistorySpeed = tune(t.HistorySpeed, d.HistorySpeed, 40, 400)
	t.Phosphor = tune(t.Phosphor, d.Phosphor, 0.05, 1.5)
	t.Glow = tune(t.Glow, d.Glow, 0, 3)
	t.ShimmerPass = tune(t.ShimmerPass, d.ShimmerPass, 0.2, 1.5)
	t.Shimmer = tune(t.Shimmer, d.Shimmer, 0, 2.5)
	t.Transition = tune(t.Transition, d.Transition, 0.1, 0.8)
	t.Bounce = tune(t.Bounce, d.Bounce, 0, 0.6)
	t.AutoRelease = tune(t.AutoRelease, d.AutoRelease, 0.5, 10)
	t.NoiseFloor = tune(t.NoiseFloor, d.NoiseFloor, -80, -30)
	if t.Processing > ProcessingPulse {
		t.Processing = d.Processing
	}
	rows := min(MaxDots/int(t.Columns), MaxRows)
	t.Rows = min(t.Rows, float32(rows))
	return t
}

func tune(value, fallback, low, high float32) float32 {
	if !finite32(value) {
		value = fallback
	}
	return min(max(value, low), high)
}

func tuneWhole(value, fallback float32, low, high int) float32 {
	value = tune(value, fallback, float32(low), float32(high))
	return float32(int(value + 0.5))
}

// Stage is the semantic phase of the displayed voice interaction.
type Stage uint8

const (
	StageRecording Stage = iota
	StageProcessing
	StageDone
	StageFailed
)

// Frame is one fresh host observation. LoudnessDB is dBFS; use -120 for exact
// silence (the value returned by Loudness(0)).
type Frame struct {
	Stage        Stage
	Visible      bool
	LoudnessDB   float32
	ReduceMotion bool
}

// Pose carries global geometry and material terms for the renderer.
type Pose struct {
	Width    float32
	Height   float32
	Opacity  float32
	Scale    float32
	Softness float32
	Cool     float32
	Fail     float32
	Glow     float32
	Grid     float32
	Live     float32
	Reveal   float32
	Level    float32
	Clock    float32
	Done     float32
}

// Snapshot is the current renderer-neutral model output. Dots are row-major;
// each pair is [brightness, freshness], both normalized to 0..1. Dots aliases
// model storage and remains valid until the next mutating call on Model.
type Snapshot struct {
	Pose    Pose
	Columns int
	Rows    int
	Pitch   float32
	Dots    [][2]float32
}

// Model is the upstream simulation port. It owns no timers, goroutines, native
// views, audio resources, or other external state.
type Model struct {
	tuning       Tuning
	stage        Stage
	elapsed      float32
	speedAtDone  float32
	doneFrom     float32
	flowOffset   float32
	flowTurn     float32
	auto         autoLevel
	level        float32
	voices       [2]voice
	traces       [2]trace
	live         float32
	carrier      float32
	time         float32
	steps        uint64
	pending      float32
	reduceMotion bool
	columns      int
	rows         int
	head         [2][]float32
	tail         [2][]float32
	dots         [][2]float32
	ys           [2][]float32
	brightness   [2][]float32
	grid         float32
	width        spring
	opacity      spring
	scale        spring
	softness     spring
	cool         spring
	fail         spring
}

// New creates a model with the original plate and tuning defaults.
func New() *Model { return NewWithTuning(DefaultTuning()) }

// NewWithTuning creates a model with clamped parameters.
func NewWithTuning(tuning Tuning) *Model {
	m := &Model{
		tuning: tuning.clamped(), stage: StageRecording,
		auto:   defaultAutoLevel(),
		voices: [2]voice{},
		traces: [2]trace{newTrace([4]float32{0.1, 1.7, 3.3, 4.6}), newTrace([4]float32{2.4, 0.6, 5.1, 1.9})},
		width:  springAt(startWidth), opacity: springAt(0),
		scale: springAt(hiddenScale), softness: springAt(hiddenSoftness),
		cool: springAt(0), fail: springAt(0),
	}
	m.resize()
	return m
}

// Restart starts a fresh recording and retains only learned speaker level.
func (m *Model) Restart(reduceMotion bool) {
	if m == nil {
		return
	}
	auto := m.auto
	*m = *NewWithTuning(m.tuning)
	m.auto = auto
	if reduceMotion {
		m.scale.jump(1)
		m.softness.jump(0)
		m.width.jump(m.tuning.Width)
	}
}

// Tune applies clamped parameters and resizes the model's dot buffers if needed.
func (m *Model) Tune(tuning Tuning) {
	if m == nil {
		return
	}
	m.tuning = tuning.clamped()
	m.resize()
}

// Tuning returns the active, clamped parameters.
func (m *Model) Tuning() Tuning { return m.tuning }

// Advance advances the simulation by dt seconds, splitting time into fixed
// 240 Hz steps. Large frame gaps are capped at 250 ms to bound catch-up work.
func (m *Model) Advance(dt float32, frame Frame) {
	if m == nil {
		return
	}
	if frame.Stage != m.stage {
		if frame.Stage == StageDone {
			m.armDone()
		}
		if frame.Stage == StageProcessing {
			m.flowOffset = -m.width.x/2 - 4
		}
		m.stage = frame.Stage
		m.elapsed = 0
	}
	m.reduceMotion = frame.ReduceMotion
	if !finite32(dt) || dt < 0 {
		dt = 0
	}
	m.pending += min(dt, 0.25)
	for m.pending >= step {
		m.pending -= step
		m.tick(frame)
	}
}

// Snapshot returns the current model output without allocating a new dot slice.
func (m *Model) Snapshot() Snapshot {
	return Snapshot{Pose: m.pose(), Columns: m.columns, Rows: m.rows, Pitch: m.tuning.Spacing, Dots: m.dots}
}

// Settled reports whether the hidden plate has finished fading out.
func (m *Model) Settled() bool { return m.opacity.target == 0 && m.opacity.x < 0.002 }

func (m *Model) resize() {
	columns, rows := int(m.tuning.Columns), int(m.tuning.Rows)
	if columns == m.columns && rows == m.rows {
		return
	}
	m.columns, m.rows = columns, rows
	dots := columns * rows
	samples := (columns-1)*samplesPerColumn + 1
	m.head = [2][]float32{make([]float32, dots), make([]float32, dots)}
	m.tail = [2][]float32{make([]float32, dots), make([]float32, dots)}
	m.dots = make([][2]float32, dots)
	m.ys = [2][]float32{make([]float32, samples), make([]float32, samples)}
	m.brightness = [2][]float32{make([]float32, samples), make([]float32, samples)}
}

func (m *Model) pose() Pose {
	clock, done := m.elapsed, float32(0)
	if m.reduceMotion {
		clock = 0
	}
	if m.stage == StageDone {
		done = 1
	}
	return Pose{
		Width: max(m.width.x, m.tuning.Height), Height: m.tuning.Height,
		Opacity: clamp(m.opacity.x, 0, 1), Scale: max(m.scale.x, 0.01),
		Softness: clamp(m.softness.x, 0, hiddenSoftness),
		Cool:     clamp(m.cool.x, 0, 1), Fail: clamp(m.fail.x, 0, 1),
		Glow: (m.traces[0].glow + m.traces[1].glow) / 2 * m.tuning.Glow,
		Grid: m.grid, Live: m.live,
		Reveal: max((m.scale.x-hiddenScale)/(1-hiddenScale), 0),
		Level:  clamp(m.voices[0].level, 0, 1),
		Clock:  clock,
		Done:   done,
	}
}

// Loudness converts a normalized linear RMS amplitude to dBFS. Zero maps to
// -120 dBFS, matching the model's finite silence floor.
func Loudness(rms float32) float32 {
	if !finite32(rms) {
		return -120
	}
	return float32(20 * math.Log10(float64(max(rms, 0)+1e-6)))
}

func fixedLevel(loudness float32) float32 {
	return clamp((loudness+40)/30, 0, 1)
}

const (
	autoMinSpan    = float32(16)
	autoRange      = float32(24)
	autoRoomMargin = float32(5)
	autoAttack     = float32(0.04)
	roomFall       = float32(0.3)
	roomRise       = float32(6)
)

type autoLevel struct{ peak, room float32 }

func defaultAutoLevel() autoLevel { return autoLevel{peak: -30, room: -55} }

func (a *autoLevel) step(loudness, h, release, noiseFloor float32) float32 {
	db := max(loudness, -120)
	follow := func(value *float32, target, duration float32) {
		*value += (target - *value) * (1 - exp(-h/max(duration, 1e-3)))
	}
	roomTime := roomRise
	if db < a.room {
		roomTime = roomFall
	}
	follow(&a.room, db, roomTime)
	lowest := max(a.room, noiseFloor) + autoMinSpan
	if db > a.peak {
		follow(&a.peak, db, autoAttack)
	} else {
		follow(&a.peak, lowest, release)
	}
	a.peak = max(a.peak, lowest)
	return a.level(db, noiseFloor)
}

func (a *autoLevel) level(loudness, noiseFloor float32) float32 {
	low := max(a.room, noiseFloor) + autoRoomMargin
	low = max(low, a.peak-autoRange)
	return clamp((loudness-low)/max(a.peak-low, 1), 0, 1)
}

type voice struct{ level, reservoir, energy, kick, kickVelocity, band float32 }

func (v *voice) step(raw, h float32) {
	previous := v.level
	time := float32(0.095)
	if raw > v.level {
		time = 0.035
	}
	v.level += (raw - v.level) * (1 - exp(-h/time))
	rise := max((v.level-previous)/h, 0)
	v.reservoir += (1.75*v.level*v.level - v.reservoir/1.8) * h
	v.energy = 1 - exp(-v.reservoir*1.45)
	w, z := tau*1.3, float32(0.22)
	v.kickVelocity += (-w*w*v.kick - 2*z*w*v.kickVelocity + 15*rise + 2.2*v.level) * h
	v.kick += v.kickVelocity * h
	time = 0.14
	if raw > v.band {
		time = 0.025
	}
	v.band += (raw - v.band) * (1 - exp(-h/time))
}

type trace struct {
	envelope, amplitude, amplitudeVelocity   float32
	glowFast, glowSpring, glowSpringVelocity float32
	glowSlow, glow                           float32
	phases                                   [4]float32
	history                                  []float32
	head                                     int
}

func newTrace(phases [4]float32) trace {
	return trace{phases: phases, history: make([]float32, historyLength*5)}
}

func (t *trace) tick(v *voice, live, speed float32, tuning *Tuning, h float32) {
	follow := func(value *float32, target, rise, fall float32) {
		duration := fall
		if target > *value {
			duration = rise
		}
		*value += (target - *value) * (1 - exp(-h/duration))
	}
	follow(&t.envelope, v.level, 0.022, tuning.Release)
	w, z := tau*tuning.Response, float32(0.52)
	t.amplitudeVelocity += (-w*w*(t.amplitude-t.envelope) - 2*z*w*t.amplitudeVelocity) * h
	t.amplitude += t.amplitudeVelocity * h
	follow(&t.glowFast, v.level, 0.045, 0.13)
	gw, gz := tau*3.6, float32(0.5)
	t.glowSpringVelocity += (-gw*gw*(t.glowSpring-t.glowFast) - 2*gz*gw*t.glowSpringVelocity) * h
	t.glowSpring += t.glowSpringVelocity * h
	follow(&t.glowSlow, v.level, 0.15, 0.7)
	swell := 0.8*max(t.glowSpring-0.25, 0) + 0.04*max(v.kick, -0.5)
	t.glow = min((max(swell, 0)+0.42*t.glowSlow+0.2*v.energy)*live, 1.15)
	goSpeed := 0.6 + 1.1*min(t.envelope*1.6, 1)
	for i, frequency := range traceSpeeds {
		t.phases[i] = mod(t.phases[i]+h*tau*frequency*goSpeed*speed, tau)
	}
	// The four prototype bands are flat for this voice source, so the
	// original grouped-band mean reduces to one shared emphasis factor.
	emphasis := 0.35 + 0.65*min(v.band/(v.band+0.06), 2)
	offset := t.head * 5
	amplitude := max(t.amplitude, 0) * live
	t.history[offset] = amplitude
	for i, base := range traceBase {
		t.history[offset+1+i] = amplitude * base * emphasis
	}
	t.head = (t.head + 1) % historyLength
}

func (t *trace) past(delay float32, channel int) float32 {
	d := min(delay/step, historyLength-2)
	i := int(math.Floor(float64(d)))
	k := d - float32(i)
	a := (t.head + 2*historyLength - 1 - i) % historyLength
	b := (a + historyLength - 1) % historyLength
	return t.history[a*5+channel]*(1-k) + t.history[b*5+channel]*k
}

type springSpec struct{ duration, bounce float32 }
type spring struct{ x, v, target float32 }

func springAt(x float32) spring  { return spring{x: x, target: x} }
func (s *spring) jump(x float32) { s.x, s.v, s.target = x, 0, x }
func (s *spring) stepTo(target float32, spec springSpec, dt float32) {
	s.target = target
	s.step(spec, dt)
}
func (s *spring) step(spec springSpec, dt float32) {
	if dt <= 0 || !finite32(dt) {
		return
	}
	omega := tau / max(spec.duration, 0.01)
	zeta := float32(1)
	if spec.bounce >= 0 {
		zeta = max(1-spec.bounce, 0)
	} else {
		zeta = 1 / max(1+spec.bounce, 0.01)
	}
	x0, v0 := s.x-s.target, s.v
	if zeta < 1-criticalTolerance {
		decay := zeta * omega
		omegaD := omega * sqrt(1-zeta*zeta)
		e := exp(-decay * dt)
		sin, cos := sinCos(omegaD * dt)
		sinOverD := sin / omegaD
		s.x = s.target + e*(x0*(cos+decay*sinOverD)+v0*sinOverD)
		s.v = e * (-x0*omega*omega*sinOverD + v0*(cos-decay*sinOverD))
	} else if zeta > 1+criticalTolerance {
		root := sqrt(zeta*zeta - 1)
		rootSum := zeta + root
		slowRoot, fastRoot := -omega/rootSum, -omega*rootSum
		denom := slowRoot - fastRoot
		slowExp, fastExp := exp(slowRoot*dt), exp(fastRoot*dt)
		p00 := (-fastRoot*slowExp + slowRoot*fastExp) / denom
		p01 := (slowExp - fastExp) / denom
		p10 := slowRoot * fastRoot * (fastExp - slowExp) / denom
		p11 := (slowRoot*slowExp - fastRoot*fastExp) / denom
		s.x = s.target + p00*x0 + p01*v0
		s.v = p10*x0 + p11*v0
	} else {
		e := exp(-omega * dt)
		c := v0 + omega*x0
		s.x = s.target + (x0+c*dt)*e
		s.v = (v0 - omega*c*dt) * e
	}
}

func smoothstep(a, b, x float32) float32 {
	t := clamp((x-a)/(b-a), 0, 1)
	return t * t * (3 - 2*t)
}
func bump(d, width float32) float32 { return exp(-((d / width) * (d / width))) }
func hermite(start, speed, end, t float32) float32 {
	t = clamp(t, 0, 1)
	t2, t3 := t*t, t*t*t
	return (2*t3-3*t2+1)*start + (t3-2*t2+t)*wipeDuration*speed + (3*t2-2*t3)*end
}
func passPosition(p float32) float32 { return 0.5 - 0.5*cos(pi*clamp(p, 0, 1)) }

type workingKind uint8

const (
	workingFlow workingKind = iota
	workingPulse
)

type working struct {
	kind                                 workingKind
	offset, spacing, time, period, speed float32
}

func (w working) light(x, halfWidth float32) float32 {
	if w.kind == workingFlow {
		d := remEuclid(x-w.offset+w.spacing/2, w.spacing) - w.spacing/2
		return bump(d, 7)
	}
	d := abs(x)
	lit := float32(0)
	age := mod(w.time, w.period)
	for age*w.speed < halfWidth+12 && age <= w.time+1e-4 {
		radius := age * w.speed
		fade := smoothstep(0, ringBirth, age) * (1 - 0.6*smoothstep(0.3*halfWidth, halfWidth, radius))
		lit += bump(d-radius, 6) * fade
		age += w.period
	}
	return min(lit, 1)
}

type envelopeKind uint8

const (
	envelopeEnter envelopeKind = iota
	envelopeProcess
	envelopeWork
	envelopeClear
)

type envelope struct {
	kind                      envelopeKind
	reach, head, unfold       float32
	iron, front               float32
	working                   working
	swell, halfWidth          float32
	beam, beamAt, field, glow float32
}
type scan struct {
	helix, decay, tail, grid float32
	envelope                 envelope
}

func (s scan) at(x float32) (amplitude, brightness, swell float32) {
	switch s.envelope.kind {
	case envelopeEnter:
		d := abs(x)
		drawn := s.envelope.unfold * (1 - smoothstep(max(s.envelope.reach-14, 0), max(s.envelope.reach, 0.001), d))
		amplitude = drawn * (1 - smoothstep(max(s.envelope.reach-22, 0), max(s.envelope.reach-3, 0.001), d))
		beam := s.envelope.head * bump(d-s.envelope.reach, 6)
		swell = 1.8 * s.envelope.unfold * (s.envelope.head / 2.8) * bump(d-max(s.envelope.reach-14, 0), 14)
		brightness = amplitude + beam
	case envelopeProcess:
		amplitude = s.envelope.iron
		brightness = amplitude + s.envelope.head*bump(x-s.envelope.front, 5)
	case envelopeWork:
		light := s.envelope.working.light(x, s.envelope.halfWidth)
		amplitude = 1
		brightness = 1 + s.envelope.head*light
		swell = s.envelope.swell * light
	case envelopeClear:
		d := abs(x)
		keep := s.envelope.field * smoothstep(s.envelope.front-12, s.envelope.front-2, d)
		light := s.envelope.working.light(x, s.envelope.halfWidth)
		amplitude = keep
		brightness = keep*(1+s.envelope.glow*light) + s.envelope.head*bump(d-s.envelope.front, 6) + s.envelope.beam*bump(x-s.envelope.beamAt, 5)
		swell = keep * s.envelope.swell * light
	}
	return
}

func exp(x float32) float32  { return float32(math.Exp(float64(x))) }
func sqrt(x float32) float32 { return float32(math.Sqrt(float64(x))) }
func sinCos(x float32) (float32, float32) {
	return float32(math.Sin(float64(x))), float32(math.Cos(float64(x)))
}
func sin(x float32) float32    { return float32(math.Sin(float64(x))) }
func cos(x float32) float32    { return float32(math.Cos(float64(x))) }
func mod(x, y float32) float32 { return float32(math.Mod(float64(x), float64(y))) }
func remEuclid(x, y float32) float32 {
	r := mod(x, y)
	if r < 0 {
		r += abs(y)
	}
	return r
}
func abs(x float32) float32              { return float32(math.Abs(float64(x))) }
func clamp(x, low, high float32) float32 { return min(max(x, low), high) }
func finite32(x float32) bool            { return !float32NaN(x) && !float32Inf(x) }
func float32NaN(x float32) bool          { return x != x }
func float32Inf(x float32) bool          { return math.IsInf(float64(x), 0) }

func (m *Model) tick(frame Frame) {
	h, tuning := step, m.tuning
	recording := m.stage == StageRecording && frame.Visible
	level := float32(0)
	if recording {
		loudness := frame.LoudnessDB
		if !finite32(loudness) {
			loudness = -120
		}
		if loudness > 0 {
			loudness = 0
		}
		if tuning.AutoLevel {
			m.level = m.auto.step(loudness, h, tuning.AutoRelease, tuning.NoiseFloor)
		} else {
			m.level = fixedLevel(loudness)
		}
		level = clamp(m.level*tuning.Gain, 0, 1)
	} else {
		m.level = 0
	}
	m.voices[0].step(level, h)
	m.voices[1].step(level*0.85, h)
	liveTarget := float32(0)
	if recording {
		liveTarget = 1
	}
	m.live += (liveTarget - m.live) * (1 - exp(-h/0.55))
	speed := tuning.BraidSpeed
	if frame.ReduceMotion {
		speed = 0
	}
	for i := range m.traces {
		m.traces[i].tick(&m.voices[i], m.live, speed, &tuning, h)
	}
	energy := max(m.voices[0].energy, m.voices[1].energy)
	working := m.stage == StageProcessing || m.stage == StageDone
	flowing := working && tuning.Processing == ProcessingFlow
	flowTarget := float32(0)
	if flowing {
		flowTarget = 1
	}
	m.flowTurn += (flowTarget - m.flowTurn) * (1 - exp(-h/0.25))
	if working {
		m.flowOffset += m.flowSpeed() * h
	}
	rest := carrierRest + 0.22*energy
	turn := rest + (m.flowTurnHz()-rest)*m.flowTurn
	m.carrier = mod(m.carrier+h*tau*turn*speed, tau)

	width := tuning.Width
	if m.stage == StageProcessing || m.stage == StageDone {
		width = tuning.ProcessingWidth
	}
	if m.stage == StageFailed {
		width = m.width.target
	}
	cool, failure := float32(0), float32(0)
	if m.stage == StageProcessing || m.stage == StageDone {
		cool = 1
	}
	if m.stage == StageFailed {
		failure = 1
	}
	shown := float32(0)
	if frame.Visible {
		shown = 1
	}
	fade := springSpec{duration: tuning.Transition}
	bouncy := springSpec{duration: tuning.Transition, bounce: tuning.Bounce}
	if frame.ReduceMotion {
		m.width.jump(width)
		m.scale.jump(1)
		m.softness.jump(0)
		m.opacity.stepTo(shown, springSpec{duration: 0.16}, h)
		m.cool.stepTo(cool, springSpec{duration: 0.16}, h)
		m.fail.stepTo(failure, springSpec{duration: 0.16}, h)
	} else {
		m.width.stepTo(width, bouncy, h)
		m.opacity.stepTo(shown, fade, h)
		scale, softness := hiddenScale, hiddenSoftness
		if frame.Visible {
			scale, softness = 1, 0
		}
		m.scale.stepTo(scale, bouncy, h)
		m.softness.stepTo(softness, fade, h)
		m.cool.stepTo(cool, fade, h)
		m.fail.stepTo(failure, fade, h)
	}
	m.elapsed += h
	m.time += h
	m.steps++
	if m.steps%2 == 0 {
		m.beam(2 * h)
	}
}

func (m *Model) flowSpacing() float32 { return flowSpacing * m.tuning.ProcessingWidth }
func (m *Model) flowSpeed() float32   { return m.flowSpacing() / m.workPeriod() }
func (m *Model) workPeriod() float32  { return 2 * max(m.tuning.ShimmerPass, 0.05) }
func (m *Model) flowTurnHz() float32  { return m.flowSpeed() * m.tuning.HelixCycles / max(m.width.x, 1) }
func (m *Model) pulseSpeed() float32 {
	return (m.tuning.ProcessingWidth/2 + 12) / (1.4 * m.workPeriod())
}

func (m *Model) workingAt(time float32) working {
	if m.tuning.Processing == ProcessingPulse {
		return working{kind: workingPulse, time: time, period: m.workPeriod(), speed: m.pulseSpeed()}
	}
	return working{kind: workingFlow, offset: m.flowOffset, spacing: m.flowSpacing()}
}

func (m *Model) armDone() {
	m.speedAtDone = 1.5 * m.pulseSpeed()
	m.doneFrom = 0
	if m.stage == StageProcessing {
		m.doneFrom = m.elapsed
	}
}

func (m *Model) processHead(elapsed float32) float32 {
	pass := max(m.tuning.ShimmerPass, 0.05)
	return (2.2 + abs(sin(pi*(elapsed/pass+0.5)))) * smoothstep(0, 0.14, elapsed) * m.tuning.Shimmer
}

func (m *Model) processFront(elapsed, width float32) float32 {
	pass := max(m.tuning.ShimmerPass, 0.05)
	position := 0.5 - 0.5*cos(pi*(elapsed/pass+0.5))
	return -width/2 + passInset + (width-2*passInset)*position
}

func (m *Model) scan(width float32) scan {
	e := m.elapsed
	still := func(helix float32) scan {
		return scan{helix: helix, decay: 1, tail: 1, grid: 1, envelope: envelope{kind: envelopeEnter, reach: 1e4, unfold: 1}}
	}
	if m.stage == StageFailed || m.reduceMotion {
		return still(1)
	}
	switch m.stage {
	case StageRecording:
		return scan{helix: 1, decay: 1, tail: 1, grid: min(e/0.3, 1), envelope: envelope{
			kind: envelopeEnter, reach: (width/2 + 12) * passPosition(e/enterDuration),
			head: 2.8 * (1 - smoothstep(0.08, enterDuration, e)), unfold: smoothstep(0, 0.1, e),
		}}
	case StageProcessing:
		if m.tuning.Processing != ProcessingSweep {
			enter := smoothstep(0, 0.35, e)
			return scan{helix: 1 + (workHelix-1)*enter, decay: 1, tail: 1, grid: 1, envelope: envelope{
				kind: envelopeWork, working: m.workingAt(e),
				head: 2.5 * smoothstep(0.05, 0.3, e) * m.tuning.Shimmer, swell: 0.9 * enter, halfWidth: width / 2,
			}}
		}
		iron := 1 - smoothstep(0, 0.28, e)
		return scan{helix: iron, decay: 1, tail: 1, grid: 1, envelope: envelope{
			kind: envelopeProcess, iron: iron, front: m.processFront(e, width), head: m.processHead(e),
		}}
	case StageDone:
		target := width/2 + 8
		front := hermite(0, m.speedAtDone, target, e/wipeDuration)
		sweep := m.tuning.Processing == ProcessingSweep
		helix, field, glow := workHelix, float32(1), float32(2.5)
		if sweep {
			helix, field, glow = 0, 0, 0
		}
		beam := float32(0)
		if sweep && m.doneFrom > 0 {
			beam = m.processHead(m.doneFrom+e) * (1 - smoothstep(0, beamFade, e))
		}
		return scan{helix: helix, decay: 1 - 0.6*smoothstep(0, 0.12, e), tail: 1 - smoothstep(0, 0.15, e), grid: 1 - smoothstep(0.2, 0.4, e), envelope: envelope{
			kind: envelopeClear, front: front,
			head: 2.8 * smoothstep(0, 0.04, e) * (1 - smoothstep(0.22, 0.32, e)),
			beam: beam, beamAt: m.processFront(m.doneFrom+e, width), field: field,
			working: m.workingAt(m.doneFrom + e), glow: glow * m.tuning.Shimmer * (1 - smoothstep(0, 0.18, e)),
			swell: 0.9, halfWidth: width / 2,
		}}
	default:
		return still(1)
	}
}

func (m *Model) beam(hb float32) {
	columns, rows, sub := m.columns, m.rows, samplesPerColumn
	samples := (columns-1)*sub + 1
	pitch := m.tuning.Spacing
	width := max(m.width.x, m.tuning.Height)
	current := m.scan(width)
	m.grid = current.grid
	mid := float32(rows-1) / 2
	half := mid - 0.55
	breath := float32(0.5)
	if !m.reduceMotion {
		breath = 0.5 + 0.5*sin(m.time*tau/breathPeriod)
	}
	rest := (0.55 + 0.35*breath) * (0.4 + 0.6*m.live) * current.helix
	halfWidth := width / 2
	for k := 0; k < 2; k++ {
		tr := &m.traces[k]
		sign := float32(1)
		if k == 1 {
			sign = -1
		}
		ys, brightness := m.ys[k], m.brightness[k]
		for s := 0; s < samples; s++ {
			x := (float32(s)/float32(sub) - float32(columns-1)/2) * pitch
			if abs(x) > halfWidth {
				ys[s], brightness[s] = mid, 0
				continue
			}
			amplitude, bright, swell := current.at(x)
			own := abs(x)
			xn := (x + halfWidth) / width
			ends := min(smoothstep(-halfWidth+3, -halfWidth+17, x), smoothstep(-halfWidth+3, -halfWidth+17, -x))
			reach := 1 - 0.8*smoothstep(14, halfWidth, own)
			delay := own / m.tuning.HistorySpeed
			a := tr.past(delay, 0)
			spread := float32(0)
			for c, spatial := range traceSpatial {
				spread += tr.past(delay, c+1) * sin(tau*spatial*xn-tr.phases[c])
			}
			helix := (rest + swell + half*0.55*a*reach) * sin(tau*m.tuning.HelixCycles*xn-m.carrier)
			y := half * tanh((helix+half*0.7*spread*reach)/half) * ends * amplitude
			ys[s] = mid - sign*y
			spatial := (0.3 + 0.7*min(a*1.8, 1)) * (0.45 + 0.55*ends)
			brightness[s] = spatial*amplitude + 0.36*max(bright-amplitude, 0)
		}
		for s := 0; s < samples; s++ {
			before, after := ys[max(s-1, 0)], ys[min(s+1, samples-1)]
			slope := (after - before) * float32(sub) / 2
			brightness[s] *= 1 - 0.55 + 0.55/sqrt(1+0.22*slope*slope)
		}
	}

	headKeep := exp(-hb / (phosphorHead * current.decay))
	tailKeep := exp(-hb / (m.tuning.Phosphor * current.decay))
	dx := float32(1) / float32(sub)
	for i := 0; i < columns; i++ {
		s0, s1 := max(i-3, 0)*sub, min((i+3)*sub, samples-1)
		for j := 0; j < rows; j++ {
			n := j*columns + i
			now, total := float32(0), float32(0)
			lit := [2]float32{}
			for k := 0; k < 2; k++ {
				ys := m.ys[k]
				best, nearest := float32(math.MaxFloat32), i*sub
				for s := s0; s < s1; s++ {
					ay, dy := ys[s], ys[s+1]-ys[s]
					px, py := float32(i)-float32(s)*dx, float32(j)-ay
					t := clamp((px*dx+py*dy)/(dx*dx+dy*dy), 0, 1)
					ex, ey := px-t*dx, py-t*dy
					d2 := ex*ex + ey*ey
					if d2 < best {
						best, nearest = d2, s
					}
				}
				brightness := m.brightness[k][nearest]
				onBeam := exp(-best / 0.25)
				instant := min(onBeam+0.5*exp(-sqrt(best)/0.8), 1.2) * brightness
				core := onBeam * brightness
				m.head[k][n] = max(m.head[k][n]*headKeep, core)
				m.tail[k][n] = max(m.tail[k][n]*tailKeep, core)
				light := max(instant, max(m.head[k][n], 0.55*current.tail*m.tail[k][n]))
				lit[k] = light
				now += instant
				total += light
			}
			power := 1 - (1-min(lit[0], 1))*(1-min(lit[1], 1))
			m.dots[n] = [2]float32{clamp(power, 0, 1), min(now/max(total, 1e-4), 1)}
		}
	}
}

func tanh(x float32) float32 { return float32(math.Tanh(float64(x))) }
