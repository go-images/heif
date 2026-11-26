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
	// (the work of decoding every tile is bounded by codedWork, from the parameter sets)
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

// hevcConfig is what the hvcC of an hvc1 property means to the decoder, built once per
// property: the parameter sets as units, and the ones a slice header is read against.
// Parameter sets come from the hvcC and from nowhere else.
type hevcConfig struct {
	lengthSize int
	units      []h265.Unit
	sps        map[uint32]h265.SPS
	pps        map[uint32]h265.PPS
}

// newHevcConfig reads the hvcC of a property. A set that does not parse is left out of
// the maps (the decoder reports it, and a slice that names it is refused).
func newHevcConfig(id uint32, hp *property) (*hevcConfig, error) {
	c := &hevcConfig{lengthSize: hp.hvcc.lengthSize, sps: map[uint32]h265.SPS{}, pps: map[uint32]h265.PPS{}}
	var sets []byte
	for _, n := range hp.hvcc.nals {
		if len(n) > 0 && n[0]>>1&0x3F < 32 { // parameter sets and SEI, not pictures
			return nil, fmt.Errorf("%w: item %d: hvcC holds a coded slice", ErrInvalid, id)
		}
		sets = binary.BigEndian.AppendUint32(sets, uint32(len(n)))
		sets = append(sets, n...)
	}
	var err error
	if c.units, err = h265.SplitLengthPrefixed(sets, 4); err != nil {
		return nil, fmt.Errorf("%w: hvcC: %w", ErrCorrupt, err)
	}
	for _, u := range c.units {
		switch u.Type {
		case h265.UnitSPS:
			if s, err := h265.ParseSPS(u); err == nil {
				c.sps[s.ID] = s
			}
		case h265.UnitPPS:
			if p, err := h265.ParsePPS(u); err == nil {
				c.pps[p.ID] = p
			}
		}
	}
	return c, nil
}

// codedPixels is the number of pixels the decoder makes for a picture of this configuration,
// which the SPS states and the ispe of the item only claims: the coded size, before the
// conformance window takes its margin off. Every SPS of the hvcC must give the output size
// the item states (so a 1x1 ispe cannot buy a full-size decode before it is refused), and a
// coded size over max is refused. The largest coded size of the sets is returned, since
// the slice decides which of them applies.
func (c *hevcConfig) codedPixels(id uint32, w, h uint32, max int64) (int64, error) {
	// A set that does not parse costs nothing here: the decoder reads it with the same
	// parser and refuses the stream.
	var most uint64
	for _, s := range c.sps {
		if s.Width != w || s.Height != h {
			return 0, fmt.Errorf("%w: item %d is %dx%d by its parameter set but states %dx%d", ErrInvalid, id, s.Width, s.Height, w, h)
		}
		if px := uint64(s.CodedWidth) * uint64(s.CodedHeight); px > uint64(max) {
			return 0, fmt.Errorf("%w: item %d codes %dx%d pixels", ErrImageTooLarge, id, s.CodedWidth, s.CodedHeight)
		} else if px > most {
			most = px
		}
	}
	return int64(most), nil
}

// codedWork is the pixels decoding the base image of a plan processes, read from the
// parameter sets of its items: every tile of a grid, at the coded size. It refuses an item
// whose parameter set does not state the size its ispe does, and a total over four times
// the pixel limit (a grid of ordinary tiles holds less), before any picture is decoded.
// cfgs receives the configuration of each item, built once for each hvcC.
func (f *file) codedWork(pl *plan, cfgs map[*item]*hevcConfig) (int64, error) {
	built := map[*property]*hevcConfig{}
	items, w, h, budget := []*item{pl.base}, pl.w, pl.h, f.lim.MaxPixels
	if g := pl.grid; g != nil {
		items, w, h, budget = g.tiles, g.tw, g.th, 4*f.lim.MaxPixels
		if g.tw == 0 { // only meta was read: the tiles are the references, the size the first's
			to := f.refsOf("dimg", pl.base.id)
			if len(to) > f.lim.MaxTiles {
				return 0, fmt.Errorf("%w: %d tiles", ErrTooManyTiles, len(to))
			}
			items = items[:0:0]
			for _, id := range to {
				t := f.byID[id]
				if t == nil || t.typ != "hvc1" {
					return 0, fmt.Errorf("%w: grid %d tile %d is not an hvc1 item", ErrUnsupported, pl.base.id, id)
				}
				items = append(items, t)
			}
			tps, err := f.propsOf(items[0])
			if err != nil {
				return 0, err
			}
			sz := find(tps, "ispe")
			if sz == nil || sz.w == 0 || sz.h == 0 {
				return 0, fmt.Errorf("%w: grid tile size", ErrInvalid)
			}
			w, h = sz.w, sz.h
		}
	}
	var total int64
	for _, it := range items {
		hp, err := f.hvccOf(it)
		if err != nil {
			return 0, err
		}
		cfg := built[hp]
		if cfg == nil {
			if cfg, err = newHevcConfig(it.id, hp); err != nil {
				return 0, err
			}
			built[hp] = cfg
		}
		cfgs[it] = cfg
		px, err := cfg.codedPixels(it.id, w, h, f.lim.MaxPixels)
		if err != nil {
			return 0, err
		}
		if total += px; total > budget {
			return 0, fmt.Errorf("%w: the pictures of %d items hold more than %d pixels", ErrImageTooLarge, len(items), budget)
		}
	}
	return total, nil
}

// slicePPSID reads slice_pic_parameter_set_id of a slice segment header (7.3.6.1): after
// first_slice_segment_in_pic_flag, and no_output_of_prior_pics_flag for a random access
// picture, it is an ue(v). It states a number below 64, so a longer code is refused.
func slicePPSID(u h265.Unit) (uint32, bool) {
	b := u.Unescape()
	bit := func(i int) uint32 {
		if i>>3 >= len(b) {
			return 2 // past the end
		}
		return uint32(b[i>>3]>>(7-(i&7))) & 1
	}
	pos := 1
	if u.Type.IsRandomAccess() {
		pos++
	}
	zeros := 0
	for bit(pos+zeros) == 0 {
		if zeros++; zeros > 6 {
			return 0, false
		}
	}
	if bit(pos+zeros) != 1 {
		return 0, false
	}
	v := uint32(1)
	for i := 1; i <= zeros; i++ {
		x := bit(pos + zeros + i)
		if x > 1 {
			return 0, false
		}
		v = v<<1 | x
	}
	return v - 1, true
}

// scanUnits walks the length-prefixed NAL units of item data without keeping them. An item
// is one access unit of coded slices: only slice segments, SEI and access unit delimiters
// are allowed (a parameter set in the data would replace the hvcC's, and the decoder
// reads the picture against whichever it saw last). It refuses more than max units, more
// than one picture, and slice segments that do not move forward through the picture (a
// strictly increasing slice_segment_address, so no more segments than the picture has
// coding tree blocks): a stream of tiny units, of pictures, or of segments that each start
// the picture again must not cost memory or decoding time in proportion to a file that
// states them by the million. A picture starts at a slice segment whose
// first_slice_segment_in_pic_flag is set.
func scanUnits(data []byte, cfg *hevcConfig, max int) error {
	n, pictures := 0, 0
	var prev *h265.SliceHeader // the last independent segment of the picture
	last := int64(-1)          // the address of the segment before
	for len(data) > 0 {
		if len(data) < cfg.lengthSize {
			return fmt.Errorf("%w: a unit length is cut short", ErrCorrupt)
		}
		var l uint64
		for _, b := range data[:cfg.lengthSize] {
			l = l<<8 | uint64(b)
		}
		data = data[cfg.lengthSize:]
		if l > uint64(len(data)) {
			return fmt.Errorf("%w: a unit of %d bytes runs past the item", ErrCorrupt, l)
		}
		if n++; n > max {
			return fmt.Errorf("%w: more than %d NAL units in an item", ErrTooManyItems, max)
		}
		u := data[:l]
		data = data[l:]
		if len(u) < 2 {
			return fmt.Errorf("%w: a NAL unit of %d bytes has no header", ErrCorrupt, len(u))
		}
		unit := h265.Unit{Type: h265.UnitType(u[0] >> 1 & 0x3F), Payload: u[2:]}
		switch {
		case unit.Type == h265.UnitAUD || unit.Type == h265.UnitPrefixSEI || unit.Type == h265.UnitSuffixSEI:
			continue
		case !unit.Type.IsSliceSegment():
			return fmt.Errorf("%w: a NAL unit of type %d in item data (parameter sets belong in the hvcC)", ErrInvalid, unit.Type)
		case len(u) < 3:
			return fmt.Errorf("%w: a slice segment of %d bytes", ErrCorrupt, len(u))
		}
		if first := u[2]&0x80 != 0; first {
			if pictures++; pictures > 1 {
				return fmt.Errorf("%w: an item holds more than one picture", ErrInvalid)
			}
			prev, last = nil, -1
		} else if pictures == 0 {
			return fmt.Errorf("%w: a slice segment comes before the first of its picture", ErrCorrupt)
		}
		id, ok := slicePPSID(unit)
		pps, found := cfg.pps[id]
		sps, haveSPS := cfg.sps[pps.SPSID]
		if !ok || !found || !haveSPS {
			return fmt.Errorf("%w: a slice segment names a parameter set the hvcC does not hold", ErrCorrupt)
		}
		h, err := h265.ParseSliceSegmentHeaderFull(unit, sps, pps, prev)
		if err != nil {
			return fmt.Errorf("%w: slice segment header: %w", ErrCorrupt, err)
		}
		// (the header parser has refused an address outside the picture)
		if int64(h.SegmentAddress) <= last {
			return fmt.Errorf("%w: slice segment at address %d after %d", ErrInvalid, h.SegmentAddress, last)
		}
		last, prev = int64(h.SegmentAddress), &h
	}
	return nil
}

// hevcDecoder is what decodeHEVC needs of a decoder (a seam for the tests).
type hevcDecoder interface {
	Decode(h265.Unit) ([]h265.Output, error)
	Flush() ([]h265.Output, error)
}

// step feeds the decoder one unit, or ends the stream when u is nil. A panic of the
// decoder on a hostile stream is a corrupt picture, not the caller's crash.
func step(dec hevcDecoder, u *h265.Unit) (out []h265.Output, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("%w: the decoder failed: %v", ErrCorrupt, r)
		}
	}()
	if u == nil {
		return dec.Flush()
	}
	return dec.Decode(*u)
}

// decodeHEVC decodes an hvc1 item to its picture with dec, which carries no state the
// item needs: every item sends its parameter sets again, and a still picture is an IDR.
func (f *file) decodeHEVC(dec hevcDecoder, it *item, cfg *hevcConfig, w, h uint32) (*h265.DecodedPicture, error) {
	data, err := f.itemData(it)
	if err != nil {
		return nil, err
	}
	if err := scanUnits(data, cfg, maxNALUnits); err != nil {
		return nil, fmt.Errorf("item %d: %w", it.id, err)
	}
	more, _ := h265.SplitLengthPrefixed(data, cfg.lengthSize) // scanUnits has walked this framing
	units := make([]h265.Unit, 0, len(cfg.units)+len(more))
	units = append(append(units, cfg.units...), more...) // the parameter sets, then the item
	var pic *h265.DecodedPicture
	for i := 0; i <= len(units); i++ {
		var out []h265.Output
		if i < len(units) {
			out, err = step(dec, &units[i])
		} else {
			out, err = step(dec, nil) // the end of the stream
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
	// The sizes the parameter sets state are checked, and their pixels counted, before any
	// picture is decoded: the ispe of an item is a claim, and the decoder makes what the
	// SPS says.
	cfgs := map[*item]*hevcConfig{}
	if _, err := f.codedWork(pl, cfgs); err != nil {
		return nil, err
	}
	if pl.grid == nil {
		return f.decodeHEVC(f.newDecoder(), pl.base, cfgs[pl.base], pl.w, pl.h)
	}
	g := pl.grid
	var out *h265.DecodedPicture
	var first h265.DecodedPicture // the format of the first tile, kept after its planes are released
	// One decoder is alive at a time, and it serves the tiles that share their hvcC (a
	// grid's tiles all do), so that the planes of a tile are reused by the next. A tile
	// with another hvcC replaces it: the memory the decoder limit allows is then spent
	// once for the file, not once per distinct hvcC. The configuration of a property is
	// built once.
	var dec *h265.Decoder
	var cur *hevcConfig
	for i, t := range g.tiles {
		cfg := cfgs[t] // the same one for the tiles that share an hvcC
		if cfg != cur {
			dec, cur = f.newDecoder(), cfg
		}
		pic, err := f.decodeHEVC(dec, t, cfg, g.tw, g.th)
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
