// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/go-avkit/h265"
)

// synth is a picture built by hand: a luma plane of w x h and, when cw > 0, two chroma
// planes of cw x ch; 8-bit samples all set to v.
func synth(w, h, cw, ch int, v uint8) *h265.DecodedPicture {
	plane := func(w, h int) h265.Plane {
		p := h265.Plane{Width: w, Height: h, Stride: w, Pix8: make([]uint8, w*h)}
		for i := range p.Pix8 {
			p.Pix8[i] = v
		}
		return p
	}
	pic := &h265.DecodedPicture{Components: 1, BitDepth: [3]int{8, 8, 8}}
	pic.Planes[0] = plane(w, h)
	if cw > 0 {
		pic.Components = 3
		pic.Planes[1], pic.Planes[2] = plane(cw, ch), plane(cw, ch)
	}
	return pic
}

func TestShifts(t *testing.T) {
	defer guard(t)
	for _, tc := range []struct {
		name   string
		pic    *h265.DecodedPicture
		hs, vs uint
		ok     bool
	}{
		{"monochrome", synth(8, 6, 0, 0, 1), 0, 0, true},
		{"4:4:4", synth(8, 6, 8, 6, 1), 0, 0, true},
		{"4:2:0", synth(8, 6, 4, 3, 1), 1, 1, true},
		{"4:2:0 odd", synth(7, 5, 4, 3, 1), 1, 1, true},
		{"4:2:2", synth(8, 6, 4, 6, 1), 1, 0, true},
		{"4:4:0", synth(8, 6, 8, 3, 1), 0, 1, true},
		{"width of a third", synth(9, 6, 3, 6, 1), 0, 0, false},
		{"height of a third", synth(8, 9, 8, 3, 1), 0, 0, false},
	} {
		hs, vs, ok := shifts(tc.pic)
		if ok != tc.ok || (ok && (hs != tc.hs || vs != tc.vs)) {
			t.Errorf("%s: %d %d %v", tc.name, hs, vs, ok)
		}
	}
	odd := synth(8, 6, 4, 3, 1)
	odd.Planes[2].Width = 3 // the two chroma planes disagree
	if _, _, ok := shifts(odd); ok {
		t.Error("chroma planes of two sizes")
	}
}

// An oriented picture is never a window on the decoder's planes, which are not turned.
func TestRenderTurnsWhatItReturnsAsDecoded(t *testing.T) {
	defer guard(t)
	pic := synth(8, 6, 4, 3, 1)
	img, err := render(pic, nil, placement{o: oSwap})
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 6 || b.Dy() != 8 {
		t.Errorf("turned picture is %v, want 6x8", b)
	}
}

func TestRenderRefusals(t *testing.T) {
	defer guard(t)
	// chroma planes of an unusual size
	if _, err := render(synth(9, 6, 3, 6, 1), nil, placement{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("render: %v", err)
	}
	if _, err := newGridPicture(synth(9, 6, 3, 6, 1), 20, 20); !errors.Is(err, ErrUnsupported) {
		t.Errorf("grid: %v", err)
	}
	// pictures too large for one buffer: the planes need no samples to be refused
	big := &h265.DecodedPicture{Components: 1, BitDepth: [3]int{8, 8, 8}}
	big.Planes[0] = h265.Plane{Width: 1 << 16, Height: 1 << 16, Stride: 1 << 16}
	if _, err := convertGray(big, true, placement{}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("gray: %v", err)
	}
	big3 := &h265.DecodedPicture{Components: 3, BitDepth: [3]int{8, 8, 8}}
	for c := range big3.Planes {
		big3.Planes[c] = h265.Plane{Width: 1 << 16, Height: 1 << 16, Stride: 1 << 16}
	}
	if _, err := convertColour(big3, 0, 0, 0.299, 0.114, true, placement{}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("colour: %v", err)
	}
	if _, err := render(big, nil, placement{}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("render gray: %v", err)
	}
	if _, err := render(big3, &nclx{Matrix: 1}, placement{}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("render colour: %v", err)
	}
	if _, err := newGridPicture(synth(4, 4, 2, 2, 1), 1<<17, 1<<17); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("grid picture: %v", err)
	}
	if _, err := newRaster(1<<10, 1<<10, 2048); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("raster: %v", err)
	}
	if r, err := newRaster(1<<10, 1<<10, 2047); err != nil || len(r.pix) != 1<<20*2047 {
		t.Errorf("raster just under the bound: %v", err)
	}
}

func TestPlaceEdges(t *testing.T) {
	defer guard(t)
	r := &raster{pix: []byte{1, 2, 3, 4}, stride: 2, w: 2, h: 2, bpp: 1}
	if got := place(r, placement{}); got != r {
		t.Error("nothing to do copies")
	}
	if _, err := compose([]xform{{kind: 'c', clap: [8]uint32{1, 0, 1, 1, 0, 1, 0, 1}}}, 4, 4); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad aperture: %v", err)
	}
}

func TestDirectSubsampling(t *testing.T) {
	defer guard(t)
	for _, tc := range []struct {
		name  string
		pic   *h265.DecodedPicture
		ratio image.YCbCrSubsampleRatio
	}{
		{"4:4:4", synth(8, 6, 8, 6, 1), image.YCbCrSubsampleRatio444},
		{"4:2:2", synth(8, 6, 4, 6, 1), image.YCbCrSubsampleRatio422},
		{"4:4:0", synth(8, 6, 8, 3, 1), image.YCbCrSubsampleRatio440},
		{"4:2:0", synth(8, 6, 4, 3, 1), image.YCbCrSubsampleRatio420},
	} {
		hs, vs, _ := shifts(tc.pic)
		img := direct(tc.pic, hs, vs, image.Rect(0, 0, 8, 6))
		if y, ok := img.(*image.YCbCr); !ok || y.SubsampleRatio != tc.ratio {
			t.Errorf("%s: %T", tc.name, img)
		}
	}
	// a window not at the origin starts at its own samples
	pic := synth(8, 6, 4, 3, 7)
	pic.Planes[0].Pix8[2*8+4] = 99 // luma (4,2)
	pic.Planes[1].Pix8[1*4+2] = 77 // chroma (2,1)
	img := direct(pic, 1, 1, image.Rect(4, 2, 8, 6)).(*image.YCbCr)
	if img.Y[0] != 99 || img.Cb[0] != 77 || img.Bounds() != image.Rect(0, 0, 4, 4) {
		t.Errorf("window: %d %d %v", img.Y[0], img.Cb[0], img.Bounds())
	}
}

func TestSameFormat(t *testing.T) {
	defer guard(t)
	a := synth(8, 6, 4, 3, 1)
	if !sameFormat(a, synth(8, 6, 4, 3, 2)) {
		t.Error("equal formats differ")
	}
	mono := synth(8, 6, 0, 0, 1)
	deep := synth(8, 6, 4, 3, 1)
	deep.BitDepth = [3]int{10, 10, 10}
	wide := synth(8, 6, 4, 3, 1)
	for c := range wide.Planes {
		wide.Planes[c].Pix16, wide.Planes[c].Pix8 = make([]uint16, len(wide.Planes[c].Pix8)), nil
	}
	for name, b := range map[string]*h265.DecodedPicture{
		"components": mono, "depth": deep, "sample size": wide,
		"luma width": synth(9, 6, 4, 3, 1), "luma height": synth(8, 7, 4, 3, 1),
		"chroma width": synth(8, 6, 5, 3, 1), "chroma height": synth(8, 6, 4, 4, 1),
	} {
		if sameFormat(a, b) {
			t.Errorf("%s: formats called the same", name)
		}
	}
	// monochrome pictures compare luma only
	if !sameFormat(mono, synth(8, 6, 0, 0, 5)) {
		t.Error("equal monochrome formats differ")
	}
}

func TestFirstPicture(t *testing.T) {
	defer guard(t)
	ok := synth(4, 4, 2, 2, 1)
	other := synth(4, 4, 2, 2, 2)
	if got, err := firstPicture(nil, nil, nil); got != nil || err != nil {
		t.Errorf("nothing: %v %v", got, err)
	}
	if got, err := firstPicture(nil, []h265.Output{{Picture: ok}, {Picture: other}}, nil); got != ok || err != nil {
		t.Errorf("two pictures: %v %v", got, err)
	}
	if got, err := firstPicture(ok, []h265.Output{{Picture: other}}, nil); got != ok || err != nil {
		t.Errorf("a later picture replaces the first: %v %v", got, err)
	}
	if _, err := firstPicture(nil, nil, errors.New("boom")); !errors.Is(err, ErrCorrupt) {
		t.Errorf("error: %v", err)
	}
	if _, err := firstPicture(nil, []h265.Output{{Picture: ok, Corrupt: true}}, nil); !errors.Is(err, ErrCorrupt) {
		t.Errorf("corrupt: %v", err)
	}
	bad := synth(4, 4, 2, 2, 1)
	bad.Components = 2
	if _, err := firstPicture(nil, []h265.Output{{Picture: bad}}, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("two components: %v", err)
	}
}

func TestCopyTileOfDeepPictures(t *testing.T) {
	defer guard(t)
	// a 10-bit grid of 2x2 tiles decodes to the same pixels as the tile, repeated
	c := loadCoded(t, "yuv420p10le", 1, 10)
	tile, err := Decode(bytes.NewReader(single(c, 36, 22).bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.NewReader(gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false)).bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 40; y++ {
		for x := 0; x < 60; x++ {
			if got.At(x, y) != tile.At(x%36, y%22) {
				t.Fatalf("pixel (%d,%d) is %v, want %v", x, y, got.At(x, y), tile.At(x%36, y%22))
			}
		}
	}
}

func TestAlphaDetection(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	aux := func(urn string) hfile {
		f := single(c, 36, 22)
		f.props = append(f.props, mkbox("auxC", u32(0), []byte(urn+"\x00")))
		f.items = append(f.items, hitem{id: 2, typ: "hvc1", data: c.data, props: []int{1, 2, len(f.props)}})
		f.refs = []href{{"auxl", 2, []uint32{1}}}
		return f
	}
	has := func(f hfile) bool {
		res, err := DecodeOptions(bytes.NewReader(f.bytes()), Options{})
		if err != nil {
			t.Fatal(err)
		}
		return res.HasAlpha
	}
	if !has(aux("urn:mpeg:hevc:2015:auxid:1")) || !has(aux("urn:mpeg:mpegB:cicp:systems:auxiliary:alpha")) {
		t.Error("an alpha plane is not reported")
	}
	if has(aux("urn:mpeg:hevc:2015:auxid:2")) {
		t.Error("a depth map is reported as alpha")
	}
	f := aux("urn:mpeg:hevc:2015:auxid:1")
	f.refs = []href{{"thmb", 2, []uint32{1}}}
	if has(f) {
		t.Error("a thumbnail is reported as alpha")
	}
	f = aux("urn:mpeg:hevc:2015:auxid:1")
	f.refs = []href{{"auxl", 2, []uint32{7, 8}}}
	if has(f) {
		t.Error("an auxiliary of another item is reported")
	}
	f = aux("urn:mpeg:hevc:2015:auxid:1")
	f.refs = []href{{"auxl", 77, []uint32{1}}}
	if has(f) {
		t.Error("an auxiliary item that does not exist is reported")
	}
	f = aux("urn:mpeg:hevc:2015:auxid:1")
	f.items[1].props = []int{1, 2, 90}
	if has(f) {
		t.Error("an auxiliary item with a bad property index is reported")
	}
	f = single(c, 36, 22)
	f.items = append(f.items, hitem{id: 2, typ: "hvc1", data: c.data, props: []int{1, 2}})
	f.refs = []href{{"auxl", 2, []uint32{1}}}
	if has(f) {
		t.Error("an auxiliary item without an auxC is reported")
	}
}

func TestBadTileProperties(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	for _, tile := range []int{1, 3} { // the first tile is read by parseGrid, the others by the decoder
		f := gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
		f.items[tile].props = []int{2, 3, 90}
		if _, err := Decode(bytes.NewReader(f.bytes())); !errors.Is(err, ErrInvalid) {
			t.Errorf("tile item %d: %v", tile, err)
		}
	}
}

// A grid too large for one buffer is refused before any tile beyond the first is decoded.
// parseGrid cannot make one under the default limits, so the plan is built by hand.
func TestGridPictureTooLarge(t *testing.T) {
	defer guard(t)
	f, err := parseFile(goodBytes(t), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	pl := &plan{grid: &grid{rows: 1, cols: 1, w: 1 << 17, h: 1 << 17, tw: 36, th: 22, tiles: []*item{f.byID[1]}}}
	if _, err := f.decodeBase(pl); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("%v", err)
	}
}

func TestConfigRefusesABadAperture(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	data := single(c, 36, 22, clap(20, 0, 10, 1, 0, 1, 0, 1)).bytes()
	if _, err := DecodeConfig(bytes.NewReader(data)); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}
