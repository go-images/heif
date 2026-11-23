// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/go-avkit/h265"
)

// xform is one transformative property, applied in the order ipma lists them.
type xform struct {
	kind byte // 'r' rotation, 'm' mirror, 'c' clean aperture
	n    uint8
	clap [8]uint32
}

// grid is a grid derived image (ISO/IEC 23008-12 6.6.2.3.2): rows x cols tiles of the
// same size, assembled and cut to w x h.
type grid struct {
	rows, cols int
	w, h       uint32
	tiles      []*item
	tw, th     uint32 // the tile size, from the ispe of the first tile
}

// plan is what the primary item resolves to: a coded base image (an hvc1 item or a grid)
// and the transformations to apply to it, innermost first (an iden item applies its own
// after those of the item it points at).
type plan struct {
	base   *item
	props  []*property
	grid   *grid
	w, h   uint32  // the base image size
	xforms []xform // every transformation, in order; place holds what they come to
	place  placement
	nclx   *nclx
	icc    []byte
	alpha  bool
}

// resolve follows an item to the image it is made of.
func (f *file) resolve(id uint32, depth int) (*plan, error) {
	if depth > f.lim.MaxDepth {
		return nil, ErrNestingTooDeep
	}
	it := f.byID[id]
	if it == nil {
		return nil, fmt.Errorf("%w: item %d does not exist", ErrInvalid, id)
	}
	ps, err := f.propsOf(it)
	if err != nil {
		return nil, err
	}
	var pl *plan
	switch it.typ {
	case "hvc1":
		sz := find(ps, "ispe")
		if sz == nil || sz.w == 0 || sz.h == 0 {
			return nil, fmt.Errorf("%w: item %d has no image size", ErrInvalid, id)
		}
		pl = &plan{base: it, props: ps, w: sz.w, h: sz.h}
	case "grid":
		g, err := f.parseGrid(it, ps)
		if err != nil {
			return nil, err
		}
		pl = &plan{base: it, props: ps, grid: g, w: g.w, h: g.h}
	case "iden":
		to := f.refsOf("dimg", id)
		if len(to) != 1 {
			return nil, fmt.Errorf("%w: identity item %d derives from %d items", ErrInvalid, id, len(to))
		}
		if pl, err = f.resolve(to[0], depth+1); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: item type %q", ErrUnsupported, it.typ)
	}
	moves := 0
	for _, p := range ps {
		switch p.typ {
		case "irot", "imir", "clap":
			if moves++; moves > maxTransforms {
				return nil, fmt.Errorf("%w: item %d has more than %d transformative properties", ErrInvalid, id, maxTransforms)
			}
		}
		switch p.typ {
		case "irot":
			if p.turns != 0 { // a rotation by zero is no transformation
				pl.xforms = append(pl.xforms, xform{kind: 'r', n: p.turns})
			}
		case "imir":
			pl.xforms = append(pl.xforms, xform{kind: 'm', n: p.axis})
		case "clap":
			pl.xforms = append(pl.xforms, xform{kind: 'c', clap: p.clap})
		case "colr":
			if p.isNCLX {
				c := p.nclx
				pl.nclx = &c
			} else if p.icc != nil {
				pl.icc = p.icc
			}
		}
	}
	if depth == 0 {
		pl.alpha = f.hasAlpha(id)
		if uint64(pl.w)*uint64(pl.h) > uint64(f.lim.MaxPixels) {
			return nil, fmt.Errorf("%w: %dx%d", ErrImageTooLarge, pl.w, pl.h)
		}
		if pl.place, err = compose(pl.xforms, int(pl.w), int(pl.h)); err != nil {
			return nil, err
		}
	}
	return pl, nil
}

// refsOf returns the targets of the first reference of a type from an item.
func (f *file) refsOf(typ string, from uint32) []uint32 {
	for _, r := range f.refs {
		if r.typ == typ && r.from == from {
			return r.to
		}
	}
	return nil
}

// hasAlpha reports whether an alpha plane is attached to the item (an auxl reference
// from an auxiliary item whose type is an alpha URN). This reader does not decode it.
//
// Each auxiliary item is looked at once, however many references name it, and the targets
// of a reference are searched only when the item is an alpha plane.
func (f *file) hasAlpha(id uint32) bool {
	alpha := map[uint32]bool{}
	for _, r := range f.refs {
		if r.typ != "auxl" {
			continue
		}
		isAlpha, seen := alpha[r.from]
		if !seen {
			isAlpha = f.isAlphaItem(r.from)
			alpha[r.from] = isAlpha
		}
		if isAlpha && slices.Contains(r.to, id) {
			return true
		}
	}
	return false
}

// isAlphaItem reports whether an item carries an auxC property naming an alpha plane.
func (f *file) isAlphaItem(id uint32) bool {
	aux := f.byID[id]
	if aux == nil {
		return false
	}
	ps, _ := f.propsOf(aux) // a list that cannot be read names no alpha plane
	a := find(ps, "auxC")
	return a != nil && isAlphaURN(a.auxType)
}

func isAlphaURN(s string) bool {
	return s == "urn:mpeg:hevc:2015:auxid:1" || s == "urn:mpeg:mpegB:cicp:systems:auxiliary:alpha"
}

func (f *file) parseGrid(it *item, ps []*property) (*grid, error) {
	if f.cfg {
		// only meta was read: the descriptor may sit in mdat, so take the size from ispe
		sz := find(ps, "ispe")
		to := f.refsOf("dimg", it.id)
		if sz == nil || len(to) == 0 || sz.w == 0 || sz.h == 0 {
			return nil, fmt.Errorf("%w: grid %d without a size or tiles", ErrInvalid, it.id)
		}
		// (the pixel bound is applied to the size of every item by resolve)
		t := f.byID[to[0]]
		if t == nil || t.typ != "hvc1" {
			return nil, fmt.Errorf("%w: grid %d tile %d is not an hvc1 item", ErrUnsupported, it.id, to[0])
		}
		return &grid{w: sz.w, h: sz.h, tiles: []*item{t}}, nil
	}
	data, err := f.itemData(it)
	if err != nil {
		return nil, err
	}
	r := rd{b: data}
	version := r.u8()
	flags := r.u8()
	g := &grid{rows: int(r.u8()) + 1, cols: int(r.u8()) + 1}
	if flags&1 != 0 {
		g.w, g.h = r.u32(), r.u32()
	} else {
		g.w, g.h = uint32(r.u16()), uint32(r.u16())
	}
	if err := r.err("grid"); err != nil {
		return nil, err
	}
	if version != 0 {
		return nil, fmt.Errorf("%w: grid version %d", ErrUnsupported, version)
	}
	if g.rows*g.cols > f.lim.MaxTiles {
		return nil, fmt.Errorf("%w: %dx%d", ErrTooManyTiles, g.rows, g.cols)
	}
	to := f.refsOf("dimg", it.id)
	if len(to) != g.rows*g.cols {
		return nil, fmt.Errorf("%w: grid %d of %dx%d tiles has %d tile references", ErrInvalid, it.id, g.rows, g.cols, len(to))
	}
	if g.w == 0 || g.h == 0 || uint64(g.w)*uint64(g.h) > uint64(f.lim.MaxPixels) {
		return nil, fmt.Errorf("%w: grid of %dx%d", ErrImageTooLarge, g.w, g.h)
	}
	if sz := find(ps, "ispe"); sz != nil && (sz.w != g.w || sz.h != g.h) {
		return nil, fmt.Errorf("%w: grid %d is %dx%d but its ispe states %dx%d", ErrInvalid, it.id, g.w, g.h, sz.w, sz.h)
	}
	for _, id := range to {
		t := f.byID[id]
		if t == nil || t.typ != "hvc1" {
			return nil, fmt.Errorf("%w: grid %d tile %d is not an hvc1 item", ErrUnsupported, it.id, id)
		}
		g.tiles = append(g.tiles, t)
	}
	tps, err := f.propsOf(g.tiles[0])
	if err != nil {
		return nil, err
	}
	sz := find(tps, "ispe")
	if sz == nil || sz.w == 0 || sz.h == 0 || uint64(sz.w)*uint64(sz.h) > uint64(f.lim.MaxPixels) {
		return nil, fmt.Errorf("%w: grid tile size", ErrInvalid)
	}
	g.tw, g.th = sz.w, sz.h
	// the tiles cover the output, and no row or column of them lies outside it
	if uint64(g.cols)*uint64(g.tw) < uint64(g.w) || uint64(g.cols-1)*uint64(g.tw) >= uint64(g.w) ||
		uint64(g.rows)*uint64(g.th) < uint64(g.h) || uint64(g.rows-1)*uint64(g.th) >= uint64(g.h) {
		return nil, fmt.Errorf("%w: %dx%d tiles of %dx%d do not make a %dx%d image", ErrInvalid, g.cols, g.rows, g.tw, g.th, g.w, g.h)
	}
	// every tile is decoded in full, and extents may overlap, so the decoding work is
	// bounded by the pixels of all the tiles: a grid of ordinary tiles holds less than
	// four times its own pixels
	// (divided, so that nothing overflows)
	if uint64(g.tw)*uint64(g.th)/4 > uint64(f.lim.MaxPixels)/uint64(g.rows*g.cols) {
		return nil, fmt.Errorf("%w: %d tiles of %dx%d", ErrImageTooLarge, g.rows*g.cols, g.tw, g.th)
	}
	return g, nil
}

// hvccOf returns the decoder configuration property of an hvc1 item.
func (f *file) hvccOf(it *item) (*property, error) {
	ps, err := f.propsOf(it)
	if err != nil {
		return nil, err
	}
	hp := find(ps, "hvcC")
	if hp == nil {
		return nil, fmt.Errorf("%w: item %d has no hvcC", ErrInvalid, it.id)
	}
	return hp, nil
}

// newDecoder makes an HEVC decoder under the limits of the file. Pictures given back
// with Release are reused, so the tiles of a grid share the planes of one another.
func (f *file) newDecoder() *h265.Decoder {
	return h265.NewDecoder(h265.Options{
		Limits:          h265.Limits{MaxPixels: f.lim.MaxPixels, MaxDPBBytes: f.lim.MaxDecoderBytes},
		RecyclePictures: true,
	})
}

// maxNALUnits is the most NAL units one item may hold: a picture of a still image has a
// few slices, and the largest level of HEVC allows hundreds.
const maxNALUnits = 1 << 12

// scanUnits walks the length-prefixed NAL units of item data without keeping them, and
// refuses more than max units or more than one picture: a still image is one access unit,
// and a stream of tiny units (or of pictures) must not cost memory or decoding time in
// proportion to a file that states them by the million. A picture starts at a slice
// segment whose first_slice_segment_in_pic_flag is set.
func scanUnits(data []byte, lengthSize, max int) error {
	n, pictures := 0, 0
	for len(data) > 0 {
		if len(data) < lengthSize {
			return fmt.Errorf("%w: a unit length is cut short", ErrCorrupt)
		}
		var l uint64
		for _, b := range data[:lengthSize] {
			l = l<<8 | uint64(b)
		}
		data = data[lengthSize:]
		if l > uint64(len(data)) {
			return fmt.Errorf("%w: a unit of %d bytes runs past the item", ErrCorrupt, l)
		}
		if n++; n > max {
			return fmt.Errorf("%w: more than %d NAL units in an item", ErrTooManyItems, max)
		}
		u := data[:l]
		data = data[l:]
		if len(u) >= 3 && u[0]>>1&0x3F < 32 && u[2]&0x80 != 0 {
			if pictures++; pictures > 1 {
				return fmt.Errorf("%w: an item holds more than one picture", ErrInvalid)
			}
		}
	}
	return nil
}

// decodeHEVC decodes an hvc1 item to its picture with dec, which carries no state the
// item needs: every item sends its parameter sets again, and a still picture is an IDR.
func (f *file) decodeHEVC(dec *h265.Decoder, it *item, hp *property, w, h uint32) (*h265.DecodedPicture, error) {
	data, err := f.itemData(it)
	if err != nil {
		return nil, err
	}
	if err := scanUnits(data, hp.hvcc.lengthSize, maxNALUnits); err != nil {
		return nil, fmt.Errorf("item %d: %w", it.id, err)
	}
	var sets []byte
	for _, n := range hp.hvcc.nals {
		if len(n) > 0 && n[0]>>1&0x3F < 32 { // parameter sets and SEI, not pictures
			return nil, fmt.Errorf("%w: item %d: hvcC holds a coded slice", ErrInvalid, it.id)
		}
		sets = binary.BigEndian.AppendUint32(sets, uint32(len(n)))
		sets = append(sets, n...)
	}
	units, err := h265.SplitLengthPrefixed(sets, 4)
	if err != nil {
		return nil, fmt.Errorf("%w: hvcC: %w", ErrCorrupt, err)
	}
	more, _ := h265.SplitLengthPrefixed(data, hp.hvcc.lengthSize) // scanUnits has walked this framing
	units = append(units, more...)                                // the parameter sets, then the item
	var pic *h265.DecodedPicture
	for i := 0; i <= len(units); i++ {
		var out []h265.Output
		if i < len(units) {
			out, err = dec.Decode(units[i])
		} else {
			out, err = dec.Flush() // the end of the stream
		}
		if pic, err = firstPicture(pic, out, err); err != nil {
			return nil, fmt.Errorf("item %d: %w", it.id, err)
		}
	}
	if pic == nil {
		return nil, fmt.Errorf("%w: item %d holds no picture", ErrCorrupt, it.id)
	}
	if pw, ph := pic.Planes[0].Width, pic.Planes[0].Height; int64(pw) != int64(w) || int64(ph) != int64(h) {
		return nil, fmt.Errorf("%w: item %d is %dx%d but states %dx%d", ErrInvalid, it.id, pw, ph, w, h)
	}
	return pic, nil
}

// firstPicture keeps the first picture the decoder outputs, and refuses what it reports as
// an error or as a corrupt picture, or a picture whose planes are not what they state.
func firstPicture(pic *h265.DecodedPicture, out []h265.Output, err error) (*h265.DecodedPicture, error) {
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	for _, o := range out {
		if o.Corrupt {
			return nil, fmt.Errorf("%w: a picture is corrupt", ErrCorrupt)
		}
		if pic == nil {
			if err := checkPlanes(o.Picture); err != nil {
				return nil, err
			}
			pic = o.Picture
		}
	}
	return pic, nil
}

// checkPlanes guards what the colour conversion relies on: one plane or three, and 8-bit
// or 16-bit samples present in every plane of the picture.
func checkPlanes(pic *h265.DecodedPicture) error {
	if pic.Components != 1 && pic.Components != 3 {
		return fmt.Errorf("%w: %d colour components", ErrUnsupported, pic.Components)
	}
	for c := 0; c < pic.Components; c++ {
		p := &pic.Planes[c]
		if p.Width <= 0 || p.Height <= 0 || p.Stride < p.Width || len(p.Pix8)+len(p.Pix16) < (p.Height-1)*p.Stride+p.Width {
			return fmt.Errorf("%w: plane %d is not what its size states", ErrCorrupt, c)
		}
	}
	return nil
}

// decodeBase decodes the coded base image of a plan.
func (f *file) decodeBase(pl *plan) (*h265.DecodedPicture, error) {
	if pl.grid == nil {
		hp, err := f.hvccOf(pl.base)
		if err != nil {
			return nil, err
		}
		return f.decodeHEVC(f.newDecoder(), pl.base, hp, pl.w, pl.h)
	}
	g := pl.grid
	var out *h265.DecodedPicture
	var first h265.DecodedPicture // the format of the first tile, kept after its planes are released
	decoders := map[*property]*h265.Decoder{}
	for i, t := range g.tiles {
		// one decoder for the tiles that share their hvcC (a grid's tiles all do), so that
		// the planes of a tile are reused by the next
		hp, err := f.hvccOf(t)
		if err != nil {
			return nil, err
		}
		dec := decoders[hp]
		if dec == nil {
			dec = f.newDecoder()
			decoders[hp] = dec
		}
		pic, err := f.decodeHEVC(dec, t, hp, g.tw, g.th)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			first = *pic
			if out, err = newGridPicture(pic, int(g.w), int(g.h)); err != nil {
				return nil, err
			}
		} else if !sameFormat(&first, pic) {
			return nil, fmt.Errorf("%w: grid tile %d differs in format from the first", ErrInvalid, t.id)
		}
		copyTile(out, pic, (i%g.cols)*int(g.tw), (i/g.cols)*int(g.th))
		pic.Release()
	}
	return out, nil
}

func sameFormat(a, b *h265.DecodedPicture) bool {
	if a.Components != b.Components || a.BitDepth != b.BitDepth {
		return false
	}
	for c := 0; c < a.Components; c++ {
		if a.Planes[c].Width != b.Planes[c].Width || a.Planes[c].Height != b.Planes[c].Height || a.Planes[c].Is8() != b.Planes[c].Is8() {
			return false
		}
	}
	return true
}

// shifts returns the chroma subsampling of a picture as shifts (0 or 1), or false when
// its chroma planes are not a half or the whole of the luma plane.
func shifts(p *h265.DecodedPicture) (hs, vs uint, ok bool) {
	if p.Components == 1 {
		return 0, 0, true
	}
	l, c := p.Planes[0], p.Planes[1]
	if p.Planes[2].Width != c.Width || p.Planes[2].Height != c.Height {
		return 0, 0, false
	}
	switch c.Width {
	case l.Width:
	case (l.Width + 1) / 2:
		hs = 1
	default:
		return 0, 0, false
	}
	switch c.Height {
	case l.Height:
	case (l.Height + 1) / 2:
		vs = 1
	default:
		return 0, 0, false
	}
	return hs, vs, true
}

// newGridPicture allocates the picture a grid is assembled into, in the format of its
// first tile.
func newGridPicture(tile *h265.DecodedPicture, w, h int) (*h265.DecodedPicture, error) {
	hs, vs, ok := shifts(tile)
	if !ok {
		return nil, fmt.Errorf("%w: chroma planes of an unusual size", ErrUnsupported)
	}
	out := &h265.DecodedPicture{Components: tile.Components, BitDepth: tile.BitDepth}
	for c := 0; c < tile.Components; c++ {
		pw, ph := w, h
		if c > 0 {
			pw, ph = (w+int(hs))>>hs, (h+int(vs))>>vs
		}
		if !fits(uint64(pw) * uint64(ph)) {
			return nil, ErrImageTooLarge
		}
		p := h265.Plane{Width: pw, Height: ph, Stride: pw}
		if tile.Planes[c].Is8() {
			p.Pix8 = make([]uint8, pw*ph)
		} else {
			p.Pix16 = make([]uint16, pw*ph)
		}
		out.Planes[c] = p
	}
	return out, nil
}

// copyTile copies a tile into the grid picture at luma position (x0, y0), cut at the
// picture's edge. A tile whose offset would fall inside a chroma sample (an odd tile size
// of a subsampled picture) is placed at the sample below it, which is all a decoder can
// do with it.
func copyTile(dst, tile *h265.DecodedPicture, x0, y0 int) {
	hs, vs, _ := shifts(tile)
	for c := 0; c < tile.Components; c++ {
		sx, sy := x0, y0
		if c > 0 {
			sx, sy = x0>>hs, y0>>vs
		}
		sp, dp := &tile.Planes[c], &dst.Planes[c]
		w := min(sp.Width, dp.Width-sx)
		h := min(sp.Height, dp.Height-sy)
		for y := 0; y < h; y++ {
			if sp.Is8() {
				copy(dp.Pix8[(sy+y)*dp.Stride+sx:][:w], sp.Pix8[y*sp.Stride:][:w])
			} else {
				copy(dp.Pix16[(sy+y)*dp.Stride+sx:][:w], sp.Pix16[y*sp.Stride:][:w])
			}
		}
	}
}
