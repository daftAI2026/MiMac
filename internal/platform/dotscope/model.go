// Package dotscope draws the voice indicator from native simulation frames.
package dotscope

const (
	MaxDots     = 1024
	PlateHeight = float32(26)
	LibraryName = "libMiMacVoiceDots.dylib"
)

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
	Shift    float32
}

// Snapshot is the current renderer-neutral model output. Dots are row-major;
// each pair is [brightness, freshness], both normalized to 0..1. Dots aliases
// model storage until the next paint.
type Snapshot struct {
	Pose    Pose
	Columns int
	Rows    int
	Pitch   float32
	Dots    [][2]float32
}
