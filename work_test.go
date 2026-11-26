package heif

import (
	"bytes"
	"errors"
	"testing"
)

// workOf is DecodeWork on data; a panic is a failed assertion, not a crash, so that a
// refusal the function does not make is reported as that.
func workOf(t *testing.T, name string, data []byte, lim Limits) (n int64, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: DecodeWork panicked: %v", name, r)
			err = errors.New("panic")
		}
	}()
	return DecodeWork(bytes.NewReader(data), Options{Limits: lim})
}

// DecodeWork is the pixels the decoder processes, read from the head: the size of a single
// picture, and every tile of a grid however small the grid is cropped to.
func TestDecodeWork(t *testing.T) {
	c := loadCoded(t, "yuv420p", 1, 8)
	const cols = 8
	w := uint32((cols-1)*4096 + 1)
	s := loadStream(t, "flat4096")
	big := coded{hvcC: hvcCBox(1, 8, 8, 4, s.sets), data: append(lp(s.rest...), lp(s.slice)...)}
	for name, tc := range map[string]struct {
		data []byte
		want int64
	}{
		// the picture of 36x22 is coded as 40x24 and cropped: 960 pixels are decoded
		"single":                {single(c, 36, 22).bytes(), 40 * 24},
		"cropped tiles":         {gridFile(c, 36, 22, 64, 64, 64*36, 64*22, gridDesc(64, 64, 64*36, 64*22, false)).bytes(), 64 * 64 * 40 * 24},
		"grid cropped to a row": {gridFile(big, 4096, 4096, 1, cols, w, 1, gridDesc(1, cols, w, 1, true)).bytes(), cols * 4096 * 4096},
	} {
		got, err := workOf(t, name, tc.data, Limits{})
		if err != nil || got != tc.want {
			t.Errorf("%s: DecodeWork = %d, %v; want %d", name, got, err, tc.want)
		}
	}
	data := single(c, 36, 22).bytes()
	for name, tc := range map[string]struct {
		data []byte
		lim  Limits
		want error
	}{
		"a tile over the pixel limit": {gridFile(c, 8193, 8192, 1, 2, 8194, 1, gridDesc(1, 2, 8194, 1, true)).bytes(), Limits{}, ErrInvalid},
		"a file with no ftyp box":     {bytes.Replace(data, []byte("ftyp"), []byte("free"), 1), Limits{}, ErrNotHEIF},
		"a file over the limit":       {data, Limits{MaxBytes: 8}, ErrFileTooLarge},
		"a picture over the limit":    {data, Limits{MaxPixels: 100}, ErrImageTooLarge},
	} {
		if _, err := workOf(t, name, tc.data, tc.lim); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}
