// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func readTestdata(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// planesMD5 hashes an *image.YCbCr as ffmpeg's rawvideo output does: the visible rows of
// Y, then Cb, then Cr.
func planesMD5(t testing.TB, img image.Image) string {
	t.Helper()
	y, ok := img.(*image.YCbCr)
	if !ok {
		t.Fatalf("%T is not an *image.YCbCr", img)
	}
	h := md5.New()
	r := y.Rect
	for row := 0; row < r.Dy(); row++ {
		h.Write(y.Y[row*y.YStride:][:r.Dx()])
	}
	cw, ch := (r.Dx()+1)/2, (r.Dy()+1)/2
	for _, p := range [][]byte{y.Cb, y.Cr} {
		for row := 0; row < ch; row++ {
			h.Write(p[row*y.CStride:][:cw])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// psnr is the peak signal to noise ratio over R, G and B of two images of one size.
func psnr(a, b image.Image) float64 {
	var sum float64
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			for _, d := range [3]float64{float64(ar) - float64(br), float64(ag) - float64(bg), float64(ab) - float64(bb)} {
				sum += (d / 257) * (d / 257)
			}
		}
	}
	return 10 * math.Log10(255*255/(sum/float64(3*r.Dx()*r.Dy())))
}

// big.heic was made by Apple's encoder (sips -s format heic, from internal/mkpng's
// big.png): a grid of tiles. The md5 is ffmpeg's rawvideo yuv420p output of the file, the
// exact oracle for the decoded planes.
func TestGridMatchesFFmpeg(t *testing.T) {
	img, err := Decode(bytes.NewReader(readTestdata(t, "big.heic")))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 1100 || b.Dy() != 700 {
		t.Errorf("bounds %v", b)
	}
	if got, want := planesMD5(t, img), "ef46b0c937d3b18deaab84dffbfd3130"; got != want {
		t.Errorf("planes md5 %s, want ffmpeg's %s", got, want)
	}
}

// small.heic (67x45) carries a clean aperture over a 68x46 coded picture, which ffmpeg
// does not apply, so its oracle is Apple's own decode (sips -s format png), compared by
// PSNR: the two differ in colour conversion only (measured 52 dB).
func TestSmallMatchesApple(t *testing.T) {
	img, err := Decode(bytes.NewReader(readTestdata(t, "small.heic")))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := png.Decode(bytes.NewReader(readTestdata(t, "small.sips.png")))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds() != ref.Bounds() {
		t.Fatalf("bounds %v, Apple's %v", img.Bounds(), ref.Bounds())
	}
	if p := psnr(img, ref); p < 45 {
		t.Errorf("PSNR against Apple's decode %.1f dB", p)
	}
}
