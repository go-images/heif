// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"fmt"
	"image"
)

// orient is one of the eight orientations of a rectangular image (the dihedral group D4):
// an optional transposition, then an optional left-right and an optional top-bottom flip
// of the result. Any run of irot and imir properties composes to one of them, so a file
// that lists hundreds of them costs one pass over the pixels, not one per property.
type orient uint8

const (
	oSwap  orient = 1 << iota // the transposition: (x, y) goes to (y, x)
	oFlipX                    // left and right swap, after the transposition
	oFlipY                    // top and bottom swap, after the transposition
)

// flipX and flipY append a mirror to the orientation; the flips come after the
// transposition, so they only toggle.
func (o orient) flipX() orient { return o ^ oFlipX }
func (o orient) flipY() orient { return o ^ oFlipY }

// transpose appends a transposition: it swaps the two flips and toggles the transposition.
func (o orient) transpose() orient {
	n := (o & oSwap) ^ oSwap
	if o&oFlipX != 0 {
		n |= oFlipY
	}
	if o&oFlipY != 0 {
		n |= oFlipX
	}
	return n
}

// rotate appends n (1 to 3) anti-clockwise quarter turns. The pixel at (x, y) of a w x h
// image goes to (y, w-1-x) for one turn: a transposition, then a top-bottom flip.
func (o orient) rotate(n uint8) orient {
	switch n {
	case 1:
		return o.transpose().flipY()
	case 2:
		return o.flipX().flipY()
	case 3:
		return o.transpose().flipX()
	}
	return o // no turn
}

// placement is what the transformative properties of the primary item come to: the crop
// rectangle of the coded image (the zero rectangle stands for the whole of it), then the
// orientation of the cropped result.
type placement struct {
	crop image.Rectangle
	o    orient
}

// plain reports that nothing is cropped or turned.
func (p placement) plain() bool { return p.crop.Empty() && p.o == 0 }

// dims is the size of the result for a w x h coded image.
func (p placement) dims(w, h int) (int, int) {
	if !p.crop.Empty() {
		w, h = p.crop.Dx(), p.crop.Dy()
	}
	if p.o&oSwap != 0 {
		w, h = h, w
	}
	return w, h
}

// maxTransforms is the most irot, imir and clap properties one item may carry. They
// compose in constant time, so this only keeps a file from spending its bytes on
// properties that do nothing.
const maxTransforms = 16

// compose folds the transformations, in the order they apply, into one crop and one
// orientation of a w x h image. A clean aperture is taken in the image as the properties
// before it left it, then moved back to coordinates of the coded image, so the crops
// meet in one rectangle.
func compose(xs []xform, w, h int) (placement, error) {
	rect := image.Rect(0, 0, w, h)
	var o orient
	for _, x := range xs {
		switch x.kind {
		case 'r':
			o = o.rotate(x.n)
		case 'm':
			if x.n == 0 {
				o = o.flipY()
			} else {
				o = o.flipX()
			}
		default:
			cw, ch := rect.Dx(), rect.Dy()
			if o&oSwap != 0 {
				cw, ch = ch, cw
			}
			u0, v0, u1, v1, err := cropRect(x.clap, cw, ch)
			if err != nil {
				return placement{}, err
			}
			if o&oFlipX != 0 {
				u0, u1 = cw-u1, cw-u0
			}
			if o&oFlipY != 0 {
				v0, v1 = ch-v1, ch-v0
			}
			if o&oSwap != 0 {
				u0, v0, u1, v1 = v0, u0, v1, u1
			}
			rect = image.Rect(rect.Min.X+u0, rect.Min.Y+v0, rect.Min.X+u1, rect.Min.Y+v1)
		}
	}
	if rect == image.Rect(0, 0, w, h) {
		rect = image.Rectangle{}
	}
	return placement{crop: rect, o: o}, nil
}

// target is the raster a w x h picture is converted into, cropped and turned as it is
// written: the colour conversion puts each pixel of the crop at its place in the result,
// so no copy of the unturned picture exists. The pixel at (x, y) of the picture, counted
// from the corner of the crop as (cx, cy), lies at base + cx*sx + cy*sy bytes of r.pix.
// A pixel of the result at (u, v) is that of the cropped picture at (a, b), or (b, a) when
// the orientation transposes, where a and b are u and v, each counted from the far edge
// when its flip is set; this is that map taken the other way round.
type target struct {
	r      *raster
	crop   image.Rectangle
	base   int
	sx, sy int
}

// newTarget makes the raster for a w x h picture under a placement. A crop that does not
// lie inside the picture is refused.
func newTarget(w, h, bpp int, p placement) (*target, error) {
	rect := p.crop
	if rect.Empty() {
		rect = image.Rect(0, 0, w, h)
	}
	if !rect.In(image.Rect(0, 0, w, h)) {
		return nil, fmt.Errorf("%w: crop %v outside the %dx%d picture", ErrInvalid, rect, w, h)
	}
	ow, oh := p.dims(w, h)
	r, err := newRaster(ow, oh, bpp)
	if err != nil {
		return nil, err
	}
	// a runs along the width of the result and b along its height, from either edge
	t := &target{r: r, crop: rect}
	da, db := bpp, r.stride
	if p.o&oFlipX != 0 {
		t.base += (ow - 1) * bpp
		da = -bpp
	}
	if p.o&oFlipY != 0 {
		t.base += (oh - 1) * r.stride
		db = -r.stride
	}
	t.sx, t.sy = da, db
	if p.o&oSwap != 0 {
		t.sx, t.sy = db, da
	}
	return t, nil
}

// at is the offset in r.pix of the pixel (x, y) of the picture, which lies in the crop.
func (t *target) at(x, y int) int {
	return t.base + (x-t.crop.Min.X)*t.sx + (y-t.crop.Min.Y)*t.sy
}
