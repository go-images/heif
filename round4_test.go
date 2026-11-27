package heif

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Limits.MaxMemory is checked from the parameter sets before anything is decoded, at the byte:
// the planes of the coded picture (one byte a sample up to 8 bits, two above; 1, 1.5, 2 or 3
// samples a pixel) plus the converted result (1 or 2 bytes grey, 4 or 8 colour) a pixel.
func TestRound4MaxMemoryIsTheEstimate(t *testing.T) {
	defer guard(t)
	const out = 36 * 22
	for _, tc := range []struct {
		name          string
		chroma, bits  int
		planes2, rbpp int64 // halves of a byte a coded pixel, bytes an output pixel
	}{
		{"yuv420p", 1, 8, 3, 4},
		{"yuv420p10le", 1, 10, 6, 8},
		{"yuv422p", 2, 8, 4, 4},
		{"yuv444p", 3, 8, 6, 4},
		{"yuv444p10le", 3, 10, 12, 8},
		{"gray", 0, 8, 2, 1},
		{"gray10le", 0, 10, 4, 2},
	} {
		data := single(loadCoded(t, tc.name, tc.chroma, tc.bits), 36, 22).bytes()
		px, err := workOf(t, tc.name, data, Limits{MaxMemory: 1 << 40})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want := px*tc.planes2/2 + out*tc.rbpp
		if _, err := workOf(t, tc.name, data, Limits{MaxMemory: want}); err != nil {
			t.Errorf("%s: %d bytes, the estimate, is refused: %v", tc.name, want, err)
		}
		_, err = workOf(t, tc.name, data, Limits{MaxMemory: want - 1})
		if !errors.Is(err, ErrImageTooLarge) || !strings.Contains(err.Error(), "MaxMemory") {
			t.Errorf("%s: %d bytes: %v, want %v naming MaxMemory", tc.name, want-1, err, ErrImageTooLarge)
		}
		var derr error
		n := bombed(func() { _, derr = DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxMemory: want - 1}}) })
		if !errors.Is(derr, ErrImageTooLarge) || n > 1<<20 {
			t.Errorf("%s: Decode: %v after %d bytes, want a refusal before any picture", tc.name, derr, n)
		}
	}
}

// A grid holds the planes of one tile and of the assembled picture besides the result.
func TestRound4MaxMemoryOfAGrid(t *testing.T) {
	defer guard(t)
	c := loadCoded(t, "yuv420p", 1, 8)
	data := gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false)).bytes()
	const want = 960*3/2 + 60*40*3/2 + 60*40*4
	if _, err := workOf(t, "grid", data, Limits{MaxMemory: want}); err != nil {
		t.Errorf("the estimate, %d bytes: %v", want, err)
	}
	if _, err := workOf(t, "grid", data, Limits{MaxMemory: want - 1}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("one byte under: %v", err)
	}
}

// The default holds a 48-megapixel 8-bit 4:2:0 photo and not 8192x8192, which needs 352 MiB,
// and stops 16-bit 4:4:4 near 24 megapixels, from the figures of the estimate.
func TestRound4DefaultMemoryBound(t *testing.T) {
	defer guard(t)
	def := DefaultLimits().MaxMemory
	if def != 320<<20 {
		t.Fatalf("default %d", def)
	}
	cost := func(px, planes2, rbpp int64) int64 { return px*planes2/2 + px*rbpp }
	if m := cost(48e6, 3, 4); m > def || m < 250<<20 {
		t.Errorf("48 MP 8-bit 4:2:0 is %d MiB", m>>20)
	}
	if m := cost(1<<26, 3, 4); m <= def {
		t.Errorf("8192x8192 8-bit 4:2:0 is %d MiB and should be over the default", m>>20)
	}
	if m := cost(23e6, 12, 8); m > def || cost(24e6, 12, 8) <= def {
		t.Errorf("16-bit 4:4:4 does not stop between 23 and 24 MP: %d MiB", m>>20)
	}
	// 8192x8192 is refused at the defaults before a picture is decoded, and DecodeWork says so
	s := loadStream(t, "flat8192")
	data := s.file(lp(append(s.rest, s.slice)...), 8192, 8192)
	var err error
	n := bombed(func() { _, err = DecodeOptions(bytes.NewReader(data), Options{}) })
	if !errors.Is(err, ErrImageTooLarge) || n > absoluteAlloc {
		t.Errorf("Decode at the default: %v after %d bytes", err, n)
	}
	if _, err := workOf(t, "work", data, Limits{}); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("DecodeWork at the default: %v", err)
	}
}

// A grid of 2x2 tiles of 4096x4096 (the 8192x8192 picture again) decodes under the memory it
// was estimated at, and its measured peak stays under 400 MiB.
func TestRound4PeakHeapOfAGrid(t *testing.T) {
	defer guard(t)
	s := loadStream(t, "flat4096")
	c := coded{hvcC: hvcCBox(1, 8, 8, 4, s.sets), data: append(lp(s.rest...), lp(s.slice)...)}
	data := gridFile(c, 4096, 4096, 2, 2, 8192, 8192, gridDesc(2, 2, 8192, 8192, false)).bytes()
	var err error
	peak := peakHeap(func() { _, err = DecodeOptions(bytes.NewReader(data), Options{Limits: Limits{MaxMemory: 400 << 20}}) })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("grid of 2x2 tiles of 4096x4096: peak heap %d MiB", peak>>20)
	if peak > 400<<20 {
		t.Errorf("peak %d MiB", peak>>20)
	}
}
