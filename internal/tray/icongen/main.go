// Command icongen draws the tray icons. Run it with go generate ./internal/tray.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

// The glyph lives on a 22x22 grid: a road over one arch.
const grid = 22.0

type shape func(x, y float64) bool

func rect(x0, y0, x1, y1 float64) shape {
	return func(x, y float64) bool { return x >= x0 && x <= x1 && y >= y0 && y <= y1 }
}

func disc(cx, cy, r float64) shape {
	return func(x, y float64) bool { return math.Hypot(x-cx, y-cy) <= r }
}

// bridge is a solid road with one arched opening below it.
func bridge(top, bottom float64) shape {
	rx, ry := 8.0, bottom-top-2.6
	opening := func(x, y float64) bool { return math.Hypot((x-11)/rx, (y-bottom)/ry) <= 1 }
	return without(rect(0.5, top, 21.5, bottom), opening)
}

func union(s ...shape) shape {
	return func(x, y float64) bool {
		for _, f := range s {
			if f(x, y) {
				return true
			}
		}
		return false
	}
}

func without(a, b shape) shape {
	return func(x, y float64) bool { return a(x, y) && !b(x, y) }
}

var (
	glyphs = map[string]shape{
		"on": bridge(5.5, 17.5),
		// The middle is missing: no way across.
		"off": without(bridge(5.5, 17.5), rect(8, 0, 14, 22)),
		"attention": union(
			bridge(11, 21),
			rect(9.6, 0.5, 12.4, 5.4),
			disc(11, 7.9, 1.5),
		),
	}

	// Windows has no template icons, so each state also gets a colour.
	tiles = map[string]color.NRGBA{
		"on":        {0x1f, 0x7a, 0x45, 0xff},
		"off":       {0x5b, 0x64, 0x70, 0xff},
		"attention": {0xb4, 0x4a, 0x0b, 0xff},
	}
)

const samples = 8

func coverage(s shape, px, py int, scale float64) float64 {
	n := 0
	for i := range samples {
		for j := range samples {
			x := (float64(px) + (float64(i)+0.5)/samples) * scale
			y := (float64(py) + (float64(j)+0.5)/samples) * scale
			if s(x, y) {
				n++
			}
		}
	}
	return float64(n) / (samples * samples)
}

func template(s shape, size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			img.SetNRGBA(x, y, color.NRGBA{A: uint8(math.Round(255 * coverage(s, x, y, grid/float64(size))))})
		}
	}
	return img
}

func tile(s shape, bg color.NRGBA, size int) *image.NRGBA {
	const radius = 4.5
	rounded := func(x, y float64) bool {
		dx := max(radius-x, x-(grid-radius), 0)
		dy := max(radius-y, y-(grid-radius), 0)
		return math.Hypot(dx, dy) <= radius
	}
	// The glyph keeps a margin inside the tile.
	const margin = 2.4
	inner := func(x, y float64) bool {
		k := grid / (grid - 2*margin)
		return s((x-margin)*k, (y-margin)*k)
	}
	mix := func(c uint8, t float64) uint8 { return uint8(math.Round(float64(c)*(1-t) + 255*t)) }
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			scale := grid / float64(size)
			t := coverage(inner, x, y, scale)
			a := uint8(math.Round(255 * coverage(rounded, x, y, scale)))
			img.SetNRGBA(x, y, color.NRGBA{mix(bg.R, t), mix(bg.G, t), mix(bg.B, t), a})
		}
	}
	return img
}

func encode(img image.Image) []byte {
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&b, img); err != nil {
		log.Fatal(err)
	}
	return b.Bytes()
}

// ico packs PNG images into one icon file.
func ico(sizes []int, pngs [][]byte) []byte {
	var b bytes.Buffer
	put := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	put([3]uint16{0, 1, uint16(len(pngs))})
	offset := 6 + 16*len(pngs)
	for i, p := range pngs {
		put([4]uint8{uint8(sizes[i]), uint8(sizes[i]), 0, 0})
		put([2]uint16{1, 32})
		put([2]uint32{uint32(len(p)), uint32(offset)})
		offset += len(p)
	}
	for _, p := range pngs {
		b.Write(p)
	}
	return b.Bytes()
}

func main() {
	out := flag.String("out", "icons", "output directory")
	flag.Parse()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(*out, name), data, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	sizes := []int{16, 32, 48}
	for name, glyph := range glyphs {
		write(name+".png", encode(template(glyph, 44)))
		var pngs [][]byte
		for _, size := range sizes {
			pngs = append(pngs, encode(tile(glyph, tiles[name], size)))
		}
		write(name+".ico", ico(sizes, pngs))
	}
}
