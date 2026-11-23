// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

// Package heif reads HEIF/HEIC still images (ISO/IEC 23008-12 over the ISO base media
// file format of ISO/IEC 14496-12) in pure Go. The HEVC pictures are decoded by
// github.com/go-avkit/h265.
//
// What it reads: the primary item of a file whose ftyp brands include heic, heix, hevc,
// hevx, heim, heis, mif1 or msf1, when that item is an hvc1 image, a grid of hvc1 tiles,
// or an identity (iden) of one of them, with the transformative properties irot, imir
// and clap applied in the order the file lists them. 8-bit and deeper (to 16-bit)
// 4:0:0, 4:2:0, 4:2:2 and 4:4:4 pictures are supported. Colour is converted with the
// matrix and range of the nclx colr property (BT.601 full range without one); the
// primaries and transfer characteristic are not applied, and an ICC profile is returned,
// not applied.
//
// What it does not read: alpha auxiliary images (Result.HasAlpha says one exists), depth
// maps, HDR gain maps, overlays (iovl), thumbnails, image sequences, other codecs
// (AV1, JPEG, VVC), and items stored in another file.
//
// The input is untrusted: see Limits.
package heif

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
)

func init() {
	for _, brand := range []string{"heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1"} {
		image.RegisterFormat("heif", "????ftyp"+brand, Decode, DecodeConfig)
	}
}

// Result is a decoded file.
type Result struct {
	// Image is the primary image, transformations applied. It is an *image.YCbCr or
	// *image.Gray sharing the HEVC decoder's planes when no transformation applies
	// and the picture is 8-bit full-range BT.601, else an *image.RGBA, *image.RGBA64,
	// *image.Gray or *image.Gray16.
	Image image.Image
	// ICCProfile is the colr ICC profile of the primary item, as stored (nil if none).
	ICCProfile []byte
	// Nclx is the nclx colr property of the primary item (nil if none).
	Nclx *Nclx
	// HasAlpha says an alpha plane is attached to the primary item; it is not decoded.
	HasAlpha bool
}

// Nclx is a colour description by code points (ITU-T H.273): colour primaries, transfer
// characteristics, matrix coefficients and range.
type Nclx = nclx

// Decode reads the primary image of a HEIF file with the default limits.
func Decode(r io.Reader) (image.Image, error) {
	res, err := DecodeOptions(r, Options{})
	if err != nil {
		return nil, err
	}
	return res.Image, nil
}

// DecodeOptions reads the primary image of a HEIF file under the given options.
func DecodeOptions(r io.Reader, o Options) (*Result, error) {
	lim := o.Limits.withDefaults()
	// one byte more than the limit tells a file of exactly the limit from a longer one; at
	// the largest limit there is no byte more to read, and no longer file to tell
	n := lim.MaxBytes
	if n < math.MaxInt64 {
		n++
	}
	data, err := io.ReadAll(io.LimitReader(r, n))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > lim.MaxBytes {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrFileTooLarge, lim.MaxBytes)
	}
	f, err := parseFile(data, lim)
	if err != nil {
		return nil, err
	}
	pl, err := f.primaryPlan()
	if err != nil {
		return nil, err
	}
	pic, err := f.decodeBase(pl)
	if err != nil {
		return nil, err
	}
	img, err := render(pic, pl.nclx, pl.place)
	if err != nil {
		return nil, err
	}
	return &Result{Image: img, ICCProfile: pl.icc, Nclx: pl.nclx, HasAlpha: pl.alpha}, nil
}

func (f *file) primaryPlan() (*plan, error) {
	if !f.havePrimary {
		return nil, fmt.Errorf("%w: no pitm box", ErrNoPrimaryItem)
	}
	pl, err := f.resolve(f.primary, 0)
	if err != nil {
		return nil, err
	}
	return pl, nil
}

// DecodeConfig returns the size and colour model of the primary image without decoding
// it; only the boxes up to meta are read.
func DecodeConfig(r io.Reader) (image.Config, error) {
	return DecodeConfigOptions(r, Options{})
}

// DecodeConfigOptions is DecodeConfig under the given options.
func DecodeConfigOptions(r io.Reader, o Options) (image.Config, error) {
	lim := o.Limits.withDefaults()
	head, err := readHead(r, lim.MaxBytes)
	if err != nil {
		return image.Config{}, err
	}
	f, err := parseFile(head, lim)
	if err != nil {
		return image.Config{}, err
	}
	f.cfg = true
	pl, err := f.primaryPlan()
	if err != nil {
		return image.Config{}, err
	}
	w, h := pl.place.dims(int(pl.w), int(pl.h))
	return image.Config{ColorModel: f.colorModel(pl), Width: w, Height: h}, nil
}

// colorModel is the colour model of the image Decode returns for a plan, which depends
// on the bit depth of the item's hvcC and on whether a transformation applies.
func (f *file) colorModel(pl *plan) color.Model {
	coded := pl.base
	if pl.grid != nil {
		coded = pl.grid.tiles[0]
	}
	wide, mono := false, false
	if ps, err := f.propsOf(coded); err == nil {
		if hp := find(ps, "hvcC"); hp != nil {
			wide = hp.hvcc.lumaBits > 8 || hp.hvcc.chromaBits > 8
			mono = hp.hvcc.chroma == 0
		}
	}
	matrix, full := colourOf(pl.nclx)
	switch {
	case mono && !wide:
		return color.GrayModel
	case mono:
		return color.Gray16Model
	case wide:
		return color.RGBA64Model
	case pl.place.plain() && full && (matrix == 2 || matrix == 5 || matrix == 6):
		return color.YCbCrModel
	}
	return color.RGBAModel
}

// blackHole discards what is written to it. It has no ReadFrom, unlike io.Discard, whose
// pooled 8 KiB buffer the race detector keeps emptying: skipping a stream of small boxes
// with io.Discard allocated 273 MiB under -race.
type blackHole struct{}

func (blackHole) Write(p []byte) (int, error) { return len(p), nil }

// readHead reads the top-level boxes of a file up to and including meta, skipping the
// others (mdat in particular), and returns ftyp and meta joined. A box declared to run
// to the end of the file ends the search.
func readHead(r io.Reader, max int64) ([]byte, error) {
	var out []byte
	var read int64
	var hdr [16]byte // one for the whole walk, not one per box
	for {
		if _, err := io.ReadFull(r, hdr[:8]); err != nil {
			return nil, fmt.Errorf("%w: no meta box before the end of the file", ErrTruncated)
		}
		size := uint64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		hl := uint64(8)
		if size == 1 {
			if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
				return nil, fmt.Errorf("%w: box header", ErrTruncated)
			}
			size, hl = binary.BigEndian.Uint64(hdr[8:16]), 16
		}
		if size == 0 || size < hl {
			return nil, fmt.Errorf("%w: box %q of %d bytes before meta", ErrInvalid, typ, size)
		}
		if size > uint64(max-read) {
			return nil, fmt.Errorf("%w: box %q of %d bytes", ErrFileTooLarge, typ, size)
		}
		read += int64(size)
		body := int64(size - hl)
		if typ != "ftyp" && typ != "meta" {
			if _, err := io.CopyN(blackHole{}, r, body); err != nil {
				return nil, fmt.Errorf("%w: box %q", ErrTruncated, typ)
			}
			continue
		}
		// the buffer grows with the bytes that arrive, not with the size the box declares
		buf := bytes.NewBuffer(append(out, hdr[:hl]...))
		if _, err := io.CopyN(buf, r, body); err != nil {
			return nil, fmt.Errorf("%w: box %q", ErrTruncated, typ)
		}
		out = buf.Bytes()
		if typ == "meta" {
			return out, nil
		}
	}
}
