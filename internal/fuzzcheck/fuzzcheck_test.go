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
