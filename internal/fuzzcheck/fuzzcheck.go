// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package fuzzcheck is what the fuzz targets of the parsers share: the bound
// on what a parse may allocate, and the writer of the seed corpus files.
package fuzzcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	// AllocBase is what any parse may allocate whatever the input.
	AllocBase = 16 << 20
	// AllocPerByte is what a parse may allocate for each byte of input, on top
	// of AllocBase: the parsed form of a valid file is a small multiple of it, a
	// count or size field in a header must not buy more than that.
	AllocPerByte = 64
)

// Limit is the most a parse of n input bytes may allocate.
func Limit(n int) uint64 { return AllocBase + AllocPerByte*uint64(n) }

// Bounded runs f, which parses and walks n bytes of input, and fails t if it
// allocated more than [Limit] bytes in all (runtime.MemStats.TotalAlloc, which
// counts allocations whether or not they were freed): a hostile count or size
// in a header must be refused before the make that it would size.
func Bounded(t testing.TB, n int, f func()) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > Limit(n) {
		t.Fatalf("allocated %d bytes for %d bytes of input, above the %d bound", got, n, Limit(n))
	}
}

// BadPathElement reports whether name, one element of a file path, makes the
// module zip of a release fail (cmd/go: "malformed file path"): a Windows
// device name (CON, PRN, AUX, NUL, COM1-9, LPT1-9, with or without an
// extension, in any case), or a character Windows forbids in a file name. A
// corpus file named "nul" broke the module zip of v0.17.0, v0.18.0 and v0.19.0:
// `go get` of them failed.
func BadPathElement(name string) bool {
	if name == "" || strings.ContainsAny(name, "<>:\"|?*\\") {
		return true
	}
	base, _, _ := strings.Cut(name, ".")
	switch up := strings.ToUpper(base); up {
	case "CON", "PRN", "AUX", "NUL":
		return true
	default:
		return len(up) == 4 && (strings.HasPrefix(up, "COM") || strings.HasPrefix(up, "LPT")) && up[3] >= '1' && up[3] <= '9'
	}
}

// WriteSeed writes data as seed file name of fuzz target target, in the Go
// fuzz corpus format, under dir (the package's testdata/fuzz).
func WriteSeed(dir, target, name string, data []byte) error {
	if BadPathElement(name) {
		return fmt.Errorf("fuzzcheck: seed name %q cannot be a file in a Go module (a Windows device name or an invalid character): the module zip of every release would fail", name)
	}
	d := filepath.Join(dir, target)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, name), []byte(fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", data)), 0o644)
}
