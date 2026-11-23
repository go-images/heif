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
		msg  string // what stderr must hold
	}{
		{"covered", []string{write("a", "mode: set\nx.go:1.1,2.2 3 1\nx.go:3.1,4.2 2 5\n")}, 0, "0 of 5", ""},
		{"missed", []string{write("b", "mode: set\nx.go:1.1,2.2 3 1\nx.go:3.1,4.2 2 0\n")}, 1, "2 of 5", ""},
		{"no args", nil, 2, "", "usage:"},
		{"two args", []string{"a", "b"}, 2, "", "usage:"},
		{"missing", []string{filepath.Join(dir, "none")}, 2, "", "no such file"},
		{"no mode", []string{write("c", "x.go:1.1,2.2 3 1\n")}, 2, "", "not a coverage profile"},
		{"empty", []string{write("d", "")}, 2, "", "empty profile"},
		{"short line", []string{write("e", "mode: set\nx.go 3\n")}, 2, "", "bad line"},
		{"long line", []string{write("e2", "mode: set\nx.go 3 1 9\n")}, 2, "", "bad line"},
		{"bad count", []string{write("f", "mode: set\nx.go:1.1,2.2 a 1\n")}, 2, "", "bad line"},
		{"bad hits", []string{write("g", "mode: set\nx.go:1.1,2.2 3 b\n")}, 2, "", "bad line"},
		{"huge line", []string{write("h", "mode: set\n"+strings.Repeat("x", 1<<20)+"\n")}, 2, "", "too long"},
	} {
		var out, errw bytes.Buffer
		got := func() (code int) {
			defer func() {
				if r := recover(); r != nil { // a panic is a failure of this case, not of the binary
					t.Errorf("%s: panic %v", tc.name, r)
					code = -1
				}
			}()
			return run(tc.args, &out, &errw)
		}()
		if got != tc.want || !strings.Contains(out.String(), tc.out) || !strings.Contains(errw.String(), tc.msg) {
			t.Errorf("%s: %d %q %q, want %d %q %q", tc.name, got, out.String(), errw.String(), tc.want, tc.out, tc.msg)
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
