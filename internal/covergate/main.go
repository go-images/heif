// Copyright (c) 2026, go-images
// SPDX-License-Identifier: BSD-3-Clause

// Command covergate reads a Go coverage profile and exits 1 unless every statement of
// it was executed.
//
//	covergate cover.out
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

var osExit = os.Exit

func main() { osExit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run returns the exit status: 0 when every statement ran, 1 when one did not, 2 on a
// bad invocation or an unreadable profile.
func run(args []string, out, errw io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errw, "usage: covergate <profile>")
		return 2
	}
	f, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintln(errw, err)
		return 2
	}
	defer f.Close()
	var total, missed int
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if !strings.HasPrefix(line, "mode:") {
				fmt.Fprintln(errw, "not a coverage profile")
				return 2
			}
			continue
		}
		fs := strings.Fields(line)
		if len(fs) != 3 {
			fmt.Fprintf(errw, "bad line %q\n", line)
			return 2
		}
		n, err1 := strconv.Atoi(fs[1])
		c, err2 := strconv.Atoi(fs[2])
		if err1 != nil || err2 != nil {
			fmt.Fprintf(errw, "bad line %q\n", line)
			return 2
		}
		total += n
		if c == 0 {
			missed += n
			fmt.Fprintf(out, "not covered: %s\n", fs[0])
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(errw, err)
		return 2
	}
	if first {
		fmt.Fprintln(errw, "empty profile")
		return 2
	}
	fmt.Fprintf(out, "%d of %d statements not covered\n", missed, total)
	if missed > 0 {
		return 1
	}
	return 0
}
