package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"os"
	"testing"
)

func decode(t *testing.T, name string, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return img
}

func TestIcons(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"off", "on", "attention"} {
		data, err := os.ReadFile("icons/" + name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		img := decode(t, name, data)
		if img.Bounds() != image.Rect(0, 0, 44, 44) {
			t.Errorf("%s.png is %v", name, img.Bounds())
		}
		var shape []byte
		for y := range 44 {
			for x := range 44 {
				r, g, b, a := img.At(x, y).RGBA()
				if r|g|b != 0 {
					t.Fatalf("%s.png is not a black template", name)
				}
				shape = append(shape, byte(a>>8))
			}
		}
		if seen[string(shape)] {
			t.Errorf("%s.png has the shape of another state", name)
		}
		seen[string(shape)] = true

		ico, err := os.ReadFile("icons/" + name + ".ico")
		if err != nil {
			t.Fatal(err)
		}
		le := binary.LittleEndian
		if le.Uint16(ico) != 0 || le.Uint16(ico[2:]) != 1 || le.Uint16(ico[4:]) != 3 {
			t.Fatalf("%s.ico header % x", name, ico[:6])
		}
		for i, size := range []int{16, 32, 48} {
			e := ico[6+16*i:]
			length, offset := le.Uint32(e[8:]), le.Uint32(e[12:])
			img := decode(t, name, ico[offset:offset+length])
			if int(e[0]) != size || int(e[1]) != size || img.Bounds() != image.Rect(0, 0, size, size) {
				t.Errorf("%s.ico image %d: %dx%d %v", name, i, e[0], e[1], img.Bounds())
			}
		}
	}
}
