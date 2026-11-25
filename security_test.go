// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"errors"
	"image"
	"math"
	"strings"
	"testing"
)

// These are the regression tests of an independent security review. Each hostile file is
// decoded at the DEFAULT limits and must be refused, or cost a bounded number of bytes of
// allocation (exact under load, unlike a time). Before the fixes each of them allocated
// from hundreds of MiB to tens of GiB, from a file of 70 KB to 64 MiB.

// smallBudget is what a refusal of a small hostile file may allocate.
const smallBudget = 8 << 20

// readCost is what reading a file of that length costs before any box is looked at, by
// either entry point, measured on an ftyp and a meta of empty boxes: it changes with the
// build (the race detector makes it five times the length), the parser's own cost must not.
func readCost(length int) uint64 {
	ftyp := mkbox("ftyp", []byte("heic"), u32(0))
	junk := bytes.Join([][]byte{ftyp, mkbox("meta", make([]byte, length-len(ftyp)-8))}, nil)
	whole := bombed(func() { _, _ = DecodeOptions(bytes.NewReader(junk), Options{}) })
	head := bombed(func() { _, _ = DecodeConfig(bytes.NewReader(junk)) })
	return max(whole, head)
}

func decodeDefault(data []byte) error {
	_, err := DecodeOptions(bytes.NewReader(data), Options{})
	return err
}

// A: ignorable 2-byte NAL units with 1-byte lengths, assembled from overlapping extents
// (was 70 KB of file for 1172 MiB).
func TestSecurityNALUnitBomb(t *testing.T) {
	defer guard(t)
	pat := bytes.Repeat([]byte{2, 0x4E, 0x01}, 1365) // 4095 bytes of 1365 units
	for _, dup := range []int{1024, 4096, 8192} {
		f := single(coded{hvcC: hvcCBox(1, 8, 8, 1, nil), data: pat}, 36, 22)
		f.items[0].dup = dup
		data := f.bytes()
		var err error
		if n := bombed(func() { err = decodeDefault(data) }); n > readCost(len(data))+smallBudget {
			t.Errorf("dup %d: %d bytes allocated", dup, n)
		}
		if !errors.Is(err, ErrFileTooLarge) {
			t.Errorf("dup %d (%d bytes): %v", dup, len(data), err)
		}
	}
	// one extent, so nothing is copied: the units themselves are bounded
	unit := []byte{2, 0x4E, 0x01} // an SEI unit of its two header bytes
	for _, tc := range []struct {
		n    int
		want error
	}{{maxNALUnits, nil}, {maxNALUnits + 1, ErrTooManyItems}} {
		err := scanUnits(bytes.Repeat(unit, tc.n), &hevcConfig{lengthSize: 1}, maxNALUnits)
		if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
			t.Errorf("%d units: %v", tc.n, err)
		}
	}
	f := single(coded{hvcC: hvcCBox(1, 8, 8, 1, nil), data: bytes.Repeat(unit, 1<<20)}, 36, 22)
	data := f.bytes()
	var err error
	if n := bombed(func() { err = decodeDefault(data) }); n > readCost(len(data))+smallBudget {
		t.Errorf("a million units: %d bytes allocated", n)
	}
	if !errors.Is(err, ErrTooManyItems) {
		t.Errorf("a million units: %v", err)
	}
}

// The extents of all the items of a file copy no more than the file holds.
func TestSecurityAssembledBytesAreOneBudget(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	f := single(c, 36, 22)
	f.items[0].split = 10 // two extents: copied
	pf, err := parseFile(f.bytes(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	it := pf.byID[1]
	for i := 0; i < 1000; i++ {
		if _, err = pf.itemData(it); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("assembled %d bytes of a %d-byte file: %v", pf.spent, len(pf.data), err)
	}
	// exactly the length of the file is allowed
	pf, _ = parseFile(f.bytes(), DefaultLimits())
	pf.spent = uint64(len(pf.data)) - uint64(len(c.data))
	if _, err = pf.itemData(pf.byID[1]); err != nil {
		t.Errorf("a budget met exactly: %v", err)
	}
}

// D: an item holds one picture; a second access unit is refused, not decoded.
func TestSecuritySecondPictureRefused(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	if err := decodeDefault(single(c, 36, 22).bytes()); err != nil {
		t.Fatal(err)
	}
	for _, dup := range []int{2, 1000} {
		twice := c
		twice.data = bytes.Repeat(c.data, dup)
		var err error
		if n := bombed(func() { err = decodeDefault(single(twice, 36, 22).bytes()) }); n > 4*smallBudget {
			t.Errorf("%d pictures: %d bytes allocated", dup, n)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%d pictures: %v", dup, err)
		}
	}
	// the same file with a coded slice smuggled into the hvcC
	slice := []byte{19 << 1, 1, 0x80, 0, 0}
	bad := coded{hvcC: hvcCBox(1, 8, 8, 4, [][]byte{slice}), data: c.data}
	if err := decodeDefault(single(bad, 36, 22).bytes()); !errors.Is(err, ErrInvalid) {
		t.Errorf("a slice in the hvcC: %v", err)
	}
	// an empty unit has no header, and a slice segment of two bytes no flag
	if err := scanUnits([]byte{0}, &hevcConfig{lengthSize: 1}, 10); !errors.Is(err, ErrCorrupt) {
		t.Errorf("an empty unit: %v", err)
	}
	// an empty parameter set in the hvcC is not read past its end
	_ = decodeDefault(single(coded{hvcC: hvcCBox(1, 8, 8, 4, [][]byte{{}}), data: c.data}, 36, 22).bytes())
	cfg4 := &hevcConfig{lengthSize: 4}
	if err := scanUnits([]byte{0, 0, 0, 2, 2 << 1, 1}, cfg4, 10); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a short slice: %v", err)
	}
	for _, tc := range []struct {
		data []byte
		why  string
	}{
		{[]byte{0, 0}, "cut short"},
		{[]byte{0, 0, 0}, "cut short"},
		{[]byte{0, 0, 0, 0}, "no header"}, // a length and nothing after it is whole
		{[]byte{0, 0, 0, 9, 1}, "runs past"},
	} {
		if err := scanUnits(tc.data, cfg4, 10); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%v: %v, want %q", tc.data, err, tc.why)
		}
	}
}

// An hvcC of tens of thousands of empty units costs a slice header per two bytes.
func TestSecurityHvccUnitCap(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	for _, tc := range []struct {
		n    int
		want error
	}{{maxHvcCUnits, nil}, {maxHvcCUnits + 1, ErrInvalid}} {
		sets := make([][]byte, tc.n)
		for i := range sets {
			sets[i] = []byte{0x4E, 1} // an SEI unit
		}
		_, err := parseFile(single(coded{hvcC: hvcCBox(1, 8, 8, 4, sets), data: c.data}, 36, 22).bytes(), DefaultLimits())
		if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
			t.Errorf("%d units: %v", tc.n, err)
		}
	}
}

// C: DecodeConfig on a file whose auxl references all name the primary item (was 566 KB
// for 12.2 s and 82 GiB, also through image.DecodeConfig).
func auxlFile(entries, assocs, refs, targets int) []byte {
	infe := fbox("infe", 2, 0, u16(1, 0), []byte("hvc1"), []byte{0})
	iinf := fbox("iinf", 0, 0, u16(1), infe)
	body := u32(uint32(1 + entries))
	body = append(body, u16(1)...)
	body = append(body, 1, 1)
	for i := 0; i < entries; i++ {
		body = append(body, u16(uint16(9+i))...)
		body = append(body, uint8(assocs))
		body = append(body, make([]byte, assocs)...)
	}
	iprp := mkbox("iprp", mkbox("ipco", ispe(36, 22)), fbox("ipma", 0, 0, body))
	to := make([]uint16, targets)
	for i := range to {
		to[i] = 1
	}
	one := mkbox("auxl", u16(9), u16(uint16(targets)), u16(to...))
	iref := fbox("iref", 0, 0, bytes.Repeat(one, refs))
	return rawFile(hdlrOK, pitmOK, iinf, iprp, iref)
}

func TestSecurityAuxiliaryReferences(t *testing.T) {
	defer guard(t)
	for _, tc := range []struct {
		name                           string
		entries, assocs, refs, targets int
		want                           error
	}{
		{"the probe", 40, 255, 2, 65535, ErrInvalid},
		{"many associations", 80, 255, 2, 65535, ErrInvalid},
		{"repeated targets", 40, 0, 2, 65535, ErrTooManyItems},
		{"within the bounds", 40, maxAssocs, 1, 60000, nil},
		{"associations at the bound", 1, maxAssocs, 1, 1, nil},
		{"associations over the bound", 1, maxAssocs + 1, 1, 1, ErrInvalid},
	} {
		data := auxlFile(tc.entries, tc.assocs, tc.refs, tc.targets)
		var err error
		if n := bombed(func() { _, err = DecodeConfig(bytes.NewReader(data)) }); n > 4*smallBudget {
			t.Errorf("%s: %d bytes allocated for a file of %d", tc.name, n, len(data))
		}
		if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		var sniffed error
		if n := bombed(func() { _, _, sniffed = image.DecodeConfig(bytes.NewReader(data)) }); n > 4*smallBudget {
			t.Errorf("%s sniffed: %d bytes allocated", tc.name, n)
		}
		if (sniffed == nil) != (err == nil) {
			t.Errorf("%s: image.DecodeConfig says %v, DecodeConfig %v", tc.name, sniffed, err)
		}
	}
}

// The reference targets of a file are bounded to four for each item the limit allows.
func TestSecurityReferenceTargetBound(t *testing.T) {
	defer guard(t)
	lim := Limits{MaxItems: 4}.withDefaults()
	for _, tc := range []struct {
		n    int
		want error
	}{{16, nil}, {17, ErrTooManyItems}} {
		iref := fbox("iref", 0, 0, mkbox("auxl", u16(9), u16(uint16(tc.n)), u16(make([]uint16, tc.n)...)))
		data := bytes.Join([][]byte{mkbox("ftyp", []byte("heic"), u32(0)), mkbox("meta", u32(0), hdlrOK, pitmOK, iref)}, nil)
		_, err := parseFile(data, lim)
		if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
			t.Errorf("%d targets: %v", tc.n, err)
		}
	}
}

// Every auxiliary item is resolved once, however many references name it.
func TestSecurityAlphaResolvedOnce(t *testing.T) {
	defer guard(t)
	f := &file{byID: map[uint32]*item{}, props: []property{{typ: "auxC", auxType: "urn:mpeg:hevc:2015:auxid:1"}}}
	f.byID[9] = &item{id: 9, assocs: []assoc{{index: 1}}}
	for i := 0; i < 2000; i++ {
		f.refs = append(f.refs, ref{typ: "auxl", from: 9, to: []uint32{uint32(100 + i)}})
	}
	f.refs = append(f.refs, ref{typ: "dimg", from: 9, to: []uint32{1}}, ref{typ: "auxl", from: 77, to: []uint32{1}})
	var got bool
	// propsOf allocates once per call, so 2000 resolutions would show as 2000 allocations
	if n := testing.AllocsPerRun(5, func() { got = f.hasAlpha(1999) }); n > 20 || !got {
		t.Errorf("%v allocations, alpha %v", n, got)
	}
	for _, id := range []uint32{1, 5, 99, 2100} {
		if f.hasAlpha(id) {
			t.Errorf("item %d has no alpha: only 100 to 2099 are named", id)
		}
	}
}

// B: transformative properties compose into one orientation and one crop.
func TestSecurityTransformChain(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	for _, n := range []int{254, maxTransforms + 1} {
		props := make([][]byte, n)
		for i := range props {
			props[i] = irot(1)
		}
		data := single(c, 36, 22, props...).bytes()
		var err error
		if m := bombed(func() { err = decodeDefault(data) }); m > 4*smallBudget {
			t.Errorf("%d rotations: %d bytes allocated", n, m)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%d rotations: %v", n, err)
		}
	}
}

// The largest legal chain: every item of a chain of eight derived items carries the most
// transformations. It is one pass over the pixels, not one per property.
func TestSecurityTransformChainIsOnePass(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	const w, h = 36, 22
	moves := make([][]byte, 0, maxTransforms)
	for i := 0; i < maxTransforms; i++ {
		moves = append(moves, irot(1))
	}
	one, err := Decode(bytes.NewReader(single(c, w, h, colrNclx(1, 1, 1, true), irot(1)).bytes()))
	if err != nil {
		t.Fatal(err)
	}
	data := single(c, w, h, append([][]byte{colrNclx(1, 1, 1, true)}, moves...)...).bytes() // 16 quarter turns: none
	base, err := Decode(bytes.NewReader(single(c, w, h, colrNclx(1, 1, 1, true)).bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var got *Result
	n := bombed(func() { got, err = DecodeOptions(bytes.NewReader(data), Options{}) })
	if err != nil {
		t.Fatal(err)
	}
	sameImage(t, "16 turns", got.Image, base)
	// 16 rotations of a 36x22 picture, each a new raster, would take 16 times the memory
	// of one; a single pass takes none more than a decode without a transformation
	var ref uint64
	ref = bombed(func() { _, err = Decode(bytes.NewReader(single(c, w, h, colrNclx(1, 1, 1, true), irot(1)).bytes())) })
	if n > ref+ref/2 {
		t.Errorf("16 turns allocated %d bytes, one turn %d", n, ref)
	}
	_ = one
}

// E: iloc extents, four bytes of file for sixteen of memory (was 64 MiB for 535 MiB).
func ilocFile(items, extents int) []byte {
	var entries []byte
	for i := 0; i < items; i++ {
		entries = append(entries, u16(uint16(i+1), 0, 0, uint16(extents))...) // id, method, data ref, count
		entries = append(entries, make([]byte, 4*extents)...)
	}
	return rawFile(hdlrOK, pitmOK, fbox("iloc", 1, 0, u16(0x4000), u16(uint16(items)), entries))
}

func TestSecurityIlocExtents(t *testing.T) {
	defer guard(t)
	var err error
	data := ilocFile(256, 65535)
	// reading the file costs what it costs; the records cost next to nothing
	if n := bombed(func() { _, err = DecodeConfig(bytes.NewReader(data)) }); n > readCost(len(data))+smallBudget {
		t.Errorf("the probe: %d bytes allocated for a file of %d", n, len(data))
	}
	if !errors.Is(err, ErrTooManyItems) {
		t.Errorf("the probe: %v", err)
	}
	lim := Limits{MaxItems: 8}.withDefaults()
	for _, tc := range []struct {
		name           string
		items, extents int
		want           error
	}{
		{"an item at the bound", 1, 8, nil},
		{"an item over the bound", 1, 9, ErrTooManyItems},
		{"a file at the bound", 4, 8, nil},
		{"a file over the bound", 5, 8, ErrTooManyItems},
	} {
		_, err := parseFile(ilocFileWithFtyp(tc.items, tc.extents), lim)
		if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

func ilocFileWithFtyp(items, extents int) []byte {
	var entries []byte
	for i := 0; i < items; i++ {
		entries = append(entries, u16(uint16(i+1), 0, 0, uint16(extents))...)
		entries = append(entries, make([]byte, 4*extents)...)
	}
	return bytes.Join([][]byte{mkbox("ftyp", []byte("heic"), u32(0)), mkbox("meta", u32(0), hdlrOK, pitmOK,
		fbox("iloc", 1, 0, u16(0x4000), u16(uint16(items)), entries))}, nil)
}

// An item has one ipma entry.
func TestSecurityTwoIpmaEntries(t *testing.T) {
	defer guard(t)
	ipma := fbox("ipma", 0, 0, u32(2), u16(1), []byte{0}, u16(1), []byte{0})
	data := rawFile(hdlrOK, pitmOK, mkbox("iprp", mkbox("ipco", ispe(1, 1)), ipma))
	if _, err := DecodeConfig(bytes.NewReader(data)); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

// A grid read by DecodeConfig states its size by ispe, which must be one a grid can have.
func TestSecurityConfigOfAGridSize(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	for _, tc := range []struct {
		name string
		w, h uint32
		want error
	}{
		{"zero width", 0, 22, ErrInvalid},
		{"zero height", 36, 0, ErrInvalid},
		{"too many pixels", 1 << 16, 1 << 16, ErrImageTooLarge},
		{"the limit", 1 << 13, 1 << 13, nil},
	} {
		data := gridFile(c, 36, 22, 1, 1, tc.w, tc.h, gridDesc(1, 1, tc.w, tc.h, true)).bytes()
		cfg, err := DecodeConfig(bytes.NewReader(data))
		if !errors.Is(err, tc.want) || tc.want == nil && (err != nil || cfg.Width != int(tc.w)) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// A limit of the largest int64 must not wrap when one byte is added to tell a file of
// exactly the limit from a longer one.
func TestSecurityMaxBytesAtTheLargestValue(t *testing.T) {
	defer guard(t)
	data := goodBytes(t)
	if _, err := DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxBytes: math.MaxInt64}}); err != nil {
		t.Errorf("MaxInt64: %v", err)
	}
	if _, err := DecodeConfigOptions(bytes.NewReader(data), Options{Limits: Limits{MaxBytes: math.MaxInt64}}); err != nil {
		t.Errorf("config at MaxInt64: %v", err)
	}
	if _, err := DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxBytes: int64(len(data)) - 1}}); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("one byte over: %v", err)
	}
}
