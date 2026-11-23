// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"fmt"
	"strings"
)

// property is one box of ipco. Only the fields of its type are set.
type property struct {
	typ string

	// ispe: the image width and height before the transformative properties.
	w, h uint32

	// irot: the rotation in quarter turns, anti-clockwise. imir: the axis (0 swaps
	// top and bottom, 1 left and right, as libheif reads it; see mirror).
	turns, axis uint8

	// clap: width, height, horizontal and vertical offset of the clean aperture, each
	// a fraction (num/den).
	clap [8]uint32

	// colr: kind "nclx" (colour primaries, transfer, matrix, full range) or an ICC
	// profile ("prof" or "rICC").
	isNCLX bool
	nclx   nclx
	icc    []byte

	// hvcC.
	hvcc hvcC

	// auxC: the auxiliary type URN.
	auxType string

	// pixi: bits per channel.
	bits []uint8
}

// nclx is a colour description by code points (ITU-T H.273).
type nclx struct {
	Primaries, Transfer, Matrix uint16
	FullRange                   bool
}

// hvcC is the HEVC decoder configuration record (ISO/IEC 14496-15 8.3.3.1).
type hvcC struct {
	chroma     int // chroma_format_idc: 0 monochrome, 1 4:2:0, 2 4:2:2, 3 4:4:4
	lumaBits   int // bit depths of the coded pictures
	chromaBits int
	lengthSize int      // bytes of a NAL unit's length in the item data (1 to 4)
	nals       [][]byte // the parameter-set NAL units of the arrays, headers included
}

// knownProps are the property types this reader understands or can ignore. An essential
// association with any other type makes the item unreadable (ISO/IEC 23008-12 6.5.1).
var knownProps = map[string]bool{
	"ispe": true, "hvcC": true, "irot": true, "imir": true, "clap": true, "colr": true,
	"pixi": true, "auxC": true, "pasp": true, "clli": true, "mdcv": true, "cclv": true,
	"rloc": true, "lsel": true, "altt": true, "cmin": true, "cmex": true, "udes": true,
}

func parseProperty(b box) (property, error) {
	p := property{typ: b.typ}
	r := rd{b: b.body}
	switch b.typ {
	case "ispe":
		r.full()
		p.w, p.h = r.u32(), r.u32()
	case "irot":
		p.turns = r.u8() & 3
	case "imir":
		p.axis = r.u8() & 1
	case "clap":
		for i := range p.clap {
			p.clap[i] = r.u32()
		}
	case "colr":
		switch kind := r.fourcc(); kind {
		case "nclx":
			p.isNCLX = true
			p.nclx = nclx{Primaries: r.u16(), Transfer: r.u16(), Matrix: r.u16()}
			p.nclx.FullRange = r.u8()&0x80 != 0
		case "prof", "rICC":
			p.icc = r.rest()
		}
	case "pixi":
		r.full()
		n := int(r.u8())
		p.bits = append(p.bits, r.take(n)...)
	case "auxC":
		r.full()
		z := r.rest()
		if i := strings.IndexByte(string(z), 0); i >= 0 {
			z = z[:i]
		}
		p.auxType = string(z)
	case "hvcC":
		p.hvcc = parseHvcC(&r)
	}
	return p, r.err(b.typ)
}

// parseHvcC reads an hvcC; a cut box leaves r failed, which the caller reports.
func parseHvcC(r *rd) hvcC {
	var h hvcC
	// version, profile, compatibility, constraints, level, segmentation, parallelism,
	// chroma_format_idc (16), bit depths minus 8 (17, 18), frame rate
	if fixed := r.take(21); fixed != nil {
		h.chroma, h.lumaBits, h.chromaBits = int(fixed[16]&3), int(fixed[17]&7)+8, int(fixed[18]&7)+8
	}
	h.lengthSize = int(r.u8()&3) + 1
	arrays := int(r.u8())
	for i := 0; i < arrays && !r.bad; i++ {
		r.u8() // array_completeness, NAL_unit_type
		n := int(r.u16())
		for k := 0; k < n && !r.bad; k++ {
			l := int(r.u16())
			if nal := r.take(l); nal != nil {
				h.nals = append(h.nals, nal)
			}
		}
	}
	return h
}

// props returns the properties of an item, in the order ipma lists them (which is the
// order the transformative ones apply in). An essential property this reader does not
// know makes the item unsupported.
func (f *file) propsOf(it *item) ([]*property, error) {
	out := make([]*property, 0, len(it.assocs))
	for _, a := range it.assocs {
		if a.index == 0 {
			continue
		}
		if a.index > len(f.props) {
			return nil, fmt.Errorf("%w: item %d names property %d of %d", ErrInvalid, it.id, a.index, len(f.props))
		}
		p := &f.props[a.index-1]
		if a.essential && !knownProps[p.typ] {
			return nil, fmt.Errorf("%w: essential property %q of item %d", ErrUnsupported, p.typ, it.id)
		}
		out = append(out, p)
	}
	return out, nil
}

// find returns the first property of a type.
func find(ps []*property, typ string) *property {
	for _, p := range ps {
		if p.typ == typ {
			return p
		}
	}
	return nil
}
