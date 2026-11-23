// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package heif

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLocalFiles decodes every file of the directories named in HEIF_LOCAL_DIRS (a path
// list) and of the files named in HEIF_LOCAL_FILES. The Nokia conformance files and
// libheif's example.heic carry no licence that lets them be committed, so they are read
// from a local clone and the test is skipped when none is named. Each file must decode
// or end in a named error, and DecodeConfig must agree with Decode.
func TestLocalFiles(t *testing.T) {
	var files []string
	for _, d := range filepath.SplitList(os.Getenv("HEIF_LOCAL_DIRS")) {
		m, err := filepath.Glob(filepath.Join(d, "*.heic"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	files = append(files, filepath.SplitList(os.Getenv("HEIF_LOCAL_FILES"))...)
	if len(files) == 0 {
		t.Skip("HEIF_LOCAL_DIRS and HEIF_LOCAL_FILES not set")
	}
	var ok, refused int
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		res, err := DecodeOptions(f, Options{})
		f.Close()
		if err != nil {
			if !isNamed(err) {
				t.Errorf("%s: unnamed error %v", name, err)
			}
			t.Logf("%s: %v", filepath.Base(name), err)
			refused++
			continue
		}
		f, _ = os.Open(name)
		cfg, cerr := DecodeConfig(f)
		f.Close()
		b := res.Image.Bounds()
		if cerr != nil || cfg.Width != b.Dx() || cfg.Height != b.Dy() {
			t.Errorf("%s: Decode %v, DecodeConfig %+v %v", name, b, cfg, cerr)
		}
		ok++
	}
	t.Logf("%d decoded, %d refused", ok, refused)
}
