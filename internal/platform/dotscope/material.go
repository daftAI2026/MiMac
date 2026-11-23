package dotscope

import (
	"image"
	"math"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// The Dot scope material equations and palette are adapted under the MIT
// license. Copyright (c) 2026 Christopher Langton. See LICENSE.txt.

const materialShaderSource = `package main

var Width float
var Height float
var Opacity float
var Scale float
var Softness float
var Pitch float
var Columns float
var Rows float
var Glow float
var Grid float
var Live float
var Cool float
var Dark float
var Base vec4
var Core vec4
var Plate vec4
var Rim vec4
var Shadow vec4

func dotComponent(index int) float {
	x := index%64
	y := index/64
	encoded := image0At(vec2(float(x), float(y)))
	return (floor(encoded.r*255+0.5)*256+floor(encoded.g*255+0.5))/65535
}

func dotValue(index int) vec2 {
	return vec2(dotComponent(index*2), dotComponent(index*2+1))
}

func sstep(edge0 float, edge1 float, x float) float {
	t := clamp((x-edge0)/(edge1-edge0), 0, 1)
	return t*t*(3-2*t)
}

func roundedBox(point vec2, halfSize vec2, radius float) float {
	q := abs(point)-halfSize+vec2(radius)
	return length(max(q, vec2(0)))+min(max(q.x, q.y), 0)-radius
}

func coverage(distance float, softness float) float {
	return sstep(softness, -softness, distance)
}

func over(destination vec4, color vec3, alphaIn float) vec4 {
	alpha := clamp(alphaIn, 0, 1)
	return vec4(color*alpha+destination.rgb*(1-alpha), alpha+destination.a*(1-alpha))
}

func dithered(resultIn vec4, position vec4) vec4 {
	result := resultIn
	if result.a > 0 {
		pixel := floor(position.xy)
		n1 := fract(52.9829189*fract(dot(pixel, vec2(0.06711056, 0.00583715))))
		n2 := fract(52.9829189*fract(dot(pixel+vec2(17, 31), vec2(0.06711056, 0.00583715))))
		dither := ((n1-n2)/255)*result.a
		result = vec4(clamp(result.rgb+vec3(dither), vec3(0), vec3(result.a)), result.a)
	}
	return result
}

// Shadow's source shape is softened with tanh; this logistic form keeps the
// same transfer curve in the portable shader language.
func shadowFalloff(distance float) float {
	x := distance/6.5
	return 1/(1+exp(2*x))
}

func Fragment(dst vec4, pos vec2) vec4 {
	scale := max(Scale, 0.001)
	point := (pos-rectSize()*0.5)/scale
	pixel := 1/(pixelScale()*scale)
	aa := pixel*0.75+max(Softness, 0)*0.72
	halfSize := vec2(Width, Height)*0.5
	radius := clamp(Height*0.5, 0, Height*0.5)
	distance := roundedBox(point, halfSize, radius)
	columns := int(Columns+0.5)
	rows := int(Rows+0.5)
	pitch := max(Pitch, 0.001)
	centerOffset := vec2(float(columns-1), float(rows-1))*0.5
	i0 := int(floor(point.x/pitch+centerOffset.x+0.5))
	j0 := int(floor(point.y/pitch+centerOffset.y+0.5))
	unlit := mix(0.085, 0.07, Dark)
	full := mix(1, 0.96, Dark)
	curve := mix(0.75, 0.9, Dark)
	presence := 0.5+0.5*max(Live, Cool)
	var result vec4
	result = over(result, Shadow.rgb, Shadow.a*shadowFalloff(roundedBox(point-vec2(0, 3), halfSize, radius)))
	shape := coverage(distance, aa)
	result = over(result, Plate.rgb, Plate.a*shape)
	result = over(result, Rim.rgb, Rim.a*coverage(abs(distance)-0.3, aa))
	if Glow > 0.01 {
		reach := 34+20*Glow
		t := clamp(length(point)/reach, 0, 1)
		strength := mix(0.19, 0.3, Dark)*0.55*min(1, Glow)
		middle := mix(Base.rgb, Core.rgb, 0.5)
		result = over(result, mix(middle, Base.rgb, t), strength*(1-t)*shape)
	}
	for dj := -1; dj <= 1; dj++ {
		for di := -1; di <= 1; di++ {
			i := i0+di
			j := j0+dj
			if i < 0 || j < 0 || i >= columns || j >= rows {
				continue
			}
			center := (vec2(float(i), float(j))-centerOffset)*pitch
			cap := -roundedBox(center, halfSize, radius)
			mask := sstep(1.4, 4.6, cap)
			if mask <= 0 {
				continue
			}
			index := j*columns+i
			value := dotValue(index)
			power := max(value.x, 0.00001)
			fresh := clamp(value.y, 0, 1)
			row := abs(center.y)/max(halfSize.y, 0.001)
			floorAlpha := unlit*(1-0.7*sstep(0.35, 0.95, row))*presence*Grid
			alpha := floorAlpha+(full-floorAlpha)*pow(power, curve*mix(1.4, 1, fresh))
			dotRadius := (0.25+0.17*pow(power, 0.6))*mix(1, mix(0.82, 1, fresh), sstep(0.03, 0.2, power))*pitch
			core := clamp(power*power*1.1, 0, 0.9)*mix(0.3, 1, fresh)
			color := mix(Base.rgb, Core.rgb, core)
			fromCenter := length(point-center)
			if power > 0.05 {
				result = over(result, color, 0.132*power*power*mask*shape*coverage(fromCenter-dotRadius*2, aa))
			}
			result = over(result, color, alpha*mask*shape*coverage(fromCenter-dotRadius, aa))
		}
	}
	result = result*Opacity
	return dithered(result, dst)
}
`

const (
	dotTextureWidth  = 64
	dotTextureHeight = 32
)

type materialUniformSet struct {
	width, height, opacity, scale, softness, pitch, columns, rows shader.Uniform
	glow, grid, live, cool, dark                                  shader.Uniform
	base, core, plate, rim, shadow                                shader.Uniform
}

type materialProgram struct {
	shader   *shader.Shader
	uniforms materialUniformSet
}

var compileMaterial = sync.OnceValues(func() (*materialProgram, error) {
	compiled, err := shader.Compile(materialShaderSource)
	if err != nil {
		return nil, err
	}
	uniform := func(name string) shader.Uniform { return compiled.Uniform(name) }
	return &materialProgram{
		shader: compiled,
		uniforms: materialUniformSet{
			width: uniform("Width"), height: uniform("Height"),
			opacity: uniform("Opacity"), scale: uniform("Scale"),
			softness: uniform("Softness"), pitch: uniform("Pitch"),
			columns: uniform("Columns"), rows: uniform("Rows"),
			glow: uniform("Glow"), grid: uniform("Grid"),
			live: uniform("Live"), cool: uniform("Cool"), dark: uniform("Dark"),
			base: uniform("Base"), core: uniform("Core"), plate: uniform("Plate"),
			rim: uniform("Rim"), shadow: uniform("Shadow"),
		},
	}, nil
})

// Renderer keeps reusable shader values and an 8 KiB dot image for one paint
// stream. The image has fixed dimensions, so painting updates its existing
// texture. Renderer must be used by one paint thread at a time.
type Renderer struct {
	program *materialProgram
	values  *shader.Values
	pixels  *image.RGBA
	bitmap  *ui.Bitmap
}

// NewRenderer compiles the shader once, on the first call, and returns a
// reusable renderer for one panel or other paint stream.
func NewRenderer() (*Renderer, error) {
	program, err := compileMaterial()
	if err != nil {
		return nil, err
	}
	pixels := image.NewRGBA(image.Rect(0, 0, dotTextureWidth, dotTextureHeight))
	bitmap := ui.NewBitmap(pixels)
	return &Renderer{program: program, values: program.shader.NewValues(), pixels: pixels, bitmap: bitmap}, nil
}

// Draw paints the current Dot scope snapshot into bounds. dark selects the
// system appearance palette. Snapshot's dots are copied before this call
// returns, so the model may advance immediately after Draw.
func (r *Renderer) Draw(p *ui.Painter, bounds ui.Rect, snapshot Snapshot, dark bool) {
	if r == nil || p == nil || bounds.W <= 0 || bounds.H <= 0 || snapshot.Columns <= 0 || snapshot.Rows <= 0 {
		return
	}
	if r.program == nil || r.values == nil || r.pixels == nil || r.bitmap == nil {
		return
	}

	columns := min(snapshot.Columns, MaxDots)
	rows := min(snapshot.Rows, MaxDots/columns)
	count := min(columns*rows, min(len(snapshot.Dots), MaxDots))
	for i, dot := range snapshot.Dots[:count] {
		storeDotComponent(r.pixels.Pix, 2*i, finiteClamp(dot[0], 0, 1))
		storeDotComponent(r.pixels.Pix, 2*i+1, finiteClamp(dot[1], 0, 1))
	}
	for i := count; i < MaxDots; i++ {
		storeDotComponent(r.pixels.Pix, 2*i, 0)
		storeDotComponent(r.pixels.Pix, 2*i+1, 0)
	}
	r.bitmap.Update(r.pixels)

	pose := snapshot.Pose
	width := positive(pose.Width, 112)
	height := positive(pose.Height, PlateHeight)
	pitch := positive(snapshot.Pitch, 2.6)
	palette := materialPalette(dark, pose.Cool, pose.Fail)

	u := r.program.uniforms
	r.values.
		Float(u.width, width).
		Float(u.height, height).
		Float(u.opacity, finiteClamp(pose.Opacity, 0, 1)).
		Float(u.scale, positive(pose.Scale, 0.001)).
		Float(u.softness, finiteClamp(pose.Softness, 0, 64)).
		Float(u.pitch, pitch).
		Float(u.columns, float32(columns)).
		Float(u.rows, float32(rows)).
		Float(u.glow, finiteClamp(pose.Glow, 0, 3)).
		Float(u.grid, finiteClamp(pose.Grid, 0, 1)).
		Float(u.live, finiteClamp(pose.Live, 0, 1)).
		Float(u.cool, finiteClamp(pose.Cool, 0, 1)).
		Float(u.dark, boolFloat(dark)).
		Vec4(u.base, palette.base[0], palette.base[1], palette.base[2], 1).
		Vec4(u.core, palette.core[0], palette.core[1], palette.core[2], 1).
		Vec4(u.plate, palette.plate[0], palette.plate[1], palette.plate[2], palette.plate[3]).
		Vec4(u.rim, palette.rim[0], palette.rim[1], palette.rim[2], palette.rim[3]).
		Vec4(u.shadow, palette.shadow[0], palette.shadow[1], palette.shadow[2], palette.shadow[3])

	r.program.shader.Draw(p, bounds, shader.Options{Values: r.values, Images: [4]*ui.Bitmap{r.bitmap}})
}

func storeDotComponent(pix []byte, index int, value float32) {
	value16 := uint16(math.Round(float64(finiteClamp(value, 0, 1) * 65535)))
	offset := index * 4
	pix[offset] = byte(value16 >> 8)
	pix[offset+1] = byte(value16)
	pix[offset+2] = 0
	pix[offset+3] = 255
}

type materialColors struct {
	base, core         [3]float32
	plate, rim, shadow [4]float32
}

func materialPalette(dark bool, cool, fail float32) materialColors {
	var you, youCore, agent, agentCore, coral, coralCore [3]float32
	if dark {
		you = [3]float32{0.86, 0.83, 0.77}
		youCore = [3]float32{0.98, 0.96, 0.92}
		agent = [3]float32{0.70, 0.78, 0.90}
		agentCore = [3]float32{0.92, 0.95, 1.0}
		coral = [3]float32{1.0, 0.38, 0.31}
		coralCore = [3]float32{1.0, 0.66, 0.6}
	} else {
		you = [3]float32{0.62, 0.53, 0.40}
		youCore = [3]float32{0.47, 0.38, 0.26}
		agent = [3]float32{0.40, 0.47, 0.58}
		agentCore = [3]float32{0.24, 0.31, 0.43}
		coral = [3]float32{0.9, 0.27, 0.21}
		coralCore = [3]float32{0.7, 0.18, 0.13}
	}
	cool, fail = finiteClamp(cool, 0, 1), finiteClamp(fail, 0, 1)
	base := mix3(mix3(you, agent, cool), coral, fail)
	core := mix3(mix3(youCore, agentCore, cool), coralCore, fail)
	colors := materialColors{base: base, core: core}
	if dark {
		colors.plate = [4]float32{17.0 / 255, 17.0 / 255, 20.0 / 255, 0.94}
		colors.rim = [4]float32{1, 1, 1, 0.11}
		colors.shadow = [4]float32{0, 0, 0, 0.5}
	} else {
		colors.plate = [4]float32{252.0 / 255, 251.0 / 255, 249.0 / 255, 0.96}
		colors.rim = [4]float32{0, 0, 0, 0.09}
		colors.shadow = [4]float32{20.0 / 255, 24.0 / 255, 40.0 / 255, 0.2}
	}
	return colors
}

func mix3(a, b [3]float32, t float32) [3]float32 {
	return [3]float32{
		a[0] + (b[0]-a[0])*t,
		a[1] + (b[1]-a[1])*t,
		a[2] + (b[2]-a[2])*t,
	}
}

func finiteClamp(value, low, high float32) float32 {
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
		return low
	}
	return min(max(value, low), high)
}

func positive(value, fallback float32) float32 {
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value <= 0 {
		return fallback
	}
	return value
}

func boolFloat(value bool) float32 {
	if value {
		return 1
	}
	return 0
}
