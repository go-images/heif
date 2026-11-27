// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"fmt"
)

// extent is one piece of an item's data (iloc).
type extent struct{ off, length uint64 }

// location is the iloc entry of an item.
type location struct {
	method  uint8
	dataRef uint16
	base    uint64
	extents []extent
}

// assoc is one property association of ipma: the 1-based index into ipco (0 = none).
type assoc struct {
	index     int
	essential bool
}

// item is one entry of the item information box with what the other boxes say of it.
type item struct {
	id        uint32
	typ       string
	hidden    bool
	described bool // an infe entry named it
	loc       *location
	assocs    []assoc
	// associated says an ipma entry named the item
	associated bool
}

// maxAssocs is the most property associations one item may have: real files state a
// dozen, and each costs sixteen bytes of memory per byte of file.
const maxAssocs = 64

// ref is one reference of iref: from -> to..., of a type (dimg, thmb, auxl, cdsc).
type ref struct {
	typ  string
	from uint32
	to   []uint32
}

// file is a parsed HEIF file: its items, properties and references. Item data stays in
// data (and idat), it is not copied.
type file struct {
	lim         Limits
	data        []byte
	brands      []string
	primary     uint32
	havePrimary bool
	items       []*item
	byID        map[uint32]*item
	props       []property
	idat        []byte
	refs        []ref
	extents     int    // extent records read from iloc, all items together
	targets     int    // reference targets read from iref, all references together
	spent       uint64 // bytes of item data copied to assemble items (see itemData)
	cfg         bool   // only meta was read (DecodeConfig): item data is not available
}

// stillBrands are the brands (ftyp) of a still-image file this reader accepts: the HEVC
// image brands of ISO/IEC 23008-12 and the generic image-collection ones.
var stillBrands = map[string]bool{
	"heic": true, "heix": true, "hevc": true, "hevx": true,
	"heim": true, "heis": true, "mif1": true, "msf1": true,
}

// parseFile reads the ftyp and meta boxes of data.
func parseFile(data []byte, lim Limits) (*file, error) {
	p := &parser{lim: lim}
	top, err := p.list(data, 0)
	if err != nil {
		return nil, err
	}
	f := &file{lim: lim, data: data, byID: map[uint32]*item{}}
	var meta []byte
	haveMeta := false
	for _, b := range top {
		switch {
		case b.typ == "ftyp" && f.brands == nil:
			if err := f.parseFtyp(b.body); err != nil {
				return nil, err
			}
		case b.typ == "meta" && !haveMeta:
			meta, haveMeta = b.body, true
		}
	}
	if f.brands == nil {
		return nil, fmt.Errorf("%w: no ftyp box", ErrNotHEIF)
	}
	ok := false
	for _, b := range f.brands {
		ok = ok || stillBrands[b]
	}
	if !ok {
		return nil, fmt.Errorf("%w: brands %q", ErrNotHEIF, f.brands)
	}
	if !haveMeta {
		return nil, fmt.Errorf("%w: no meta box", ErrInvalid)
	}
	if err := f.parseMeta(p, meta); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *file) parseFtyp(b []byte) error {
	r := rd{b: b}
	f.brands = append(f.brands, r.fourcc())
	r.u32() // minor_version
	for len(r.b) >= 4 {
		f.brands = append(f.brands, r.fourcc())
	}
	return r.err("ftyp")
}

func (f *file) parseMeta(p *parser, b []byte) error {
	r := rd{b: b}
	r.full()
	if err := r.err("meta"); err != nil {
		return err
	}
	kids, err := p.list(r.rest(), 1)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, k := range kids {
		if seen[k.typ] {
			return fmt.Errorf("%w: two %q boxes in meta", ErrInvalid, k.typ)
		}
		seen[k.typ] = true
	}
	for _, k := range kids {
		switch k.typ {
		case "hdlr":
			err = f.parseHdlr(k.body)
		case "pitm":
			err = f.parsePitm(k.body)
		case "iinf":
			err = f.parseIinf(p, k.body)
		case "iloc":
			err = f.parseIloc(k.body)
		case "iref":
			err = f.parseIref(p, k.body)
		case "iprp":
			err = f.parseIprp(p, k.body)
		case "idat":
			f.idat = k.body
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *file) parseHdlr(b []byte) error {
	r := rd{b: b}
	r.full()
	r.u32() // pre_defined
	h := r.fourcc()
	if err := r.err("hdlr"); err != nil {
		return err
	}
	if h != "pict" {
		return fmt.Errorf("%w: meta handler %q is not pict", ErrUnsupported, h)
	}
	return nil
}

func (f *file) parsePitm(b []byte) error {
	r := rd{b: b}
	v, _ := r.full()
	f.primary = r.idv(v != 0)
	f.havePrimary = true
	return r.err("pitm")
}

func (f *file) parseIinf(p *parser, b []byte) error {
	r := rd{b: b}
	v, _ := r.full()
	r.idv(v != 0) // entry_count: the boxes themselves are counted
	if err := r.err("iinf"); err != nil {
		return err
	}
	kids, err := p.list(r.rest(), 2)
	if err != nil {
		return err
	}
	for _, k := range kids {
		if k.typ != "infe" {
			continue
		}
		info, err := parseInfe(k.body)
		if err != nil {
			return err
		}
		it, err := f.itemFor(info.id)
		if err != nil {
			return err
		}
		if it.described {
			return fmt.Errorf("%w: item %d is described twice", ErrInvalid, info.id)
		}
		it.described, it.typ, it.hidden = true, info.typ, info.hidden
	}
	return nil
}

// itemFor returns the item of an identifier, creating it (within MaxItems) when no box
// has named it yet: iloc and ipma may be read before iinf, or name an item nobody
// describes.
func (f *file) itemFor(id uint32) (*item, error) {
	if it := f.byID[id]; it != nil {
		return it, nil
	}
	if len(f.items) >= f.lim.MaxItems {
		return nil, ErrTooManyItems
	}
	it := &item{id: id}
	f.byID[id] = it
	f.items = append(f.items, it)
	return it, nil
}

// parseInfe reads an item information entry (ISO/IEC 14496-12 8.11.6): versions 2 and 3
// carry the four-character item type; the older versions carry only a MIME type, which a
// still-image reader has no use for, so their item type stays empty.
func parseInfe(b []byte) (*item, error) {
	r := rd{b: b}
	v, flags := r.full()
	it := &item{hidden: flags&1 != 0}
	it.id = r.idv(v >= 3)
	r.u16() // item_protection_index
	if v >= 2 {
		it.typ = r.fourcc()
	}
	return it, r.err("infe")
}

func (f *file) parseIloc(b []byte) error {
	r := rd{b: b}
	v, _ := r.full()
	if v > 2 {
		return fmt.Errorf("%w: iloc version %d", ErrUnsupported, v)
	}
	sizes := r.u16()
	offSize, lenSize, baseSize, idxSize := int(sizes>>12), int(sizes>>8&15), int(sizes>>4&15), int(sizes&15)
	if v == 0 {
		idxSize = 0
	}
	for _, s := range [...]int{offSize, lenSize, baseSize, idxSize} {
		if s != 0 && s != 4 && s != 8 {
			return fmt.Errorf("%w: iloc field size %d", ErrInvalid, s)
		}
	}
	var n uint32
	if v < 2 {
		n = uint32(r.u16())
	} else {
		n = r.u32()
	}
	if err := r.err("iloc"); err != nil {
		return err
	}
	if uint64(n) > uint64(f.lim.MaxItems) {
		return ErrTooManyItems
	}
	perExtent := offSize + lenSize + idxSize
	for i := uint32(0); i < n; i++ {
		l := &location{}
		id := r.idv(v >= 2)
		if v >= 1 {
			l.method = uint8(r.u16() & 15)
		}
		l.dataRef = r.u16()
		l.base = r.uvar(baseSize)
		count := int(r.u16())
		if perExtent == 0 && count > 1 {
			return fmt.Errorf("%w: item %d has %d extents of no size", ErrInvalid, id, count)
		}
		// an extent record is a few bytes of file and sixteen of memory: bound them
		// before they are kept
		if f.extents += count; count > f.lim.MaxItems || f.extents > 4*f.lim.MaxItems {
			return fmt.Errorf("%w: %d extents", ErrTooManyItems, f.extents)
		}
		for e := 0; e < count && !r.bad; e++ {
			r.uvar(idxSize) // extent_index: only construction method 2 uses it
			off := r.uvar(offSize)
			length := r.uvar(lenSize)
			l.extents = append(l.extents, extent{off, length})
		}
		if err := r.err("iloc entry"); err != nil {
			return err
		}
		it, err := f.itemFor(id)
		if err != nil {
			return err
		}
		if it.loc != nil {
			return fmt.Errorf("%w: item %d has two locations", ErrInvalid, id)
		}
		it.loc = l
	}
	return nil
}

func (f *file) parseIref(p *parser, b []byte) error {
	r := rd{b: b}
	v, _ := r.full()
	if err := r.err("iref"); err != nil {
		return err
	}
	kids, err := p.list(r.rest(), 2)
	if err != nil {
		return err
	}
	for _, k := range kids {
		if len(f.refs) >= f.lim.MaxItems {
			return ErrTooManyItems
		}
		kr := rd{b: k.body}
		x := ref{typ: k.typ, from: kr.idv(v != 0)}
		count := int(kr.u16())
		if f.targets += count; f.targets > 4*f.lim.MaxItems {
			return fmt.Errorf("%w: %d reference targets", ErrTooManyItems, f.targets)
		}
		for i := 0; i < count && !kr.bad; i++ {
			x.to = append(x.to, kr.idv(v != 0))
		}
		if err := kr.err("iref " + k.typ); err != nil {
			return err
		}
		f.refs = append(f.refs, x)
	}
	return nil
}

func (f *file) parseIprp(p *parser, b []byte) error {
	kids, err := p.list(b, 2)
	if err != nil {
		return err
	}
	for _, k := range kids {
		switch k.typ {
		case "ipco":
			if err := f.parseIpco(p, k.body); err != nil {
				return err
			}
		case "ipma":
			if err := f.parseIpma(k.body); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *file) parseIpco(p *parser, b []byte) error {
	kids, err := p.list(b, 3)
	if err != nil {
		return err
	}
	for _, k := range kids {
		pr, err := parseProperty(k)
		if err != nil {
			return err
		}
		f.props = append(f.props, pr)
	}
	return nil
}

func (f *file) parseIpma(b []byte) error {
	r := rd{b: b}
	v, flags := r.full()
	n := r.u32()
	if err := r.err("ipma"); err != nil {
		return err
	}
	if uint64(n) > uint64(f.lim.MaxItems) {
		return ErrTooManyItems
	}
	for i := uint32(0); i < n; i++ {
		id := r.idv(v != 0)
		count := int(r.u8())
		if count > maxAssocs {
			return fmt.Errorf("%w: item %d has %d property associations", ErrInvalid, id, count)
		}
		var as []assoc
		for k := 0; k < count && !r.bad; k++ {
			if flags&1 != 0 {
				x := r.u16()
				as = append(as, assoc{index: int(x & 0x7FFF), essential: x&0x8000 != 0})
			} else {
				x := r.u8()
				as = append(as, assoc{index: int(x & 0x7F), essential: x&0x80 != 0})
			}
		}
		if err := r.err("ipma entry"); err != nil {
			return err
		}
		it, err := f.itemFor(id)
		if err != nil {
			return err
		}
		// an item has one entry of associations (ISO/IEC 23008-12 9.3: not verified
		// against the text of the standard, which was not at hand): a second one would
		// make every later look-up of its properties cost the sum of them
		if it.associated {
			return fmt.Errorf("%w: item %d has two ipma entries", ErrInvalid, id)
		}
		it.associated = true
		it.assocs = as
	}
	return nil
}
