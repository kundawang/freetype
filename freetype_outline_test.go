// Copyright 2026 The Freetype-Go Authors. All rights reserved.
// Use of this source code is governed by your choice of either the
// FreeType License or the GNU General Public License version 2 (or
// any later version), both of which can be found in the LICENSE file.

package freetype

import (
	"image"
	"image/color"
	"image/draw"
	"io/ioutil"
	"testing"

	"github.com/golang/freetype/raster"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

var (
	outlineBg   = color.RGBA{0x00, 0x00, 0x00, 0xff} // black
	outlineEdge = color.RGBA{0xff, 0x00, 0x00, 0xff} // red
	outlineFill = color.RGBA{0xff, 0xff, 0xff, 0xff} // white
	outlineAlt  = color.RGBA{0x00, 0x00, 0xff, 0xff} // blue
)

func loadTestFont(t *testing.T) *truetype.Font {
	t.Helper()
	data, err := ioutil.ReadFile("testdata/luxisr.ttf")
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseFont(data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// render draws s at p onto a fresh w*h image whose background is outlineBg,
// and returns the image.
func render(t *testing.T, c *Context, s string, p fixed.Point26_6, w, h int) *image.RGBA {
	t.Helper()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(outlineBg), image.ZP, draw.Src)
	c.SetDst(dst)
	c.SetClip(dst.Bounds())
	if _, err := c.DrawString(s, p); err != nil {
		t.Fatalf("DrawString(%q): %v", s, err)
	}
	return dst
}

// newOutlineContext returns a Context drawing white fill (and optionally a
// red outline of the given width in points) in Luxi Sans at 72 DPI.
func newOutlineContext(t *testing.T, fontSize, outlineWidth float64) *Context {
	t.Helper()
	c := NewContext()
	c.SetFont(loadTestFont(t))
	c.SetFontSize(fontSize)
	c.SetSrc(image.NewUniform(outlineFill))
	if outlineWidth != 0 {
		c.SetOutlineWidth(outlineWidth)
		c.SetOutlineSrc(image.NewUniform(outlineEdge))
	}
	return c
}

// classify maps a pixel to 0 (background), 1 (outline) or 2 (fill), whichever
// of the three palette colors it is closest to.
func classify(c color.RGBA) int {
	dist := func(k color.RGBA) int {
		dr := int(c.R) - int(k.R)
		dg := int(c.G) - int(k.G)
		db := int(c.B) - int(k.B)
		return dr*dr + dg*dg + db*db
	}
	best, bestD := 0, dist(outlineBg)
	if d := dist(outlineEdge); d < bestD {
		best, bestD = 1, d
	}
	if d := dist(outlineFill); d < bestD {
		best, bestD = 2, d
	}
	return best
}

// nonBgBounds returns the bounding box of all non-background pixels.
func nonBgBounds(m *image.RGBA) image.Rectangle {
	b := m.Bounds()
	r := image.Rectangle{Min: b.Max, Max: b.Min}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if classify(m.RGBAAt(x, y)) != 0 {
				if x < r.Min.X {
					r.Min.X = x
				}
				if x >= r.Max.X {
					r.Max.X = x + 1
				}
				if y < r.Min.Y {
					r.Min.Y = y
				}
				if y >= r.Max.Y {
					r.Max.Y = y + 1
				}
			}
		}
	}
	return r
}

// countClass returns the number of pixels classified as k.
func countClass(m *image.RGBA, k int) int {
	n := 0
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if classify(m.RGBAAt(x, y)) == k {
				n++
			}
		}
	}
	return n
}

// rowRuns classifies every pixel on row y and returns the run-length encoded
// sequence of classes.
func rowRuns(m *image.RGBA, y int) [][2]int {
	b := m.Bounds()
	var runs [][2]int
	prev, n := -1, 0
	flush := func() {
		if prev >= 0 {
			runs = append(runs, [2]int{prev, n})
		}
	}
	for x := b.Min.X; x < b.Max.X; x++ {
		k := classify(m.RGBAAt(x, y))
		if k != prev {
			flush()
			prev, n = k, 0
		}
		n++
	}
	flush()
	return runs
}

// TestOutlineFollowsContours draws an "O" and checks, on a scanline through
// its middle, the exact sequence of background/outline/fill runs. This
// verifies that the outline hugs the glyph contours (including the counter),
// that the inner hole remains a hole, and that the fill is painted over the
// outline (fill between the outline runs, never the other way round).
func TestOutlineFollowsContours(t *testing.T) {
	c := newOutlineContext(t, 48, 6)
	m := render(t, c, "O", Pt(30, 200), 300, 300)

	bb := nonBgBounds(m)
	if bb.Empty() {
		t.Fatal("nothing was drawn")
	}
	y := (bb.Min.Y + bb.Max.Y) / 2
	runs := rowRuns(m, y)

	// From left to right we expect: background, outline, fill, outline,
	// background (the counter), outline, fill, outline, background.
	want := []int{0, 1, 2, 1, 0, 1, 2, 1, 0}
	if len(runs) != len(want) {
		t.Fatalf("row %d: got %d runs %v, want %d runs with classes %v", y, len(runs), runs, len(want), want)
	}
	for i, w := range want {
		if runs[i][0] != w {
			t.Fatalf("row %d: run %d is class %d (runs %v), want class %d", y, i, runs[i][0], runs, w)
		}
	}
	// The outline runs should be several pixels wide (6pt at 72 DPI), and
	// the hole run should be a substantial gap, not a sliver.
	for _, i := range []int{1, 3, 5, 7} {
		if runs[i][1] < 3 {
			t.Errorf("outline run %d is only %d px wide, want >= 3", i, runs[i][1])
		}
	}
	if runs[4][1] < 5 {
		t.Errorf("counter hole is only %d px wide, want >= 5", runs[4][1])
	}
}

// TestOutlineHolesPreserved checks that the counters of O, B and g remain
// enclosed background regions when outlined: a background pixel that cannot
// be reached from the image border without crossing outline or fill pixels.
func TestOutlineHolesPreserved(t *testing.T) {
	for _, s := range []string{"O", "B", "g"} {
		c := newOutlineContext(t, 48, 5)
		m := render(t, c, s, Pt(30, 200), 300, 300)

		b := m.Bounds()
		// Flood fill the background from the image border.
		seen := make([]bool, b.Dx()*b.Dy())
		idx := func(x, y int) int { return (y-b.Min.Y)*b.Dx() + (x - b.Min.X) }
		var queue [][2]int
		push := func(x, y int) {
			if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
				return
			}
			if seen[idx(x, y)] || classify(m.RGBAAt(x, y)) != 0 {
				return
			}
			seen[idx(x, y)] = true
			queue = append(queue, [2]int{x, y})
		}
		push(b.Min.X, b.Min.Y)
		push(b.Max.X-1, b.Min.Y)
		push(b.Min.X, b.Max.Y-1)
		push(b.Max.X-1, b.Max.Y-1)
		for len(queue) > 0 {
			p := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			push(p[0]+1, p[1])
			push(p[0]-1, p[1])
			push(p[0], p[1]+1)
			push(p[0], p[1]-1)
		}
		enclosed := 0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if !seen[idx(x, y)] && classify(m.RGBAAt(x, y)) == 0 {
					enclosed++
				}
			}
		}
		if enclosed == 0 {
			t.Errorf("%q: no enclosed background pixels; the glyph's counter was filled in", s)
		}
	}
}

// TestOutlineExtendsOutward checks that the outlined glyph's bounding box
// grows by roughly the outline width on every side, i.e. the outline expands
// along the contours rather than sitting inside the fill.
func TestOutlineExtendsOutward(t *testing.T) {
	const width = 8.0 // points == pixels at 72 DPI
	plain := render(t, newOutlineContext(t, 48, 0), "g", Pt(50, 150), 300, 300)
	outlined := render(t, newOutlineContext(t, 48, width), "g", Pt(50, 150), 300, 300)

	b0, b1 := nonBgBounds(plain), nonBgBounds(outlined)
	if b0.Empty() || b1.Empty() {
		t.Fatal("nothing was drawn")
	}
	growth := [4]int{
		b0.Min.X - b1.Min.X,
		b0.Min.Y - b1.Min.Y,
		b1.Max.X - b0.Max.X,
		b1.Max.Y - b0.Max.Y,
	}
	for i, g := range growth {
		if g < 5 || g > 12 {
			t.Errorf("growth on side %d is %d px, want roughly %v px (in [5, 12])", i, g, width)
		}
	}
}

// TestOutlineCache checks that switching outline widths and colors does not
// serve stale glyph cache entries.
func TestOutlineCache(t *testing.T) {
	const s = "OBg"
	p := Pt(20, 120)

	c := newOutlineContext(t, 32, 2)
	img1 := render(t, c, s, p, 400, 200)

	c.SetOutlineWidth(8)
	img2 := render(t, c, s, p, 400, 200)

	c.SetOutlineWidth(2)
	img3 := render(t, c, s, p, 400, 200)

	// Switching only the outline color must not change the geometry: going
	// back to the original color must reproduce img1 exactly.
	c.SetOutlineSrc(image.NewUniform(outlineAlt))
	render(t, c, s, p, 400, 200)
	c.SetOutlineSrc(image.NewUniform(outlineEdge))
	img4 := render(t, c, s, p, 400, 200)

	// A fresh context at width 2 must match the reused context.
	img5 := render(t, newOutlineContext(t, 32, 2), s, p, 400, 200)

	if !sameImage(img1, img3) {
		t.Error("width 2 -> 8 -> 2: re-render at width 2 differs from the first render (stale cache?)")
	}
	if !sameImage(img1, img4) {
		t.Error("color switch round-trip changed the output (stale cache?)")
	}
	if !sameImage(img1, img5) {
		t.Error("cached render differs from a fresh context's render")
	}
	if sameImage(img1, img2) {
		t.Error("width 2 and width 8 produced identical output")
	}
}

func sameImage(a, b *image.RGBA) bool {
	if !a.Bounds().Eq(b.Bounds()) {
		return false
	}
	bb := a.Bounds()
	for y := bb.Min.Y; y < bb.Max.Y; y++ {
		for x := bb.Min.X; x < bb.Max.X; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				return false
			}
		}
	}
	return true
}

// TestOutlineZeroAndNegativeWidth checks that a zero or negative outline
// width disables outlining, producing exactly the fill-only output.
func TestOutlineZeroAndNegativeWidth(t *testing.T) {
	want := render(t, newOutlineContext(t, 32, 0), "OBg", Pt(20, 100), 400, 200)

	for _, w := range []float64{0, -1, -7.5} {
		c := newOutlineContext(t, 32, 0)
		c.SetOutlineWidth(w)
		c.SetOutlineSrc(image.NewUniform(outlineEdge))
		got := render(t, c, "OBg", Pt(20, 100), 400, 200)
		if !sameImage(want, got) {
			t.Errorf("width %v: output differs from fill-only output", w)
		}
	}
}

// TestOutlineSpace checks that outlining a contour-less glyph (a space) is a
// no-op: no error and no pixels touched.
func TestOutlineSpace(t *testing.T) {
	c := newOutlineContext(t, 32, 4)
	m := render(t, c, " ", Pt(20, 100), 200, 200)
	if n := countClass(m, 1) + countClass(m, 2); n != 0 {
		t.Errorf("drawing a space touched %d pixels", n)
	}
}

// TestOutlineClip checks that the outline respects the clip rectangle.
func TestOutlineClip(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(outlineBg), image.ZP, draw.Src)
	clip := image.Rect(40, 40, 160, 160)

	c := newOutlineContext(t, 96, 10)
	c.SetDst(dst)
	c.SetClip(clip)
	if _, err := c.DrawString("O", Pt(10, 150)); err != nil {
		t.Fatal(err)
	}
	b := dst.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if (image.Point{x, y}).In(clip) {
				continue
			}
			if got := dst.RGBAAt(x, y); got != outlineBg {
				t.Fatalf("pixel (%d, %d) outside the clip rectangle was modified: %v", x, y, got)
			}
		}
	}
	if countClass(dst, 1) == 0 {
		t.Error("no outline pixels were drawn inside the clip rectangle")
	}
}

// TestOutlineThinStroke checks that a sub-pixel outline width has a defined
// result: it draws without error and never fills the counter.
func TestOutlineThinStroke(t *testing.T) {
	c := newOutlineContext(t, 48, 0.4)
	m := render(t, c, "O", Pt(30, 200), 300, 300)
	if countClass(m, 2) == 0 {
		t.Error("no fill pixels drawn")
	}
	// The center of the counter must remain background.
	bb := nonBgBounds(m)
	if bb.Empty() {
		t.Fatal("nothing was drawn")
	}
	cx, cy := (bb.Min.X+bb.Max.X)/2, (bb.Min.Y+bb.Max.Y)/2
	if k := classify(m.RGBAAt(cx, cy)); k != 0 {
		t.Errorf("center of the counter is class %d, want background", k)
	}
}

// TestOutlineHugeGlyph checks that very large glyphs with a wide outline do
// not overflow the rasterizer's bounds or error out.
func TestOutlineHugeGlyph(t *testing.T) {
	c := newOutlineContext(t, 400, 20)
	m := render(t, c, "O", Pt(50, 550), 700, 700)
	if countClass(m, 1) == 0 || countClass(m, 2) == 0 {
		t.Error("expected both outline and fill pixels for a 400pt glyph")
	}
	// The counter must survive even at this size.
	bb := nonBgBounds(m)
	cx, cy := (bb.Min.X+bb.Max.X)/2, (bb.Min.Y+bb.Max.Y)/2
	if k := classify(m.RGBAAt(cx, cy)); k != 0 {
		t.Errorf("center of the counter is class %d, want background", k)
	}
}

// TestOutlineTransparentSrc checks the defined results for transparent
// sources: a transparent fill leaves only the outline ring, and transparent
// fill plus outline draws nothing.
func TestOutlineTransparentSrc(t *testing.T) {
	// Transparent fill, opaque outline: only the ring is drawn.
	c := newOutlineContext(t, 48, 6)
	c.SetSrc(image.Transparent)
	m := render(t, c, "O", Pt(30, 200), 300, 300)
	if countClass(m, 1) == 0 {
		t.Error("transparent fill: no outline pixels drawn")
	}
	if countClass(m, 2) != 0 {
		t.Error("transparent fill: fill-colored pixels were drawn")
	}
	// The interior of the glyph (inside the ring) must remain background.
	bb := nonBgBounds(m)
	cx, cy := (bb.Min.X+bb.Max.X)/2, (bb.Min.Y+bb.Max.Y)/2
	if k := classify(m.RGBAAt(cx, cy)); k != 0 {
		t.Errorf("transparent fill: center of the counter is class %d, want background", k)
	}

	// Transparent fill and transparent outline: nothing at all is drawn.
	c = newOutlineContext(t, 48, 6)
	c.SetSrc(image.Transparent)
	c.SetOutlineSrc(image.Transparent)
	m = render(t, c, "O", Pt(30, 200), 300, 300)
	if n := countClass(m, 1) + countClass(m, 2); n != 0 {
		t.Errorf("transparent fill and outline: %d pixels were drawn", n)
	}
}

// TestOutlineJoinStyles checks that explicit join styles (and the nil
// default) render without error and produce an outline.
func TestOutlineJoinStyles(t *testing.T) {
	for _, j := range []raster.Joiner{nil, raster.RoundJoiner, raster.BevelJoiner} {
		c := newOutlineContext(t, 48, 6)
		c.SetOutlineJoin(j)
		m := render(t, c, "B", Pt(30, 200), 300, 300)
		if countClass(m, 1) == 0 {
			t.Errorf("joiner %v: no outline pixels drawn", j)
		}
	}
}

// TestOutlineHintingAndDPI checks that outlining works together with the
// existing hinting policies and DPI settings.
func TestOutlineHintingAndDPI(t *testing.T) {
	for _, h := range []font.Hinting{font.HintingNone, font.HintingFull} {
		c := newOutlineContext(t, 24, 3)
		c.SetHinting(h)
		m := render(t, c, "OBg", Pt(10, 100), 400, 200)
		if countClass(m, 1) == 0 || countClass(m, 2) == 0 {
			t.Errorf("hinting %v: expected both outline and fill pixels", h)
		}
	}
	// A higher DPI must scale the outline width just like the font size.
	c := newOutlineContext(t, 24, 4)
	c.SetDPI(144) // 4pt at 144 DPI is 8 px.
	m := render(t, c, "O", Pt(30, 150), 300, 300)
	bb := nonBgBounds(m)
	if bb.Empty() {
		t.Fatal("nothing was drawn")
	}
	runs := rowRuns(m, (bb.Min.Y+bb.Max.Y)/2)
	if len(runs) != 9 {
		t.Fatalf("got %d runs %v, want 9", len(runs), runs)
	}
	if runs[1][1] < 6 {
		t.Errorf("outline run is %d px wide, want >= 6 (4pt at 144 DPI = 8 px)", runs[1][1])
	}
}
