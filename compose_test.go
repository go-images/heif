// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"errors"
	"slices"
	"testing"
)

// pixmap is the oracle's picture: one value per pixel, indexed [y][x].
type pixmap [][]int

func numbered(w, h int) pixmap {
	g := make(pixmap, h)
	for y := range g {
		g[y] = make([]int, w)
		for x := range g[y] {
			g[y][x] = y*w + x
		}
	}
	return g
}

// step applies one transformative property to a picture, one property at a time, the way
// the file states them: this is what compose and place must come to in one pass.
func (g pixmap) step(t *testing.T, x xform) (pixmap, bool) {
	h, w := len(g), len(g[0])
	switch x.kind {
	case 'r': // anticlockwise: (x, y) goes to (y, w-1-x)
		if x.n == 0 {
			return g, true
		}
		out := make(pixmap, w)
		for i := range out {
			out[i] = make([]int, h)
		}
		for y := range g {
			for xx := range g[y] {
				out[w-1-xx][y] = g[y][xx]
			}
		}
		return out.step(t, xform{kind: 'r', n: x.n - 1})
	case 'm':
		out := make(pixmap, h)
		for y := range g {
			if x.n == 0 { // top to bottom
				out[h-1-y] = slices.Clone(g[y])
			} else {
				out[y] = slices.Clone(g[y])
				slices.Reverse(out[y])
			}
		}
		return out, true
	}
	u0, v0, u1, v1, err := cropRect(x.clap, w, h)
	if err != nil {
		return nil, false
	}
	out := make(pixmap, 0, v1-v0)
	for _, row := range g[v0:v1] {
		out = append(out, slices.Clone(row[u0:u1]))
	}
	return out, true
}

func (g pixmap) raster() *raster {
	h, w := len(g), len(g[0])
	r := &raster{pix: make([]byte, w*h), stride: w, w: w, h: h, bpp: 1}
	for y := range g {
		for x := range g[y] {
			r.pix[y*w+x] = byte(g[y][x])
		}
	}
	return r
}

// Every sequence of up to three transformations of a picture composes into one placement
// that gives the picture those transformations give one after the other.
func TestComposeMatchesOneAtATime(t *testing.T) {
	defer guard(t)
	alphabet := []xform{
		{kind: 'r', n: 0}, {kind: 'r', n: 1}, {kind: 'r', n: 2}, {kind: 'r', n: 3},
		{kind: 'm', n: 0}, {kind: 'm', n: 1},
		{kind: 'c', clap: [8]uint32{4, 1, 3, 1, 0, 1, 0, 1}}, // 4x3 at the centre
		{kind: 'c', clap: [8]uint32{3, 1, 2, 1, 1, 1, 0, 1}}, // 3x2 one to the right
		{kind: 'c', clap: [8]uint32{2, 1, 3, 1, 0, 1, 1, 2}}, // 2x3 half a row down
		{kind: 'c', clap: [8]uint32{2, 1, 2, 1, 1, 2, 0, 1}}, // 2x2 half a column right
	}
	const w, h = 6, 5
	orients := map[orient]bool{}
	checked := 0
	var walk func(seq []xform)
	walk = func(seq []xform) {
		if len(seq) > 0 {
			want, ok := numbered(w, h), true
			for _, x := range seq {
				if want, ok = want.step(t, x); !ok {
					break
				}
			}
			pl, err := compose(seq, w, h)
			if !ok {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("%v: an aperture outside the picture: %v", seq, err)
				}
			} else {
				if err != nil {
					t.Fatalf("%v: %v", seq, err)
				}
				got := place(numbered(w, h).raster(), pl)
				gw, gh := pl.dims(w, h)
				if got.w != gw || got.h != gh || got.w != len(want[0]) || got.h != len(want) {
					t.Fatalf("%v: %dx%d, dims %dx%d, want %dx%d", seq, got.w, got.h, gw, gh, len(want[0]), len(want))
				}
				for y := range want {
					for x := range want[y] {
						if int(got.pix[y*got.stride+x]) != want[y][x] {
							t.Fatalf("%v: pixel (%d,%d) is %d, want %d", seq, x, y, got.pix[y*got.stride+x], want[y][x])
						}
					}
				}
				orients[pl.o] = true
				checked++
			}
		}
		if len(seq) == 3 {
			return
		}
		for _, x := range alphabet {
			walk(append(slices.Clone(seq), x))
		}
	}
	walk(nil)
	if len(orients) != 8 || checked < 500 {
		t.Errorf("%d sequences reach %d of the 8 orientations", checked, len(orients))
	}
	// a plain placement has nothing to do and says so
	if pl, _ := compose([]xform{{kind: 'r', n: 2}, {kind: 'r', n: 2}}, w, h); !pl.plain() {
		t.Errorf("two half turns: %+v", pl)
	}
	if pl, _ := compose([]xform{{kind: 'm', n: 0}, {kind: 'm', n: 0}}, w, h); !pl.plain() {
		t.Errorf("two mirrors: %+v", pl)
	}
}
