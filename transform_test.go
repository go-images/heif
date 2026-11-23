// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// xfBase decodes the 36x22 4:2:0 test picture through the RGBA path with no
// transformation: what each transformed result is compared with.
func xfBase(t testing.TB, c coded, extra ...[]byte) image.Image {
	t.Helper()
	img, err := Decode(bytes.NewReader(single(c, 36, 22, append([][]byte{colrNclx(1, 1, 1, true)}, extra...)...).bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// mapped builds the image a transformation must give from the base image, pixel by pixel
// through the coordinate map, written independently of the package code.
func mapped(base image.Image, w, h int, src func(x, y int) (int, int)) image.Image {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx, sy := src(x, y)
			out.Set(x, y, base.At(sx, sy))
		}
	}
	return out
}

func sameImage(t *testing.T, name string, got, want image.Image) {
	t.Helper()
	if got.Bounds() != want.Bounds() {
		t.Errorf("%s: bounds %v, want %v", name, got.Bounds(), want.Bounds())
		return
	}
	b := got.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if got.At(x, y) != want.At(x, y) {
				t.Errorf("%s: pixel (%d,%d) is %v, want %v", name, x, y, got.At(x, y), want.At(x, y))
				return
			}
		}
	}
}

func TestTransforms(t *testing.T) {
	c := loadCoded(t, "yuv420p", 1, 8)
	base := xfBase(t, c)
	const w, h = 36, 22
	for _, tc := range []struct {
		name  string
		extra [][]byte
		w, h  int
		src   func(x, y int) (int, int)
	}{
		// irot is anti-clockwise: the top-right corner of the source ends at the top-left.
		{"irot1", [][]byte{irot(1)}, h, w, func(x, y int) (int, int) { return w - 1 - y, x }},
		{"irot2", [][]byte{irot(2)}, w, h, func(x, y int) (int, int) { return w - 1 - x, h - 1 - y }},
		{"irot3", [][]byte{irot(3)}, h, w, func(x, y int) (int, int) { return y, h - 1 - x }},
		{"irot0", [][]byte{irot(0)}, w, h, func(x, y int) (int, int) { return x, y }},
		{"irot4", [][]byte{irot(4)}, w, h, func(x, y int) (int, int) { return x, y }}, // only two bits count
		{"imir0", [][]byte{imir(0)}, w, h, func(x, y int) (int, int) { return x, h - 1 - y }},
		{"imir1", [][]byte{imir(1)}, w, h, func(x, y int) (int, int) { return w - 1 - x, y }},
		{"imir2", [][]byte{imir(2)}, w, h, func(x, y int) (int, int) { return x, h - 1 - y }}, // only one bit counts
		// the order the properties are listed in is the order they apply in
		// rotate, then flip left-right: out(x,y) = rotated(h-1-x, y) = base(w-1-y, h-1-x)
		{"irot1 then imir1", [][]byte{irot(1), imir(1)}, h, w, func(x, y int) (int, int) { return w - 1 - y, h - 1 - x }},
		// flip left-right, then rotate: out(x,y) = flipped(w-1-y, x) = base(y, x)
		{"imir1 then irot1", [][]byte{imir(1), irot(1)}, h, w, func(x, y int) (int, int) { return y, x }},
	} {
		got, err := Decode(bytes.NewReader(single(c, w, h, append([][]byte{colrNclx(1, 1, 1, true)}, tc.extra...)...).bytes()))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		sameImage(t, tc.name, got, mapped(base, tc.w, tc.h, tc.src))
		cfg, err := DecodeConfig(bytes.NewReader(single(c, w, h, tc.extra...).bytes()))
		if err != nil || cfg.Width != tc.w || cfg.Height != tc.h {
			t.Errorf("%s: config %v %v", tc.name, cfg, err)
		}
	}
}

func TestCleanAperture(t *testing.T) {
	c := loadCoded(t, "yuv420p", 1, 8)
	base := xfBase(t, c)
	const w, h = 36, 22
	// clap: width num/den, height num/den, horizontal offset, vertical offset
	for _, tc := range []struct {
		name           string
		clap           []uint32
		x0, y0, cw, ch int
	}{
		{"centred", []uint32{20, 1, 10, 1, 0, 1, 0, 1}, 8, 6, 20, 10},
		{"offset", []uint32{20, 1, 10, 1, 4, 1, 2, 1}, 12, 8, 20, 10},
		{"negative offset", []uint32{20, 1, 10, 1, uint32(0xFFFFFFFC), 1, uint32(0xFFFFFFFE), 1}, 4, 4, 20, 10},
		// 20.5 x 10.5 with the edges rounded the way libheif does: left down, top to nearest
		{"fractions", []uint32{41, 2, 21, 2, 1, 2, 1, 2}, 8, 6, 21, 11},
		{"odd corner", []uint32{21, 1, 11, 1, 0, 1, 0, 1}, 7, 6, 21, 11},
		{"larger than the image", []uint32{100, 1, 100, 1, 0, 1, 0, 1}, 0, 0, w, h},
		{"to the corner", []uint32{20, 1, 10, 1, 8, 1, 6, 1}, 16, 12, 20, 10},
	} {
		got, err := Decode(bytes.NewReader(single(c, w, h, colrNclx(1, 1, 1, true), clap(tc.clap...)).bytes()))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		sameImage(t, tc.name, got, mapped(base, tc.cw, tc.ch, func(x, y int) (int, int) { return tc.x0 + x, tc.y0 + y }))
	}
	for _, tc := range []struct {
		name string
		clap []uint32
	}{
		{"zero denominator", []uint32{20, 0, 10, 1, 0, 1, 0, 1}},
		{"zero offset denominator", []uint32{20, 1, 10, 1, 0, 0, 0, 1}},
		{"empty", []uint32{0, 1, 10, 1, 0, 1, 0, 1}},
		{"outside", []uint32{10, 1, 10, 1, 500, 1, 0, 1}},
	} {
		if _, err := Decode(bytes.NewReader(single(c, w, h, clap(tc.clap...)).bytes())); !errorsIs(err, ErrInvalid) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// A clean aperture whose corner falls on a chroma sample shares the decoder's planes; one
// that does not is converted. Both give the same pixels.
func TestApertureWindowSharesPlanes(t *testing.T) {
	c := loadCoded(t, "yuv420p", 1, 8)
	for _, tc := range []struct {
		name   string
		clap   []uint32
		direct bool
	}{
		{"even corner", []uint32{20, 1, 10, 1, 0, 1, 0, 1}, true}, // (8,6)
		{"odd x", []uint32{20, 1, 10, 1, 1, 1, 0, 1}, false},      // (9,6)
		{"odd y", []uint32{20, 1, 10, 1, 0, 1, 1, 1}, false},      // (8,7)
	} {
		whole, err := Decode(bytes.NewReader(single(c, 36, 22).bytes()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(bytes.NewReader(single(c, 36, 22, clap(tc.clap...)).bytes()))
		if err != nil {
			t.Fatal(err)
		}
		_, isYCbCr := got.(*image.YCbCr)
		if isYCbCr != tc.direct {
			t.Errorf("%s: %T", tc.name, got)
		}
		x0, y0 := 8, 6
		if tc.name == "odd x" {
			x0 = 9
		}
		if tc.name == "odd y" {
			y0 = 7
		}
		b := got.Bounds()
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				a, c2 := colorDiff(got.At(x, y), whole.At(x0+x, y0+y))
				if a > 3 || c2 > 3 {
					t.Fatalf("%s: (%d,%d) %v vs %v", tc.name, x, y, got.At(x, y), whole.At(x0+x, y0+y))
				}
			}
		}
	}
}

func colorDiff(a, b color.Color) (int, int) {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	d := func(x, y uint32) int {
		if x > y {
			return int(x-y) >> 8
		}
		return int(y-x) >> 8
	}
	return max(d(ar, br), d(ag, bg), d(ab, bb)), 0
}

func TestApertureOfMonochromeSharesPlanes(t *testing.T) {
	g := loadCoded(t, "gray", 0, 8)
	got, err := Decode(bytes.NewReader(single(g, 36, 22, clap(21, 1, 11, 1, 0, 1, 0, 1)).bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if gray, ok := got.(*image.Gray); !ok || gray.Bounds().Dx() != 21 || gray.Bounds().Dy() != 11 {
		t.Fatalf("%T %v", got, got.Bounds())
	}
	whole, _ := Decode(bytes.NewReader(single(g, 36, 22).bytes()))
	if got.At(0, 0) != whole.At(7, 6) || got.At(20, 10) != whole.At(27, 16) {
		t.Error("the window is not at the clean aperture")
	}
}

func TestMonochromeAndDeepTransforms(t *testing.T) {
	for _, tc := range []struct {
		file         string
		chroma, bits int
	}{{"gray", 0, 8}, {"gray10le", 0, 10}, {"yuv444p10le", 3, 10}} {
		c := loadCoded(t, tc.file, tc.chroma, tc.bits)
		base, err := Decode(bytes.NewReader(single(c, 36, 22, colrNclx(1, 1, 1, true)).bytes()))
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range [][]byte{irot(1), irot(2), irot(3), imir(0), imir(1)} {
			got, err := Decode(bytes.NewReader(single(c, 36, 22, colrNclx(1, 1, 1, true), x).bytes()))
			if err != nil {
				t.Fatal(err)
			}
			// each transformation is a permutation of the pixels
			if hist(base) != hist(got) {
				t.Errorf("%s: %v changes the pixel multiset", tc.file, x[4:])
			}
		}
	}
}

// hist is an order-independent digest of an image's pixels.
func hist(img image.Image) [3]uint64 {
	var s [3]uint64
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			s[0] += uint64(r)
			s[1] += uint64(g) * 3
			s[2] += uint64(bl) * uint64(bl)
		}
	}
	return s
}
