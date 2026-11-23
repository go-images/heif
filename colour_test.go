// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/go-avkit/h265"
)

// planesOf decodes the base picture of a built file without converting it.
func planesOf(t testing.TB, data []byte) *h265.DecodedPicture {
	t.Helper()
	f, err := parseFile(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	pl, err := f.primaryPlan()
	if err != nil {
		t.Fatal(err)
	}
	pic, err := f.decodeBase(pl)
	if err != nil {
		t.Fatal(err)
	}
	return pic
}

// refRGB converts one pixel of a picture the way ITU-T H.273 defines the matrix: in
// floating point, the oracle for the fixed-point tables.
func refRGB(p *h265.DecodedPicture, x, y int, kr, kb float64, full bool) (r, g, b float64) {
	at := func(c int, x, y int) float64 {
		pl := p.Planes[c]
		if c > 0 && pl.Width < p.Planes[0].Width {
			x >>= 1
		}
		if c > 0 && pl.Height < p.Planes[0].Height {
			y >>= 1
		}
		if pl.Pix8 != nil {
			return float64(pl.Pix8[y*pl.Stride+x])
		}
		return float64(pl.Pix16[y*pl.Stride+x])
	}
	d := p.BitDepth[0]
	scale := float64(int(1) << (d - 8))
	yv := at(0, x, y)
	if p.Components == 1 {
		if full {
			return yv / (math.Exp2(float64(d)) - 1), yv / (math.Exp2(float64(d)) - 1), yv / (math.Exp2(float64(d)) - 1)
		}
		v := (yv - 16*scale) / (219 * scale)
		return v, v, v
	}
	cb, cr := at(1, x, y), at(2, x, y)
	var ey, ecb, ecr float64
	if full {
		m := math.Exp2(float64(d)) - 1
		ey, ecb, ecr = yv/m, (cb-math.Exp2(float64(d-1)))/m, (cr-math.Exp2(float64(d-1)))/m
	} else {
		ey, ecb, ecr = (yv-16*scale)/(219*scale), (cb-128*scale)/(224*scale), (cr-128*scale)/(224*scale)
	}
	kg := 1 - kr - kb
	r = ey + 2*(1-kr)*ecr
	b = ey + 2*(1-kb)*ecb
	g = ey - 2*(1-kb)*kb/kg*ecb - 2*(1-kr)*kr/kg*ecr
	return r, g, b
}

func TestColourConversion(t *testing.T) {
	defer guard(t)
	type kk struct{ kr, kb float64 }
	coefs := map[uint16]kk{1: {0.2126, 0.0722}, 2: {0.299, 0.114}, 5: {0.299, 0.114}, 6: {0.299, 0.114}, 9: {0.2627, 0.0593}}
	for _, name := range []struct {
		file         string
		chroma, bits int
	}{
		{"gray", 0, 8}, {"gray10le", 0, 10}, {"yuv420p", 1, 8}, {"yuv420p10le", 1, 10},
		{"yuv422p", 2, 8}, {"yuv444p", 3, 8}, {"yuv444p10le", 3, 10}, {"yuv420p12le", 1, 12},
	} {
		c := loadCoded(t, name.file, name.chroma, name.bits)
		for matrix, k := range coefs {
			for _, full := range []bool{true, false} {
				data := single(c, 36, 22, colrNclx(1, 1, matrix, full)).bytes()
				res, err := DecodeOptions(bytes.NewReader(data), Options{})
				if err != nil {
					t.Fatalf("%s m%d full=%v: %v", name.file, matrix, full, err)
				}
				if res.Nclx == nil || res.Nclx.Matrix != matrix || res.Nclx.FullRange != full {
					t.Fatalf("nclx %+v", res.Nclx)
				}
				pic := planesOf(t, data)
				top, tol := 255.0, 0.51
				if name.bits > 8 {
					top, tol = 65535, 1.01
				}
				if res.Image.ColorModel() == color.YCbCrModel {
					tol = 2.5 // the image package's own rounding
				}
				for y := 0; y < 22; y++ {
					for x := 0; x < 36; x++ {
						r, g, b := refRGB(pic, x, y, k.kr, k.kb, full)
						gr, gg, gb, _ := res.Image.At(x, y).RGBA()
						scale := 65535 / top
						for i, w := range [3]float64{r, g, b} {
							want := math.Min(math.Max(w*top, 0), top)
							got := float64([3]uint32{gr, gg, gb}[i]) / scale
							if math.Abs(got-want) > tol {
								t.Fatalf("%s m%d full=%v (%d,%d) channel %d: %.2f, want %.2f", name.file, matrix, full, x, y, i, got, want)
							}
						}
					}
				}
			}
		}
	}
}

func TestColourModels(t *testing.T) {
	defer guard(t)
	for _, tc := range []struct {
		file         string
		chroma, bits int
		extra        [][]byte
		want         color.Model
		typ          string
	}{
		{"yuv420p", 1, 8, nil, color.YCbCrModel, "*image.YCbCr"},
		{"yuv420p", 1, 8, [][]byte{colrNclx(1, 1, 6, true)}, color.YCbCrModel, "*image.YCbCr"},
		{"yuv420p", 1, 8, [][]byte{colrNclx(1, 1, 2, true)}, color.YCbCrModel, "*image.YCbCr"},
		{"yuv420p", 1, 8, [][]byte{colrNclx(1, 1, 5, true)}, color.YCbCrModel, "*image.YCbCr"},
		{"yuv420p", 1, 8, [][]byte{colrNclx(1, 1, 1, true)}, color.RGBAModel, "*image.RGBA"},
		{"yuv420p", 1, 8, [][]byte{colrNclx(1, 1, 6, false)}, color.RGBAModel, "*image.RGBA"},
		{"yuv420p10le", 1, 10, nil, color.RGBA64Model, "*image.RGBA64"},
		{"gray", 0, 8, nil, color.GrayModel, "*image.Gray"},
		{"gray", 0, 8, [][]byte{colrNclx(1, 1, 6, false)}, color.GrayModel, "*image.Gray"},
		{"gray10le", 0, 10, nil, color.Gray16Model, "*image.Gray16"},
		{"yuv422p", 2, 8, nil, color.YCbCrModel, "*image.YCbCr"},
		{"yuv444p", 3, 8, nil, color.YCbCrModel, "*image.YCbCr"},
	} {
		c := loadCoded(t, tc.file, tc.chroma, tc.bits)
		data := single(c, 36, 22, tc.extra...).bytes()
		img, err := Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		cfg, err := DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ColorModel != tc.want || img.ColorModel() != tc.want || cfg.Width != 36 || cfg.Height != 22 {
			t.Errorf("%s %v: config %v %dx%d, image %T, want %v", tc.file, tc.extra, cfg.ColorModel, cfg.Width, cfg.Height, img, tc.want)
		}
		if got := typeName(img); got != tc.typ {
			t.Errorf("%s: %s, want %s", tc.file, got, tc.typ)
		}
	}
}

func typeName(img image.Image) string {
	switch img.(type) {
	case *image.YCbCr:
		return "*image.YCbCr"
	case *image.RGBA:
		return "*image.RGBA"
	case *image.RGBA64:
		return "*image.RGBA64"
	case *image.Gray:
		return "*image.Gray"
	case *image.Gray16:
		return "*image.Gray16"
	}
	return "other"
}

func TestUnsupportedMatrix(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	data := single(c, 36, 22, colrNclx(1, 1, 8, true)).bytes()
	if _, err := Decode(bytes.NewReader(data)); !errorsIs(err, ErrUnsupported) {
		t.Errorf("matrix 8: %v", err)
	}
	// a monochrome picture has no matrix to be wrong about
	g := loadCoded(t, "gray", 0, 8)
	if _, err := Decode(bytes.NewReader(single(g, 36, 22, colrNclx(1, 1, 8, false)).bytes())); err != nil {
		t.Errorf("gray with matrix 8: %v", err)
	}
}

func TestICCAndAlphaReported(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	h := single(c, 36, 22, colrICC([]byte("profile-bytes")))
	// an alpha auxiliary item pointing at item 1
	h.props = append(h.props, mkbox("auxC", u32(0), []byte("urn:mpeg:mpegB:cicp:systems:auxiliary:alpha\x00")))
	h.items = append(h.items, hitem{id: 2, typ: "hvc1", data: c.data, props: []int{1, 2, len(h.props)}})
	h.refs = []href{{"auxl", 2, []uint32{1}}}
	res, err := DecodeOptions(bytes.NewReader(h.bytes()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.ICCProfile) != "profile-bytes" || !res.HasAlpha || res.Nclx != nil {
		t.Errorf("icc %q alpha %v nclx %v", res.ICCProfile, res.HasAlpha, res.Nclx)
	}
}
