package heif

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/go-avkit/h265"
)

// The oracle of the conversion: the raster as a picture is converted whole, then cropped and
// turned as a second raster. The decoder converts straight into the turned raster instead.

// reorient returns the raster turned to an orientation, as a new raster. The pixel at
// (u, v) of the result is that of the source at (a, b), or (b, a) when the orientation
// transposes, where a and b are u and v, each counted from the far edge when its flip is set.
func reorient(r *raster, o orient) *raster {
	w2, h2 := r.w, r.h
	if o&oSwap != 0 {
		w2, h2 = r.h, r.w
	}
	b := r.bpp
	out := &raster{pix: make([]byte, w2*h2*b), stride: w2 * b, w: w2, h: h2, bpp: b}
	for v := 0; v < h2; v++ {
		bb := v
		if o&oFlipY != 0 {
			bb = h2 - 1 - v
		}
		for u := 0; u < w2; u++ {
			a := u
			if o&oFlipX != 0 {
				a = w2 - 1 - u
			}
			sx, sy := a, bb
			if o&oSwap != 0 {
				sx, sy = bb, a
			}
			copy(out.pix[v*out.stride+u*b:][:b], r.pix[sy*r.stride+sx*b:][:b])
		}
	}
	return out
}

// crop returns the rectangle of a raster, sharing the pixels.
func crop(r *raster, rect image.Rectangle) *raster {
	return &raster{
		pix:    r.pix[rect.Min.Y*r.stride+rect.Min.X*r.bpp:],
		stride: r.stride,
		w:      rect.Dx(), h: rect.Dy(), bpp: r.bpp,
	}
}

// place crops a raster and turns the result.
func place(r *raster, p placement) *raster {
	if !p.crop.Empty() {
		r = crop(r, p.crop)
	}
	if p.o == 0 {
		return r
	}
	return reorient(r, p.o)
}

// pictureOf is a picture of w x h whose samples all differ, at a bit depth and with the
// chroma at the given shifts.
func pictureOf(w, h, depth int, colour bool) *h265.DecodedPicture {
	plane := func(seed int) h265.Plane {
		p := h265.Plane{Width: w, Height: h, Stride: w + 3} // the stride is not the width
		if depth > 8 {
			p.Pix16 = make([]uint16, p.Stride*h)
			for i := range p.Pix16 {
				p.Pix16[i] = uint16((i*37 + seed*11) % (1 << depth))
			}
		} else {
			p.Pix8 = make([]uint8, p.Stride*h)
			for i := range p.Pix8 {
				p.Pix8[i] = uint8(i*37 + seed*11)
			}
		}
		return p
	}
	pic := &h265.DecodedPicture{Components: 1, BitDepth: [3]int{depth, depth, depth}}
	pic.Planes[0] = plane(1)
	if colour {
		pic.Components = 3
		pic.Planes[1], pic.Planes[2] = plane(2), plane(3)
	}
	return pic
}

// Converting straight into the cropped and turned raster gives the pixels of converting the
// whole picture and then cropping and turning it, for every orientation, with and without a
// crop, in every sample layout.
func TestConversionIntoTheTurnedRaster(t *testing.T) {
	defer guard(t)
	const w, h = 9, 7
	crops := []image.Rectangle{{}, image.Rect(2, 1, 8, 5), image.Rect(0, 0, 1, 1), image.Rect(8, 6, 9, 7)}
	for _, colour := range []bool{false, true} {
		for _, depth := range []int{8, 10} {
			pic := pictureOf(w, h, depth, colour)
			whole := func(pl placement) (*raster, error) {
				if colour {
					return convertColour(pic, 0, 0, 0.2126, 0.0722, false, pl)
				}
				return convertGray(pic, false, pl)
			}
			unplaced, err := whole(placement{})
			if err != nil {
				t.Fatal(err)
			}
			for o := orient(0); o < 8; o++ {
				for _, c := range crops {
					pl := placement{crop: c, o: o}
					want := place(unplaced, pl)
					got, err := whole(pl)
					if err != nil {
						t.Fatal(err)
					}
					if got.w != want.w || got.h != want.h || got.bpp != want.bpp {
						t.Fatalf("colour %v depth %d orient %d crop %v: %dx%d of %d bytes, want %dx%d of %d",
							colour, depth, o, c, got.w, got.h, got.bpp, want.w, want.h, want.bpp)
					}
					for y := 0; y < want.h; y++ {
						a, b := got.pix[y*got.stride:][:want.w*want.bpp], want.pix[y*want.stride:][:want.w*want.bpp]
						if !bytes.Equal(a, b) {
							t.Fatalf("colour %v depth %d orient %d crop %v: row %d is % x, want % x", colour, depth, o, c, y, a, b)
						}
					}
				}
			}
		}
	}
}

// A crop outside the picture is refused, not written past the raster.
func TestTargetRefusesACropOutsideThePicture(t *testing.T) {
	defer guard(t)
	if _, err := newTarget(4, 4, 1, placement{crop: image.Rect(2, 2, 5, 4)}); !errors.Is(err, ErrInvalid) {
		t.Errorf("crop outside: %v", err)
	}
	if _, err := convertGray(pictureOf(4, 4, 8, false), true, placement{crop: image.Rect(0, 0, 4, 5)}); !errors.Is(err, ErrInvalid) {
		t.Errorf("gray: %v", err)
	}
}
