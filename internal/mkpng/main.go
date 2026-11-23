// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

// Command mkpng writes the synthetic source images the testdata HEIC files are made
// from: mkpng <dir> writes small.png (67x45, odd sizes) and big.png (1100x700, which an
// encoder splits into a grid), each an asymmetric pattern so a rotation or mirror shows.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

func pattern(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x + y) * 255 / (w + h)), 255}
			switch {
			case x < w/4 && y < h/4: // a red block marks the top-left corner
				c = color.RGBA{230, 20, 20, 255}
			case x >= w-w/6 && y >= h-h/6: // a blue one the bottom-right
				c = color.RGBA{20, 20, 230, 255}
			case (x/8+y/8)%2 == 0 && x > w/2:
				c = color.RGBA{240, 240, 240, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkpng <dir>")
		os.Exit(2)
	}
	for name, d := range map[string][2]int{"small.png": {67, 45}, "big.png": {1100, 700}} {
		f, err := os.Create(filepath.Join(os.Args[1], name))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := png.Encode(f, pattern(d[0], d[1])); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		f.Close()
	}
}
