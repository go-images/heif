// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

// Command mkhevc encodes one intra picture of each pixel format the tests need with
// ffmpeg's libx265: mkhevc <source.png> <dir> writes <dir>/<pix_fmt>.hevc, Annex B
// streams that the tests wrap in HEIF items.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var formats = []string{"gray", "gray10le", "yuv420p", "yuv420p10le", "yuv422p", "yuv444p", "yuv444p10le", "yuv420p12le"}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mkhevc source.png dir")
		os.Exit(2)
	}
	for _, pf := range formats {
		cmd := exec.Command("ffmpeg", "-v", "error", "-y", "-i", os.Args[1], "-frames:v", "1",
			"-vf", "scale=36:22", "-pix_fmt", pf, "-c:v", "libx265",
			"-x265-params", "log-level=error:keyint=1", "-f", "hevc", filepath.Join(os.Args[2], pf+".hevc"))
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, pf, err)
			os.Exit(1)
		}
	}
}
