package heif

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-avkit/h265"
)

// bitsOf reads the bits of an RBSP.
type bitsOf struct {
	b   []byte
	pos int
}

func (r *bitsOf) bit() byte  { v := getbit(r.b, r.pos); r.pos++; return v }
func (r *bitsOf) skip(n int) { r.pos += n }
func (r *bitsOf) ue() {
	z := 0
	for r.bit() == 0 {
		z++
	}
	r.skip(z)
}

// tiledPPS rewrites a PPS NAL unit without tiles as one that states cols x rows uniform tiles:
// it finds tiles_enabled_flag by reading the syntax before it (7.3.2.3.1) and writes the tile
// syntax after entropy_coding_sync_enabled_flag.
func tiledPPS(t testing.TB, nal []byte, cols, rows int) []byte {
	t.Helper()
	rb := unitOf(t, nal).Unescape()
	r := &bitsOf{b: rb}
	r.ue()
	r.ue() // pps_pic_parameter_set_id, pps_seq_parameter_set_id
	r.skip(1 + 1 + 3 + 1 + 1)
	r.ue()
	r.ue()
	r.ue() // num_ref_idx_l0/l1_default_minus1, init_qp_minus26
	r.skip(2)
	if r.bit() == 1 { // cu_qp_delta_enabled_flag
		r.ue()
	}
	r.ue()
	r.ue() // pps_cb_qp_offset, pps_cr_qp_offset
	r.skip(4)
	tiles := r.pos
	if getbit(rb, tiles) != 0 || getbit(rb, tiles+1) != 0 {
		t.Fatal("the PPS already has tiles or wavefronts")
	}
	last := len(rb)*8 - 1
	for getbit(rb, last) == 0 {
		last--
	}
	var w bitw
	for i := 0; i < tiles; i++ {
		w.put(getbit(rb, i))
	}
	w.put(1)            // tiles_enabled_flag
	w.put(0)            // entropy_coding_sync_enabled_flag
	ue := func(v int) { // ue(v), v+1 in binary behind as many zeros
		n := 0
		for (v+1)>>n > 1 {
			n++
		}
		for i := 0; i < n; i++ {
			w.put(0)
		}
		for i := n; i >= 0; i-- {
			w.put(byte((v + 1) >> i & 1))
		}
	}
	ue(cols - 1)
	ue(rows - 1)
	w.put(1) // uniform_spacing_flag
	w.put(1) // loop_filter_across_tiles_enabled_flag
	for i := tiles + 2; i <= last; i++ {
		w.put(getbit(rb, i))
	}
	for w.n%8 != 0 {
		w.put(0)
	}
	out := []byte{nal[0], nal[1]}
	zeros := 0
	for _, c := range w.b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// With tiles the slice segments of a picture are in increasing order of CtbAddrRsToTs, not of
// the raster address of H.265 7.4.7.1: a 4x4 picture of 2x2 tiles holds blocks 0,1,4,5 in its
// first tile, and a segment at block 4 is followed by one at block 2 (6-7: ts 2 then 4).
func TestTiledSegmentsAreOrderedInTileScan(t *testing.T) {
	defer guard(t)
	s := loadStream(t, "flat256")
	if s.sps.Log2CtbSize != 6 || s.sps.CodedWidth != 256 || s.sps.CodedHeight != 256 {
		t.Fatalf("the stream is %dx%d with blocks of %d", s.sps.CodedWidth, s.sps.CodedHeight, 1<<s.sps.Log2CtbSize)
	}
	cfgOf := func(cols, rows int) (*hevcConfig, h265.SPS, h265.PPS) {
		var sets [][]byte
		for _, n := range s.sets {
			if n[0]>>1&0x3F == 34 {
				n = tiledPPS(t, n, cols, rows)
			}
			sets = append(sets, n)
		}
		cfg, err := newHevcConfig(1, &property{typ: "hvcC", hvcc: hvcC{lengthSize: 4, nals: sets}})
		if err != nil {
			t.Fatal(err)
		}
		_, pps := paramsOf(t, sets)
		if !pps.TilesEnabled || pps.TileColumns != uint32(cols) || pps.TileRows != uint32(rows) {
			t.Fatalf("the PPS is %+v", pps)
		}
		return cfg, s.sps, pps
	}
	cfg, sps, pps := cfgOf(2, 2)
	first := segmentAt(t, s.slice, s.sps, s.pps, 0, true, true)
	at := func(a uint64) []byte { return segmentAt(t, s.slice, s.sps, s.pps, a, false, true) }
	if _, err := h265.ParseSliceSegmentHeaderFull(unitOf(t, at(4)), sps, pps, nil); err != nil {
		t.Fatalf("the rewritten header does not parse: %v", err)
	}
	for _, tc := range []struct {
		name string
		nals [][]byte
		want error
	}{
		{"raster order, tile order", [][]byte{first, at(2), at(8)}, nil},
		{"a later tile first in raster order", [][]byte{first, at(4), at(2), at(8)}, nil},
		{"the second tile before the first row ends", [][]byte{first, at(2), at(4)}, ErrInvalid},
		{"the same block twice", [][]byte{first, at(4), at(4)}, ErrInvalid},
		{"back into an earlier tile", [][]byte{first, at(8), at(5)}, ErrInvalid},
	} {
		err := scanUnits(lp(tc.nals...), cfg, maxNALUnits)
		if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// a tile grid that does not fit the picture is a corrupt stream, not a panic
	// a layout that cannot be built is a corrupt stream (the seam: the header parser has
	// validated the pair already, so no stream gets there)
	defer func(f func(h265.SPS, h265.PPS) (*h265.CtbLayout, error)) { newCtbLayout = f }(newCtbLayout)
	newCtbLayout = func(h265.SPS, h265.PPS) (*h265.CtbLayout, error) { return nil, errors.New("no layout") }
	if err := scanUnits(lp(first), cfg, maxNALUnits); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "tiles") {
		t.Errorf("a layout that fails: %v, want ErrCorrupt naming the tiles", err)
	}
}
