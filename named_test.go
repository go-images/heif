// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Several refusals end in the same named error whichever check made them, so that a test
// of the error alone would pass after a check was deleted and the next one refused instead.
// These assert the check that spoke, through its message, and what must not be in it.
func TestTheCheckThatRefuses(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	mutate := func(f func(*hfile)) []byte {
		h := single(c, 36, 22)
		f(&h)
		return h.bytes()
	}
	parts := topBoxes(t, goodBytes(t)) // ftyp, meta, mdat
	ftyp, meta := parts[0], parts[1]
	for _, tc := range []struct {
		name   string
		data   []byte
		want   error
		in     string // the message says this
		notIn  string // and does not say this
		config bool   // read through DecodeConfig
	}{
		{"no ftyp", fbox("meta", 0, 0, hdlrOK), ErrNotHEIF, "no ftyp", "", false},
		{"two hdlr", rawFile(hdlrOK, hdlrOK, pitmOK), ErrInvalid, `two "hdlr"`, "", false},
		{"item described twice", rawFile(hdlrOK, pitmOK, fbox("iinf", 0, 0, u16(2),
			fbox("infe", 2, 0, u16(1, 0), []byte("hvc1"), u8s(0)), fbox("infe", 2, 0, u16(1, 0), []byte("hvc1"), u8s(0)))),
			ErrInvalid, "described twice", "", false},
		{"hvcC with a NAL unit of one byte", mutate(func(h *hfile) { h.props[1] = hvcCBox(1, 8, 8, 4, [][]byte{{0x40}}) }),
			ErrCorrupt, "hvcC:", "holds no picture", false},
		{"length past the data", mutate(func(h *hfile) { h.items[0].data = append(u32(1000), 0x26, 0x01) }),
			ErrCorrupt, "item 1:", "holds no picture", false},
		{"garbage item data", mutate(func(h *hfile) { h.items[0].data = append(u32(6), 0x26, 0x01, 1, 2, 3, 4) }),
			ErrCorrupt, "item 1", "holds no picture", false},
		{"first slice cut", mutate(func(h *hfile) { h.items[0].data = h.items[0].data[:len(h.items[0].data)/2] }),
			ErrCorrupt, "item 1", "holds no picture", false},
		{"no picture in the item", mutate(func(h *hfile) { h.items[0].data = nil }),
			ErrCorrupt, "holds no picture", "", false},
		// a clean aperture that cannot be applied is refused before the picture is decoded
		{"bad aperture, undecodable picture", func() []byte {
			h := single(c, 36, 22, clap(20, 0, 10, 1, 0, 1, 0, 1))
			h.items[0].data = append(u32(6), 0x26, 0x01, 1, 2, 3, 4)
			return h.bytes()
		}(), ErrInvalid, "aperture", "", false},
		{"skipped box cut", bytes.Join([][]byte{ftyp, append(u32(100), "free"...), u8s(1, 2)}, nil),
			ErrTruncated, `"free"`, "", true},
		{"meta cut", bytes.Join([][]byte{ftyp, meta[:len(meta)-1]}, nil),
			ErrTruncated, `box "meta"`, "runs past", true}, // the read fails, not the parse of what it got
	} {
		var err error
		if tc.config {
			_, err = DecodeConfig(bytes.NewReader(tc.data))
		} else {
			_, err = Decode(bytes.NewReader(tc.data))
		}
		switch {
		case !errors.Is(err, tc.want):
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		case !strings.Contains(err.Error(), tc.in):
			t.Errorf("%s: %q does not say %q", tc.name, err, tc.in)
		case tc.notIn != "" && strings.Contains(err.Error(), tc.notIn):
			t.Errorf("%s: %q says %q", tc.name, err, tc.notIn)
		}
	}
	// a bad aperture is not a corrupt picture
	h := single(c, 36, 22, clap(20, 0, 10, 1, 0, 1, 0, 1))
	h.items[0].data = append(u32(6), 0x26, 0x01, 1, 2, 3, 4)
	if _, err := Decode(bytes.NewReader(h.bytes())); errors.Is(err, ErrCorrupt) {
		t.Errorf("the picture was decoded before the aperture was checked: %v", err)
	}
}

// A grid tile that names an essential property the reader does not know makes the grid
// unsupported, whether it is the tile parseGrid reads or one the decoder reads.
func TestGridTileWithAnUnknownEssentialProperty(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	for _, tile := range []int{1, 3} {
		f := gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
		f.props = append(f.props, mkbox("zzzz"))
		f.items[tile].props = []int{2, 3, -len(f.props)}
		if _, err := Decode(bytes.NewReader(f.bytes())); !errors.Is(err, ErrUnsupported) {
			t.Errorf("tile item %d: %v", tile, err)
		}
	}
}

// parseGrid refuses a descriptor it cannot fetch with the error of the fetch.
func TestGridDescriptorUnreadable(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	f, err := parseFile(gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false)).bytes(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	it := f.byID[1]
	ps, err := f.propsOf(it)
	if err != nil {
		t.Fatal(err)
	}
	it.loc = &location{method: 2, extents: it.loc.extents}
	if _, err := f.parseGrid(it, ps); !errors.Is(err, ErrUnsupported) {
		t.Errorf("%v", err)
	}
}

// One extent is a window of the file, not a copy of it.
func TestSingleExtentIsAWindow(t *testing.T) {
	defer guard(t)
	f, err := parseFile(goodBytes(t), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	it := f.byID[1]
	d, err := f.itemData(it)
	if err != nil || len(d) == 0 {
		t.Fatalf("%d bytes, %v", len(d), err)
	}
	if &d[0] != &f.data[it.loc.base+it.loc.extents[0].off] {
		t.Error("a single extent was copied")
	}
}
