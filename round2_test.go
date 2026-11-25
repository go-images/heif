// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-avkit/h265"
)

// The regression tests of the second round of the security review: parameter sets smuggled
// into the item data, slice segments that start the picture again, one decoder per hvcC,
// the hvcC rebuilt per tile, and a panic of the decoder. The hostile streams are built
// from flat pictures of 256x256, 4096x4096, 8192x4320 and 8192x8192 (testdata/hevc).

func loadNALs(t testing.TB, name string) (sets, rest [][]byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "hevc", name+".hevc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range annexB(t, raw) {
		switch n[0] >> 1 & 0x3F {
		case 32, 33, 34:
			sets = append(sets, n)
		default:
			rest = append(rest, n)
		}
	}
	return sets, rest
}

// lp is NAL units, each behind its length in four bytes.
func lp(nals ...[]byte) []byte {
	var d []byte
	for _, n := range nals {
		d = binary.BigEndian.AppendUint32(d, uint32(len(n)))
		d = append(d, n...)
	}
	return d
}

func unitOf(t testing.TB, nal []byte) h265.Unit {
	t.Helper()
	us, err := h265.SplitLengthPrefixed(lp(nal), 4)
	if err != nil || len(us) != 1 {
		t.Fatalf("unit: %v (%d)", err, len(us))
	}
	return us[0]
}

func paramsOf(t testing.TB, sets [][]byte) (sps h265.SPS, pps h265.PPS) {
	t.Helper()
	var err error
	for _, n := range sets {
		switch n[0] >> 1 & 0x3F {
		case 33:
			if sps, err = h265.ParseSPS(unitOf(t, n)); err != nil {
				t.Fatal(err)
			}
		case 34:
			if pps, err = h265.ParsePPS(unitOf(t, n)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return sps, pps
}

type bitw struct {
	b []byte
	n int
}

func (w *bitw) put(bit byte) {
	if w.n%8 == 0 {
		w.b = append(w.b, 0)
	}
	if bit != 0 {
		w.b[w.n/8] |= 0x80 >> (w.n % 8)
	}
	w.n++
}

func getbit(b []byte, i int) byte { return b[i/8] >> (7 - i%8) & 1 }

func ctbsOf(sps h265.SPS) uint64 {
	ctb := uint64(1) << sps.Log2CtbSize
	return ((uint64(sps.CodedWidth) + ctb - 1) / ctb) * ((uint64(sps.CodedHeight) + ctb - 1) / ctb)
}

// nonFirst rewrites the first slice segment of a picture as an independent slice segment
// that is not the first, at slice_segment_address addr (written in as many bits as the
// picture needs, so that an address past its last block is as writable as any other).
func nonFirst(t testing.TB, nal []byte, sps h265.SPS, pps h265.PPS, addr uint64) []byte {
	t.Helper()
	u := unitOf(t, nal)
	h, err := h265.ParseSliceSegmentHeaderFull(u, sps, pps, nil)
	if err != nil {
		t.Fatal(err)
	}
	un := u.Unescape()
	pos := 1
	if u.Type.IsRandomAccess() {
		pos++
	}
	z := 0
	for getbit(un, pos+z) == 0 {
		z++
	}
	end := pos + 2*z + 1
	var w bitw
	w.put(0)
	for i := 1; i < end; i++ {
		w.put(getbit(un, i))
	}
	if pps.DependentSlicesEnabled {
		w.put(0)
	}
	n := ctbsOf(sps)
	bits := 0
	for uint64(1)<<bits < n {
		bits++
	}
	for i := bits - 1; i >= 0; i-- {
		w.put(byte(addr >> i & 1))
	}
	for i := end; i < h.HeaderBits; i++ {
		w.put(getbit(un, i))
	}
	w.put(1)
	for w.n%8 != 0 {
		w.put(0)
	}
	out := append(w.b, un[h.DataOffset:]...)
	esc := []byte{nal[0], nal[1]}
	zeros := 0
	for _, c := range out {
		if zeros >= 2 && c <= 3 {
			esc = append(esc, 3)
			zeros = 0
		}
		esc = append(esc, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return esc
}

// stream is one of the flat pictures: its parameter sets, its first slice segment, and
// what is left (SEI) of it.
type stream struct {
	sets  [][]byte
	slice []byte
	rest  [][]byte // the units that are not parameter sets nor slice segments
	sps   h265.SPS
	pps   h265.PPS
	cfg   *hevcConfig
}

func loadStream(t testing.TB, name string) stream {
	t.Helper()
	s := stream{}
	var rest [][]byte
	s.sets, rest = loadNALs(t, name)
	for _, n := range rest {
		if n[0]>>1&0x3F < 32 {
			s.slice = n
		} else {
			s.rest = append(s.rest, n)
		}
	}
	if s.slice == nil {
		t.Fatalf("%s: no slice", name)
	}
	s.sps, s.pps = paramsOf(t, s.sets)
	var err error
	if s.cfg, err = newHevcConfig(1, &property{typ: "hvcC", hvcc: hvcC{lengthSize: 4, nals: s.sets}}); err != nil {
		t.Fatal(err)
	}
	return s
}

// file makes a still image of the stream with the given item data.
func (s stream) file(data []byte, w, h uint32) []byte {
	return single(coded{hvcC: hvcCBox(1, 8, 8, 4, s.sets), data: data}, w, h).bytes()
}

// The ceilings of the hostile files: what one may allocate and how long one may take, the
// same under the race detector and on a slow runner. Each is more than ten times what the
// refusal measures and a hundredth of what the defect cost (400 s, 87 GiB).
const (
	absoluteAlloc = 160 << 20
	absoluteTime  = 20 * time.Second
)

// within runs f and says whether it returned in d; f is left running when it did not.
func within(d time.Duration, f func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestWithin(t *testing.T) {
	if !within(time.Minute, func() {}) {
		t.Error("a function that returns is late")
	}
	stop := make(chan struct{})
	defer close(stop)
	if within(10*time.Millisecond, func() { <-stop }) {
		t.Error("a function that waits has returned")
	}
}

// refused decodes a hostile file at the default limits and requires the named error, within
// the absolute ceilings.
func refused(t *testing.T, name string, data []byte, want error) {
	t.Helper()
	var err error
	var n uint64
	start := time.Now()
	// a decode that does not end fails the test, where waiting for it would hang the suite
	if !within(3*absoluteTime, func() { n = bombed(func() { err = decodeDefault(data) }) }) {
		t.Fatalf("%s: not decoded or refused in %v", name, 3*absoluteTime)
	}
	if el := time.Since(start); el > absoluteTime {
		t.Errorf("%s: took %v", name, el)
	}
	if n > absoluteAlloc {
		t.Errorf("%s: %d bytes allocated for a file of %d", name, n, len(data))
	}
	if !errors.Is(err, want) {
		t.Errorf("%s: %v, want %v", name, err, want)
	}
}

// A slice that panics the decoder cannot be reached: the parameter sets of the item data
// were the way to it (a 739-byte file that killed the caller).
func TestRound2ParameterSetsInItemDataAreRefused(t *testing.T) {
	defer guard(t)
	smallSets, smallRest := loadNALs(t, "yuv420p")
	var good []byte
	for _, n := range smallRest {
		good = append(good, lp(n)...)
	}
	if err := decodeDefault(single(coded{hvcC: hvcCBox(1, 8, 8, 4, smallSets), data: good}, 36, 22).bytes()); err != nil {
		t.Fatal(err)
	}
	big := loadStream(t, "flat256")
	for _, typ := range []byte{32, 33, 34, 36, 37, 38, 41, 48, 63} {
		unit := []byte{typ << 1, 1, 0x80, 0}
		for _, tc := range []struct {
			name string
			data []byte
		}{
			{"before the slice", append(lp(unit), good...)},
			{"after the slice", append(append([]byte{}, good...), lp(unit)...)},
		} {
			refused(t, fmt.Sprintf("type %d %s", typ, tc.name), single(coded{hvcC: hvcCBox(1, 8, 8, 4, smallSets), data: tc.data}, 36, 22).bytes(), ErrInvalid)
		}
	}
	// the sets of another picture between the slice segments of this one
	for _, addr := range []uint64{0, ctbsOf(big.sps) - 1} {
		nals := [][]byte{smallRest[len(smallRest)-1]}
		nals = append(nals, big.sets...)
		nals = append(nals, nonFirst(t, big.slice, big.sps, big.pps, addr))
		refused(t, fmt.Sprintf("sets of another picture, slice at %d", addr),
			single(coded{hvcC: hvcCBox(1, 8, 8, 4, smallSets), data: lp(nals...)}, 36, 22).bytes(), ErrInvalid)
	}
	// SEI and an access unit delimiter are allowed
	aud := []byte{35 << 1, 1, 0x50}
	data := append(lp(aud), good...)
	if err := decodeDefault(single(coded{hvcC: hvcCBox(1, 8, 8, 4, smallSets), data: data}, 36, 22).bytes()); err != nil {
		t.Errorf("an access unit delimiter: %v", err)
	}
}

// fakeDecoder panics the way h265 does on a hostile stream.
type fakeDecoder struct{ onDecode, onFlush func() }

func (d fakeDecoder) Decode(h265.Unit) ([]h265.Output, error) {
	if d.onDecode != nil {
		d.onDecode()
	}
	return nil, nil
}

func (d fakeDecoder) Flush() ([]h265.Output, error) {
	if d.onFlush != nil {
		d.onFlush()
	}
	return nil, nil
}

func TestRound2DecoderPanicIsACorruptPicture(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	pf, err := parseFile(single(c, 36, 22).bytes(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	hp, err := pf.hvccOf(pf.byID[1])
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := newHevcConfig(1, hp)
	if err != nil {
		t.Fatal(err)
	}
	boom := func() { var a []int; _ = a[len(c.data)] } // index out of range
	for _, tc := range []struct {
		name string
		dec  fakeDecoder
	}{
		{"in Decode", fakeDecoder{onDecode: boom}},
		{"in Flush", fakeDecoder{onFlush: boom}},
	} {
		pic, err := pf.decodeHEVC(tc.dec, pf.byID[1], cfg, 36, 22)
		if !errors.Is(err, ErrCorrupt) || pic != nil {
			t.Errorf("a panic %s: %v", tc.name, err)
		}
	}
	// a fake that does not panic gives no picture
	if _, err := pf.decodeHEVC(fakeDecoder{}, pf.byID[1], cfg, 36, 22); !errors.Is(err, ErrCorrupt) {
		t.Errorf("no picture: %v", err)
	}
}

// Slice segments must move forward through the picture: a strictly increasing
// slice_segment_address, inside it (4095 segments at address 0 were 4095 decodes of the
// picture, 1.7 s each at 4096x4096).
func TestRound2SliceSegmentAddresses(t *testing.T) {
	defer guard(t)
	s := loadStream(t, "flat256")
	n := ctbsOf(s.sps)
	at := func(addr uint64) []byte { return nonFirst(t, s.slice, s.sps, s.pps, addr) }
	for _, tc := range []struct {
		name string
		nals [][]byte
		want error
	}{
		{"one segment", [][]byte{s.slice}, nil},
		{"forward", [][]byte{s.slice, at(1), at(n - 1)}, nil},
		{"the same address again", [][]byte{s.slice, at(0)}, ErrInvalid},
		{"the same address twice", [][]byte{s.slice, at(3), at(3)}, ErrInvalid},
		{"backwards", [][]byte{s.slice, at(5), at(3)}, ErrInvalid},
		{"a restart after the last", [][]byte{s.slice, at(n - 1), at(0)}, ErrInvalid},
		{"before the first", [][]byte{at(2)}, ErrCorrupt},
		{"a second picture", [][]byte{s.slice, s.slice}, ErrInvalid},
		{"a picture after segments", [][]byte{s.slice, at(1), s.slice}, ErrInvalid},
		{"a cut header", [][]byte{s.slice[:3]}, ErrCorrupt},
		{"a slice of two bytes", [][]byte{s.slice[:2]}, ErrCorrupt},
		{"a parameter set", [][]byte{s.slice, s.sets[0]}, ErrInvalid},
	} {
		err := scanUnits(lp(tc.nals...), s.cfg, maxNALUnits)
		if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// 8192x4320 has 128x68 blocks, which 14 bits of address can overrun (the header parser
	// refuses that)
	w := loadStream(t, "flat8192x4320")
	last := ctbsOf(w.sps) - 1
	if last != 128*68-1 {
		t.Fatalf("%d blocks", last+1)
	}
	for _, tc := range []struct {
		addr uint64
		want error
	}{{last, nil}, {last + 1, ErrCorrupt}, {1<<14 - 1, ErrCorrupt}} {
		err := scanUnits(lp(w.slice, nonFirst(t, w.slice, w.sps, w.pps, tc.addr)), w.cfg, maxNALUnits)
		if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
			t.Errorf("address %d of %d: %v", tc.addr, last+1, err)
		}
	}
	// a slice that names a parameter set the hvcC does not hold
	none, _ := newHevcConfig(1, &property{hvcc: hvcC{lengthSize: 4}})
	if err := scanUnits(lp(s.slice), none, maxNALUnits); !errors.Is(err, ErrCorrupt) {
		t.Errorf("no parameter sets: %v", err)
	}
	onlyPPS, _ := newHevcConfig(1, &property{hvcc: hvcC{lengthSize: 4, nals: s.sets[2:]}})
	if err := scanUnits(lp(s.slice), onlyPPS, maxNALUnits); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a PPS without its SPS: %v", err)
	}
}

// 4095 segments at address 0, on the 4096x4096 picture that was 1.7 s a segment.
func TestRound2SliceRestartBomb(t *testing.T) {
	defer guard(t)
	s := loadStream(t, "flat4096")
	again := nonFirst(t, s.slice, s.sps, s.pps, 0)
	for _, n := range []int{1, 64, maxNALUnits - 1} {
		nals := [][]byte{s.slice}
		for i := 0; i < n; i++ {
			nals = append(nals, again)
		}
		refused(t, fmt.Sprintf("%d restarts", n), s.file(lp(nals...), 4096, 4096), ErrInvalid)
	}
	// the picture itself decodes, inside the same ceilings
	var err error
	start := time.Now()
	m := bombed(func() { err = decodeDefault(s.file(lp(append(s.rest, s.slice)...), 4096, 4096)) })
	if m > 2<<30 || time.Since(start) > absoluteTime || err != nil { // measured 271 MB for the picture
		t.Errorf("one 4096x4096 picture: %d bytes, %v, %v", m, time.Since(start), err)
	}
}

// The hostile files of the first round again, against absolute ceilings: the bounds of
// security_test.go are relative to what reading the file costs, which is how a defect that
// cost a decode per segment went by.
func TestRound2AbsoluteCeilings(t *testing.T) {
	defer guard(t)
	f := single(coded{hvcC: hvcCBox(1, 8, 8, 1, nil), data: bytes.Repeat([]byte{2, 0x4E, 0x01}, 1365)}, 36, 22)
	f.items[0].dup = 8192
	refused(t, "NAL unit bomb", f.bytes(), ErrFileTooLarge)
	refused(t, "a million units", single(coded{hvcC: hvcCBox(1, 8, 8, 1, nil), data: bytes.Repeat([]byte{2, 0x4E, 0x01}, 1<<19)}, 36, 22).bytes(), ErrTooManyItems)
	c := loadCoded(t, "yuv420p", 1, 8)
	c.data = bytes.Repeat(c.data, 1000)
	refused(t, "1000 pictures", single(c, 36, 22).bytes(), ErrInvalid)
}

// A coded slice in the hvcC of a tile of a grid is refused as it is in a single item.
func TestRound2GridTileHvcCHoldsASlice(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	c.hvcC = hvcCBox(1, 8, 8, 4, [][]byte{{19 << 1, 1, 0x80, 0, 0}})
	data := gridFile(c, 36, 22, 1, 2, 72, 22, gridDesc(1, 2, 72, 22, false)).bytes()
	if err := decodeDefault(data); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

// slicePPSID reads the id after the flags, and nothing it should not.
func TestRound2SlicePPSID(t *testing.T) {
	defer guard(t)
	for _, tc := range []struct {
		name string
		typ  h265.UnitType
		b    []byte
		want uint32
		ok   bool
	}{
		{"id 0, a trailing picture", h265.UnitTrailR, []byte{0xC0}, 0, true},
		{"id 1", h265.UnitTrailR, []byte{0xA0}, 1, true},
		{"id 2", h265.UnitTrailR, []byte{0xB0}, 2, true},
		{"id 3", h265.UnitTrailR, []byte{0x90}, 3, true},
		{"id 0, an IDR (one more flag)", h265.UnitIDRWRADL, []byte{0xA0}, 0, true},
		{"id 1, an IDR", h265.UnitIDRWRADL, []byte{0x90}, 1, true},
		{"id 63, six zeros", h265.UnitTrailR, []byte{0x81, 0x00}, 63, true},
		{"seven zeros", h265.UnitTrailR, []byte{0x80, 0x80}, 0, false},
		{"a code cut short", h265.UnitTrailR, []byte{0x81}, 0, false},
		{"no byte at all", h265.UnitTrailR, nil, 0, false},
	} {
		got, ok := slicePPSID(h265.Unit{Type: tc.typ, Payload: tc.b})
		if ok != tc.ok || ok && got != tc.want {
			t.Errorf("%s: %d, %v", tc.name, got, ok)
		}
	}
}

// peakHeap runs f and returns the largest live heap it was seen to hold.
func peakHeap(f func()) uint64 {
	runtime.GC()
	var peak atomic.Uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()
	f()
	close(stop)
	<-done
	return peak.Load()
}

// A grid one row high of 4096x4096 tiles that each state an hvcC of their own was one
// decoder per property, all alive until the end: 855 MiB against 70.
func TestRound2OneDecoderAtATime(t *testing.T) {
	defer guard(t)
	s := loadStream(t, "flat4096")
	c := coded{hvcC: hvcCBox(1, 8, 8, 4, s.sets), data: lp(s.rest...)}
	c.data = append(c.data, lp(s.slice)...)
	const cols = 8
	w := uint32((cols-1)*4096 + 1)
	build := func(distinct bool) []byte {
		g := gridFile(c, 4096, 4096, 1, cols, w, 1, gridDesc(1, cols, w, 1, true))
		if distinct {
			for i := 1; i < len(g.items); i++ {
				g.props = append(g.props, c.hvcC)
				g.items[i].props = []int{2, len(g.props)}
			}
		}
		return g.bytes()
	}
	var shared, distinct uint64
	for _, d := range []bool{false, true} {
		data := build(d)
		var err error
		peak := peakHeap(func() { _, err = DecodeOptions(bytes.NewReader(data), Options{}) })
		if err != nil {
			t.Fatalf("distinct %v: %v", d, err)
		}
		if d {
			distinct = peak
		} else {
			shared = peak
		}
	}
	t.Logf("peak heap: shared hvcC %d MiB, an hvcC per tile %d MiB", shared>>20, distinct>>20)
	// 8 decoders of 70 MiB would be 560; one at a time is the size of one plus the garbage
	// the collector has not yet taken, which the shared case shows is within a few times it
	if distinct > 3*shared+128<<20 {
		t.Errorf("an hvcC per tile holds %d MiB at its peak, one hvcC %d MiB", distinct>>20, shared>>20)
	}
}

// A grid of 4096 tiles whose hvcC is 4 MB of SEI: the configuration is built once for the
// property, not once per tile (it was 87 GiB of copies).
func TestRound2ConfigurationBuiltOncePerProperty(t *testing.T) {
	defer guard(t)
	sets, rest := loadNALs(t, "yuv420p")
	sei := append([]byte{39 << 1, 1}, bytes.Repeat([]byte{0xFF}, 65533)...)
	all := append([][]byte{}, sets...)
	for i := 0; i < 61; i++ {
		all = append(all, sei)
	}
	c := coded{hvcC: hvcCBox(1, 8, 8, 4, all), data: lp(rest...)}
	g := gridFile(c, 36, 22, 64, 64, 64*36, 64*22, gridDesc(64, 64, 64*36, 64*22, false))
	data := g.bytes()
	var err error
	start := time.Now()
	n := bombed(func() { err = decodeDefault(data) })
	t.Logf("%d bytes allocated in %v for a file of %d: %v", n, time.Since(start), len(data), err)
	if err != nil {
		t.Fatal(err)
	}
	if n > 1<<30 || time.Since(start) > absoluteTime { // measured 592 MB; the defect was 87 GiB
		t.Errorf("%d bytes in %v", n, time.Since(start))
	}
}

// The default limits hold what Limits says they hold: a single still of 8192x4320 and one
// of 8192x8192 (MaxPixels) decode, in one picture's memory.
func TestRound2LargeStillsAtTheDefaultLimits(t *testing.T) {
	defer guard(t)
	for _, tc := range []struct {
		name string
		w, h uint32
	}{{"flat8192x4320", 8192, 4320}, {"flat8192", 8192, 8192}} {
		s := loadStream(t, tc.name)
		var res *Result
		var err error
		start := time.Now()
		peak := peakHeap(func() {
			res, err = DecodeOptions(bytes.NewReader(s.file(lp(append(s.rest, s.slice)...), tc.w, tc.h)), Options{})
		})
		t.Logf("%s: %v, peak %d MiB, %v", tc.name, time.Since(start), peak>>20, err)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if b := res.Image.Bounds(); b.Dx() != int(tc.w) || b.Dy() != int(tc.h) {
			t.Errorf("%s: %v", tc.name, b)
		}
		if peak > 1<<30 || time.Since(start) > absoluteTime { // measured 233 MiB and 0.5 s at most
			t.Errorf("%s: peak %d MiB in %v", tc.name, peak>>20, time.Since(start))
		}
	}
}
