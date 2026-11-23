// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// withFreeBox returns the file with a free box of n body bytes after its ftyp.
func withFreeBox(t *testing.T, n int) []byte {
	t.Helper()
	data := readTestdata(t, "small.heic")
	ftyp := int(binary.BigEndian.Uint32(data[:4]))
	box := make([]byte, 8+n)
	binary.BigEndian.PutUint32(box, uint32(len(box)))
	copy(box[4:], "free")
	return append(append(append([]byte{}, data[:ftyp]...), box...), data[ftyp:]...)
}

// readHead walks past boxes it does not need in chunks of its skip buffer; a body that is
// empty, shorter than, exactly, and longer than one chunk must all land on the next box.
func TestSkippedBoxesOfEverySize(t *testing.T) {
	defer guard(t)
	want, err := DecodeConfig(bytes.NewReader(readTestdata(t, "small.heic")))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 511, 512, 513, 1024, 1300} {
		got, err := DecodeConfig(bytes.NewReader(withFreeBox(t, n)))
		if err != nil || got != want {
			t.Errorf("free box of %d bytes: %v %v, want %v", n, got, err, want)
		}
	}
}

func TestSkippedBoxCutShort(t *testing.T) {
	defer guard(t)
	for _, n := range []int{1, 512, 513, 1300} {
		data := withFreeBox(t, n)
		ftyp := int(binary.BigEndian.Uint32(data[:4]))
		cut := ftyp + 8 + n - 1 // one byte short of the body
		if _, err := DecodeConfig(bytes.NewReader(data[:cut])); !errors.Is(err, ErrTruncated) {
			t.Errorf("free box of %d bytes cut short: %v", n, err)
		}
	}
}
