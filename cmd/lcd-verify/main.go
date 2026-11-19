// Copyright 2026 The Freetype-Go Authors. All rights reserved.
// Use of this source code is governed by your choice of either the
// FreeType License or the GNU General Public License version 2 (or
// any later version), both of which can be found in the LICENSE file.

// Command lcd-verify renders a fixed-size string with grayscale, RGB and BGR
// LCD sub-pixel rasterization onto white, dark and translucent backgrounds,
// writes the results as PNG files and prints numeric edge-displacement
// checks. It is a manual verification aid for the optional LCD rendering
// path, not a unit test.
//
// Usage:
//
//	go run ./cmd/lcd-verify -font testdata/luxisr.ttf -out artifacts
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"github.com/golang/freetype"
)

const (
	// Fixed small size, as required for the sub-pixel verification.
	fontSize = 9
	dpi      = 144 // a high-density, landscape panel-like resolution
	text     = "Subpixel 0123"
)

type bg struct {
	name string
	fill func(r image.Rectangle) *image.RGBA
}

func main() {
	fontPath := flag.String("font", "testdata/luxisr.ttf", "TrueType font file")
	outDir := flag.String("out", "artifacts", "output directory for PNG files")
	flag.Parse()

	data, err := os.ReadFile(*fontPath)
	must(err)
	f, err := freetype.ParseFont(data)
	must(err)
	must(os.MkdirAll(*outDir, 0o755))

	backgrounds := []bg{
		{"white", func(r image.Rectangle) *image.RGBA {
			m := image.NewRGBA(r)
			draw.Draw(m, r, image.White, image.ZP, draw.Src)
			return m
		}},
		{"dark", func(r image.Rectangle) *image.RGBA {
			m := image.NewRGBA(r)
			draw.Draw(m, r, image.NewUniform(color.RGBA{R: 16, G: 16, B: 24, A: 255}),
				image.ZP, draw.Src)
			return m
		}},
		{"translucent", func(r image.Rectangle) *image.RGBA {
			m := image.NewRGBA(r)
			for i := 0; i < len(m.Pix); i += 4 {
				// Opaque-looking red at 40% alpha: valid premultiplied.
				m.Pix[i+0], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = 40, 0, 0, 102
			}
			return m
		}},
	}

	modes := []struct {
		name string
		cfg  *freetype.LCDConfig
	}{
		{"gray", nil},
		{"rgb", &freetype.LCDConfig{StripOrder: freetype.LCDRGB, Filter: freetype.LCDFilterDefault}},
		{"bgr", &freetype.LCDConfig{StripOrder: freetype.LCDBGR, Filter: freetype.LCDFilterDefault}},
		{"rgb-light", &freetype.LCDConfig{StripOrder: freetype.LCDRGB, Filter: freetype.LCDFilterLight}},
	}

	bounds := image.Rect(0, 0, 220, 36)
	for _, b := range backgrounds {
		var pens []image.Point
		imgs := map[string]*image.RGBA{}
		for _, mode := range modes {
			dst := b.fill(bounds)
			c := freetype.NewContext()
			if mode.cfg != nil {
				must(c.SetLCD(mode.cfg))
			}
			c.SetDst(dst)
			c.SetClip(bounds)
			// Light text on dark, dark text elsewhere.
			if b.name == "dark" {
				c.SetSrc(image.NewUniform(color.RGBA{R: 235, G: 235, B: 240, A: 255}))
			} else {
				c.SetSrc(image.Black)
			}
			c.SetFont(f)
			c.SetFontSize(fontSize)
			c.SetDPI(dpi)
			pen, err := c.DrawString(text, freetype.Pt(4, 22))
			must(err)
			pens = append(pens, image.Point{X: int(pen.X >> 6), Y: int(pen.Y >> 6)})
			imgs[mode.name] = dst
			path := filepath.Join(*outDir, fmt.Sprintf("lcd-%s-%s.png", b.name, mode.name))
			must(writePNG(path, dst))
			fmt.Printf("wrote %s\n", path)
		}
		// All modes must return the same pen position.
		for i := 1; i < len(pens); i++ {
			if pens[i] != pens[0] {
				fmt.Printf("FAIL: pen mismatch on %s: %v vs %v\n", b.name, pens[0], pens[i])
				os.Exit(1)
			}
		}
		fmt.Printf("OK  %-11s pen=%v identical across all modes\n", b.name, pens[0])

		// On the white background, check the RGB/BGR channel swap and that
		// colored (non-gray) edge pixels exist.
		if b.name == "white" {
			verifySwap(imgs["rgb"], imgs["bgr"])
			verifyColorEdges(imgs["rgb"])
		}
	}
}

// verifySwap asserts that the BGR image is the exact R<->B swap of the RGB
// image and reports how many asymmetric edge pixels were observed.
func verifySwap(rgb, bgr *image.RGBA) {
	asym := 0
	for i := 0; i < len(rgb.Pix); i += 4 {
		r, g, b := rgb.Pix[i], rgb.Pix[i+1], rgb.Pix[i+2]
		br, bg, bb := bgr.Pix[i], bgr.Pix[i+1], bgr.Pix[i+2]
		if g != bg || r != bb || b != br {
			fmt.Printf("FAIL: RGB/BGR swap mismatch at pixel %d\n", i/4)
			os.Exit(1)
		}
		if r != b {
			asym++
		}
	}
	fmt.Printf("OK  RGB/BGR exact channel swap on %d asymmetric edge pixels\n", asym)
}

// verifyColorEdges reports the number of pixels whose R, G and B coverage
// differ, which is the defining property of LCD rendering.
func verifyColorEdges(rgb *image.RGBA) {
	split := 0
	for i := 0; i < len(rgb.Pix); i += 4 {
		r, g, b := rgb.Pix[i], rgb.Pix[i+1], rgb.Pix[i+2]
		if r != g || g != b {
			split++
		}
	}
	if split == 0 {
		fmt.Println("FAIL: no sub-pixel color split found")
		os.Exit(1)
	}
	fmt.Printf("OK  %d sub-pixel (R/G/B split) edge pixels\n", split)
}

func writePNG(path string, m image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, m)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
