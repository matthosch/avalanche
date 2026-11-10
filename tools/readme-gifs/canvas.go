package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"io"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// rgb is an opaque colour.
type rgb struct{ r, g, b uint8 }

func hex(s string) rgb {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	if err != nil {
		panic(err)
	}
	return rgb{uint8(v >> 16), uint8(v >> 8), uint8(v)}
}

// mix is a blended toward b by t (0 = a, 1 = b).
func mix(a, b rgb, t float64) rgb {
	l := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return rgb{l(a.r, b.r), l(a.g, b.g), l(a.b, b.b)}
}

// The theme: a dark card that reads well on GitHub's light and dark README backgrounds.
var (
	colBG     = hex("#0d1117")
	colPanel  = hex("#161b22")
	colPanel2 = hex("#1f2630")
	colBorder = hex("#30363d")
	colText   = hex("#e6edf3")
	colMuted  = hex("#8b949e")
	colBlue   = hex("#58a6ff")
	colGreen  = hex("#3fb950")
	colRed    = hex("#f85149")
	colAmber  = hex("#d29922")
	colPurple = hex("#bc8cff")
	colCyan   = hex("#39c5cf")
)

// palette is every base colour blended toward every accent, so anti-aliased edges on any
// background quantize without dithering noise.
func palette() color.Palette {
	bases := []rgb{colBG, colPanel, colPanel2}
	accents := []rgb{colBorder, colText, colMuted, colBlue, colGreen, colRed, colAmber, colPurple, colCyan}
	var p color.Palette
	seen := map[rgb]bool{}
	add := func(c rgb) {
		if !seen[c] {
			seen[c] = true
			p = append(p, color.RGBA{c.r, c.g, c.b, 255})
		}
	}
	for _, b := range bases {
		add(b)
	}
	for _, b := range bases {
		for _, a := range accents {
			for i := 1; i <= 9; i++ {
				add(mix(b, a, float64(i)/9))
			}
		}
	}
	if len(p) > 256 {
		panic(fmt.Sprintf("palette has %d colours", len(p)))
	}
	return p
}

// Fonts: the Go fonts, embedded, so output is the same on every machine.
type fontKind int

const (
	regular fontKind = iota
	bold
	mono
	monoBold
)

var (
	parsed = map[fontKind]*opentype.Font{}
	faces  = map[[2]int]font.Face{}
)

func face(kind fontKind, size float64) font.Face {
	key := [2]int{int(kind), int(size * 10)}
	if f, ok := faces[key]; ok {
		return f
	}
	f, ok := parsed[kind]
	if !ok {
		data := map[fontKind][]byte{regular: goregular.TTF, bold: gobold.TTF, mono: gomono.TTF, monoBold: gomonobold.TTF}[kind]
		var err error
		if f, err = opentype.Parse(data); err != nil {
			panic(err)
		}
		parsed[kind] = f
	}
	ff, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	faces[key] = ff
	return ff
}

// canvas is one frame being drawn.
type canvas struct {
	img  *image.RGBA
	w, h int
}

func newCanvas(w, h int) *canvas {
	return &canvas{img: image.NewRGBA(image.Rect(0, 0, w, h)), w: w, h: h}
}

func (c *canvas) clear(col rgb) {
	draw.Draw(c.img, c.img.Bounds(), image.NewUniform(color.RGBA{col.r, col.g, col.b, 255}), image.Point{}, draw.Src)
}

// path is a vector outline, filled with canvas.fill.
type path struct {
	ops [][]float64 // kind, then points
}

func (p *path) moveTo(x, y float64) { p.ops = append(p.ops, []float64{0, x, y}) }
func (p *path) lineTo(x, y float64) { p.ops = append(p.ops, []float64{1, x, y}) }
func (p *path) cubeTo(ax, ay, bx, by, x, y float64) {
	p.ops = append(p.ops, []float64{2, ax, ay, bx, by, x, y})
}
func (p *path) close() { p.ops = append(p.ops, []float64{3}) }

func (c *canvas) fill(p *path, col rgb, alpha float64) {
	if alpha <= 0 || len(p.ops) == 0 {
		return
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, op := range p.ops {
		for i := 1; i+1 < len(op); i += 2 {
			minX, maxX = math.Min(minX, op[i]), math.Max(maxX, op[i])
			minY, maxY = math.Min(minY, op[i+1]), math.Max(maxY, op[i+1])
		}
	}
	x0, y0 := int(math.Floor(minX))-1, int(math.Floor(minY))-1
	x1, y1 := int(math.Ceil(maxX))+1, int(math.Ceil(maxY))+1
	rect := image.Rect(x0, y0, x1, y1).Intersect(c.img.Bounds())
	if rect.Empty() {
		return
	}
	w, h := x1-x0, y1-y0
	z := vector.NewRasterizer(w, h)
	fx, fy := float64(x0), float64(y0)
	for _, op := range p.ops {
		switch op[0] {
		case 0:
			z.MoveTo(float32(op[1]-fx), float32(op[2]-fy))
		case 1:
			z.LineTo(float32(op[1]-fx), float32(op[2]-fy))
		case 2:
			z.CubeTo(float32(op[1]-fx), float32(op[2]-fy), float32(op[3]-fx), float32(op[4]-fy), float32(op[5]-fx), float32(op[6]-fy))
		case 3:
			z.ClosePath()
		}
	}
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	src := image.NewUniform(color.NRGBA{col.r, col.g, col.b, uint8(math.Round(math.Min(alpha, 1) * 255))})
	draw.DrawMask(c.img, rect, src, image.Point{}, mask, rect.Min.Sub(image.Pt(x0, y0)), draw.Over)
}

const kappa = 0.5522847498

func roundRectPath(x, y, w, h, r float64) *path {
	r = math.Min(r, math.Min(w, h)/2)
	k := r * kappa
	p := &path{}
	p.moveTo(x+r, y)
	p.lineTo(x+w-r, y)
	p.cubeTo(x+w-r+k, y, x+w, y+r-k, x+w, y+r)
	p.lineTo(x+w, y+h-r)
	p.cubeTo(x+w, y+h-r+k, x+w-r+k, y+h, x+w-r, y+h)
	p.lineTo(x+r, y+h)
	p.cubeTo(x+r-k, y+h, x, y+h-r+k, x, y+h-r)
	p.lineTo(x, y+r)
	p.cubeTo(x, y+r-k, x+r-k, y, x+r, y)
	p.close()
	return p
}

func (c *canvas) roundRect(x, y, w, h, r float64, col rgb, alpha float64) {
	c.fill(roundRectPath(x, y, w, h, r), col, alpha)
}

// box is a rounded rectangle with a border of width bw.
func (c *canvas) box(x, y, w, h, r float64, fill, border rgb, bw float64) {
	c.roundRect(x, y, w, h, r, border, 1)
	c.roundRect(x+bw, y+bw, w-2*bw, h-2*bw, r-bw, fill, 1)
}

func (c *canvas) circle(cx, cy, r float64, col rgb, alpha float64) {
	c.roundRect(cx-r, cy-r, 2*r, 2*r, r, col, alpha)
}

// line is a straight stroke of width w.
func (c *canvas) line(x0, y0, x1, y1, w float64, col rgb, alpha float64) {
	dx, dy := x1-x0, y1-y0
	l := math.Hypot(dx, dy)
	if l == 0 {
		return
	}
	nx, ny := -dy/l*w/2, dx/l*w/2
	p := &path{}
	p.moveTo(x0+nx, y0+ny)
	p.lineTo(x1+nx, y1+ny)
	p.lineTo(x1-nx, y1-ny)
	p.lineTo(x0-nx, y0-ny)
	p.close()
	c.fill(p, col, alpha)
}

func (c *canvas) dashed(x0, y0, x1, y1, w, dash, gap float64, col rgb, alpha float64) {
	l := math.Hypot(x1-x0, y1-y0)
	for s := 0.0; s < l; s += dash + gap {
		e := math.Min(s+dash, l)
		c.line(x0+(x1-x0)*s/l, y0+(y1-y0)*s/l, x0+(x1-x0)*e/l, y0+(y1-y0)*e/l, w, col, alpha)
	}
}

// Text alignment.
const (
	left = iota
	center
	right
)

func textWidth(f font.Face, s string) float64 {
	return float64(font.MeasureString(f, s)) / 64
}

// text draws s with its baseline at y.
func (c *canvas) text(s string, x, y float64, f font.Face, col rgb, alpha float64, align int) {
	for _, r := range s {
		if _, ok := f.GlyphAdvance(r); !ok {
			panic(fmt.Sprintf("font has no glyph for %q in %q", r, s))
		}
	}
	switch align {
	case center:
		x -= textWidth(f, s) / 2
	case right:
		x -= textWidth(f, s)
	}
	d := font.Drawer{
		Dst:  c.img,
		Src:  image.NewUniform(color.NRGBA{col.r, col.g, col.b, uint8(math.Round(math.Min(alpha, 1) * 255))}),
		Face: f,
		Dot:  fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)},
	}
	d.DrawString(s)
}

// wrap breaks s into lines no wider than width.
func wrap(f font.Face, s string, width float64) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		try := strings.TrimSpace(cur + " " + word)
		if cur != "" && textWidth(f, try) > width {
			lines = append(lines, cur)
			cur = word
		} else {
			cur = try
		}
	}
	return append(lines, cur)
}

// pill is a label on a rounded, filled background, centred on x with its middle at y.
func (c *canvas) pill(s string, x, y float64, f font.Face, fill, fg rgb, alpha float64) {
	w := textWidth(f, s) + 16
	x = math.Max(8+w/2, math.Min(float64(c.w)-8-w/2, x))
	c.roundRect(x-w/2, y-11, w, 22, 11, fill, alpha)
	c.text(s, x, y+4.5, f, fg, alpha, center)
}

// ease is a smooth 0→1 curve for t in [0, 1].
func ease(t float64) float64 {
	t = clamp01(t)
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}

func clamp01(t float64) float64 { return math.Max(0, math.Min(1, t)) }

// phase is how far t is through the window [start, start+dur], from 0 to 1.
func phase(t, start, dur float64) float64 { return clamp01((t - start) / dur) }

// encoder turns RGBA frames into a looping GIF, storing only the region of each frame that
// changed and merging identical frames into one longer one.
type encoder struct {
	pal    color.Palette
	lut    []uint16
	frames []*image.Paletted
	delays []int
	prev   *image.Paletted
}

func newEncoder() *encoder {
	return &encoder{pal: palette(), lut: make([]uint16, 1<<15)}
}

func (e *encoder) index(r, g, b uint8) uint8 {
	key := int(r>>3)<<10 | int(g>>3)<<5 | int(b>>3)
	if v := e.lut[key]; v != 0 {
		return uint8(v - 1)
	}
	i := e.pal.Index(color.RGBA{r, g, b, 255})
	e.lut[key] = uint16(i + 1)
	return uint8(i)
}

// add appends a frame shown for delay hundredths of a second.
func (e *encoder) add(img *image.RGBA, delay int) {
	b := img.Bounds()
	cur := image.NewPaletted(b, e.pal)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			o := img.PixOffset(x, y)
			cur.Pix[cur.PixOffset(x, y)] = e.index(img.Pix[o], img.Pix[o+1], img.Pix[o+2])
		}
	}
	if e.prev == nil {
		e.frames, e.delays, e.prev = append(e.frames, cur), append(e.delays, delay), cur
		return
	}
	changed := image.Rectangle{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := cur.PixOffset(b.Min.X, y)
		for x := 0; x < b.Dx(); x++ {
			if cur.Pix[row+x] != e.prev.Pix[row+x] {
				changed = changed.Union(image.Rect(b.Min.X+x, y, b.Min.X+x+1, y+1))
			}
		}
	}
	if changed.Empty() {
		e.delays[len(e.delays)-1] += delay
		return
	}
	e.frames = append(e.frames, cur.SubImage(changed).(*image.Paletted))
	e.delays = append(e.delays, delay)
	e.prev = cur
}

// hold lengthens the last frame, so the loop pauses on it.
func (e *encoder) hold(delay int) { e.delays[len(e.delays)-1] += delay }

func (e *encoder) write(w io.Writer, width, height int) error {
	return gif.EncodeAll(w, &gif.GIF{
		Image: e.frames, Delay: e.delays, LoopCount: 0,
		Config: image.Config{ColorModel: e.pal, Width: width, Height: height},
	})
}
