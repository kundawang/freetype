// Copyright 2026 The Freetype-Go Authors. All rights reserved.
// Use of this source code is governed by your choice of either the
// FreeType License or the GNU General Public License version 2 (or
// any later version), both of which can be found in the LICENSE file.

package freetype

import (
	"errors"
	"image"

	"github.com/golang/freetype/truetype"
	"golang.org/x/image/math/fixed"
)

// LCDStripOrder selects the physical left-to-right order of the colored
// sub-pixels of an LCD panel.
type LCDStripOrder int

const (
	// LCDRGB is the common strip order where red is the left-most sub-pixel,
	// green the middle one and blue the right-most one.
	LCDRGB LCDStripOrder = iota
	// LCDBGR is the reversed strip order (blue, green, red).
	LCDBGR
)

// LCDFilter selects the horizontal low-pass filter applied to the three
// sub-pixel coverage channels. Filtering trades a little color fringing for
// reduced fringes at glyph edges.
type LCDFilter int

const (
	// LCDFilterDefault is FreeType's default five-tap filter,
	// {8, 77, 86, 77, 8}.
	LCDFilterDefault LCDFilter = iota
	// LCDFilterLight is FreeType's lighter five-tap filter,
	// {0, 85, 86, 85, 0}.
	LCDFilterLight
	// LCDFilterNone disables filtering and uses each sub-pixel coverage
	// value directly: {0, 0, 256, 0, 0}.
	LCDFilterNone
	// LCDFilterCustom uses the caller-supplied LCDConfig.FilterWeights.
	LCDFilterCustom
)

// lcdThird is one third of a pixel in 26.6 fixed point units. The three
// rasterization passes shift the outline by 0, 1/3 and 2/3 of a pixel.
const lcdThird = 64 / 3

// Predefined five-tap filter weights. Every preset sums to 256, so a
// coverage value stays in the range [0, 255].
var (
	lcdDefaultWeights = [5]int{8, 77, 86, 77, 8}
	lcdLightWeights   = [5]int{0, 85, 86, 85, 0}
	lcdNoneWeights    = [5]int{0, 0, 256, 0, 0}
)

// LCDConfig configures optional LCD sub-pixel (ClearType-style) text
// rendering. The zero value is not valid; use SetLCD with a configured
// value, or nil to disable sub-pixel rendering.
type LCDConfig struct {
	// StripOrder is the panel's physical sub-pixel order: LCDRGB or LCDBGR.
	StripOrder LCDStripOrder
	// Filter selects the horizontal sub-pixel filter.
	Filter LCDFilter
	// FilterWeights is the five-tap filter used when Filter is
	// LCDFilterCustom. Every weight must be non-negative and their sum must
	// be positive. It is ignored for the other Filter values.
	FilterWeights []int
	// Grayscale forces the conventional single-coverage (grayscale)
	// rendering path even when LCD rendering is configured. It also provides
	// an explicit fallback for destination images that cannot hold color
	// sub-pixel coverage.
	Grayscale bool
}

// weights returns the five-tap filter and its (positive) sum.
func (cfg *LCDConfig) weights() ([5]int, int, error) {
	switch cfg.Filter {
	case LCDFilterDefault:
		return lcdDefaultWeights, 256, nil
	case LCDFilterLight:
		return lcdLightWeights, 256, nil
	case LCDFilterNone:
		return lcdNoneWeights, 256, nil
	case LCDFilterCustom:
		if len(cfg.FilterWeights) != 5 {
			return [5]int{}, 0, errors.New("freetype: LCD FilterWeights must have exactly 5 entries")
		}
		var w [5]int
		sum := 0
		for i, v := range cfg.FilterWeights {
			if v < 0 {
				return [5]int{}, 0, errors.New("freetype: LCD FilterWeights must be non-negative")
			}
			w[i] = v
			sum += v
		}
		if sum == 0 {
			return [5]int{}, 0, errors.New("freetype: LCD FilterWeights must have a positive sum")
		}
		return w, sum, nil
	default:
		return [5]int{}, 0, errors.New("freetype: invalid LCD Filter value")
	}
}

// validate checks a configuration without mutating the Context.
func (cfg *LCDConfig) validate() error {
	switch cfg.StripOrder {
	case LCDRGB, LCDBGR:
	default:
		return errors.New("freetype: invalid LCD StripOrder value")
	}
	_, _, err := cfg.weights()
	return err
}

// An lcdMask holds the per-channel LCD coverage of a rasterized glyph. Every
// slice has w*h entries, one byte per pixel, with 0 meaning no coverage and
// 255 full coverage. pix[0..2] are the red, green and blue coverages in
// destination color order (so BGR panels are already swapped), and pix[3] is
// the combined alpha coverage, which is the maximum of the three channels.
type lcdMask struct {
	rect image.Rectangle
	pix  [4][]uint8
}

// An lcdCacheEntry is the LCD counterpart of cacheEntry. It is kept in a
// separate cache so that LCD masks can never be confused with grayscale
// (image.Alpha) masks.
type lcdCacheEntry struct {
	valid        bool
	glyph        truetype.Index
	advanceWidth fixed.Int26_6
	mask         *lcdMask
	offset       image.Point
}

// lcdActive reports whether the next glyph should use the LCD path. LCD
// rendering requires a configured, non-grayscale context and a destination
// whose pixels are addressable as separate R, G, B and A bytes. Any other
// destination transparently falls back to the grayscale path.
func (c *Context) lcdActive() bool {
	if c.lcd == nil || c.lcd.Grayscale {
		return false
	}
	switch c.dst.(type) {
	case *image.RGBA, *image.NRGBA:
		return true
	}
	return false
}

// rasterizeLCD rasterizes glyph at the given sub-pixel offsets and returns
// its advance width, three-channel coverage mask and integer-pixel offset.
// The outline is rasterized three times, shifted by 0, 1/3 and 2/3 of a
// pixel; each pass measures one vertical stripe of sub-pixels. As in the
// grayscale path, fx and fy are 26.6 fixed point values in [0, 1).
func (c *Context) rasterizeLCD(glyph truetype.Index, fx, fy fixed.Int26_6) (
	fixed.Int26_6, *lcdMask, image.Point, error) {

	var (
		masks [3]*image.Alpha
		offs  [3]image.Point
		adv   fixed.Int26_6
		union image.Rectangle
	)
	for k := 0; k < 3; k++ {
		advance, mask, offset, err := c.rasterize(glyph, fx+fixed.Int26_6(k*lcdThird), fy)
		if err != nil {
			return 0, nil, image.Point{}, err
		}
		adv = advance
		masks[k] = mask
		offs[k] = offset
		r := mask.Bounds().Add(offset)
		if k == 0 {
			union = r
		} else {
			union = union.Union(r)
		}
	}
	if union.Empty() {
		// Glyphs without contours (e.g. a space) have an empty mask but a
		// valid advance width.
		return adv, &lcdMask{}, image.Point{}, nil
	}
	w, h := union.Dx(), union.Dy()
	mask := &lcdMask{rect: image.Rect(0, 0, w, h)}
	for i := range mask.pix {
		mask.pix[i] = make([]uint8, w*h)
	}

	// raw holds the unfiltered coverages in spatial sub-pixel order: for a
	// row, raw[s] is the coverage at global sub-pixel s, where sub-pixels
	// 3*x, 3*x+1 and 3*x+2 are the left, center and right vertical stripes of
	// output pixel x (continuous positions x, x+1/3, x+2/3). Keeping them in
	// spatial order is what makes the horizontal 5-tap filter meaningful.
	//
	// Pass k rasterizes the outline shifted right by k/3 of a pixel, so its
	// column c measures coverage at continuous position c-k/3. Solving
	// c-k/3 = (3*x+sub)/3 for each stripe gives the pass/column used below.
	raw := make([]uint8, 3*w*h)
	passAt := func(k, col, row int) uint8 {
		m := masks[k]
		mb := m.Bounds()
		lx := col - offs[k].X
		ly := row - offs[k].Y
		if lx < mb.Min.X || lx >= mb.Max.X || ly < mb.Min.Y || ly >= mb.Max.Y {
			return 0
		}
		return m.Pix[(ly-mb.Min.Y)*m.Stride+(lx-mb.Min.X)]
	}
	for y := union.Min.Y; y < union.Max.Y; y++ {
		row := (y - union.Min.Y) * 3 * w
		for x := union.Min.X; x < union.Max.X; x++ {
			xr := x - union.Min.X
			// Left stripe (position x): pass 0, column x.
			raw[row+3*xr+0] = passAt(0, x, y)
			// Center stripe (position x+1/3): pass 2, column x+1.
			raw[row+3*xr+1] = passAt(2, x+1, y)
			// Right stripe (position x+2/3): pass 1, column x+1.
			raw[row+3*xr+2] = passAt(1, x+1, y)
		}
	}

	weights, sum, err := c.lcd.weights()
	if err != nil {
		return 0, nil, image.Point{}, err
	}
	bgr := c.lcd.StripOrder == LCDBGR
	for y := 0; y < h; y++ {
		rowBase := 3 * y * w
		pixBase := y * w
		for x := 0; x < w; x++ {
			var cov [3]uint8
			var maxCov uint8
			for ch := 0; ch < 3; ch++ {
				// Destination channel ch sits on spatial stripe ch for an RGB
				// panel; for BGR the physical stripe order is reversed.
				stripe := ch
				if bgr {
					stripe = 2 - ch
				}
				center := rowBase + 3*x + stripe
				acc := 0
				for j, weight := range weights {
					if weight == 0 {
						continue
					}
					q := center + j - 2
					var v int
					if q >= rowBase && q < rowBase+3*w {
						v = int(raw[q])
					}
					acc += weight * v
				}
				v := (acc + sum/2) / sum
				if v > 255 {
					v = 255
				}
				cov[ch] = uint8(v)
				if uint8(v) > maxCov {
					maxCov = uint8(v)
				}
			}
			mask.pix[0][pixBase+x] = cov[0]
			mask.pix[1][pixBase+x] = cov[1]
			mask.pix[2][pixBase+x] = cov[2]
			mask.pix[3][pixBase+x] = maxCov
		}
	}
	return adv, mask, union.Min, nil
}

// glyphLCD is the cached counterpart of rasterizeLCD, mirroring glyph.
func (c *Context) glyphLCD(glyph truetype.Index, p fixed.Point26_6) (
	fixed.Int26_6, *lcdMask, image.Point, error) {

	ix, fx := int(p.X>>6), p.X&0x3f
	iy, fy := int(p.Y>>6), p.Y&0x3f
	tg := int(glyph) % nGlyphs
	tx := int(fx) / (64 / nXFractions)
	ty := int(fy) / (64 / nYFractions)
	t := ((tg*nXFractions)+tx)*nYFractions + ty
	if e := c.lcdCache[t]; e.valid && e.glyph == glyph {
		return e.advanceWidth, e.mask, e.offset.Add(image.Point{ix, iy}), nil
	}
	advanceWidth, mask, offset, err := c.rasterizeLCD(glyph, fx, fy)
	if err != nil {
		return 0, nil, image.Point{}, err
	}
	c.lcdCache[t] = lcdCacheEntry{
		valid:        true,
		glyph:        glyph,
		advanceWidth: advanceWidth,
		mask:         mask,
		offset:       offset,
	}
	return advanceWidth, mask, offset.Add(image.Point{ix, iy}), nil
}

// sat8 converts a 16-bit value (range [0, 65535]) to 8 bits with
// round-to-nearest, clamping to 255. A plain (v+0x80)>>8 would wrap the
// maximum value 65535 around to zero.
func sat8(v uint32) uint8 {
	b := (v + 0x80) >> 8
	if b > 255 {
		return 255
	}
	return uint8(b)
}

// sat8Div returns the rounded, clamped 8-bit quotient n/d where the true
// quotient is known to be at most 255 (e.g. an un-premultiplied ratio).
func sat8Div(n, d uint32) uint8 {
	q := (n + d/2) / d
	if q > 255 {
		return 255
	}
	return uint8(q)
}

// drawLCDMask composites an LCD coverage mask onto the destination using the
// source-over operator. Each destination color channel is blended with its
// own coverage, while the alpha channel uses the combined (maximum) coverage.
// Pixels with zero coverage are left untouched.
func (c *Context) drawLCDMask(mask *lcdMask, offset image.Point) {
	glyphRect := mask.rect.Add(offset)
	dr := c.clip.Intersect(glyphRect)
	if dr.Empty() {
		return
	}
	// The source is conventionally an image.Uniform; sample it once at the
	// glyph's origin. color.Color.RGBA returns alpha-premultiplied 16-bit
	// values.
	sr, sg, sb, sa := c.src.At(glyphRect.Min.X, glyphRect.Min.Y).RGBA()
	const m = 1<<16 - 1

	switch dst := c.dst.(type) {
	case *image.RGBA:
		for y := dr.Min.Y; y < dr.Max.Y; y++ {
			lrow := (y - offset.Y - mask.rect.Min.Y) * mask.rect.Dx()
			for x := dr.Min.X; x < dr.Max.X; x++ {
				li := lrow + (x - offset.X - mask.rect.Min.X)
				if mask.pix[3][li] == 0 {
					continue
				}
				// Each color channel gets its own sub-pixel coverage, so the
				// destination factor is per-channel and cannot be hoisted.
				mc0 := uint32(mask.pix[0][li]) * 0x101
				mc1 := uint32(mask.pix[1][li]) * 0x101
				mc2 := uint32(mask.pix[2][li]) * 0x101
				ma16 := uint32(mask.pix[3][li]) * 0x101
				i := dst.PixOffset(x, y)
				dr16 := uint32(dst.Pix[i+0]) * 0x101
				dg16 := uint32(dst.Pix[i+1]) * 0x101
				db16 := uint32(dst.Pix[i+2]) * 0x101
				da16 := uint32(dst.Pix[i+3]) * 0x101
				// Source-over in 16-bit premultiplied arithmetic. The
				// per-channel factor f = sa*mc/m is computed first, so every
				// product below stays below (2^16-1)^2 and the two added
				// terms cannot overflow uint32. The alpha channel uses the
				// effective combined coverage sa*ma/m, so a fully opaque
				// source (sa == m) yields an opaque result regardless of the
				// mask value.
				f0, f1, f2 := sa*mc0/m, sa*mc1/m, sa*mc2/m
				out0 := sr*mc0/m + dr16*(m-f0)/m
				out1 := sg*mc1/m + dg16*(m-f1)/m
				out2 := sb*mc2/m + db16*(m-f2)/m
				effA := sa * ma16 / m
				// Split the source-over alpha so the effA*m product cannot
				// overflow uint32.
				out3 := effA + da16*(m-effA)/m
				dst.Pix[i+0] = sat8(out0)
				dst.Pix[i+1] = sat8(out1)
				dst.Pix[i+2] = sat8(out2)
				dst.Pix[i+3] = sat8(out3)
			}
		}
	case *image.NRGBA:
		for y := dr.Min.Y; y < dr.Max.Y; y++ {
			lrow := (y - offset.Y - mask.rect.Min.Y) * mask.rect.Dx()
			for x := dr.Min.X; x < dr.Max.X; x++ {
				li := lrow + (x - offset.X - mask.rect.Min.X)
				if mask.pix[3][li] == 0 {
					continue
				}
				mc0 := uint32(mask.pix[0][li]) * 0x101
				mc1 := uint32(mask.pix[1][li]) * 0x101
				mc2 := uint32(mask.pix[2][li]) * 0x101
				ma16 := uint32(mask.pix[3][li]) * 0x101
				i := dst.PixOffset(x, y)
				// Convert the straight NRGBA pixel to premultiplied 16-bit.
				// Multiplications are ordered so no uint32 intermediate
				// overflows.
				da8 := uint32(dst.Pix[i+3])
				dr16 := uint32(dst.Pix[i+0]) * da8 * 0x101 / 255
				dg16 := uint32(dst.Pix[i+1]) * da8 * 0x101 / 255
				db16 := uint32(dst.Pix[i+2]) * da8 * 0x101 / 255
				da16 := da8 * 0x101
				f0, f1, f2 := sa*mc0/m, sa*mc1/m, sa*mc2/m
				out0 := sr*mc0/m + dr16*(m-f0)/m
				out1 := sg*mc1/m + dg16*(m-f1)/m
				out2 := sb*mc2/m + db16*(m-f2)/m
				effA := sa * ma16 / m
				out3 := effA + da16*(m-effA)/m
				if out3 == 0 {
					dst.Pix[i+0], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = 0, 0, 0, 0
					continue
				}
				// Un-premultiply to 8 bits with round-to-nearest. The ratio
				// out/out3 is at most 1, so out*255/out3 is at most 255 and
				// the product fits in uint32.
				dst.Pix[i+0] = sat8Div(out0*255, out3)
				dst.Pix[i+1] = sat8Div(out1*255, out3)
				dst.Pix[i+2] = sat8Div(out2*255, out3)
				dst.Pix[i+3] = sat8(out3)
			}
		}
	}
}
