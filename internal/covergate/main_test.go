// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name string
		args []string
		want int
		out  string
	}{
		{"covered", []string{write("a", "mode: set\nx.go:1.1,2.2 3 1\nx.go:3.1,4.2 2 5\n")}, 0, "0 of 5"},
		{"missed", []string{write("b", "mode: set\nx.go:1.1,2.2 3 1\nx.go:3.1,4.2 2 0\n")}, 1, "2 of 5"},
		{"no args", nil, 2, ""},
		{"missing", []string{filepath.Join(dir, "none")}, 2, ""},
		{"no mode", []string{write("c", "x.go:1.1,2.2 3 1\n")}, 2, ""},
		{"empty", []string{write("d", "")}, 2, ""},
		{"short line", []string{write("e", "mode: set\nx.go 3\n")}, 2, ""},
		{"bad count", []string{write("f", "mode: set\nx.go:1.1,2.2 a 1\n")}, 2, ""},
		{"bad hits", []string{write("g", "mode: set\nx.go:1.1,2.2 3 b\n")}, 2, ""},
		{"long line", []string{write("h", "mode: set\n"+strings.Repeat("x", 1<<20)+"\n")}, 2, ""},
	} {
		var out, errw bytes.Buffer
		if got := run(tc.args, &out, &errw); got != tc.want || !strings.Contains(out.String(), tc.out) {
			t.Errorf("%s: %d %q %q, want %d %q", tc.name, got, out.String(), errw.String(), tc.want, tc.out)
		}
	}
}

func TestMain_(t *testing.T) {
	old := osExit
	defer func() { osExit = old }()
	var code int
	osExit = func(c int) { code = c }
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"covergate"}
	main()
	if code != 2 {
		t.Errorf("exit %d", code)
	}
}
