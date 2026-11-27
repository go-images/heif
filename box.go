// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"encoding/binary"
	"fmt"
)

// rd reads big-endian fields from a byte slice. The first read past the end sets bad
// and every read after it returns zero, so a parser checks once, at its end.
type rd struct {
	b   []byte
	bad bool
}

func (r *rd) take(n int) []byte {
	if r.bad || n < 0 || len(r.b) < n {
		r.bad = true
		r.b = nil
		return nil
	}
	t := r.b[:n:n]
	r.b = r.b[n:]
	return t
}

func (r *rd) u8() uint8 {
	if t := r.take(1); t != nil {
		return t[0]
	}
	return 0
}

func (r *rd) u16() uint16 {
	if t := r.take(2); t != nil {
		return binary.BigEndian.Uint16(t)
	}
	return 0
}

func (r *rd) u32() uint32 {
	if t := r.take(4); t != nil {
		return binary.BigEndian.Uint32(t)
	}
	return 0
}

func (r *rd) u64() uint64 {
	if t := r.take(8); t != nil {
		return binary.BigEndian.Uint64(t)
	}
	return 0
}

// uvar reads a field of size 0, 4 or 8 bytes (the sizes iloc allows); 0 reads as 0.
func (r *rd) uvar(size int) uint64 {
	switch size {
	case 4:
		return uint64(r.u32())
	case 8:
		return r.u64()
	}
	return 0
}

// idv reads an item identifier: 16 bits, or 32 when wide.
func (r *rd) idv(wide bool) uint32 {
	if wide {
		return r.u32()
	}
	return uint32(r.u16())
}

func (r *rd) fourcc() string { return string(r.take(4)) }

// rest returns what is left and consumes it.
func (r *rd) rest() []byte {
	t := r.b
	r.b = nil
	return t
}

// full reads the version and flags of a FullBox.
func (r *rd) full() (version uint8, flags uint32) {
	v := r.u32()
	return uint8(v >> 24), v & 0xFFFFFF
}

func (r *rd) err(what string) error {
	if r.bad {
		return fmt.Errorf("%w: %s", ErrTruncated, what)
	}
	return nil
}

// box is one ISO base media file format box: its type and its payload (after the header,
// and after the 16 bytes of a uuid type).
type box struct {
	typ  string
	body []byte
}

// parser walks box lists under the limits of the file.
type parser struct {
	lim   Limits
	boxes int
}

// list splits b into the boxes it holds (ISO/IEC 14496-12 4.2): a 32-bit size, a type,
// a 64-bit size when the 32-bit one is 1, "to the end" when it is 0.
func (p *parser) list(b []byte, depth int) ([]box, error) {
	if depth > p.lim.MaxDepth {
		return nil, ErrNestingTooDeep
	}
	var out []box
	for len(b) > 0 {
		p.boxes++
		if p.boxes > p.lim.MaxBoxes {
			return nil, ErrTooManyBoxes
		}
		r := rd{b: b}
		size := uint64(r.u32())
		typ := r.fourcc()
		hdr := uint64(8)
		if size == 1 {
			size = r.u64()
			hdr = 16
		}
		if typ == "uuid" {
			r.take(16)
			hdr += 16
		}
		if err := r.err("box header"); err != nil {
			return nil, err
		}
		if size == 0 {
			size = uint64(len(b))
		}
		if size < hdr {
			return nil, fmt.Errorf("%w: box %q of %d bytes is shorter than its header", ErrInvalid, typ, size)
		}
		if size > uint64(len(b)) {
			return nil, fmt.Errorf("%w: box %q of %d bytes runs past its container (%d bytes left)", ErrTruncated, typ, size, len(b))
		}
		out = append(out, box{typ: typ, body: b[hdr:size]})
		b = b[size:]
	}
	return out, nil
}
