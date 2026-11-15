// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wad

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/go-quake1/engine/internal/fuzzcheck"
)

// fuzzSeeds are the committed seeds (testdata/fuzz/FuzzOpen, written by
// TestWriteFuzzSeeds): a small valid WAD2, cuts of it and corruptions of its
// header and directory, all synthetic.
func fuzzSeeds() map[string][]byte {
	valid := func() []byte {
		r := buildWAD([]spec{
			{name: "PALETTE", typ: TypPalette, payload: bytes.Repeat([]byte{0xAB}, 768)},
			{name: "Hello", typ: TypQPic, payload: []byte("payload-data")},
			{name: "empty", typ: TypLabel},
		})
		b := make([]byte, r.Len())
		_, _ = r.ReadAt(b, 0)
		return b
	}
	mut := func(f func(b []byte)) []byte {
		b := valid()
		f(b)
		return b
	}
	put := func(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
	table := func(b []byte) int { return int(binary.LittleEndian.Uint32(b[8:])) }
	return map[string][]byte{
		"valid":          valid(),
		"cut-directory":  valid()[:len(valid())-40],
		"cut-header":     valid()[:9],
		"bad-magic":      mut(func(b []byte) { b[3] = '3' }),
		"count-huge":     mut(func(b []byte) { put(b, 4, 0x7fffffff) }),
		"count-negative": mut(func(b []byte) { put(b, 4, 0xfffffff0) }),
		"table-negative": mut(func(b []byte) { put(b, 8, 0xfffffff0) }),
		"table-past-end": mut(func(b []byte) { put(b, 8, 0x7ffffff0) }),
		"lump-huge":      mut(func(b []byte) { put(b, table(b)+4, 0x7fffffff) }),
		"lump-negative":  mut(func(b []byte) { put(b, table(b)+4, 0xffffff00) }),
		"lump-pos-bad":   mut(func(b []byte) { put(b, table(b), 0xfffffff0) }),
	}
}

func TestWriteFuzzSeeds(t *testing.T) {
	if os.Getenv("WRITE_FUZZ_SEEDS") != "1" {
		t.Skip("WRITE_FUZZ_SEEDS=1 writes the seed corpus")
	}
	for name, data := range fuzzSeeds() {
		if err := fuzzcheck.WriteSeed("testdata/fuzz", "FuzzOpen", name, data); err != nil {
			t.Fatal(err)
		}
	}
}

func walk(t testing.TB, data []byte) {
	w, err := Open(bytes.NewReader(data))
	if err != nil {
		return // a refusal is the right answer to most inputs
	}
	_ = w.Lumps()
	ents, err := fs.ReadDir(w, ".")
	if err != nil {
		t.Fatalf("ReadDir of an opened WAD: %v", err)
	}
	for _, e := range ents {
		fd, err := w.Open(e.Name())
		if err != nil {
			continue // a lump the WAD cannot serve is refused, not a panic
		}
		_, _ = io.ReadAll(fd)
		_ = fd.Close()
		_, _ = fs.ReadFile(w, e.Name())
	}
}

// FuzzOpen: wad.Open and every lump of the WAD it returns, on any bytes: no
// panic, and no allocation above fuzzcheck.Limit however the header's count,
// table offset and lump sizes read.
func FuzzOpen(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzcheck.Bounded(t, len(data), func() { walk(t, data) })
	})
}

// A valid seed is served whole: parse then valid output.
func TestFuzzSeedValidReadsBack(t *testing.T) {
	w, err := Open(bytes.NewReader(fuzzSeeds()["valid"]))
	if err != nil {
		t.Fatal(err)
	}
	if w.Len() != 3 {
		t.Fatalf("%d lumps, want 3", w.Len())
	}
	b, err := fs.ReadFile(w, "hello")
	if err != nil || string(b) != "payload-data" {
		t.Fatalf("hello = %q, %v", b, err)
	}
}

// The crashers the fuzz target found, as refusals: a lump count of 2 G in a
// 200 byte WAD is an error (it used to size a 64 GiB make), and so is a lump
// whose size the archive does not hold (fs.ReadFile sized its buffer from the
// claim).
func TestHostileHeaderIsRefused(t *testing.T) {
	seeds := fuzzSeeds()
	if _, err := Open(bytes.NewReader(seeds["count-huge"])); err == nil {
		t.Error("2 G lumps in a 200 byte WAD were opened")
	}
	for _, name := range []string{"lump-huge", "lump-negative", "lump-pos-bad"} {
		w, err := Open(bytes.NewReader(seeds[name]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		refused := 0
		ents, _ := fs.ReadDir(w, ".")
		for _, e := range ents {
			if _, err := fs.ReadFile(w, e.Name()); errors.Is(err, ErrBadLump) {
				refused++
			}
		}
		if refused != 1 {
			t.Errorf("%s: %d lumps refused with ErrBadLump, want 1", name, refused)
		}
	}
}
