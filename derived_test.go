// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"testing"
)

// gridFile builds a file whose primary item is a grid of rows x cols copies of one coded
// picture. The grid's ispe and colr are those of the grid item; descriptor is its data.
func gridFile(c coded, tw, th uint32, rows, cols int, w, h uint32, grid []byte, gridProps ...[]byte) hfile {
	props := [][]byte{ispe(w, h), ispe(tw, th), c.hvcC}
	gp := []int{1}
	for _, p := range gridProps {
		props = append(props, p)
		gp = append(gp, len(props))
	}
	f := hfile{primary: 1, props: props}
	f.items = append(f.items, hitem{id: 1, typ: "grid", data: grid, idat: true, props: gp})
	var ids []uint32
	for i := 0; i < rows*cols; i++ {
		id := uint32(2 + i)
		ids = append(ids, id)
		f.items = append(f.items, hitem{id: id, typ: "hvc1", data: c.data, props: []int{2, 3}})
	}
	f.refs = []href{{"dimg", 1, ids}}
	return f
}

func gridDesc(rows, cols int, w, h uint32, wide bool) []byte {
	if wide {
		return append(u8s(0, 1, uint8(rows-1), uint8(cols-1)), u32(w, h)...)
	}
	return append(u8s(0, 0, uint8(rows-1), uint8(cols-1)), u16(uint16(w), uint16(h))...)
}

func TestGrid(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	tile := xfBase(t, c)
	for _, tc := range []struct {
		name       string
		rows, cols int
		w, h       uint32
		wide       bool
	}{
		{"2x2 cut", 2, 2, 60, 40, false},
		{"2x2 whole", 2, 2, 72, 44, false},
		{"1x3", 1, 3, 100, 22, false},
		{"3x1 with 32-bit sizes", 3, 1, 36, 50, true},
		{"1x1", 1, 1, 36, 22, false},
	} {
		data := gridFile(c, 36, 22, tc.rows, tc.cols, tc.w, tc.h, gridDesc(tc.rows, tc.cols, tc.w, tc.h, tc.wide), colrNclx(1, 1, 1, true)).bytes()
		got, err := Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		sameImage(t, tc.name, got, mapped(tile, int(tc.w), int(tc.h), func(x, y int) (int, int) { return x % 36, y % 22 }))
		cfg, err := DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width != int(tc.w) || cfg.Height != int(tc.h) || cfg.ColorModel != got.ColorModel() {
			t.Errorf("%s: config %+v %v", tc.name, cfg, err)
		}
	}
}

// A grid's descriptor may sit in mdat, which DecodeConfig does not read: it answers from
// the ispe of the grid, and Decode checks the descriptor against it.
func TestGridDescriptorInMdat(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	f := gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
	f.items[0].idat = false
	data := f.bytes()
	cfg, err := DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 60 || cfg.Height != 40 {
		t.Fatalf("%+v %v", cfg, err)
	}
	if _, err := Decode(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	// the descriptor and the ispe must agree
	f = gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 61, 40, false))
	if _, err := Decode(bytes.NewReader(f.bytes())); !errorsIs(err, ErrInvalid) {
		t.Errorf("descriptor 61 wide, ispe 60: %v", err)
	}
}

func TestGridErrors(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	g := loadCoded(t, "gray", 0, 8)
	grid := func(rows, cols int, w, h uint32, desc []byte) hfile {
		return gridFile(c, 36, 22, rows, cols, w, h, desc)
	}
	for _, tc := range []struct {
		name string
		f    hfile
		want error
	}{
		{"version 1", grid(2, 2, 60, 40, append(u8s(1), gridDesc(2, 2, 60, 40, false)[1:]...)), ErrUnsupported},
		{"short descriptor", grid(2, 2, 60, 40, u8s(0, 0, 1)), ErrTruncated},
		{"descriptor of 32-bit sizes cut", grid(2, 2, 60, 40, u8s(0, 1, 1, 1, 0, 0)), ErrTruncated},
		{"wrong tile count", grid(2, 3, 60, 40, gridDesc(2, 3, 60, 40, false)), ErrInvalid}, // 6 stated, then 5 below
		{"zero width", grid(2, 2, 0, 40, gridDesc(2, 2, 0, 40, false)), ErrImageTooLarge},
		{"zero height", grid(2, 2, 60, 0, gridDesc(2, 2, 60, 0, false)), ErrImageTooLarge},
		{"tiles do not cover the width", grid(2, 2, 73, 40, gridDesc(2, 2, 73, 40, false)), ErrInvalid},
		{"a column of tiles outside", grid(2, 2, 36, 40, gridDesc(2, 2, 36, 40, false)), ErrInvalid},
		{"tiles do not cover the height", grid(2, 2, 60, 45, gridDesc(2, 2, 60, 45, false)), ErrInvalid},
		{"a row of tiles outside", grid(2, 2, 60, 22, gridDesc(2, 2, 60, 22, false)), ErrInvalid},
	} {
		if tc.name == "wrong tile count" {
			tc.f.refs[0].to = tc.f.refs[0].to[:5]
			tc.f.items = tc.f.items[:6]
		}
		if _, err := Decode(bytes.NewReader(tc.f.bytes())); !errorsIs(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}

	// a tile that is not an hvc1 item
	f := grid(2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
	f.items[2].typ = "av01"
	if _, err := Decode(bytes.NewReader(f.bytes())); !errorsIs(err, ErrUnsupported) {
		t.Errorf("av01 tile: %v", err)
	}
	// a tile without a size, or with a zero one
	f = grid(2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
	f.items[1].props = []int{3}
	if _, err := Decode(bytes.NewReader(f.bytes())); !errorsIs(err, ErrInvalid) {
		t.Errorf("tile without ispe: %v", err)
	}
	f = grid(2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
	f.props[1] = ispe(0, 22)
	if _, err := Decode(bytes.NewReader(f.bytes())); !errorsIs(err, ErrInvalid) {
		t.Errorf("tile of zero width: %v", err)
	}
	// a tile that decodes to another size than it states
	f = grid(2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
	f.props[1] = ispe(36, 24)
	if _, err := Decode(bytes.NewReader(f.bytes())); !errorsIs(err, ErrInvalid) {
		t.Errorf("tile of the wrong size: %v", err)
	}
	// a tile whose pixel format differs from the first
	f = grid(2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
	f.props = append(f.props, g.hvcC)
	f.items[3].props = []int{2, len(f.props)}
	f.items[3].data = g.data
	if _, err := Decode(bytes.NewReader(f.bytes())); !errorsIs(err, ErrInvalid) {
		t.Errorf("mixed tiles: %v", err)
	}
	// a grid without a dimg reference, and DecodeConfig's view of the same faults
	f = grid(2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
	f.refs = nil
	if _, err := DecodeConfig(bytes.NewReader(f.bytes())); !errorsIs(err, ErrInvalid) {
		t.Errorf("config, no tiles: %v", err)
	}
	f = grid(2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
	f.props[0] = mkbox("free")
	if _, err := DecodeConfig(bytes.NewReader(f.bytes())); !errorsIs(err, ErrInvalid) {
		t.Errorf("config, no size: %v", err)
	}
	f = grid(2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
	f.items[1].typ = "av01"
	if _, err := DecodeConfig(bytes.NewReader(f.bytes())); !errorsIs(err, ErrUnsupported) {
		t.Errorf("config, first tile av01: %v", err)
	}
}

func TestIdentity(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	base := xfBase(t, c)
	const w, h = 36, 22
	mk := func(outer ...int) hfile {
		f := single(c, w, h, colrNclx(1, 1, 1, true), irot(1), imir(1))
		f.items[0].id = 2
		f.items[0].props = []int{1, 2, 3, -5} // ispe, hvcC, colr, imir: the inner image mirrors
		f.items = append(f.items, hitem{id: 1, typ: "iden", props: outer})
		f.primary = 1
		f.refs = []href{{"dimg", 1, []uint32{2}}}
		return f
	}
	// inner imir (left-right), outer irot (anti-clockwise): the inner transformation first
	f := mk(-4)
	got, err := Decode(bytes.NewReader(f.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	// flip left-right: m(x,y) = base(w-1-x, y); then rotate: out(x,y) = m(w-1-y, x) = base(y, x)
	sameImage(t, "iden", got, mapped(base, h, w, func(x, y int) (int, int) { return y, x }))
	cfg, err := DecodeConfig(bytes.NewReader(f.bytes()))
	if err != nil || cfg.Width != h || cfg.Height != w {
		t.Errorf("config %+v %v", cfg, err)
	}
	// the outer item's colr overrides: here none, so the inner nclx stays
	res, _ := DecodeOptions(bytes.NewReader(f.bytes()), Options{})
	if res.Nclx == nil || res.Nclx.Matrix != 1 {
		t.Errorf("nclx %+v", res.Nclx)
	}
	// an outer colr wins over the inner one
	f = mk(-4, 6)
	f.props = append(f.props, colrNclx(1, 1, 6, true))
	f.items[1].props = []int{-4, len(f.props)}
	res, err = DecodeOptions(bytes.NewReader(f.bytes()), Options{})
	if err != nil || res.Nclx.Matrix != 6 {
		t.Errorf("outer colr: %+v %v", res, err)
	}
	// an outer ICC profile
	f = mk(-4)
	f.props = append(f.props, colrICC([]byte("icc")))
	f.items[1].props = []int{-4, len(f.props)}
	res, err = DecodeOptions(bytes.NewReader(f.bytes()), Options{})
	if err != nil || string(res.ICCProfile) != "icc" {
		t.Errorf("outer icc: %+v %v", res, err)
	}

	f = mk(-4)
	f.refs = nil
	if _, err := Decode(bytes.NewReader(f.bytes())); !errorsIs(err, ErrInvalid) {
		t.Errorf("iden without source: %v", err)
	}
	f = mk(-4)
	f.refs[0].to = []uint32{2, 2}
	if _, err := Decode(bytes.NewReader(f.bytes())); !errorsIs(err, ErrInvalid) {
		t.Errorf("iden of two: %v", err)
	}
	// a chain of identities deeper than MaxDepth, and a loop
	chain := func(n int) hfile {
		f := single(c, w, h)
		f.items[0].id = uint32(n + 1)
		for i := n; i >= 1; i-- {
			f.items = append(f.items, hitem{id: uint32(i), typ: "iden"})
			f.refs = append(f.refs, href{"dimg", uint32(i), []uint32{uint32(i + 1)}})
		}
		f.primary = 1
		return f
	}
	if _, err := Decode(bytes.NewReader(chain(8).bytes())); err != nil {
		t.Errorf("chain of 8: %v", err)
	}
	if _, err := Decode(bytes.NewReader(chain(9).bytes())); !errorsIs(err, ErrNestingTooDeep) {
		t.Errorf("chain of 9: %v", err)
	}
	loop := hfile{primary: 1, items: []hitem{{id: 1, typ: "iden"}}, refs: []href{{"dimg", 1, []uint32{1}}}}
	if _, err := Decode(bytes.NewReader(loop.bytes())); !errorsIs(err, ErrNestingTooDeep) {
		t.Errorf("loop: %v", err)
	}
}
