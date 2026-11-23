// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/go-avkit/h265"
)

// allErrors are the named errors of the package: a malformed file must end in one of them.
var allErrors = []error{
	ErrNotHEIF, ErrTruncated, ErrInvalid, ErrUnsupported, ErrNoPrimaryItem, ErrCorrupt,
	ErrFileTooLarge, ErrImageTooLarge, ErrTooManyBoxes, ErrNestingTooDeep, ErrTooManyItems,
	ErrTooManyTiles,
}

func isNamed(err error) bool {
	for _, e := range allErrors {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func newFile(lim Limits) *file {
	return &file{lim: lim.withDefaults(), byID: map[uint32]*item{}}
}

func goodBytes(t testing.TB) []byte {
	t.Helper()
	return single(loadCoded(t, "yuv420p", 1, 8), 36, 22).bytes()
}

// rawFile is an ftyp then a meta holding the given boxes.
func rawFile(kids ...[]byte) []byte {
	return append(mkbox("ftyp", []byte("heic"), u32(0), []byte("mif1")), fbox("meta", 0, 0, kids...)...)
}

var (
	hdlrOK = fbox("hdlr", 0, 0, u32(0), []byte("pict"), make([]byte, 13))
	pitmOK = fbox("pitm", 0, 0, u16(1))
)

func TestStructureErrors(t *testing.T) {
	defer guard(t)
	ftypOnly := mkbox("ftyp", []byte("heic"), u32(0))
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, ErrNotHEIF},
		{"no ftyp", fbox("meta", 0, 0, hdlrOK), ErrNotHEIF},
		{"other brand", append(mkbox("ftyp", []byte("avif"), u32(0)), fbox("meta", 0, 0, hdlrOK)...), ErrNotHEIF},
		{"short ftyp", mkbox("ftyp", []byte("hei")), ErrTruncated},
		{"no meta", ftypOnly, ErrInvalid},
		{"short meta", append(ftypOnly, mkbox("meta", u8s(0))...), ErrTruncated},
		{"two hdlr", rawFile(hdlrOK, hdlrOK, pitmOK), ErrInvalid},
		{"hdlr vide", rawFile(fbox("hdlr", 0, 0, u32(0), []byte("vide"), make([]byte, 13))), ErrUnsupported},
		{"short hdlr", rawFile(fbox("hdlr", 0, 0, u32(0))), ErrTruncated},
		{"short pitm", rawFile(hdlrOK, fbox("pitm", 0, 0)), ErrTruncated},
		{"no pitm", rawFile(hdlrOK), ErrNoPrimaryItem},
		{"primary is nothing", rawFile(hdlrOK, pitmOK), ErrInvalid},
		{"box shorter than its header", append(ftypOnly, u32(4)...), ErrTruncated},
		{"box of 4 bytes", append(ftypOnly, append(u32(4), "free"...)...), ErrInvalid},
		{"box past its container", append(ftypOnly, append(u32(100), "free"...)...), ErrTruncated},
		{"64-bit size cut", append(ftypOnly, append(u32(1), "free"...)...), ErrTruncated},
		{"64-bit size too small", append(ftypOnly, append(append(u32(1), "free"...), u32(0, 12)...)...), ErrInvalid},
		{"uuid cut", append(ftypOnly, mkbox("uuid", []byte("short"))...), ErrTruncated},
		{"meta handler cut by the box", rawFile(mkbox("hdlr")), ErrTruncated},
		{"iinf cut", rawFile(hdlrOK, pitmOK, mkbox("iinf", u8s(0))), ErrTruncated},
		{"iinf child cut", rawFile(hdlrOK, pitmOK, fbox("iinf", 0, 0, u16(1), mkbox("infe", u8s(0)))), ErrTruncated},
		{"iinf bad child size", rawFile(hdlrOK, pitmOK, fbox("iinf", 0, 0, u16(1), append(u32(99), "infe"...))), ErrTruncated},
		{"item described twice", rawFile(hdlrOK, pitmOK, fbox("iinf", 0, 0, u16(2),
			fbox("infe", 2, 0, u16(1, 0), []byte("hvc1"), u8s(0)), fbox("infe", 2, 0, u16(1, 0), []byte("hvc1"), u8s(0)))), ErrInvalid},
		{"iref cut", rawFile(hdlrOK, pitmOK, mkbox("iref", u8s(0))), ErrTruncated},
		{"iref child bad size", rawFile(hdlrOK, pitmOK, fbox("iref", 0, 0, append(u32(99), "dimg"...))), ErrTruncated},
		{"iref child cut", rawFile(hdlrOK, pitmOK, fbox("iref", 0, 0, mkbox("dimg", u16(1, 3, 2)))), ErrTruncated},
		{"iprp child bad size", rawFile(hdlrOK, pitmOK, mkbox("iprp", append(u32(99), "ipco"...))), ErrTruncated},
		{"ipco child bad size", rawFile(hdlrOK, pitmOK, mkbox("iprp", mkbox("ipco", append(u32(99), "ispe"...)))), ErrTruncated},
		{"ispe cut", rawFile(hdlrOK, pitmOK, mkbox("iprp", mkbox("ipco", fbox("ispe", 0, 0, u32(1))))), ErrTruncated},
		{"hvcC cut", rawFile(hdlrOK, pitmOK, mkbox("iprp", mkbox("ipco", mkbox("hvcC", make([]byte, 10))))), ErrTruncated},
		{"ipma cut", rawFile(hdlrOK, pitmOK, mkbox("iprp", mkbox("ipma", u8s(0)))), ErrTruncated},
		{"iloc cut", rawFile(hdlrOK, pitmOK, mkbox("iloc", u8s(0))), ErrTruncated},
		{"meta child bad size", rawFile(hdlrOK, append(u32(99), "iinf"...)), ErrTruncated},
		{"iloc version 3", rawFile(hdlrOK, pitmOK, fbox("iloc", 3, 0, u16(0x4400, 0))), ErrUnsupported},
	} {
		_, err := Decode(bytes.NewReader(tc.data))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if _, err := DecodeConfig(bytes.NewReader(tc.data)); err == nil {
			t.Errorf("%s: DecodeConfig accepts it", tc.name)
		} else if !isNamed(err) {
			t.Errorf("%s: DecodeConfig gives %v, which is not a named error", tc.name, err)
		}
	}
}

// ISO/IEC 14496-12 4.2: a size of 1 is followed by a 64-bit size, a size of 0 runs to the
// end of the file, and a uuid type carries 16 more bytes of identifier.
func TestBoxHeaderForms(t *testing.T) {
	defer guard(t)
	data := goodBytes(t)
	whole, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	free64 := append(append(u32(1), "free"...), append(u32(0, 20), "pad."...)...)
	uuid := append(u32(8+16+3), append([]byte("uuid"), append(make([]byte, 16), "abc"...)...)...)
	toEnd := append(u32(0), "free"...)
	trailer := bytes.Join([][]byte{data, free64, uuid, append(toEnd, "payload"...)}, nil)
	got, err := Decode(bytes.NewReader(trailer))
	if err != nil {
		t.Fatal(err)
	}
	sameImage(t, "with odd boxes after mdat", got, whole)
}

func TestVersionsOfBoxes(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	base, err := Decode(bytes.NewReader(goodBytes(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		v    vers
	}{
		{"pitm 1", vers{pitm: 1}},
		{"iinf 1", vers{iinf: 1}},
		{"iref 1", vers{iref: 1}},
		{"ipma 1", vers{ipma: 1}},
		{"ipma flags 1", vers{ipmaFlags: 1}},
		{"infe 3", vers{infe3: true}},
		{"iloc 0", vers{iloc0: true}},
		{"iloc 2", vers{iloc2: true}},
		{"all", vers{pitm: 1, iinf: 1, iref: 1, ipma: 1, ipmaFlags: 1, infe3: true, iloc2: true}},
	} {
		f := single(c, 36, 22)
		f.v = tc.v
		got, err := Decode(bytes.NewReader(f.bytes()))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		sameImage(t, tc.name, got, base)
	}
	// infe versions 0 and 1 name no item type: such an item is not an image
	for _, v := range []uint8{0, 1} {
		m := rawFile(hdlrOK, pitmOK, fbox("iinf", 0, 0, u16(1), fbox("infe", v, 0, u16(1, 0), []byte("name\x00"), []byte("image/x\x00"))))
		if _, err := Decode(bytes.NewReader(m)); !errors.Is(err, ErrUnsupported) {
			t.Errorf("infe %d: %v", v, err)
		}
	}
}

func TestIlocFields(t *testing.T) {
	defer guard(t)
	// offset, length and base of 8 bytes, an extent index of 4 (version 1)
	f := newFile(Limits{})
	body := fbox("iloc", 1, 0, u16(0x8884, 1), u16(7, 0, 0), u32(0, 100), u16(2),
		u32(0), u32(0, 5), u32(0, 6), u32(1), u32(0, 7), u32(0, 8))[8:]
	if err := f.parseIloc(body); err != nil {
		t.Fatal(err)
	}
	l := f.byID[7].loc
	if l.base != 100 || len(l.extents) != 2 || l.extents[0] != (extent{5, 6}) || l.extents[1] != (extent{7, 8}) {
		t.Errorf("%+v", l)
	}
	for _, tc := range []struct {
		name string
		lim  Limits
		body []byte
		want error
	}{
		{"short", Limits{}, fbox("iloc", 1, 0)[8:], ErrTruncated},
		{"version 3", Limits{}, fbox("iloc", 3, 0, u16(0x4400, 0))[8:], ErrUnsupported},
		{"field size 3", Limits{}, fbox("iloc", 1, 0, u16(0x3400, 0))[8:], ErrInvalid},
		{"base size 2", Limits{}, fbox("iloc", 1, 0, u16(0x4420, 0))[8:], ErrInvalid},
		{"index size 1", Limits{}, fbox("iloc", 1, 0, u16(0x4401, 0))[8:], ErrInvalid},
		{"no count", Limits{}, fbox("iloc", 1, 0, u16(0x4400))[8:], ErrTruncated},
		{"too many items", Limits{MaxItems: 1}, fbox("iloc", 1, 0, u16(0x4400, 2))[8:], ErrTooManyItems},
		{"extents of no size", Limits{}, fbox("iloc", 1, 0, u16(0x0000, 1), u16(1, 0, 0, 2))[8:], ErrInvalid},
		{"entry cut", Limits{}, fbox("iloc", 1, 0, u16(0x4400, 1), u16(1, 0, 0, 1), u32(0))[8:], ErrTruncated},
		{"two locations", Limits{}, fbox("iloc", 0, 0, u16(0x4400, 2), u16(1, 0, 1), u32(0, 0), u16(1, 0, 1), u32(0, 0))[8:], ErrInvalid},
		{"at MaxItems", Limits{MaxItems: 1}, fbox("iloc", 0, 0, u16(0x4400, 1), u16(1, 0, 1), u32(0, 0))[8:], nil},
	} {
		f := newFile(tc.lim)
		err := f.parseIloc(tc.body)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// an item beyond MaxItems named by iloc
	f = newFile(Limits{MaxItems: 1})
	f.byID[99] = &item{id: 99}
	f.items = []*item{f.byID[99]}
	if err := f.parseIloc(fbox("iloc", 0, 0, u16(0x4400, 1), u16(1, 0, 1), u32(0, 0))[8:]); !errors.Is(err, ErrTooManyItems) {
		t.Errorf("item beyond MaxItems: %v", err)
	}
}

func TestIpmaIinfIrefLimits(t *testing.T) {
	defer guard(t)
	// ipma
	ipmaCases := []struct {
		name string
		lim  Limits
		body []byte
		want error
	}{
		{"count over MaxItems", Limits{MaxItems: 1}, fbox("ipma", 0, 0, u32(2))[8:], ErrTooManyItems},
		{"entry cut", Limits{}, fbox("ipma", 0, 0, u32(1), u16(1), u8s(3, 1))[8:], ErrTruncated},
		{"wide entry cut", Limits{}, fbox("ipma", 0, 1, u32(1), u16(1), u8s(2), u16(1))[8:], ErrTruncated},
		{"a second item beyond MaxItems", Limits{MaxItems: 1}, fbox("ipma", 0, 0, u32(1), u16(5), u8s(0))[8:], nil},
	}
	for _, tc := range ipmaCases {
		f := newFile(tc.lim)
		if tc.name == "a second item beyond MaxItems" {
			f.byID[1] = &item{id: 1}
			f.items = []*item{f.byID[1]}
			tc.want = ErrTooManyItems
		}
		if err := f.parseIpma(tc.body); !errors.Is(err, tc.want) {
			t.Errorf("ipma %s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// iinf: a child that is not an infe is skipped; an item beyond MaxItems is refused
	f := newFile(Limits{MaxItems: 1})
	p := &parser{lim: f.lim}
	infe := func(id uint16) []byte { return fbox("infe", 2, 0, u16(id, 0), []byte("hvc1"), u8s(0)) }
	if err := f.parseIinf(p, fbox("iinf", 0, 0, u16(1), mkbox("free"), infe(1))[8:]); err != nil || len(f.items) != 1 {
		t.Errorf("iinf with a free box: %v %d", err, len(f.items))
	}
	if err := f.parseIinf(p, fbox("iinf", 0, 0, u16(1), infe(2))[8:]); !errors.Is(err, ErrTooManyItems) {
		t.Errorf("iinf over MaxItems: %v", err)
	}
	// iref: more references than MaxItems
	f = newFile(Limits{MaxItems: 1})
	p = &parser{lim: f.lim}
	if err := f.parseIref(p, fbox("iref", 0, 0, mkbox("dimg", u16(1, 0)), mkbox("dimg", u16(2, 0)))[8:]); !errors.Is(err, ErrTooManyItems) {
		t.Errorf("iref over MaxItems: %v", err)
	}
}

func TestPropertyForms(t *testing.T) {
	defer guard(t)
	// a colr of an unknown kind is kept without a profile; pixi and auxC are read
	pr, err := parseProperty(box{typ: "colr", body: []byte("abcdxyz")})
	if err != nil || pr.isNCLX || pr.icc != nil {
		t.Errorf("colr abcd: %+v %v", pr, err)
	}
	pr, err = parseProperty(box{typ: "colr", body: append([]byte("rICC"), "icc"...)})
	if err != nil || string(pr.icc) != "icc" {
		t.Errorf("colr rICC: %+v %v", pr, err)
	}
	pr, err = parseProperty(box{typ: "pixi", body: append(u32(0), 3, 8, 8, 8)})
	if err != nil || !bytes.Equal(pr.bits, []byte{8, 8, 8}) {
		t.Errorf("pixi: %+v %v", pr, err)
	}
	if _, err := parseProperty(box{typ: "pixi", body: append(u32(0), 3, 8)}); !errors.Is(err, ErrTruncated) {
		t.Errorf("pixi cut: %v", err)
	}
	pr, err = parseProperty(box{typ: "auxC", body: append(u32(0), "urn:x"...)})
	if err != nil || pr.auxType != "urn:x" {
		t.Errorf("auxC without a terminator: %+v %v", pr, err)
	}
	pr, err = parseProperty(box{typ: "auxC", body: append(u32(0), "urn:x\x00more"...)})
	if err != nil || pr.auxType != "urn:x" {
		t.Errorf("auxC: %+v %v", pr, err)
	}
	if _, err := parseProperty(box{typ: "irot"}); !errors.Is(err, ErrTruncated) {
		t.Errorf("irot cut: %v", err)
	}
	// hvcC: an array whose NAL unit runs past the record
	h := hvcCBox(1, 8, 8, 4, [][]byte{{0x40, 1, 2}})
	if pr, err := parseProperty(box{typ: "hvcC", body: h[8:]}); err != nil || len(pr.hvcc.nals) != 1 {
		t.Errorf("hvcC: %+v %v", pr, err)
	}
	if _, err := parseProperty(box{typ: "hvcC", body: h[8 : len(h)-1]}); !errors.Is(err, ErrTruncated) {
		t.Errorf("hvcC cut: %v", err)
	}
	// unknown property types are kept as they are
	if pr, err := parseProperty(box{typ: "zzzz", body: []byte{1}}); err != nil || pr.typ != "zzzz" {
		t.Errorf("zzzz: %+v %v", pr, err)
	}
}

func TestPropertyAssociations(t *testing.T) {
	defer guard(t)
	f := newFile(Limits{})
	f.props = []property{{typ: "ispe"}, {typ: "zzzz"}}
	it := &item{id: 1, assocs: []assoc{{0, false}, {1, true}, {2, false}}}
	ps, err := f.propsOf(it)
	if err != nil || len(ps) != 2 {
		t.Fatalf("%v %v", ps, err)
	}
	if _, err := f.propsOf(&item{id: 1, assocs: []assoc{{2, true}}}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("essential unknown property: %v", err)
	}
	if _, err := f.propsOf(&item{id: 1, assocs: []assoc{{3, false}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("property beyond ipco: %v", err)
	}
	if find(ps, "colr") != nil {
		t.Error("find invents a property")
	}
}

func TestLimitsOfStructure(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	data := single(c, 36, 22).bytes()
	for _, tc := range []struct {
		name string
		lim  Limits
		want error
	}{
		{"max bytes", Limits{MaxBytes: int64(len(data)) - 1}, ErrFileTooLarge},
		{"max boxes", Limits{MaxBoxes: 3}, ErrTooManyBoxes},
		{"max depth", Limits{MaxDepth: 1}, ErrNestingTooDeep},
		{"max pixels", Limits{MaxPixels: 791}, ErrImageTooLarge},
		{"max items", Limits{MaxItems: 1}, nil},
	} {
		_, err := DecodeOptions(bytes.NewReader(data), Options{Limits: tc.lim})
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if _, err := DecodeConfigOptions(bytes.NewReader(data), Options{Limits: tc.lim}); tc.name != "max bytes" && !errors.Is(err, tc.want) {
			t.Errorf("%s: config %v, want %v", tc.name, err, tc.want)
		}
	}
	// exactly at the limit is accepted (the decoder measures the 40x24 coded picture)
	if _, err := DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxBytes: int64(len(data)), MaxPixels: 960}}); err != nil {
		t.Errorf("at the limits: %v", err)
	}
	if _, err := DecodeConfigOptions(bytes.NewReader(data), Options{Limits: Limits{MaxPixels: 792}}); err != nil {
		t.Errorf("config at the pixel limit: %v", err)
	}
	g := gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false)).bytes()
	if _, err := DecodeOptions(bytes.NewReader(g), Options{Limits: Limits{MaxTiles: 3}}); !errors.Is(err, ErrTooManyTiles) {
		t.Errorf("max tiles: %v", err)
	}
	if _, err := DecodeOptions(bytes.NewReader(g), Options{Limits: Limits{MaxTiles: 4}}); err != nil {
		t.Errorf("4 tiles of 4: %v", err)
	}
	// the decoder's own budget
	if _, err := DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxDecoderBytes: 1}}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("max decoder bytes: %v", err)
	}
	// a negative limit is the default
	if _, err := DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxBytes: -1, MaxPixels: -1, MaxTiles: -1, MaxBoxes: -1, MaxDepth: -1, MaxItems: -1, MaxDecoderBytes: -1}}); err != nil {
		t.Errorf("negative limits: %v", err)
	}
}

// Every tile is decoded: tiles that are long and narrow cost far more than the pixels of
// the image they make, so their total is bounded as well.
func TestGridTileBudget(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	data := gridFile(c, 36, 22, 5, 1, 2, 100, gridDesc(5, 1, 2, 100, false)).bytes()
	if _, err := DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxPixels: 800}}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("5 tiles of 792 pixels under 800: %v", err)
	}
	if _, err := Decode(bytes.NewReader(data)); err != nil {
		t.Errorf("default limits: %v", err)
	}
}

func TestItemData(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	base, err := Decode(bytes.NewReader(goodBytes(t)))
	if err != nil {
		t.Fatal(err)
	}
	// the data in several extents, and in idat
	f := single(c, 36, 22)
	f.items[0].split = 10
	got, err := Decode(bytes.NewReader(f.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	sameImage(t, "split", got, base)
	f = single(c, 36, 22)
	f.items[0].idat = true
	if got, err = Decode(bytes.NewReader(f.bytes())); err != nil {
		t.Fatal(err)
	}
	sameImage(t, "idat", got, base)

	// overlapping extents: n extents over the whole data assemble n times its size
	f = single(c, 36, 22)
	f.items[0].dup = 50
	if _, err := DecodeOptions(bytes.NewReader(f.bytes()), Options{Limits: Limits{MaxBytes: int64(len(f.bytes()))}}); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("overlapping extents: %v", err)
	}

	pf, err := parseFile(goodBytes(t), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	it := pf.byID[1]
	orig := *it.loc
	for _, tc := range []struct {
		name string
		loc  *location
		want error
	}{
		{"no location", nil, ErrInvalid},
		{"data reference", &location{dataRef: 1, extents: orig.extents}, ErrUnsupported},
		{"construction method 2", &location{method: 2, extents: orig.extents}, ErrUnsupported},
		{"past the file", &location{extents: []extent{{uint64(len(pf.data)), 1}}}, ErrTruncated},
		{"past the file by length", &location{extents: []extent{{0, uint64(len(pf.data)) + 1}}}, ErrTruncated},
		{"base and offset wrap", &location{base: math.MaxUint64, extents: []extent{{2, 0}}}, ErrTruncated},
		{"offset and length wrap", &location{extents: []extent{{2, math.MaxUint64}}}, ErrTruncated},
		{"idat is empty", &location{method: 1, extents: []extent{{0, 1}}}, ErrTruncated},
		{"ok", &orig, nil},
	} {
		it.loc = tc.loc
		_, err := pf.itemData(it)
		if !errors.Is(err, tc.want) && !(tc.want == nil && err == nil) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// base + offset
	it.loc = &location{base: 4, extents: []extent{{orig.extents[0].off - 4, orig.extents[0].length}}}
	if d, err := pf.itemData(it); err != nil || !bytes.Equal(d, c.data) {
		t.Errorf("base and offset: %v", err)
	}
}

func TestDecodeFailures(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	mutate := func(f func(*hfile)) []byte {
		h := single(c, 36, 22)
		f(&h)
		return h.bytes()
	}
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"no hvcC", mutate(func(h *hfile) { h.props[1] = mkbox("free"); h.items[0].props = []int{1} }), ErrInvalid},
		{"no ispe", mutate(func(h *hfile) { h.items[0].props = []int{2} }), ErrInvalid},
		{"zero ispe", mutate(func(h *hfile) { h.props[0] = ispe(0, 22) }), ErrInvalid},
		{"ispe of another size", mutate(func(h *hfile) { h.props[0] = ispe(36, 24) }), ErrInvalid},
		{"no item data", mutate(func(h *hfile) { h.items[0].data = nil }), ErrCorrupt},
		{"garbage item data", mutate(func(h *hfile) { h.items[0].data = append(u32(6), 0x26, 0x01, 1, 2, 3, 4) }), ErrCorrupt},
		{"length past the data", mutate(func(h *hfile) { h.items[0].data = append(u32(1000), 0x26, 0x01) }), ErrCorrupt},
		{"first slice cut", mutate(func(h *hfile) { h.items[0].data = h.items[0].data[:len(h.items[0].data)/2] }), ErrCorrupt},
		{"hvcC with a NAL unit of one byte", mutate(func(h *hfile) { h.props[1] = hvcCBox(1, 8, 8, 4, [][]byte{{0x40}}) }), ErrCorrupt},
		{"item type av01", mutate(func(h *hfile) { h.items[0].typ = "av01" }), ErrUnsupported},
		{"primary does not exist", mutate(func(h *hfile) { h.primary = 9 }), ErrInvalid},
		{"essential unknown property", mutate(func(h *hfile) {
			h.props = append(h.props, mkbox("zzzz"))
			h.items[0].props = append(h.items[0].props, -len(h.props))
		}), ErrUnsupported},
		{"property index beyond ipco", mutate(func(h *hfile) { h.items[0].props = append(h.items[0].props, 90) }), ErrInvalid},
	} {
		if _, err := Decode(bytes.NewReader(tc.data)); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestPlaneGuard(t *testing.T) {
	defer guard(t)
	ok := &h265.DecodedPicture{Components: 1}
	ok.Planes[0] = h265.Plane{Width: 4, Height: 2, Stride: 4, Pix8: make([]uint8, 8)}
	if err := checkPlanes(ok); err != nil {
		t.Fatal(err)
	}
	two := &h265.DecodedPicture{Components: 2}
	if err := checkPlanes(two); !errors.Is(err, ErrUnsupported) {
		t.Errorf("two components: %v", err)
	}
	for name, p := range map[string]h265.Plane{
		"no width":      {Width: 0, Height: 2, Stride: 4, Pix8: make([]uint8, 8)},
		"no height":     {Width: 4, Height: 0, Stride: 4, Pix8: make([]uint8, 8)},
		"stride":        {Width: 4, Height: 2, Stride: 3, Pix8: make([]uint8, 8)},
		"short samples": {Width: 4, Height: 2, Stride: 4, Pix8: make([]uint8, 7)},
		"no samples":    {Width: 4, Height: 2, Stride: 4},
	} {
		bad := &h265.DecodedPicture{Components: 1}
		bad.Planes[0] = p
		if err := checkPlanes(bad); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a 16-bit plane counts its Pix16
	w := &h265.DecodedPicture{Components: 1}
	w.Planes[0] = h265.Plane{Width: 4, Height: 2, Stride: 4, Pix16: make([]uint16, 8)}
	if err := checkPlanes(w); err != nil {
		t.Errorf("16-bit: %v", err)
	}
	// the last row needs only Width samples, not a whole stride
	last := &h265.DecodedPicture{Components: 1}
	last.Planes[0] = h265.Plane{Width: 4, Height: 2, Stride: 6, Pix8: make([]uint8, 10)}
	if err := checkPlanes(last); err != nil {
		t.Errorf("last row: %v", err)
	}
}

// failing is a reader that gives its bytes, then fails.
type failing struct {
	data []byte
	err  error
}

func (f *failing) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestReadErrors(t *testing.T) {
	defer guard(t)
	boom := errors.New("boom")
	if _, err := Decode(&failing{data: goodBytes(t)[:30], err: boom}); !errors.Is(err, boom) {
		t.Errorf("Decode: %v", err)
	}
	if _, err := DecodeConfig(&failing{data: goodBytes(t)[:30], err: boom}); !errors.Is(err, ErrTruncated) {
		t.Errorf("DecodeConfig: %v", err)
	}
}

// split cuts a file into its top-level boxes.
func topBoxes(t testing.TB, data []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for len(data) > 0 {
		n := int(binary.BigEndian.Uint32(data))
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

func TestDecodeConfigReadsOnlyTheHead(t *testing.T) {
	defer guard(t)
	data := goodBytes(t)
	parts := topBoxes(t, data) // ftyp, meta, mdat
	ftyp, meta, mdat := parts[0], parts[1], parts[2]
	want := image.Config{ColorModel: nil, Width: 36, Height: 22}
	check := func(name string, d []byte) {
		t.Helper()
		cfg, err := DecodeConfig(bytes.NewReader(d))
		if err != nil || cfg.Width != want.Width || cfg.Height != want.Height {
			t.Errorf("%s: %+v %v", name, cfg, err)
		}
	}
	check("whole", data)
	check("mdat first", bytes.Join([][]byte{ftyp, mdat, meta}, nil))
	check("nothing after meta", append(append([]byte(nil), ftyp...), meta...))
	// a meta box with a 64-bit size
	big := append(append(u32(1), "meta"...), binary.BigEndian.AppendUint64(nil, uint64(len(meta))+8)...)
	check("64-bit meta", bytes.Join([][]byte{ftyp, append(big, meta[8:]...)}, nil))
	// a box of 64 bits skipped
	skipped := append(append(u32(1), "free"...), binary.BigEndian.AppendUint64(nil, 20)...)
	check("64-bit free", bytes.Join([][]byte{ftyp, append(skipped, "abcd"...), meta}, nil))

	hdr := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, ErrTruncated},
		{"header cut", hdr(ftyp, u32(20)), ErrTruncated},
		{"size 0", hdr(ftyp, append(u32(0), "free"...)), ErrInvalid},
		{"size below the header", hdr(ftyp, append(u32(5), "free"...)), ErrInvalid},
		{"64-bit size cut", hdr(ftyp, append(u32(1), "free"...), u32(0)), ErrTruncated},
		{"64-bit size below the header", hdr(ftyp, append(u32(1), "free"...), u32(0, 15)), ErrInvalid},
		{"skipped box cut", hdr(ftyp, append(u32(100), "free"...), u8s(1, 2)), ErrTruncated},
		{"ftyp cut", hdr(append(u32(30), "ftyp"...), u8s(1, 2)), ErrTruncated},
		{"meta cut", hdr(ftyp, meta[:len(meta)-1]), ErrTruncated},
		{"no meta", hdr(ftyp, mdat), ErrTruncated},
		{"huge box", hdr(ftyp, append(u32(1), "free"...), binary.BigEndian.AppendUint64(nil, 1<<40)), ErrFileTooLarge},
		{"box over what is left", hdr(ftyp, append(u32(1<<30), "free"...)), ErrFileTooLarge},
	} {
		_, err := DecodeConfigOptions(bytes.NewReader(tc.data), Options{Limits: Limits{MaxBytes: 1 << 20}})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// the sizes of the boxes read count together against the limit
	if _, err := DecodeConfigOptions(bytes.NewReader(data), Options{Limits: Limits{MaxBytes: int64(len(ftyp)+len(meta)) - 1}}); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("ftyp and meta over the limit: %v", err)
	}
	if _, err := DecodeConfigOptions(bytes.NewReader(data), Options{Limits: Limits{MaxBytes: int64(len(ftyp) + len(meta))}}); err != nil {
		t.Errorf("ftyp and meta at the limit: %v", err)
	}
}

// Cutting any box of the meta tree at any length, and shrinking its containers to match,
// never panics and ends in a decoded image or a named error.
func TestTruncationSweep(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	grid := gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false), colrNclx(1, 1, 1, true), irot(1))
	grid.refs = append(grid.refs, href{"auxl", 6, []uint32{1}})
	grid.items = append(grid.items, hitem{id: 6, typ: "hvc1", data: c.data, props: []int{2, 3}})
	for _, v := range []vers{{}, {pitm: 1, iinf: 1, iref: 1, ipma: 1, ipmaFlags: 1, infe3: true, iloc2: true}, {iloc0: true}} {
		for _, h := range []hfile{single(c, 36, 22, colrNclx(1, 1, 1, true), irot(1)), grid} {
			h.v = v
			data := h.bytes()
			type span struct{ off, end int }
			var spans [][]span // each box with its ancestors, outermost first
			var walk func(off, end int, parents []span)
			walk = func(off, end int, parents []span) {
				for off+8 <= end {
					n := int(binary.BigEndian.Uint32(data[off:]))
					typ := string(data[off+4 : off+8])
					s := span{off, off + n}
					chain := append(append([]span(nil), parents...), s)
					spans = append(spans, chain)
					skip := map[string]int{"meta": 12, "iinf": 14, "iref": 12, "iprp": 8, "ipco": 8}[typ]
					if typ == "iinf" && v.iinf != 0 {
						skip = 16 // version 1 holds a 32-bit count
					}
					if skip > 0 {
						walk(off+skip, off+n, chain)
					}
					off += n
				}
			}
			walk(0, len(data), nil)
			for _, chain := range spans {
				leaf := chain[len(chain)-1]
				for cut := 1; cut <= leaf.end-leaf.off-8; cut++ {
					d := append(append([]byte(nil), data[:leaf.end-cut]...), data[leaf.end:]...)
					for _, s := range chain {
						binary.BigEndian.PutUint32(d[s.off:], uint32(s.end-s.off-cut))
					}
					for _, decode := range []func() error{
						func() error { _, err := Decode(bytes.NewReader(d)); return err },
						func() error { _, err := DecodeConfig(bytes.NewReader(d)); return err },
					} {
						if err := decode(); err != nil && !isNamed(err) {
							t.Fatalf("box at %d cut by %d: %v is not a named error", leaf.off, cut, err)
						}
					}
				}
			}
		}
	}
}
