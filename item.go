// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"fmt"
	"math/big"
)

// itemData returns the bytes of an item, from its iloc extents (ISO/IEC 14496-12 8.11.3).
// Construction method 0 offsets into the file, 1 into idat; method 2 (offsets into
// another item) is not supported. One extent is returned as a window of the file, several
// are copied, and their total is bounded by Limits.MaxBytes so that overlapping extents
// cannot multiply a small file.
func (f *file) itemData(it *item) ([]byte, error) {
	l := it.loc
	if l == nil {
		return nil, fmt.Errorf("%w: item %d has no location", ErrInvalid, it.id)
	}
	if l.dataRef != 0 {
		return nil, fmt.Errorf("%w: item %d is in another file (data reference %d)", ErrUnsupported, it.id, l.dataRef)
	}
	var src []byte
	switch l.method {
	case 0:
		src = f.data
	case 1:
		src = f.idat
	default:
		return nil, fmt.Errorf("%w: item %d construction method %d", ErrUnsupported, it.id, l.method)
	}
	var total uint64
	windows := make([][]byte, 0, len(l.extents))
	for _, e := range l.extents {
		start := l.base + e.off
		end := start + e.length
		if start < l.base || end < start || end > uint64(len(src)) {
			return nil, fmt.Errorf("%w: item %d extent [%d+%d+%d] outside its %d bytes", ErrTruncated, it.id, l.base, e.off, e.length, len(src))
		}
		total += e.length
		if total > uint64(f.lim.MaxBytes) {
			return nil, fmt.Errorf("%w: item %d assembles more than %d bytes", ErrFileTooLarge, it.id, f.lim.MaxBytes)
		}
		windows = append(windows, src[start:end])
	}
	if len(windows) == 1 {
		return windows[0], nil
	}
	out := make([]byte, 0, total)
	for _, w := range windows {
		out = append(out, w...)
	}
	return out, nil
}

// cropRect computes the clean aperture of a clap property for a w x h image as the
// pixel rectangle [x0,x1) x [y0,y1), the way libheif does (ISO/IEC 14496-12 12.1.4.3):
// the centre is the image centre plus the offset, the left edge is rounded down and the
// top edge to nearest; the rectangle is clamped to the image. Fractions are exact.
func cropRect(c [8]uint32, w, h int) (x0, y0, x1, y1 int, err error) {
	for _, d := range [...]uint32{c[1], c[3], c[5], c[7]} {
		if d == 0 {
			return 0, 0, 0, 0, fmt.Errorf("%w: clean aperture with a zero denominator", ErrInvalid)
		}
	}
	rat := func(n int64, d uint32) *big.Rat { return big.NewRat(n, int64(d)) }
	half := big.NewRat(1, 2)
	edge := func(off *big.Rat, size int, ap *big.Rat) *big.Rat {
		// off + (size-1)/2 - (ap-1)/2
		e := new(big.Rat).Add(off, big.NewRat(int64(size-1), 2))
		t := new(big.Rat).Sub(ap, big.NewRat(1, 1))
		return e.Sub(e, t.Mul(t, half))
	}
	floor := func(q *big.Rat) int64 { return new(big.Int).Div(q.Num(), q.Denom()).Int64() }
	round := func(q *big.Rat) int64 { return floor(new(big.Rat).Add(q, half)) }
	cw, ch := rat(int64(c[0]), c[1]), rat(int64(c[2]), c[3])
	l := floor(edge(rat(int64(int32(c[4])), c[5]), w, cw))
	t := round(edge(rat(int64(int32(c[6])), c[7]), h, ch))
	r := round(new(big.Rat).Add(new(big.Rat).Sub(cw, big.NewRat(1, 1)), big.NewRat(l, 1))) + 1
	b := round(new(big.Rat).Add(new(big.Rat).Sub(ch, big.NewRat(1, 1)), big.NewRat(t, 1))) + 1
	x0, y0, x1, y1 = clampInt(l, 0, int64(w)), clampInt(t, 0, int64(h)), clampInt(r, 0, int64(w)), clampInt(b, 0, int64(h))
	if x0 >= x1 || y0 >= y1 {
		return 0, 0, 0, 0, fmt.Errorf("%w: empty clean aperture", ErrInvalid)
	}
	return x0, y0, x1, y1, nil
}

func clampInt(v, lo, hi int64) int {
	return int(min(max(v, lo), hi))
}
