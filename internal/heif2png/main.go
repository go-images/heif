// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

// Command heif2png decodes a HEIF file and writes it as PNG: heif2png in.heic out.png.
// It is the development tool the testdata was checked with.
package main

import (
	"fmt"
	"image/png"
	"os"

	"github.com/go-images/heif"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: heif2png in.heic out.png")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	img, err := heif.Decode(f)
	if err != nil {
		return err
	}
	o, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := png.Encode(o, img); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}
