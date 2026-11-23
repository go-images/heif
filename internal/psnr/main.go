// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

// Command psnr prints the PSNR in dB between two PNG files of the same size over the
// R, G and B channels: psnr a.png b.png. It is how a decode is compared with another
// decoder's output (Apple's sips, ffmpeg).
package main

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
)

func load(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func psnr(a, b image.Image) (float64, error) {
	if a.Bounds() != b.Bounds() {
		return 0, fmt.Errorf("sizes differ: %v and %v", a.Bounds(), b.Bounds())
	}
	var sum float64
	var n int
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			for _, d := range [3]float64{float64(ar) - float64(br), float64(ag) - float64(bg), float64(ab) - float64(bb)} {
				sum += (d / 257) * (d / 257)
				n++
			}
		}
	}
	if sum == 0 {
		return math.Inf(1), nil
	}
	return 10 * math.Log10(255*255/(sum/float64(n))), nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: psnr a.png b.png")
		os.Exit(2)
	}
	a, err := load(os.Args[1])
	if err == nil {
		var b image.Image
		if b, err = load(os.Args[2]); err == nil {
			var p float64
			if p, err = psnr(a, b); err == nil {
				fmt.Printf("%.2f dB\n", p)
				return
			}
		}
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
