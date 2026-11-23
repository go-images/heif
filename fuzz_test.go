// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fuzzLimits keep every fuzz execution small: a hostile input may ask for anything.
var fuzzLimits = Limits{MaxPixels: 1 << 20, MaxBytes: 1 << 22, MaxDecoderBytes: 64 << 20}

// seedFiles are the inputs the fuzzers start from: real files and builder files that
// reach every box, version and item kind.
func seedFiles(t testing.TB) map[string][]byte {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	seeds := map[string][]byte{
		"small": read("small.heic"),
		"big":   read("big.heic"),
	}
	for _, c := range []struct {
		name         string
		chroma, bits int
	}{{"yuv420p", 1, 8}, {"gray", 0, 8}, {"yuv420p10le", 1, 10}, {"yuv422p", 2, 8}, {"yuv444p", 3, 8}} {
		seeds[c.name] = single(loadCoded(t, c.name, c.chroma, c.bits), 36, 22).bytes()
	}
	c := loadCoded(t, "yuv420p", 1, 8)
	seeds["transforms"] = single(c, 36, 22, colrNclx(1, 1, 1, true), irot(1), imir(0), clap(30, 1, 18, 1, 0, 1, 0, 1)).bytes()
	seeds["icc"] = single(c, 36, 22, colrICC([]byte("profile"))).bytes()
	grid := gridFile(c, 36, 22, 2, 2, 60, 40, gridDesc(2, 2, 60, 40, false), colrNclx(1, 1, 1, true), irot(1))
	seeds["grid"] = grid.bytes()
	grid.refs = append(grid.refs, href{"auxl", 6, []uint32{1}})
	grid.items = append(grid.items, hitem{id: 6, typ: "hvc1", data: c.data, props: []int{2, 3}})
	seeds["grid-alpha"] = grid.bytes()
	for i, v := range []vers{
		{pitm: 1, iinf: 1, iref: 1, ipma: 1, ipmaFlags: 1, infe3: true, iloc2: true},
		{iloc0: true},
	} {
		h := single(c, 36, 22)
		h.v = v
		seeds[fmt.Sprintf("versions%d", i)] = h.bytes()
	}
	h := single(c, 36, 22)
	h.items[0].split = 40
	seeds["split"] = h.bytes()
	h = single(c, 36, 22)
	h.items[0].idat = true
	seeds["idat"] = h.bytes()
	h = single(c, 36, 22)
	h.items[0].dup = 3
	seeds["overlap"] = h.bytes()
	iden := hfile{primary: 2, props: [][]byte{ispe(36, 22), c.hvcC}, items: []hitem{
		{id: 1, typ: "hvc1", data: c.data, props: []int{1, 2}},
		{id: 2, typ: "iden"},
	}, refs: []href{{"dimg", 2, []uint32{1}}}}
	seeds["iden"] = iden.bytes()
	return seeds
}

func FuzzDecode(f *testing.F) {
	for _, b := range seedFiles(f) {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, cerr := DecodeConfigOptions(bytes.NewReader(data), Options{Limits: fuzzLimits})
		if cerr != nil && !isNamed(cerr) {
			t.Fatalf("DecodeConfig: unnamed error %v", cerr)
		}
		res, err := DecodeOptions(bytes.NewReader(data), Options{Limits: fuzzLimits})
		if err != nil {
			if !isNamed(err) {
				t.Fatalf("Decode: unnamed error %v", err)
			}
			return
		}
		b := res.Image.Bounds()
		if b.Empty() {
			t.Fatalf("empty image %v", b)
		}
		if cerr == nil && (b.Dx() != cfg.Width || b.Dy() != cfg.Height) {
			t.Fatalf("Decode %v, DecodeConfig %dx%d", b, cfg.Width, cfg.Height)
		}
	})
}

// TestWriteCorpus writes the seeds as a committed fuzz corpus; run it with
// HEIF_WRITE_CORPUS=testdata/fuzz/FuzzDecode.
func TestWriteCorpus(t *testing.T) {
	dir := os.Getenv("HEIF_WRITE_CORPUS")
	if dir == "" {
		t.Skip("HEIF_WRITE_CORPUS not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, b := range seedFiles(t) {
		body := fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", b)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
