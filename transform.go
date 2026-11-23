// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

// apply runs one transformative property on a raster.
func apply(r *raster, x xform) (*raster, error) {
	switch x.kind {
	case 'r':
		if x.n == 0 {
			return r, nil
		}
		return rotate(r, int(x.n))
	case 'm':
		mirror(r, x.n)
		return r, nil
	}
	x0, y0, x1, y1, err := cropRect(x.clap, r.w, r.h)
	if err != nil {
		return nil, err
	}
	return crop(r, x0, y0, x1, y1), nil
}

// rotate turns the raster anti-clockwise by n quarter turns (1 to 3) into a new one.
func rotate(r *raster, n int) (*raster, error) {
	w2, h2 := r.w, r.h
	if n&1 != 0 {
		w2, h2 = r.h, r.w
	}
	out, err := newRaster(w2, h2, r.bpp)
	if err != nil {
		return nil, err
	}
	b := r.bpp
	for y2 := 0; y2 < h2; y2++ {
		for x2 := 0; x2 < w2; x2++ {
			var sx, sy int
			switch n {
			case 1: // the pixel at (x, y) goes to (y, w-1-x)
				sx, sy = r.w-1-y2, x2
			case 2:
				sx, sy = r.w-1-x2, r.h-1-y2
			default: // the pixel at (x, y) goes to (h-1-y, x)
				sx, sy = y2, r.h-1-x2
			}
			copy(out.pix[y2*out.stride+x2*b:][:b], r.pix[sy*r.stride+sx*b:][:b])
		}
	}
	return out, nil
}

// mirror flips the raster in place. ISO/IEC 23008-12 words imir's axis as "a vertical (0)
// or horizontal (1) axis", which reads two ways; this follows libheif (box.cc Box_imir::parse
// and image_item.cc, where 0 is "flip vertically" and 1 "flip horizontally"): axis 0
// swaps top and bottom, axis 1 swaps left and right.
func mirror(r *raster, axis uint8) {
	b := r.bpp
	if axis == 0 {
		tmp := make([]byte, r.w*b)
		for y := 0; y < r.h/2; y++ {
			top := r.pix[y*r.stride:][:r.w*b]
			bot := r.pix[(r.h-1-y)*r.stride:][:r.w*b]
			copy(tmp, top)
			copy(top, bot)
			copy(bot, tmp)
		}
		return
	}
	var tmp [8]byte
	for y := 0; y < r.h; y++ {
		row := r.pix[y*r.stride:][:r.w*b]
		for i, j := 0, (r.w-1)*b; i < j; i, j = i+b, j-b {
			copy(tmp[:b], row[i:i+b])
			copy(row[i:i+b], row[j:j+b])
			copy(row[j:j+b], tmp[:b])
		}
	}
}

// crop returns the rectangle [x0,x1) x [y0,y1) as a raster sharing the pixels.
func crop(r *raster, x0, y0, x1, y1 int) *raster {
	return &raster{
		pix:    r.pix[y0*r.stride+x0*r.bpp:],
		stride: r.stride,
		w:      x1 - x0, h: y1 - y0, bpp: r.bpp,
	}
}
