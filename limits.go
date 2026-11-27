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
	//
	// It sets the memory of a decode: the planes of the HEVC decoder and the converted
	// image are alive together, 1.5 bytes a pixel for the planes of an 8-bit 4:2:0 picture
	// and 4 for its RGBA result, 5.5 bytes a pixel live at the peak. MaxMemory bounds
	// that product; MaxPixels bounds the size of a picture (a value above 2^40 is
	// lowered to 2^40).
	MaxPixels int64
	// MaxMemory is the most live data a decode may need, estimated before anything is
	// decoded from the parameter sets (default 320 MiB): the planes of the coded picture
	// (1.5 bytes a pixel for 8-bit 4:2:0, 6 for 16-bit 4:4:4; a grid holds those of one
	// tile and of the assembled picture) plus the converted result (4 bytes a pixel, 8
	// for 16 bits, 1 or 2 for grey). It is an estimate of live data, not a bound of the
	// heap: the peak heap, the garbage the collector has not yet taken included, is about
	// 1.25 times MaxMemory (measured 437 MiB against an estimate of 352, so about 400 MiB
	// at the 320 MiB default). It does not count the input: reading the file and
	// assembling an item hold up to 2 times MaxBytes besides. At the default a
	// 48-megapixel 8-bit 4:2:0 photo (about 250 MiB) is decoded, 8192x8192 8-bit 4:2:0
	// (352 MiB) is not, 10-bit 4:2:2 (12 bytes a pixel) stops at about 28 megapixels and
	// 16-bit 4:4:4 (14) at about 24. DecodeWork applies the same check.
	MaxMemory int64
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

// maxPixelsCap is the largest MaxPixels taken: a larger value is lowered to it, so that the
// products of a pixel count with a byte cost cannot wrap an int64 (2^40 pixels is a million
// times the default's reach of any decoder here).
const maxPixelsCap = 1 << 40

// DefaultLimits are the limits a zero Limits stands for.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes:        256 << 20,
		MaxPixels:       1 << 26,
		MaxMemory:       320 << 20,
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
	if l.MaxPixels > maxPixelsCap {
		l.MaxPixels = maxPixelsCap
	}
	if l.MaxMemory <= 0 {
		l.MaxMemory = d.MaxMemory
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
