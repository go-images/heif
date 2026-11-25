// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

// Limits bound what an untrusted file can make the reader read, count or allocate.
// A zero field stands for its default.
type Limits struct {
	// MaxBytes is the longest file read (default 256 MiB). It also bounds the bytes
	// one item may assemble from its extents.
	MaxBytes int64
	// MaxPixels is the most pixels of one image: a tile, a grid, the decoded base image
	// and the result (default 1<<26, which holds a 48-megapixel photo and 8192x8192).
	MaxPixels int64
	// MaxTiles is the most tiles of one grid (default 4096).
	MaxTiles int
	// MaxBoxes is the most boxes of the file, nested ones included (default 65536).
	MaxBoxes int
	// MaxDepth is the deepest box nesting, and the longest chain of derived items
	// (default 8).
	MaxDepth int
	// MaxItems is the most items, references and property associations the reader
	// accepts in one box (default 16384).
	MaxItems int
	// MaxDecoderBytes bounds the memory of the HEVC decoder's picture buffer, as the
	// decoder estimates it for the pictures the stream states: 9 bytes a luma sample of a
	// 4:2:0 picture, for each of the pictures its buffer may hold (default 4 GiB, which
	// holds the 4 pictures an x265 stream states at 8192x8192). An item holds one picture
	// and one decoder is alive at a time, so what is allocated is that of one picture.
	// It is the limit of the file, not of each tile.
	MaxDecoderBytes int64
}

// DefaultLimits are the limits a zero Limits stands for.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes:        256 << 20,
		MaxPixels:       1 << 26,
		MaxTiles:        4096,
		MaxBoxes:        1 << 16,
		MaxDepth:        8,
		MaxItems:        1 << 14,
		MaxDecoderBytes: 4 << 30,
	}
}

// withDefaults replaces the zero (and negative) fields by their defaults.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxPixels <= 0 {
		l.MaxPixels = d.MaxPixels
	}
	if l.MaxTiles <= 0 {
		l.MaxTiles = d.MaxTiles
	}
	if l.MaxBoxes <= 0 {
		l.MaxBoxes = d.MaxBoxes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxItems <= 0 {
		l.MaxItems = d.MaxItems
	}
	if l.MaxDecoderBytes <= 0 {
		l.MaxDecoderBytes = d.MaxDecoderBytes
	}
	return l
}

// Options configure Decode.
type Options struct {
	Limits Limits
}
