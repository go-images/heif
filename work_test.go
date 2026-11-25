package heif

import (
	"bytes"
	"errors"
	"testing"
)

// DecodeWork is the pixels the decoder processes, read from the head: the size of a single
// picture, and every tile of a grid however small the grid is cropped to.
func TestDecodeWork(t *testing.T) {
	c := loadCoded(t, "yuv420p", 1, 8)
	const cols = 8
	w := uint32((cols-1)*4096 + 1)
	for name, tc := range map[string]struct {
		data []byte
		want int64
	}{
		"single":                {single(c, 36, 22).bytes(), 36 * 22},
		"grid cropped to a row": {gridFile(c, 4096, 4096, 1, cols, w, 1, gridDesc(1, cols, w, 1, true)).bytes(), cols * 4096 * 4096},
	} {
		got, err := DecodeWork(bytes.NewReader(tc.data), Options{})
		if err != nil || got != tc.want {
			t.Errorf("%s: DecodeWork = %d, %v; want %d", name, got, err, tc.want)
		}
	}
	huge := gridFile(c, 8193, 8192, 1, 2, 8194, 1, gridDesc(1, 2, 8194, 1, true)).bytes()
	if _, err := DecodeWork(bytes.NewReader(huge), Options{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a tile over the pixel limit: %v", err)
	}
	if _, err := DecodeWork(bytes.NewReader([]byte("not a picture")), Options{}); err == nil {
		t.Error("a file that is not HEIF has a work")
	}
	data := single(c, 36, 22).bytes()
	noFtyp := bytes.Replace(data, []byte("ftyp"), []byte("free"), 1)
	if _, err := DecodeWork(bytes.NewReader(noFtyp), Options{}); !errors.Is(err, ErrNotHEIF) {
		t.Errorf("a file with no ftyp box: %v", err)
	}
	if _, err := DecodeWork(bytes.NewReader(data), Options{Limits: Limits{MaxBytes: 8}}); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("a file over the limit: %v", err)
	}
	if _, err := DecodeWork(bytes.NewReader(data), Options{Limits: Limits{MaxPixels: 100}}); err == nil {
		t.Error("a picture over the pixel limit has a work")
	}
}
