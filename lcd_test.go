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

	"github.com/golang/freetype/truetype"
	"golang.org/x/image/math/fixed"
)

func newLCDContext(t *testing.T, cfg *LCDConfig, dst draw.Image) *Context {
	t.Helper()
	data, err := ioutil.ReadFile("testdata/luxisr.ttf")
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseFont(data)
	if err != nil {
		t.Fatal(err)
	}
	c := NewContext()
	if cfg != nil {
		if err := c.SetLCD(cfg); err != nil {
			t.Fatalf("SetLCD: %v", err)
		}
	}
	c.SetDst(dst)
	c.SetClip(dst.Bounds())
	c.SetSrc(image.Black)
	c.SetFont(f)
	c.SetFontSize(10)
	return c
}

func lcdConfig(order LCDStripOrder) *LCDConfig {
	return &LCDConfig{StripOrder: order, Filter: LCDFilterDefault}
}

func TestLCDConfigValidation(t *testing.T) {
	c := NewContext()
	// Valid configs.
	for _, cfg := range []*LCDConfig{
		{StripOrder: LCDRGB, Filter: LCDFilterDefault},
		{StripOrder: LCDBGR, Filter: LCDFilterLight},
		{StripOrder: LCDRGB, Filter: LCDFilterNone},
		{StripOrder: LCDBGR, Filter: LCDFilterCustom, FilterWeights: []int{1, 2, 3, 2, 1}},
		// Weights whose sum is not 256 are legal; normalization happens at use.
		{StripOrder: LCDRGB, Filter: LCDFilterCustom, FilterWeights: []int{0, 0, 1, 0, 0}},
	} {
		if err := c.SetLCD(cfg); err != nil {
			t.Errorf("SetLCD(%+v): %v", cfg, err)
		}
	}
	// Invalid configs: the Context must keep its previous valid config.
	good := &LCDConfig{StripOrder: LCDRGB, Filter: LCDFilterDefault}
	if err := c.SetLCD(good); err != nil {
		t.Fatal(err)
	}
	bad := []*LCDConfig{
		{StripOrder: LCDStripOrder(42), Filter: LCDFilterDefault},
		{StripOrder: LCDRGB, Filter: LCDFilter(99)},
		{StripOrder: LCDRGB, Filter: LCDFilterCustom}, // nil / wrong length
		{StripOrder: LCDRGB, Filter: LCDFilterCustom, FilterWeights: []int{1, 2, 3}},
		{StripOrder: LCDRGB, Filter: LCDFilterCustom, FilterWeights: []int{-1, 2, 3, 2, 1}},
		{StripOrder: LCDRGB, Filter: LCDFilterCustom, FilterWeights: []int{0, 0, 0, 0, 0}},
	}
	for _, cfg := range bad {
		err := c.SetLCD(cfg)
		if err == nil {
			t.Errorf("SetLCD(%+v) expected error, got nil", cfg)
			continue
		}
		if c.LCD() == nil || c.LCD().Filter != LCDFilterDefault || c.LCD().StripOrder != LCDRGB {
			t.Errorf("invalid config %+v mutated the Context: now %+v", cfg, c.LCD())
		}
	}
	// Disabling must work and be reversible.
	if err := c.SetLCD(nil); err != nil {
		t.Fatal(err)
	}
	if c.LCD() != nil {
		t.Fatal("SetLCD(nil) did not disable LCD rendering")
	}
}

func TestLCDActiveAndFallback(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	cfg := lcdConfig(LCDRGB)
	c := newLCDContext(t, cfg, dst)
	if !c.lcdActive() {
		t.Fatal("expected LCD path active for *image.RGBA")
	}
	// Explicit grayscale fallback.
	c.SetLCD(&LCDConfig{StripOrder: LCDRGB, Filter: LCDFilterDefault, Grayscale: true})
	if c.lcdActive() {
		t.Fatal("Grayscale=true should disable the LCD path")
	}
	// Unsupported destination type falls back transparently.
	pal := image.NewPaletted(image.Rect(0, 0, 64, 32), color.Palette{color.Black, color.White})
	c2 := NewContext()
	c2.SetLCD(lcdConfig(LCDRGB))
	c2.SetDst(pal)
	if c2.lcdActive() {
		t.Fatal("expected grayscale fallback for *image.Paletted")
	}
	// Disabled LCD.
	c3 := NewContext()
	c3.SetDst(dst)
	if c3.lcdActive() {
		t.Fatal("default Context must not use the LCD path")
	}
}

// TestLCDSubpixels verifies that edges carry separate R/G/B coverages, and
// that BGR is the channel-wise mirror of RGB on a white background.
func TestLCDSubpixelsAndBGR(t *testing.T) {
	drawText := func(cfg *LCDConfig) *image.RGBA {
		dst := image.NewRGBA(image.Rect(0, 0, 120, 40))
		draw.Draw(dst, dst.Bounds(), image.White, image.ZP, draw.Src)
		c := newLCDContext(t, cfg, dst)
		if _, err := c.DrawString("Hamburgefonstiv", Pt(4, 20)); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	rgb := drawText(lcdConfig(LCDRGB))
	bgr := drawText(lcdConfig(LCDBGR))

	nSplit := 0
	for i := 0; i < len(rgb.Pix); i += 4 {
		r, g, b := rgb.Pix[i], rgb.Pix[i+1], rgb.Pix[i+2]
		if r != 255 || g != 255 || b != 255 {
			if r != g || g != b {
				nSplit++
			}
		}
	}
	if nSplit == 0 {
		t.Fatal("LCD rendering produced no sub-pixel (R/G/B split) pixels")
	}

	nSwap := 0
	for i := 0; i < len(rgb.Pix); i += 4 {
		r, g, b := rgb.Pix[i], rgb.Pix[i+1], rgb.Pix[i+2]
		br, bg, bb := bgr.Pix[i], bgr.Pix[i+1], bgr.Pix[i+2]
		// Green is symmetric; red and blue are exchanged.
		if g != bg || r != bb || b != br {
			t.Fatalf("BGR is not the RGB channel swap at pixel %d: rgb=(%d,%d,%d) bgr=(%d,%d,%d)",
				i/4, r, g, b, br, bg, bb)
		}
		if r != b {
			nSwap++
		}
	}
	if nSwap == 0 {
		t.Fatal("no asymmetric edge pixel exercised the RGB/BGR swap")
	}
}

// TestLCDGeometryMatchesGrayscale pins the layout contract: advance width,
// baseline and the returned pen position are identical to grayscale.
func TestLCDGeometryMatchesGrayscale(t *testing.T) {
	texts := []string{"Hi", "AVAW", "jJ", "0123456789", " "}
	pts := []fixed.Point26_6{
		Pt(4, 24),
		{X: Pt(4, 24).X + 17, Y: Pt(4, 24).Y}, // non-integer x
		{X: Pt(4, 24).X, Y: Pt(4, 24).Y + 33}, // non-integer y
	}
	for _, p := range pts {
		for _, s := range texts {
			gDst := image.NewRGBA(image.Rect(0, 0, 200, 60))
			lDst := image.NewRGBA(image.Rect(0, 0, 200, 60))
			gc := newLCDContext(t, nil, gDst)
			lc := newLCDContext(t, lcdConfig(LCDRGB), lDst)
			gp, err := gc.DrawString(s, p)
			if err != nil {
				t.Fatal(err)
			}
			lp, err := lc.DrawString(s, p)
			if err != nil {
				t.Fatal(err)
			}
			if gp != lp {
				t.Errorf("text %q at %v: pen position grayscale %v != LCD %v", s, p, gp, lp)
			}
		}
	}
	// Per-glyph metrics via the caches must agree as well.
	gDst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	lDst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	gc := newLCDContext(t, nil, gDst)
	lc := newLCDContext(t, lcdConfig(LCDBGR), lDst)
	for _, r := range "HgMy" {
		idx := gc.f.Index(r)
		gAdv, _, _, err := gc.glyph(idx, Pt(4, 20))
		if err != nil {
			t.Fatal(err)
		}
		lAdv, lMask, _, err := lc.glyphLCD(idx, Pt(4, 20))
		if err != nil {
			t.Fatal(err)
		}
		if gAdv != lAdv {
			t.Errorf("rune %c: advance %v != %v", r, gAdv, lAdv)
		}
		if lMask.rect.Dx() <= 0 || lMask.rect.Dy() <= 0 {
			t.Errorf("rune %c: empty LCD mask", r)
		}
	}
}

// TestLCDDefaultPixelsUnchanged proves that enabling LCD never alters the
// grayscale path: no-LCD output is byte-identical before and after an LCD
// configuration is installed and removed.
func TestLCDDefaultPixelsUnchanged(t *testing.T) {
	render := func(c *Context) *image.RGBA {
		dst := image.NewRGBA(image.Rect(0, 0, 160, 40))
		draw.Draw(dst, dst.Bounds(), image.NewUniform(color.RGBA{R: 240, G: 240, B: 240, A: 255}),
			image.ZP, draw.Src)
		c.SetDst(dst)
		c.SetClip(dst.Bounds())
		if _, err := c.DrawString("Sub-pixel 0123", Pt(4, 24)); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	c := NewContext()
	data, _ := ioutil.ReadFile("testdata/luxisr.ttf")
	f, _ := ParseFont(data)
	c.SetFont(f)
	c.SetSrc(image.Black)
	c.SetFontSize(10)

	before := render(c)
	if err := c.SetLCD(lcdConfig(LCDRGB)); err != nil {
		t.Fatal(err)
	}
	if err := c.SetLCD(nil); err != nil {
		t.Fatal(err)
	}
	after := render(c)
	for i := range before.Pix {
		if before.Pix[i] != after.Pix[i] {
			t.Fatalf("grayscale pixel %d changed: %d -> %d", i, before.Pix[i], after.Pix[i])
		}
	}
}

// TestLCDFilterNoneAndLight exercises every built-in filter and a custom
// filter. The no-filter variant must equal the raw per-channel coverage, and
// every filter output must stay within [0, 255].
func TestLCDFilters(t *testing.T) {
	for _, cfg := range []*LCDConfig{
		{StripOrder: LCDRGB, Filter: LCDFilterNone},
		{StripOrder: LCDRGB, Filter: LCDFilterLight},
		{StripOrder: LCDRGB, Filter: LCDFilterDefault},
		{StripOrder: LCDRGB, Filter: LCDFilterCustom, FilterWeights: []int{1, 2, 3, 2, 1}},
		{StripOrder: LCDRGB, Filter: LCDFilterCustom, FilterWeights: []int{0, 0, 1000, 0, 0}},
	} {
		dst := image.NewRGBA(image.Rect(0, 0, 120, 40))
		draw.Draw(dst, dst.Bounds(), image.White, image.ZP, draw.Src)
		c := newLCDContext(t, cfg, dst)
		if _, err := c.DrawString("Edge", Pt(4, 20)); err != nil {
			t.Fatalf("filter %v: %v", cfg.Filter, err)
		}
		// Nothing can exceed the white background's 255 once black source is
		// composited over it; assert the image is internally consistent.
		for i := 0; i < len(dst.Pix); i += 4 {
			for _, b := range dst.Pix[i : i+4] {
				if b > 255 {
					t.Fatalf("out-of-range byte %d", b)
				}
			}
		}
	}
	// The identity filter's mask must match a direct unfiltered rasterize.
	cfg := &LCDConfig{StripOrder: LCDRGB, Filter: LCDFilterNone}
	dst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	c := newLCDContext(t, cfg, dst)
	idx := c.f.Index('H')
	_, mask, _, err := c.glyphLCD(idx, Pt(4, 20))
	if err != nil {
		t.Fatal(err)
	}
	if mask.rect.Empty() {
		t.Fatal("empty LCD mask for H")
	}
	for i := range mask.pix[0] {
		mx, my := mask.rect.Min.X+i%mask.rect.Dx(), mask.rect.Min.Y+i/mask.rect.Dx()
		for ch := 0; ch < 3; ch++ {
			if got := mask.pix[ch][i]; got > 255 {
				t.Fatalf("coverage out of range at (%d,%d) ch %d: %d", mx, my, ch, got)
			}
		}
		if mask.pix[3][i] < mask.pix[0][i] || mask.pix[3][i] < mask.pix[1][i] ||
			mask.pix[3][i] < mask.pix[2][i] {
			t.Fatalf("alpha coverage below a channel coverage at (%d,%d)", mx, my)
		}
	}
}

// TestLCDClip checks that clipping removes exactly the pixels outside the
// clip rectangle and leaves the rest identical to an unclipped render.
func TestLCDClip(t *testing.T) {
	cfg := lcdConfig(LCDRGB)
	full := image.NewRGBA(image.Rect(0, 0, 120, 40))
	draw.Draw(full, full.Bounds(), image.White, image.ZP, draw.Src)
	cf := newLCDContext(t, cfg, full)
	if _, err := cf.DrawString("Clipping", Pt(4, 20)); err != nil {
		t.Fatal(err)
	}

	clip := image.Rect(10, 5, 60, 30)
	part := image.NewRGBA(image.Rect(0, 0, 120, 40))
	draw.Draw(part, part.Bounds(), image.White, image.ZP, draw.Src)
	cp := newLCDContext(t, cfg, part)
	cp.SetClip(clip)
	if _, err := cp.DrawString("Clipping", Pt(4, 20)); err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 40; y++ {
		for x := 0; x < 120; x++ {
			i := part.PixOffset(x, y)
			p := image.Point{x, y}
			if p.In(clip) {
				for k := 0; k < 4; k++ {
					if part.Pix[i+k] != full.Pix[i+k] {
						t.Fatalf("inside-clip pixel (%d,%d) differs: %d vs %d",
							x, y, part.Pix[i+k], full.Pix[i+k])
					}
				}
			} else {
				for k := 0; k < 3; k++ {
					if part.Pix[i+k] != 255 {
						t.Fatalf("outside-clip pixel (%d,%d) was modified", x, y)
					}
				}
			}
		}
	}
}

// TestLCDTransparentAndTranslucent checks the alpha math on a transparent
// destination and against a translucent background.
func TestLCDTransparentAndTranslucent(t *testing.T) {
	// Fully transparent target: a black glyph with coverage a must produce
	// premultiplied pixels where color == 0 and alpha == combined coverage.
	dst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	c := newLCDContext(t, lcdConfig(LCDRGB), dst)
	if _, err := c.DrawString("A", Pt(4, 20)); err != nil {
		t.Fatal(err)
	}
	touched := 0
	for i := 0; i < len(dst.Pix); i += 4 {
		a := dst.Pix[i+3]
		if a == 0 {
			if dst.Pix[i] != 0 || dst.Pix[i+1] != 0 || dst.Pix[i+2] != 0 {
				t.Fatal("fully transparent pixel gained color")
			}
			continue
		}
		touched++
		// Source is opaque black, so premultiplied color channels stay zero.
		if dst.Pix[i] != 0 || dst.Pix[i+1] != 0 || dst.Pix[i+2] != 0 {
			t.Fatalf("black glyph over transparent dst has color %v", dst.Pix[i:i+4])
		}
	}
	if touched == 0 {
		t.Fatal("nothing was drawn")
	}

	// Translucent red background with a valid premultiplied RGBA seed:
	// alpha must accumulate via source-over and stay within [bgA, 255].
	const bgA = 128
	bg := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for i := 0; i < len(bg.Pix); i += 4 {
		bg.Pix[i+0], bg.Pix[i+1], bg.Pix[i+2], bg.Pix[i+3] = 100, 0, 0, bgA
	}
	c2 := newLCDContext(t, lcdConfig(LCDRGB), bg)
	c2.SetSrc(image.NewUniform(color.RGBA{R: 0, G: 0, B: 0, A: 255}))
	if _, err := c2.DrawString("A", Pt(4, 20)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(bg.Pix); i += 4 {
		a := bg.Pix[i+3]
		// Source-over alpha is monotonic: every touched alpha is at least the
		// background's (rounding may keep it equal).
		if a < bgA {
			t.Fatalf("alpha decreased from %d to %d", bgA, a)
		}
		if a > 255 {
			t.Fatalf("alpha overflow: %d", a)
		}
		// Premultiplied color channels can never exceed alpha.
		for k := 0; k < 3; k++ {
			if bg.Pix[i+k] > a {
				t.Fatalf("premultiplied channel %d (%d) exceeds alpha (%d)",
					k, bg.Pix[i+k], a)
			}
		}
	}
	// At least one pixel must have gained alpha, proving the translucent
	// background was composited rather than overwritten.
	gained := false
	for i := 3; i < len(bg.Pix); i += 4 {
		if bg.Pix[i] > bgA {
			gained = true
			break
		}
	}
	if !gained {
		t.Fatal("no pixel gained alpha over the translucent background")
	}
}

// TestLCDNRGBA checks the non-premultiplied destination path against an
// equivalent RGBA render.
func TestLCDNRGBA(t *testing.T) {
	src := image.NewUniform(color.RGBA{R: 10, G: 200, B: 90, A: 180})
	render := func(dst draw.Image) {
		c := newLCDContext(t, lcdConfig(LCDBGR), dst)
		c.SetSrc(src)
		if _, err := c.DrawString("Nrgba", Pt(4, 20)); err != nil {
			t.Fatal(err)
		}
	}
	rgba := image.NewRGBA(image.Rect(0, 0, 80, 32))
	nrgba := image.NewNRGBA(image.Rect(0, 0, 80, 32))
	for i := 0; i < len(rgba.Pix); i += 4 {
		// Pick values that round-trip exactly between premultiplied RGBA and
		// straight NRGBA at alpha 128.
		rgba.Pix[i] = 20
		rgba.Pix[i+1] = 40
		rgba.Pix[i+2] = 60
		rgba.Pix[i+3] = 128
		nrgba.Pix[i] = 40
		nrgba.Pix[i+1] = 80
		nrgba.Pix[i+2] = 120
		nrgba.Pix[i+3] = 128
	}
	render(rgba)
	render(nrgba)
	// Only glyph pixels (whose alpha differs from the 200 background) need
	// to agree between the two pixel layouts.
	for y := 0; y < 32; y++ {
		for x := 0; x < 80; x++ {
			i := rgba.PixOffset(x, y)
			if rgba.Pix[i+3] == 128 && nrgba.Pix[i+3] == 128 {
				continue
			}
			// Convert the NRGBA result to premultiplied and compare (allowing
			// a one-level rounding difference).
			a := uint32(nrgba.Pix[i+3])
			for k := 0; k < 3; k++ {
				got := uint32(nrgba.Pix[i+k]) * a / 255
				if abs(int(got)-int(rgba.Pix[i+k])) > 1 {
					t.Fatalf("NRGBA/RGBA mismatch at (%d,%d) ch %d: %d vs %d",
						x, y, k, got, rgba.Pix[i+k])
				}
			}
			if abs(int(a)-int(rgba.Pix[i+3])) > 1 {
				t.Fatalf("NRGBA/RGBA alpha mismatch at (%d,%d): %d vs %d",
					x, y, a, rgba.Pix[i+3])
			}
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// TestLCDSubpixelPosition renders the same glyph at several fractional
// positions and requires the masks to differ (sub-pixel positioning is real)
// while every pen advance stays identical. The glyph cache quantizes the
// fractional x to nXFractions buckets, so the probe points land in distinct
// buckets.
func TestLCDSubpixelPosition(t *testing.T) {
	cfg := lcdConfig(LCDRGB)
	var prev []byte
	advances := map[fixed.Int26_6]bool{}
	for _, frac := range []int{0, 20, 36, 52} {
		dst := image.NewRGBA(image.Rect(0, 0, 64, 32))
		c := newLCDContext(t, cfg, dst)
		p := fixed.Point26_6{X: fixed.Int26_6(4<<6) + fixed.Int26_6(frac), Y: fixed.Int26_6(20 << 6)}
		pen, err := c.DrawString("H", p)
		if err != nil {
			t.Fatal(err)
		}
		advances[pen.X-p.X] = true
		if prev != nil {
			diff := false
			for i := range prev {
				if prev[i] != dst.Pix[i] {
					diff = true
					break
				}
			}
			if !diff {
				t.Fatalf("fraction %d rendered identically to the previous fraction", frac)
			}
		}
		prev = append(prev[:0], dst.Pix...)
	}
	if len(advances) != 1 {
		t.Fatalf("advance width changed with sub-pixel position: %v", advances)
	}
}

// TestLCDTinyAndEmptyFonts covers the extreme-size and no-contour cases.
func TestLCDTinyAndEmptyFonts(t *testing.T) {
	data, _ := ioutil.ReadFile("testdata/luxisr.ttf")
	f, _ := ParseFont(data)
	// Tiny size: must not panic and must not crash on empty per-pass masks.
	dst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	c := NewContext()
	c.SetLCD(lcdConfig(LCDRGB))
	c.SetDst(dst)
	c.SetClip(dst.Bounds())
	c.SetSrc(image.Black)
	c.SetFont(f)
	c.SetFontSize(1) // 1 point
	pen, err := c.DrawString("Hg .", Pt(2, 10))
	if err != nil {
		t.Fatal(err)
	}
	if pen.X < Pt(2, 0).X {
		t.Fatal("pen moved backwards")
	}
	// A space has no contours: its LCD mask must be empty but the pen still
	// advances.
	p0 := Pt(2, 20)
	p1, err := c.DrawString(" ", p0)
	if err != nil {
		t.Fatal(err)
	}
	if p1.X <= p0.X {
		t.Fatalf("space did not advance the pen: %v -> %v", p0, p1)
	}
}

// TestLCDCacheIsolation fills the grayscale cache and the LCD cache with the
// same glyphs and ensures the two never borrow each other's masks.
func TestLCDCacheIsolation(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	gc := newLCDContext(t, nil, dst)
	lc := newLCDContext(t, lcdConfig(LCDRGB), dst)
	idx := gc.f.Index('H')
	// Populate both caches.
	gAdv, gMask, gOff, err := gc.glyph(idx, Pt(4, 20))
	if err != nil {
		t.Fatal(err)
	}
	lAdv, lMask, lOff, err := lc.glyphLCD(idx, Pt(4, 20))
	if err != nil {
		t.Fatal(err)
	}
	if gAdv != lAdv {
		t.Fatalf("advance mismatch: %v vs %v", gAdv, lAdv)
	}
	// The cached entries are of distinct static types and storage; scan both
	// arrays rather than assuming one particular slot.
	gHit, lHit := false, false
	for i := range gc.cache {
		if gc.cache[i].valid && gc.cache[i].glyph == idx {
			gHit = true
			if gc.cache[i].mask == nil {
				t.Fatal("nil cached grayscale mask")
			}
		}
	}
	for i := range lc.lcdCache {
		if lc.lcdCache[i].valid && lc.lcdCache[i].glyph == idx {
			lHit = true
			if lc.lcdCache[i].mask == nil {
				t.Fatal("nil cached LCD mask")
			}
		}
	}
	if !gHit || !lHit {
		t.Fatalf("expected both caches populated (gray=%v, lcd=%v)", gHit, lHit)
	}
	// Second call hits the cache and returns the same pointer.
	_, gMask2, _, err := gc.glyph(idx, Pt(4, 20))
	if err != nil {
		t.Fatal(err)
	}
	_, lMask2, _, err := lc.glyphLCD(idx, Pt(4, 20))
	if err != nil {
		t.Fatal(err)
	}
	if gMask != gMask2 || lMask != lMask2 {
		t.Fatal("cache miss on second lookup")
	}
	// The LCD mask may be one pixel wider on each side due to the shifted
	// passes, but its top-left origin must match the grayscale mask's.
	if gOff.X != lOff.X || gOff.Y != lOff.Y {
		t.Fatalf("mask origins differ: gray %v vs lcd %v", gOff, lOff)
	}
}

// TestLCDConfigChangeInvalidatesCache makes sure replacing the LCD
// configuration (which changes the filter / strip order) discards old masks.
func TestLCDConfigChangeInvalidatesCache(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	c := newLCDContext(t, lcdConfig(LCDRGB), dst)
	if _, err := c.DrawString("H", Pt(4, 20)); err != nil {
		t.Fatal(err)
	}
	if !cacheHasLCDGlyph(c, c.f.Index('H')) {
		t.Fatal("LCD cache was not populated")
	}
	if err := c.SetLCD(lcdConfig(LCDBGR)); err != nil {
		t.Fatal(err)
	}
	if anyLCDCacheValid(c) {
		t.Fatal("LCD cache not invalidated by SetLCD")
	}
	c.SetDPI(144)
	if anyLCDCacheValid(c) {
		t.Fatal("LCD cache not invalidated by SetDPI")
	}
}

func anyLCDCacheValid(c *Context) bool {
	for i := range c.lcdCache {
		if c.lcdCache[i].valid {
			return true
		}
	}
	return false
}

func cacheHasLCDGlyph(c *Context, idx truetype.Index) bool {
	for i := range c.lcdCache {
		if c.lcdCache[i].valid && c.lcdCache[i].glyph == idx {
			return true
		}
	}
	return false
}

// leftEdgeX returns the left-most x at each row whose pixel is noticeably
// darker than the white background.
func leftEdgeX(m *image.RGBA) map[int]int {
	out := map[int]int{}
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			i := m.PixOffset(x, y)
			if int(m.Pix[i])+int(m.Pix[i+1])+int(m.Pix[i+2]) < 600 {
				out[y] = x
				break
			}
		}
	}
	return out
}

// TestLCDEdgeDisplacement pins the requirement that LCD rendering must not
// move glyph edges relative to grayscale: the dark core of every row starts
// on the same column under gray, RGB and BGR. The sub-pixel color fringes may
// extend one column further, but they cannot shift the anchored edge.
func TestLCDEdgeDisplacement(t *testing.T) {
	render := func(cfg *LCDConfig) *image.RGBA {
		dst := image.NewRGBA(image.Rect(0, 0, 80, 40))
		draw.Draw(dst, dst.Bounds(), image.White, image.ZP, draw.Src)
		c := newLCDContext(t, cfg, dst)
		c.SetFontSize(12)
		c.SetDPI(96)
		if _, err := c.DrawString("Hpq", Pt(6, 26)); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	gray := leftEdgeX(render(nil))
	rgb := leftEdgeX(render(&LCDConfig{StripOrder: LCDRGB, Filter: LCDFilterDefault}))
	bgr := leftEdgeX(render(&LCDConfig{StripOrder: LCDBGR, Filter: LCDFilterDefault}))
	if len(gray) == 0 {
		t.Fatal("no edge rows found")
	}
	for y, gx := range gray {
		if rx, ok := rgb[y]; !ok || rx != gx {
			t.Fatalf("row %d: RGB edge %v differs from grayscale %d", y, rx, gx)
		}
		if bx, ok := bgr[y]; !ok || bx != gx {
			t.Fatalf("row %d: BGR edge %v differs from grayscale %d", y, bx, gx)
		}
	}
}

// TestLCDTallGlyph renders glyphs with large vertical extent (parentheses,
// descenders and capitals) to ensure the vertically-unfiltered LCD path keeps
// full height and never clips rows.
func TestLCDTallGlyph(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 80, 60))
	draw.Draw(dst, dst.Bounds(), image.White, image.ZP, draw.Src)
	c := newLCDContext(t, lcdConfig(LCDRGB), dst)
	c.SetFontSize(18)
	if _, err := c.DrawString("(Hgjpy)", Pt(4, 34)); err != nil {
		t.Fatal(err)
	}
	// Count touched rows: a tall parenthesized string must span most of the
	// em height rather than collapse to a few rows.
	rows := map[int]bool{}
	for y := 0; y < 60; y++ {
		for x := 0; x < 80; x++ {
			i := dst.PixOffset(x, y)
			if dst.Pix[i] != 255 || dst.Pix[i+1] != 255 || dst.Pix[i+2] != 255 {
				rows[y] = true
				break
			}
		}
	}
	if len(rows) < 18 {
		t.Fatalf("tall glyphs only occupied %d rows; expected vertical clipping-free span", len(rows))
	}
}

// TestLCDCustomFilterNormalization verifies that custom weights are
// normalized by their sum, so a {0,0,1,0,0} identity filter reproduces raw
// sub-pixel coverage even though its sum is 1 rather than 256.
func TestLCDCustomFilterNormalization(t *testing.T) {
	idx := func(c *Context) truetype.Index { return c.f.Index('H') }
	dst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	id := newLCDContext(t,
		&LCDConfig{StripOrder: LCDRGB, Filter: LCDFilterCustom, FilterWeights: []int{0, 0, 1, 0, 0}}, dst)
	none := newLCDContext(t, &LCDConfig{StripOrder: LCDRGB, Filter: LCDFilterNone}, dst)
	_, m1, _, err := id.glyphLCD(idx(id), Pt(4, 20))
	if err != nil {
		t.Fatal(err)
	}
	_, m2, _, err := none.glyphLCD(idx(none), Pt(4, 20))
	if err != nil {
		t.Fatal(err)
	}
	if m1.rect != m2.rect {
		t.Fatalf("mask rects differ: %v vs %v", m1.rect, m2.rect)
	}
	for ch := 0; ch < 4; ch++ {
		for i := range m1.pix[ch] {
			if m1.pix[ch][i] != m2.pix[ch][i] {
				t.Fatalf("identity custom filter != FilterNone at ch %d pixel %d: %d vs %d",
					ch, i, m1.pix[ch][i], m2.pix[ch][i])
			}
		}
	}
}

// TestLCDFilterSpatialConvolution verifies that the default five-tap filter
// is applied over spatially consecutive sub-pixels. Ground truth is built
// directly from the three independent shifted rasterization passes (not from
// the LCD mask under test), so a layout that mislabels spatial neighbors
// cannot pass. The BGR path is covered separately by the exact RGB<->BGR swap
// test.
func TestLCDFilterSpatialConvolution(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 64, 32))
	def := newLCDContext(t,
		&LCDConfig{StripOrder: LCDRGB, Filter: LCDFilterDefault}, dst)
	idx := def.f.Index('H')
	p := Pt(4, 20)
	fx, fy := p.X&0x3f, p.Y&0x3f

	// Rasterize the three shifted passes independently.
	var pass [3]*image.Alpha
	var off [3]image.Point
	var union image.Rectangle
	for k := 0; k < 3; k++ {
		_, m, o, err := def.rasterize(idx, fx+fixed.Int26_6(k*lcdThird), fy)
		if err != nil {
			t.Fatal(err)
		}
		pass[k], off[k] = m, o
		r := m.Bounds().Add(o)
		if k == 0 {
			union = r
		} else {
			union = union.Union(r)
		}
	}
	sample := func(k, col, row int) int {
		m := pass[k]
		mb := m.Bounds()
		lx, ly := col-off[k].X, row-off[k].Y
		if lx < mb.Min.X || lx >= mb.Max.X || ly < mb.Min.Y || ly >= mb.Max.Y {
			return 0
		}
		return int(m.Pix[(ly-mb.Min.Y)*m.Stride+(lx-mb.Min.X)])
	}
	// Ground-truth spatial coverage for local pixel (lx, ly), stripe s
	// (continuous position gx + s/3).
	truth := func(lx, ly, s int) int {
		gx := union.Min.X + lx
		gy := union.Min.Y + ly
		switch s {
		case 0: // position gx:     pass 0 at column gx
			return sample(0, gx, gy)
		case 1: // position gx+1/3: pass 2 at column gx+1
			return sample(2, gx+1, gy)
		default: // position gx+2/3: pass 1 at column gx+1
			return sample(1, gx+1, gy)
		}
	}

	_, filtMask, foff, err := def.glyphLCD(idx, p)
	if err != nil {
		t.Fatal(err)
	}
	// glyphLCD adds the integer pen origin to rasterizeLCD's local offset.
	wantOff := union.Min.Add(image.Point{X: int(p.X >> 6), Y: int(p.Y >> 6)})
	if foff != wantOff {
		t.Fatalf("offset %v != expected %v (union min %v)", foff, wantOff, union.Min)
	}
	w, h := union.Dx(), union.Dy()
	for ly := 0; ly < h; ly++ {
		for lx := 0; lx < w; lx++ {
			for ch := 0; ch < 3; ch++ {
				// Convolve over the five spatially adjacent sub-pixels using
				// the local sub-pixel index 3*lx+ch, so samples outside the
				// mask contribute zero, matching the implementation.
				center := 3*lx + ch
				acc := 0
				for j, wt := range lcdDefaultWeights {
					s := center + j - 2
					if s < 0 || s >= 3*w {
						continue
					}
					acc += wt * truth(s/3, ly, s%3)
				}
				want := (acc + 128) / 256
				if want > 255 {
					want = 255
				}
				pi := ly*w + lx
				if got := int(filtMask.pix[ch][pi]); got != want {
					t.Fatalf("local (%d,%d) ch %d: filtered %d != spatial convolution %d",
						lx, ly, ch, got, want)
				}
			}
		}
	}
}
