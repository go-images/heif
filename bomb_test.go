// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"io"
	"runtime"
	"runtime/debug"
	"testing"
)

// The bomb tests feed small hostile files that declare enormous sizes, counts or depths.
// Each must be refused with the named error, after allocating little: CI runs them under
// GOMEMLIMIT, and the allocation bound below is the local check of the same promise.
const bombBudget = 48 << 20 // bytes a refusal may allocate

// bombed runs f and returns the bytes it allocated.
func bombed(f func()) uint64 {
	old := debug.SetMemoryLimit(256 << 20)
	defer debug.SetMemoryLimit(old)
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// endless is a reader that never ends: an ftyp, then free boxes for ever.
type endless struct{ head []byte }

func (e *endless) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(e.head) == 0 {
			e.head = append(u32(8), "free"...)
		}
		c := copy(p[n:], e.head)
		e.head = e.head[c:]
		n += c
	}
	return n, nil
}

var _ io.Reader = (*endless)(nil)

func TestBombs(t *testing.T) {
	c := loadCoded(t, "yuv420p", 1, 8)
	ftyp := mkbox("ftyp", []byte("heic"), u32(0), []byte("mif1"))
	free := mkbox("free")

	// 70000 boxes inside the meta
	many := rawFile(bytes.Repeat(free, 70000))

	// 4900 tiles, more than MaxTiles, each a few bytes
	tiles := gridFile(c, 36, 22, 70, 70, 36*70, 22*70, gridDesc(70, 70, 36*70, 22*70, true), colrNclx(1, 1, 1, true))
	for i := 1; i < len(tiles.items); i++ {
		tiles.items[i].data = []byte{0}
	}

	// an iden chain of 10000
	chain := hfile{primary: 1, props: [][]byte{ispe(36, 22)}}
	var refs []href
	for i := uint32(1); i <= 10000; i++ {
		chain.items = append(chain.items, hitem{id: i, typ: "iden", props: []int{1}})
		refs = append(refs, href{"dimg", i, []uint32{i + 1}})
	}
	chain.refs = refs

	// overlapping extents: 50 extents over the whole data, 2 MiB each
	overlap := single(loadCoded(t, "yuv420p", 1, 8), 36, 22)
	overlap.items[0].data = make([]byte, 2<<20)
	overlap.items[0].dup = 50

	// the same data, with a declared size of 1x1 and tiny limits
	tiny := single(c, 1, 1).bytes()

	hostile := []struct {
		name string
		data []byte
		lim  Limits
		want error
	}{
		{"70000 boxes", many, Limits{}, ErrTooManyBoxes},
		{"4900 tiles", tiles.bytes(), Limits{}, ErrTooManyTiles},
		{"iden chain of 10000", chain.bytes(), Limits{}, ErrNestingTooDeep},
		{"overlapping extents", overlap.bytes(), Limits{MaxBytes: 16 << 20}, ErrFileTooLarge},
		{"grid of 32-bit sides", gridFile(c, 36, 22, 1, 1, 1<<31, 1<<31, gridDesc(1, 1, 1<<31, 1<<31, true)).bytes(), Limits{}, ErrImageTooLarge},
		{"grid of 65535x65535", gridFile(c, 36, 22, 1, 1, 65535, 65535, gridDesc(1, 1, 65535, 65535, false)).bytes(), Limits{}, ErrImageTooLarge},
		{"ispe 4e9x4e9", single(c, 4e9, 4e9).bytes(), Limits{}, ErrImageTooLarge},
		{"box of 0xFFFFFFFF", append(append([]byte(nil), ftyp...), append(u32(0xFFFFFFFF), "meta"...)...), Limits{}, ErrTruncated},
		{"box of 1<<63", append(append([]byte(nil), ftyp...), append(append(u32(1), "meta"...), u32(1<<31, 0)...)...), Limits{}, ErrTruncated},
		{"iloc of 4e9 items", rawFile(hdlrOK, pitmOK, fbox("iloc", 2, 0, u16(0x4400), u32(4e9))), Limits{}, ErrTooManyItems},
		{"iinf of 4e9 items", rawFile(hdlrOK, pitmOK, fbox("iinf", 1, 0, u32(4e9))), Limits{}, ErrInvalid},
		{"ipma of 4e9 entries", rawFile(hdlrOK, pitmOK, mkbox("iprp", mkbox("ipco"), fbox("ipma", 0, 0, u32(4e9)))), Limits{}, ErrTooManyItems},
		{"1x1 picture, tiny decoder limit", tiny, Limits{MaxPixels: 100}, nil},
	}
	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			var err, cerr error
			n := bombed(func() {
				_, err = DecodeOptions(bytes.NewReader(tc.data), Options{Limits: tc.lim})
				_, cerr = DecodeConfigOptions(bytes.NewReader(tc.data), Options{Limits: tc.lim})
			})
			if tc.want != nil && !errorsIs(err, tc.want) {
				t.Errorf("Decode: %v, want %v", err, tc.want)
			}
			if tc.want == nil && err == nil {
				t.Errorf("Decode accepted it")
			}
			if err != nil && !isNamed(err) {
				t.Errorf("Decode: unnamed error %v", err)
			}
			if cerr != nil && !isNamed(cerr) {
				t.Errorf("DecodeConfig: unnamed error %v", cerr)
			}
			if n > bombBudget {
				t.Errorf("allocated %d MiB", n>>20)
			}
		})
	}

	t.Run("endless reader", func(t *testing.T) {
		lim := Limits{MaxBytes: 1 << 20}
		r := io.MultiReader(bytes.NewReader(ftyp), &endless{})
		var err, cerr error
		n := bombed(func() {
			_, err = DecodeOptions(r, Options{Limits: lim})
			_, cerr = DecodeConfigOptions(io.MultiReader(bytes.NewReader(ftyp), &endless{}), Options{Limits: lim})
		})
		if !errorsIs(err, ErrFileTooLarge) && !errorsIs(err, ErrTooManyBoxes) {
			t.Errorf("Decode: %v", err)
		}
		if !errorsIs(cerr, ErrFileTooLarge) && !errorsIs(cerr, ErrTooManyBoxes) {
			t.Errorf("DecodeConfig: %v", cerr)
		}
		if n > bombBudget {
			t.Errorf("allocated %d MiB", n>>20)
		}
	})
}
