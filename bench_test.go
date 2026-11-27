// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"testing"
)

func benchDecode(b *testing.B, name string) {
	data := readTestdata(b, name)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Decode(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeGrid decodes testdata/big.heic, a grid of tiles; BenchmarkDecodeSmall a
// single 67x45 picture.
func BenchmarkDecodeGrid(b *testing.B)  { benchDecode(b, "big.heic") }
func BenchmarkDecodeSmall(b *testing.B) { benchDecode(b, "small.heic") }
