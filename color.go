// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"fmt"
	"image"
	"math"

	"github.com/go-avkit/h265"
)

// maxBuffer is the largest buffer this reader allocates in one piece, whatever
// Limits.MaxPixels says: a slice must be addressable on a 32-bit platform too.
const maxBuffer = math.MaxInt32

// raster is a decoded image in one of the image package's packed layouts: bpp is 1
// (Gray), 2 (Gray16), 4 (RGBA) or 8 (RGBA64, big-endian samples).
type raster struct {
	pix    []byte
	stride int
	w, h   int
	bpp    int
}

// fits reports whether a buffer of n bytes may be allocated.
func fits(n uint64) bool { return n <= maxBuffer }

func newRaster(w, h, bpp int) (*raster, error) {
	if !fits(uint64(w) * uint64(h) * uint64(bpp)) {
		return nil, fmt.Errorf("%w: %dx%d pixels of %d bytes", ErrImageTooLarge, w, h, bpp)
	}
	return &raster{pix: make([]byte, w*h*bpp), stride: w * bpp, w: w, h: h, bpp: bpp}, nil
}

func (r *raster) image() image.Image {
	rect := image.Rect(0, 0, r.w, r.h)
	switch r.bpp {
	case 1:
		return &image.Gray{Pix: r.pix, Stride: r.stride, Rect: rect}
	case 2:
		return &image.Gray16{Pix: r.pix, Stride: r.stride, Rect: rect}
	case 4:
		return &image.RGBA{Pix: r.pix, Stride: r.stride, Rect: rect}
	}
	return &image.RGBA64{Pix: r.pix, Stride: r.stride, Rect: rect}
}

// matrixCoefficients returns Kr and Kb of a matrix_coefficients code point (ITU-T H.273
// table 4). Unspecified (2) reads as BT.601, which is what HEIF writers mean by it.
func matrixCoefficients(m uint16) (kr, kb float64, err error) {
	switch m {
	case 1:
		return 0.2126, 0.0722, nil
	case 2, 5, 6:
		return 0.299, 0.114, nil
	case 9:
		return 0.2627, 0.0593, nil
	}
	return 0, 0, fmt.Errorf("%w: matrix_coefficients %d", ErrUnsupported, m)
}

// colourOf returns the matrix and range to use: the nclx colr of the item, else BT.601
// with full range (the HEIF default for an image without a colour description).
func colourOf(c *nclx) (matrix uint16, full bool) {
	if c == nil {
		return 6, true
	}
	return c.Matrix, c.FullRange
}

// render turns a decoded picture into an image: the colour matrix and range are applied,
// then the transformations. Without transformations, 8-bit full-range BT.601 pictures
// are returned as they are decoded (an *image.YCbCr or *image.Gray sharing the decoder's
// planes); everything else is converted to *image.RGBA, *image.RGBA64, *image.Gray or
// *image.Gray16.
func render(pic *h265.DecodedPicture, c *nclx, xs []xform) (image.Image, error) {
	matrix, full := colourOf(c)
	hs, vs, ok := shifts(pic)
	if !ok {
		return nil, fmt.Errorf("%w: chroma planes of an unusual size", ErrUnsupported)
	}
	kr, kb, err := matrixCoefficients(matrix)
	if err != nil && pic.Components == 3 {
		return nil, err
	}
	if full && pic.Is8() && (pic.Components == 1 || matrix == 2 || matrix == 5 || matrix == 6) {
		if win, ok := window(xs, pic.Planes[0].Width, pic.Planes[0].Height, hs, vs); ok {
			return direct(pic, hs, vs, win), nil
		}
	}
	var r *raster
	if pic.Components == 1 {
		r, err = convertGray(pic, full)
	} else {
		r, err = convertColour(pic, hs, vs, kr, kb, full)
	}
	if err != nil {
		return nil, err
	}
	for _, x := range xs {
		if r, err = apply(r, x); err != nil {
			return nil, err
		}
	}
	return r.image(), nil
}

// window returns the rectangle of a w x h picture that the transformations select when
// they are all clean apertures whose corner falls on a chroma sample, so that the result
// can share the decoder's planes; false when any other transformation applies.
func window(xs []xform, w, h int, hs, vs uint) (image.Rectangle, bool) {
	x0, y0 := 0, 0
	for _, x := range xs {
		// a rotation or a mirror carries no aperture, and cropRect refuses its zero
		// denominators, which ends the search just as a fault in a real one does (the
		// conversion path reports it)
		cx0, cy0, cx1, cy1, err := cropRect(x.clap, w, h)
		if err != nil {
			return image.Rectangle{}, false
		}
		x0, y0, w, h = x0+cx0, y0+cy0, cx1-cx0, cy1-cy0
	}
	if x0&(1<<hs-1) != 0 || y0&(1<<vs-1) != 0 {
		return image.Rectangle{}, false
	}
	return image.Rect(x0, y0, x0+w, y0+h), true
}

// direct wraps a window of an 8-bit picture without copying it.
func direct(pic *h265.DecodedPicture, hs, vs uint, win image.Rectangle) image.Image {
	y := &pic.Planes[0]
	rect := image.Rect(0, 0, win.Dx(), win.Dy())
	yo := win.Min.Y*y.Stride + win.Min.X
	if pic.Components == 1 {
		return &image.Gray{Pix: y.Pix8[yo:], Stride: y.Stride, Rect: rect}
	}
	cs := pic.Planes[1].Stride
	co := (win.Min.Y>>vs)*cs + win.Min.X>>hs
	ratio := image.YCbCrSubsampleRatio444
	switch {
	case hs == 1 && vs == 1:
		ratio = image.YCbCrSubsampleRatio420
	case hs == 1:
		ratio = image.YCbCrSubsampleRatio422
	case vs == 1:
		ratio = image.YCbCrSubsampleRatio440
	}
	return &image.YCbCr{
		Y: y.Pix8[yo:], Cb: pic.Planes[1].Pix8[co:], Cr: pic.Planes[2].Pix8[co:],
		YStride: y.Stride, CStride: cs,
		SubsampleRatio: ratio, Rect: rect,
	}
}

// lumaLUT maps a sample of depth d to the output range (255 for 8-bit output, 65535 for
// wider), in 16.16 fixed point, undoing the limited range when full is false.
func lumaLUT(d int, full bool, outMax float64) []int64 {
	n := 1 << d
	maxv := float64(n - 1)
	off, span := 0.0, maxv
	if !full {
		off, span = float64(int(16)<<(d-8)), float64(int(219)<<(d-8))
	}
	t := make([]int64, n)
	for v := range t {
		t[v] = int64(math.Round((float64(v) - off) / span * outMax * 65536))
	}
	return t
}

// chromaLUT maps a chroma sample of depth d to its offset from the middle, in the output
// range, in 16.16 fixed point, times k.
func chromaLUT(d int, full bool, outMax, k float64) []int64 {
	n := 1 << d
	span := float64(n - 1)
	if !full {
		span = float64(int(224) << (d - 8))
	}
	mid := float64(int(1) << (d - 1))
	t := make([]int64, n)
	for v := range t {
		t[v] = int64(math.Round((float64(v) - mid) / span * outMax * k * 65536))
	}
	return t
}

func outputBits(pic *h265.DecodedPicture) (wide bool, outMax float64) {
	if pic.Is8() {
		return false, 255
	}
	return true, 65535
}

// clamp16 rounds a 16.16 value to an integer in [0, top].
func clamp16(v, top int64) int64 {
	return min(max((v+32768)>>16, 0), top)
}

func convertGray(pic *h265.DecodedPicture, full bool) (*raster, error) {
	wide, outMax := outputBits(pic)
	bpp := 1
	if wide {
		bpp = 2
	}
	p := &pic.Planes[0]
	r, err := newRaster(p.Width, p.Height, bpp)
	if err != nil {
		return nil, err
	}
	lut := lumaLUT(pic.BitDepth[0], full, outMax)
	for y := 0; y < p.Height; y++ {
		o := y * r.stride
		for x := 0; x < p.Width; x++ {
			v := clamp16(lut[sample(p, y*p.Stride+x)], int64(outMax))
			if wide {
				r.pix[o+2*x], r.pix[o+2*x+1] = byte(v>>8), byte(v)
			} else {
				r.pix[o+x] = byte(v)
			}
		}
	}
	return r, nil
}

func sample(p *h265.Plane, i int) int {
	if p.Pix8 != nil {
		return int(p.Pix8[i])
	}
	return int(p.Pix16[i])
}

func convertColour(pic *h265.DecodedPicture, hs, vs uint, kr, kb float64, full bool) (*raster, error) {
	wide, outMax := outputBits(pic)
	bpp := 4
	if wide {
		bpp = 8
	}
	yp, bp, rp := &pic.Planes[0], &pic.Planes[1], &pic.Planes[2]
	r, err := newRaster(yp.Width, yp.Height, bpp)
	if err != nil {
		return nil, err
	}
	kg := 1 - kr - kb
	yl := lumaLUT(pic.BitDepth[0], full, outMax)
	rCr := chromaLUT(pic.BitDepth[2], full, outMax, 2*(1-kr))
	bCb := chromaLUT(pic.BitDepth[1], full, outMax, 2*(1-kb))
	gCb := chromaLUT(pic.BitDepth[1], full, outMax, -2*(1-kb)*kb/kg)
	gCr := chromaLUT(pic.BitDepth[2], full, outMax, -2*(1-kr)*kr/kg)
	om := int64(outMax)
	for y := 0; y < yp.Height; y++ {
		o := y * r.stride
		cy := (y >> vs) * bp.Stride
		for x := 0; x < yp.Width; x++ {
			l := yl[sample(yp, y*yp.Stride+x)]
			cb := sample(bp, cy+x>>hs)
			cr := sample(rp, (y>>vs)*rp.Stride+x>>hs)
			rr := clamp16(l+rCr[cr], om)
			gg := clamp16(l+gCb[cb]+gCr[cr], om)
			bb := clamp16(l+bCb[cb], om)
			if wide {
				q := r.pix[o+8*x : o+8*x+8 : o+8*x+8]
				q[0], q[1], q[2], q[3], q[4], q[5], q[6], q[7] = byte(rr>>8), byte(rr), byte(gg>>8), byte(gg), byte(bb>>8), byte(bb), 0xFF, 0xFF
			} else {
				q := r.pix[o+4*x : o+4*x+4 : o+4*x+4]
				q[0], q[1], q[2], q[3] = byte(rr), byte(gg), byte(bb), 0xFF
			}
		}
	}
	return r, nil
}
