//go:build ignore

// Draws the home screen icons into static/: a white thermometer on the page's
// accent colour. The square is filled to the edge, and the thermometer stays
// inside the middle circle that Android keeps when it masks an icon.
// Run with go generate ./internal/web.
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

var (
	accent = color.NRGBA{0xb4, 0x49, 0x0f, 0xff}
	white  = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

// A thermometer in a unit square: a round-topped stem down into a bulb.
type thermometer struct {
	stem, bulb, top float64
}

// inside reports whether (x, y) is inside the thermometer.
func (t thermometer) inside(x, y float64) bool {
	const cx, bulbY = 0.5, 0.655
	if math.Hypot(x-cx, y-bulbY) <= t.bulb {
		return true
	}
	half := t.stem / 2
	if y >= t.top && y <= bulbY && math.Abs(x-cx) <= half {
		return true
	}
	return math.Hypot(x-cx, y-t.top) <= half
}

var (
	outline = thermometer{stem: 0.13, bulb: 0.15, top: 0.26}
	glass   = thermometer{stem: 0.07, bulb: 0.12, top: 0.26}
	mercury = thermometer{stem: 0.03, bulb: 0.08, top: 0.42}
)

func colorAt(x, y float64) color.NRGBA {
	switch {
	case mercury.inside(x, y):
		return white
	case glass.inside(x, y):
		return accent
	case outline.inside(x, y):
		return white
	default:
		return accent
	}
}

func draw(size int) *image.NRGBA {
	const samples = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := range size {
		for px := range size {
			var r, g, b float64
			for sy := range samples {
				for sx := range samples {
					x := (float64(px) + (float64(sx)+0.5)/samples) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/samples) / float64(size)
					c := colorAt(x, y)
					r, g, b = r+float64(c.R), g+float64(c.G), b+float64(c.B)
				}
			}
			n := float64(samples * samples)
			img.SetNRGBA(px, py, color.NRGBA{uint8(math.Round(r / n)), uint8(math.Round(g / n)), uint8(math.Round(b / n)), 0xff})
		}
	}
	return img
}

func main() {
	for name, size := range map[string]int{
		"icon-192.png":         192,
		"icon-512.png":         512,
		"apple-touch-icon.png": 180,
	} {
		f, err := os.Create(filepath.Join("static", name))
		if err != nil {
			log.Fatal(err)
		}
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(f, draw(size)); err != nil {
			log.Fatal(err)
		}
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
	}
}
