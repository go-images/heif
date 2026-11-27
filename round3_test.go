package heif

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/go-avkit/h265"
)

// The ispe of an item is a claim; the decoder makes what the SPS says, and a picture is
// output cropped to its conformance window. A tile coded at 40x24 and cropped to its ispe
// of 36x22 is charged 960 pixels, not 792, so a budget set against DecodeWork holds.
func TestRound3WorkIsTheCodedSize(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	// A row of 256 tiles (the most a row holds) states 256 x 792 = 202,752 pixels and is
	// coded as 245,760; the work of a grid is bounded by four times the pixel limit, which
	// the ispe satisfies at 50,688 and the coded size does not.
	const cols = 256
	w := uint32((cols-1)*36 + 1)
	data := gridFile(c, 36, 22, 1, cols, w, 1, gridDesc(1, cols, w, 1, true)).bytes()
	lim := Limits{MaxPixels: 50688}
	if _, err := workOf(t, "work", data, lim); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("DecodeWork: %v, want %v", err, ErrImageTooLarge)
	}
	var err error
	n := bombed(func() { _, err = DecodeOptions(bytes.NewReader(data), Options{Limits: lim}) })
	if !errors.Is(err, ErrImageTooLarge) || n > absoluteAlloc {
		t.Errorf("Decode: %v after %d bytes, want %v before any picture", err, n, ErrImageTooLarge)
	}
	if got, err := workOf(t, "work", data, Limits{MaxPixels: 1 << 22}); err != nil || got != cols*40*24 {
		t.Errorf("under the limit: %d, %v, want %d", got, err, cols*40*24)
	}
	// a picture coded larger than the limit is refused although its ispe is not
	one := single(c, 36, 22).bytes()
	if _, err := workOf(t, "single", one, Limits{MaxPixels: 800}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("a picture coded as 960 pixels at 800: %v", err)
	}
	if _, err := DecodeOptions(bytes.NewReader(one), Options{Limits: Limits{MaxPixels: 800}}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("Decode at 800: %v", err)
	}
}

// DecodeWork reads only the head, so it takes the tiles of a grid from the references and
// the tile size from the first of them, and refuses what Decode would.
func TestRound3WorkOfAGridFromItsHead(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	for _, tc := range []struct {
		name string
		edit func(*hfile)
		lim  Limits
		want error
	}{
		{"too many tiles", func(*hfile) {}, Limits{MaxTiles: 3}, ErrTooManyTiles},
		{"a tile that is not hvc1", func(f *hfile) { f.items[2].typ = "av01" }, Limits{}, ErrUnsupported},
		{"a tile naming a property that is not there", func(f *hfile) { f.items[1].props = []int{2, 99} }, Limits{}, errNamesProperty},
		{"a tile with no ispe", func(f *hfile) { f.items[1].props = []int{3} }, Limits{}, ErrInvalid},
	} {
		f := gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false))
		tc.edit(&f)
		_, err := workOf(t, tc.name, f.bytes(), tc.lim)
		if tc.want == errNamesProperty {
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "names property") {
				t.Errorf("%s: %v", tc.name, err)
			}
		} else if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// a tile coded larger than the limit although its ispe is not, in a grid whose total is
	// under four times the limit
	one := gridFile(c, 36, 22, 1, 1, 36, 22, gridDesc(1, 1, 36, 22, false)).bytes()
	if _, err := workOf(t, "one tile", one, Limits{MaxPixels: 800}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("a tile coded as 960 pixels at 800: %v", err)
	}
}

var errNamesProperty = errors.New("names a property that is not there")

// fakeFailingDecoder fails the way a decoder does on a bad stream.
type fakeFailingDecoder struct{}

func (fakeFailingDecoder) Decode(h265.Unit) ([]h265.Output, error) {
	return nil, errors.New("bad stream")
}
func (fakeFailingDecoder) Flush() ([]h265.Output, error) { return nil, nil }

func TestRound3DecoderErrorIsReported(t *testing.T) {
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
	_, err = pf.decodeHEVC(fakeFailingDecoder{}, pf.byID[1], cfg, 36, 22)
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "bad stream") {
		t.Errorf("a decoder error: %v", err)
	}
}

// fakePictureDecoder outputs a picture of another size than its stream states.
type fakePictureDecoder struct{ pic *h265.DecodedPicture }

func (fakePictureDecoder) Decode(h265.Unit) ([]h265.Output, error) { return nil, nil }
func (d fakePictureDecoder) Flush() ([]h265.Output, error) {
	return []h265.Output{{Picture: d.pic}}, nil
}

// The size of the picture the decoder outputs is still compared with the item's, in case it
// is not the one the parameter sets stated.
func TestRound3OutputSizeIsCompared(t *testing.T) {
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
	_, err = pf.decodeHEVC(fakePictureDecoder{synth(30, 20, 0, 0, 1)}, pf.byID[1], cfg, 36, 22)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("a picture of 30x20 for an item of 36x22: %v", err)
	}
}

// A 1x1 ispe on a picture the SPS states as 8192x8192 bought one full decode before the
// size was compared. It is refused from the parameter sets, before any picture.
func TestRound3IspeIsComparedBeforeTheDecode(t *testing.T) {
	defer guard(t)
	s := loadStream(t, "flat8192")
	data := s.file(lp(append(s.rest, s.slice)...), 1, 1)
	var err error
	n := bombed(func() { _, err = decodeDefaultResult(data) })
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want %v", err, ErrInvalid)
	}
	if n > absoluteAlloc { // a decode of 8192x8192 allocates 233 MiB at its peak
		t.Errorf("refusing it allocated %d bytes: a picture was decoded", n)
	}
	if w, err := workOf(t, "work", data, Limits{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("DecodeWork = %d, %v, want %v", w, err, ErrInvalid)
	}
}

func decodeDefaultResult(data []byte) (*Result, error) {
	return DecodeOptions(bytes.NewReader(data), Options{})
}

// The peak heap of decoding a flat 8192x8192 picture, the most the default limit allows,
// with and without a rotation. The planes, the RGBA raster and a turned copy of it were
// alive at once: 9.5 bytes a pixel for 8-bit 4:2:0 (608 MiB measured).
func TestRound3PeakHeapOfATurnedPicture(t *testing.T) {
	defer guard(t)
	s := loadStream(t, "flat8192")
	c := coded{hvcC: hvcCBox(1, 8, 8, 4, s.sets), data: lp(append(s.rest, s.slice)...)}
	const px = 8192 * 8192
	peaks := map[string]uint64{}
	for name, props := range map[string][][]byte{"plain": nil, "turned": {irot(1)}} {
		data := single(c, 8192, 8192, props...).bytes()
		var err error
		peaks[name] = peakHeap(func() { _, err = DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxMemory: 400 << 20}}) })
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%s: peak heap %d MiB, %.2f bytes a pixel", name, peaks[name]>>20, float64(peaks[name])/px)
	}
	if got := float64(peaks["turned"]) / px; got > 8 { // 5.5 live, and the garbage the collector has not taken: 352 to 437 MiB seen
		t.Errorf("a turned picture holds %.2f bytes a pixel at its peak, want at most 8 (was 9.5)", got)
	}
}
