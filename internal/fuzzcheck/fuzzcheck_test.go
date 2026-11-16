// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package fuzzcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder is a testing.TB whose Fatalf records instead of stopping, so the
// failing path of Bounded can be run.
type recorder struct {
	testing.TB
	failed string
}

func (r *recorder) Helper() {}
func (r *recorder) Fatalf(format string, args ...any) {
	r.failed = fmt.Sprintf(format, args...)
}

var sink []byte

func TestLimit(t *testing.T) {
	if got := Limit(1000); got != AllocBase+64*1000 {
		t.Errorf("Limit(1000) = %d", got)
	}
}

func TestBoundedPassesASmallParse(t *testing.T) {
	r := &recorder{}
	Bounded(r, 100, func() { sink = make([]byte, 1<<10) })
	if r.failed != "" {
		t.Errorf("a 1 KiB parse of 100 bytes failed: %s", r.failed)
	}
}

func TestBoundedFailsAHostileHeader(t *testing.T) {
	r := &recorder{}
	Bounded(r, 100, func() { sink = make([]byte, 64<<20) })
	if !strings.Contains(r.failed, "above the") {
		t.Errorf("a 64 MiB parse of 100 bytes did not fail: %q", r.failed)
	}
}

func TestWriteSeed(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSeed(dir, "FuzzX", "a", []byte("hi\n\x00")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "FuzzX", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "go test fuzz v1\n[]byte(\"hi\\n\\x00\")\n"; string(b) != want {
		t.Errorf("seed file %q, want %q", b, want)
	}
	// A directory that cannot be made is an error.
	if err := WriteSeed(filepath.Join(dir, "FuzzX", "a"), "T", "n", nil); err == nil {
		t.Error("a seed written under a file")
	}
}

func TestBadPathElement(t *testing.T) {
	for name, want := range map[string]bool{
		"nul": true, "NUL": true, "Nul.txt": true, "con": true, "prn": true, "aux": true,
		"com1": true, "COM9": true, "lpt1": true, "LPT9.x": true,
		"nul-byte": false, "null": false, "com0": false, "com10": false, "lpt": false, "console": false,
		"valid": false, "a.b": false, "": true, "a:b": true, "a?": true, "a*b": true, "a|b": true,
		"a<b": true, "a>b": true, `a"b`: true, `a\b`: true,
	} {
		if got := BadPathElement(name); got != want {
			t.Errorf("BadPathElement(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestWriteSeedRefusesAWindowsName(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSeed(dir, "FuzzX", "nul", []byte("x")); err == nil {
		t.Error("a seed named nul was written")
	}
	if _, err := os.Stat(filepath.Join(dir, "FuzzX", "nul")); err == nil {
		t.Error("the refused seed exists")
	}
}

// TestRepositoryNamesBuildAModuleZip walks the whole module: a file or a
// directory whose name Windows cannot hold makes `go get` of every release
// containing it fail ("malformed file path"), which is how v0.17.0 to v0.19.0
// came out: a seed corpus file named "nul".
func TestRepositoryNamesBuildAModuleZip(t *testing.T) {
	root := filepath.Join("..", "..")
	n := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		n++
		if p != root && BadPathElement(d.Name()) {
			t.Errorf("%s: not a name a Go module can hold on Windows", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 100 {
		t.Errorf("walked %d files: the walk did not reach the module", n)
	}
}
