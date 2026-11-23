// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import "errors"

// Errors of the reader. Every error returned by this package wraps one of them
// (test with errors.Is); the text after the colon says where.
var (
	// ErrNotHEIF: the data is not a HEIF file (no ftyp box with a still-image brand).
	ErrNotHEIF = errors.New("heif: not a HEIF still-image file")
	// ErrTruncated: a box, a field or an item's data runs past the end of its container.
	ErrTruncated = errors.New("heif: truncated data")
	// ErrInvalid: the file breaks the format (a size, offset, reference or count that
	// cannot be right).
	ErrInvalid = errors.New("heif: invalid file")
	// ErrUnsupported: the file is valid but uses something this reader does not decode
	// (an item type other than hvc1, grid and iden, an overlay, a matrix, an essential
	// property it does not know).
	ErrUnsupported = errors.New("heif: unsupported feature")
	// ErrNoPrimaryItem: the file has no primary item, or it is not an image.
	ErrNoPrimaryItem = errors.New("heif: no primary image item")
	// ErrCorrupt: the HEVC bitstream of an item could not be decoded.
	ErrCorrupt = errors.New("heif: the HEVC bitstream could not be decoded")

	// ErrFileTooLarge: the file is longer than Limits.MaxBytes.
	ErrFileTooLarge = errors.New("heif: file larger than the limit")
	// ErrImageTooLarge: an image (a tile, a grid, the result) has more pixels than
	// Limits.MaxPixels, or its buffers cannot be addressed on this platform.
	ErrImageTooLarge = errors.New("heif: image larger than the limit")
	// ErrTooManyBoxes: the file holds more than Limits.MaxBoxes boxes.
	ErrTooManyBoxes = errors.New("heif: too many boxes")
	// ErrNestingTooDeep: boxes nest deeper than Limits.MaxDepth, or items derive from
	// one another deeper than that.
	ErrNestingTooDeep = errors.New("heif: boxes or derivations nested too deeply")
	// ErrTooManyItems: more than Limits.MaxItems items (or item entries).
	ErrTooManyItems = errors.New("heif: too many items")
	// ErrTooManyTiles: a grid has more than Limits.MaxTiles tiles.
	ErrTooManyTiles = errors.New("heif: too many tiles")
)
