// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// smallest returns the least value of a limit under which data decodes, trying 1 up to max.
func smallest(t *testing.T, data []byte, max int, set func(*Limits, int)) int {
	t.Helper()
	for n := 1; n <= max; n++ {
		var lim Limits
		set(&lim, n)
		if _, err := DecodeOptions(bytes.NewReader(data), Options{Limits: lim}); err == nil {
			return n
		}
	}
	return 0
}

// A limit refuses what is above it, and accepts what is exactly at it.
func TestLimitsAcceptTheirOwnValue(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	good := goodBytes(t)
	if n := smallest(t, good, 40, func(l *Limits, n int) { l.MaxDepth = n }); n != 3 {
		t.Errorf("the deepest box lies at depth %d, want 3", n)
	}
	if n := smallest(t, good, 100, func(l *Limits, n int) { l.MaxBoxes = n }); n != 15 {
		t.Errorf("the file holds %d boxes, want 15", n)
	}

	// pixels of the grid, at its bound
	data := gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false)).bytes()
	if _, err := DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxPixels: 2400}}); err != nil {
		t.Errorf("grid at 2400: %v", err)
	}
	if _, err := DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxPixels: 2399}}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("grid at 2399: %v", err)
	}
	// the pixels a tile states, and the work of all the tiles, each at its bound: the tile
	// states 40x24 (what it is coded as), so parseGrid accepts it, and the decoder, which
	// finds 36x22, refuses it as invalid, not as too large
	for _, tc := range []struct {
		name       string
		rows, cols int
		w, h       uint32
	}{
		{"tile pixels", 1, 1, 30, 20},
		{"tiles pixels", 1, 4, 121, 1},
	} {
		f := gridFile(c, 40, 24, tc.rows, tc.cols, tc.w, tc.h, gridDesc(tc.rows, tc.cols, tc.w, tc.h, false))
		_, err := DecodeOptions(bytes.NewReader(f.bytes()), Options{Limits: Limits{MaxPixels: 960}})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "but states") {
			t.Errorf("%s at 960: %v", tc.name, err)
		}
		_, err = DecodeOptions(bytes.NewReader(f.bytes()), Options{Limits: Limits{MaxPixels: 959}})
		if err == nil || !strings.Contains(err.Error(), "grid tile size") && !errors.Is(err, ErrImageTooLarge) {
			t.Errorf("%s at 959: %v", tc.name, err)
		}
	}

	// the bytes the extents assemble
	f := single(c, 36, 22)
	f.items[0].dup = 50
	n := int64(len(c.data)) * 50
	if _, err := DecodeOptions(bytes.NewReader(f.bytes()), Options{Limits: Limits{MaxBytes: n}}); err != nil {
		t.Errorf("extents at the limit: %v", err)
	}
}

func TestBufferBound(t *testing.T) {
	defer guard(t)
	if !fits(maxBuffer) || fits(maxBuffer+1) {
		t.Error("a buffer of exactly maxBuffer bytes fits, one more does not")
	}
	if _, err := newRaster(1<<16, 1<<16, 1); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("%v", err)
	}
}

func TestSmallReaders(t *testing.T) {
	defer guard(t)
	r := rd{b: []byte{1}}
	if got := r.take(0); got == nil || r.bad {
		t.Errorf("take(0): %v, bad %v", got, r.bad)
	}
	// the last brand of an ftyp is read when exactly four bytes of it are left
	f := newFile(Limits{})
	if err := f.parseFtyp(append(append([]byte("xxxx"), u32(0)...), "heic"...)); err != nil {
		t.Fatal(err)
	}
	if len(f.brands) != 2 || f.brands[1] != "heic" {
		t.Errorf("brands %q", f.brands)
	}
	// an item of one extent of no size fields is allowed, two are not
	one := fbox("iloc", 0, 0, u8s(0, 0), u16(1), u16(1, 0, 1))[8:]
	if err := newFile(Limits{}).parseIloc(one); err != nil {
		t.Errorf("one extent: %v", err)
	}
	two := fbox("iloc", 0, 0, u8s(0, 0), u16(1), u16(1, 0, 2))[8:]
	if err := newFile(Limits{}).parseIloc(two); !errors.Is(err, ErrInvalid) {
		t.Errorf("two extents: %v", err)
	}
	// an aperture of no height or no width is empty
	for _, c := range [][8]uint32{{36, 1, 0, 1, 0, 1, 0, 1}, {0, 1, 22, 1, 0, 1, 0, 1}} {
		if _, _, _, _, err := cropRect(c, 36, 22); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: %v", c, err)
		}
	}
}
