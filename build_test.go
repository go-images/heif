// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// box makes a box.
func mkbox(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(out, typ...), body...)
}

// fbox makes a FullBox.
func fbox(typ string, version uint8, flags uint32, parts ...[]byte) []byte {
	head := binary.BigEndian.AppendUint32(nil, uint32(version)<<24|flags)
	return mkbox(typ, append([][]byte{head}, parts...)...)
}

func u8s(v ...uint8) []byte { return v }
func u16(v ...uint16) []byte {
	var o []byte
	for _, x := range v {
		o = binary.BigEndian.AppendUint16(o, x)
	}
	return o
}
func u32(v ...uint32) []byte {
	var o []byte
	for _, x := range v {
		o = binary.BigEndian.AppendUint32(o, x)
	}
	return o
}

// annexB splits an Annex B stream into its NAL units.
func annexB(t testing.TB, s []byte) [][]byte {
	t.Helper()
	var nals [][]byte
	start := -1
	for i := 0; i+3 <= len(s); i++ {
		if s[i] == 0 && s[i+1] == 0 && s[i+2] == 1 {
			if start >= 0 {
				nals = append(nals, bytes.TrimRight(s[start:i], "\x00"))
			}
			start = i + 3
			i += 2
		}
	}
	if start < 0 {
		t.Fatal("no start code")
	}
	return append(nals, s[start:])
}

// coded is an HEVC picture made ready to sit in a HEIF item.
type coded struct {
	hvcC []byte // the hvcC box
	data []byte // the item data: 4-byte length-prefixed slice NAL units
}

// loadCoded reads testdata/hevc/<name>.hevc and describes it as an hvcC and item data.
// chroma and bits are what the hvcC states.
func loadCoded(t testing.TB, name string, chroma, bits int) coded {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "hevc", name+".hevc"))
	if err != nil {
		t.Fatal(err)
	}
	var sets [][]byte
	var data []byte
	for _, n := range annexB(t, raw) {
		switch typ := n[0] >> 1 & 0x3F; typ {
		case 32, 33, 34:
			sets = append(sets, n)
		default:
			data = binary.BigEndian.AppendUint32(data, uint32(len(n)))
			data = append(data, n...)
		}
	}
	return coded{hvcC: hvcCBox(chroma, bits, bits, 4, sets), data: data}
}

func hvcCBox(chroma, luma, chromaBits, lengthSize int, sets [][]byte) []byte {
	h := make([]byte, 21)
	h[0] = 1
	h[16] = 0xFC | uint8(chroma)
	h[17] = 0xF8 | uint8(luma-8)
	h[18] = 0xF8 | uint8(chromaBits-8)
	h = append(h, 0xFC|uint8(lengthSize-1), uint8(len(sets)))
	for _, s := range sets {
		h = append(h, 0x80|s[0]>>1&0x3F)
		h = append(h, u16(1, uint16(len(s)))...)
		h = append(h, s...)
	}
	return mkbox("hvcC", h)
}

// propbox helpers.
func ispe(w, h uint32) []byte { return fbox("ispe", 0, 0, u32(w, h)) }
func irot(n uint8) []byte     { return mkbox("irot", u8s(n)) }
func imir(a uint8) []byte     { return mkbox("imir", u8s(a)) }
func clap(c ...uint32) []byte { return mkbox("clap", u32(c...)) }
func colrNclx(p, t, m uint16, full bool) []byte {
	f := uint8(0)
	if full {
		f = 0x80
	}
	return mkbox("colr", []byte("nclx"), u16(p, t, m), u8s(f))
}
func colrICC(p []byte) []byte { return mkbox("colr", []byte("prof"), p) }

// hitem is one item of a built file.
type hitem struct {
	id     uint32
	typ    string
	hidden bool
	data   []byte
	idat   bool  // the data goes in idat (construction method 1)
	split  int   // when > 0, the data is two extents, the first of this many bytes
	dup    int   // when > 0, this many extents all covering the whole data
	props  []int // 1-based indexes into the file's properties; negative: essential
}

type href struct {
	typ  string
	from uint32
	to   []uint32
}

// hfile describes a HEIF file to build.
type hfile struct {
	brands  []string
	primary uint32
	items   []hitem
	props   [][]byte
	refs    []href
	v       vers
}

// vers picks the versions of the boxes bytes writes; the zero value writes pitm 0, iinf 0,
// infe 2, iloc 1, iref 0 and ipma 0.
type vers struct {
	pitm, iinf, iref, ipma uint8
	ipmaFlags              uint32
	infe3, iloc0, iloc2    bool
}

func (v vers) infeV() uint8 {
	if v.infe3 {
		return 3
	}
	return 2
}

func (v vers) ilocV() uint8 {
	switch {
	case v.iloc0:
		return 0
	case v.iloc2:
		return 2
	}
	return 1
}

func wide(v uint8, id uint32) []byte {
	if v >= 1 {
		return u32(id)
	}
	return u16(uint16(id))
}

// bytes builds the file: ftyp, meta (hdlr, pitm, iinf, iloc, iref, iprp, idat) and mdat.
func (h hfile) bytes() []byte {
	build := func(mdatOff uint32) ([]byte, []byte) {
		var infe, iloc, ipma, idat, mdat []byte
		for _, it := range h.items {
			var fl uint32
			if it.hidden {
				fl = 1
			}
			iv := h.v.infeV()
			if iv == 3 {
				infe = append(infe, fbox("infe", 3, fl, u32(it.id), u16(0), []byte(it.typ), []byte{0})...)
			} else {
				infe = append(infe, fbox("infe", 2, fl, u16(uint16(it.id), 0), []byte(it.typ), []byte{0})...)
			}
			var off uint32
			var meth uint16
			if it.idat {
				off, meth = uint32(len(idat)), 1
				idat = append(idat, it.data...)
			} else {
				off = mdatOff + 8 + uint32(len(mdat))
				mdat = append(mdat, it.data...)
			}
			lv := h.v.ilocV()
			head := wide(lv>>1, it.id) // version 2 has 32-bit identifiers
			if lv >= 1 {
				head = append(head, u16(meth)...)
			}
			head = append(head, u16(0)...) // data reference
			var count int
			var exts []byte
			switch {
			case it.dup > 0: // the whole range, it.dup times over: overlapping extents
				count = it.dup
				for i := 0; i < it.dup; i++ {
					exts = append(exts, u32(off, uint32(len(it.data)))...)
				}
			case it.split > 0 && it.split < len(it.data): // two extents
				count = 2
				exts = u32(off, uint32(it.split), off+uint32(it.split), uint32(len(it.data)-it.split))
			default:
				count = 1
				exts = u32(off, uint32(len(it.data)))
			}
			iloc = append(append(append(iloc, head...), u16(uint16(count))...), exts...)
			var as []byte
			as = append(as, uint8(len(it.props)))
			for _, p := range it.props {
				switch {
				case h.v.ipmaFlags&1 != 0 && p < 0:
					as = append(as, u16(0x8000|uint16(-p))...)
				case h.v.ipmaFlags&1 != 0:
					as = append(as, u16(uint16(p))...)
				case p < 0:
					as = append(as, 0x80|uint8(-p))
				default:
					as = append(as, uint8(p))
				}
			}
			ipma = append(ipma, wide(h.v.ipma, it.id)...)
			ipma = append(ipma, as...)
		}
		var refs []byte
		for _, r := range h.refs {
			body := append(wide(h.v.iref, r.from), u16(uint16(len(r.to)))...)
			for _, to := range r.to {
				body = append(body, wide(h.v.iref, to)...)
			}
			refs = append(refs, mkbox(r.typ, body)...)
		}
		iinfCount := u16(uint16(len(h.items)))
		if h.v.iinf != 0 {
			iinfCount = u32(uint32(len(h.items)))
		}
		ilocSizes := u16(0x4400)
		ilocCount := u16(uint16(len(h.items)))
		if h.v.ilocV() == 2 {
			ilocCount = u32(uint32(len(h.items)))
		}
		meta := fbox("meta", 0, 0,
			fbox("hdlr", 0, 0, u32(0), []byte("pict"), make([]byte, 13)),
			fbox("pitm", h.v.pitm, 0, wide(h.v.pitm, h.primary)),
			fbox("iinf", h.v.iinf, 0, iinfCount, infe),
			fbox("iloc", h.v.ilocV(), 0, ilocSizes, ilocCount, iloc),
			fbox("iref", h.v.iref, 0, refs),
			mkbox("iprp", mkbox("ipco", bytes.Join(h.props, nil)), fbox("ipma", h.v.ipma, h.v.ipmaFlags, u32(uint32(len(h.items))), ipma)),
			mkbox("idat", idat),
		)
		return meta, mkbox("mdat", mdat)
	}
	brands := h.brands
	if brands == nil {
		brands = []string{"heic", "mif1"}
	}
	ftyp := mkbox("ftyp", []byte(brands[0]), u32(0), []byte(joinStr(brands[1:])))
	meta, _ := build(0)
	meta, mdat := build(uint32(len(ftyp) + len(meta)))
	return bytes.Join([][]byte{ftyp, meta, mdat}, nil)
}

func joinStr(s []string) string {
	var o string
	for _, x := range s {
		o += x
	}
	return o
}

func toU16(v []uint32) []uint16 {
	o := make([]uint16, len(v))
	for i, x := range v {
		o[i] = uint16(x)
	}
	return o
}

// single builds a one-image file from a coded picture: item 1 is the hvc1 image.
func single(c coded, w, h uint32, extra ...[]byte) hfile {
	props := append([][]byte{ispe(w, h), c.hvcC}, extra...)
	idx := make([]int, len(props))
	for i := range idx {
		idx[i] = i + 1
		if t := string(props[i][4:8]); t == "irot" || t == "imir" || t == "clap" {
			idx[i] = -idx[i] // these are essential (ISO/IEC 23008-12 6.5)
		}
	}
	return hfile{primary: 1, items: []hitem{{id: 1, typ: "hvc1", data: c.data, props: idx}}, props: props}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }

// guard turns a panic into a failed assertion, so a test that survives a bad
// mutation only by crashing is still a red test with a message, not luck.
func guard(t testing.TB) {
	if r := recover(); r != nil {
		t.Errorf("panic: %v", r)
	}
}
