// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package pak

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
// TestWriteFuzzSeeds): a small valid pak, cuts of it and a few corruptions of
// its header, all synthetic.
func fuzzSeeds() map[string][]byte {
	valid := func() []byte {
		r := buildPAK(map[string][]byte{
			"maps/e1m1.bsp":     bytes.Repeat([]byte{1, 2, 3, 4}, 16),
			"progs.dat":         []byte("progs"),
			"gfx/palette.lmp":   bytes.Repeat([]byte{9}, 768),
			"sound/misc/a.wav":  nil,
			"models/player.mdl": {0xff},
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
	return map[string][]byte{
		"valid":           valid(),
		"cut-directory":   valid()[:len(valid())-100],
		"cut-header":      valid()[:7],
		"bad-magic":       mut(func(b []byte) { b[0] = 'X' }),
		"dirlen-huge":     mut(func(b []byte) { put(b, 8, 0x7fffffc0) }),
		"dirlen-odd":      mut(func(b []byte) { put(b, 8, 100) }),
		"dirofs-negative": mut(func(b []byte) { put(b, 4, 0xfffffff0) }),
		"dirofs-past-end": mut(func(b []byte) { put(b, 4, 0x7ffffff0) }),
		"entry-huge": mut(func(b []byte) {
			dirofs := int(binary.LittleEndian.Uint32(b[4:]))
			put(b, dirofs+60, 0x7fffffff) // first entry's size
		}),
		"entry-negative-size": mut(func(b []byte) {
			dirofs := int(binary.LittleEndian.Uint32(b[4:]))
			put(b, dirofs+60, 0xffffff00)
		}),
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

// walk opens src as a pak and reads every entry through the fs.FS API.
func walk(t testing.TB, data []byte) {
	f, err := Open(bytes.NewReader(data))
	if err != nil {
		return // a refusal is the right answer to most inputs
	}
	ents, err := fs.ReadDir(f, ".")
	if err != nil {
		t.Fatalf("ReadDir of an opened pak: %v", err)
	}
	if len(ents) > f.Len() {
		t.Fatalf("%d names for %d entries", len(ents), f.Len())
	}
	for _, e := range ents {
		fd, err := f.Open(e.Name())
		if err != nil {
			continue // an entry the pak cannot serve is refused, not a panic
		}
		_, _ = io.ReadAll(fd)
		_ = fd.Close()
		_, _ = fs.ReadFile(f, e.Name())
		_, _ = fs.Stat(f, e.Name())
	}
}

// FuzzOpen: pak.Open and every entry of the pak it returns, on any bytes: no
// panic, and no allocation above fuzzcheck.Limit however the header's offset,
// length and entry sizes read.
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
	f, err := Open(bytes.NewReader(fuzzSeeds()["valid"]))
	if err != nil {
		t.Fatal(err)
	}
	if f.Len() != 5 {
		t.Fatalf("%d entries, want 5", f.Len())
	}
	b, err := fs.ReadFile(f, "progs.dat")
	if err != nil || string(b) != "progs" {
		t.Fatalf("progs.dat = %q, %v", b, err)
	}
}

// The crashers the fuzz target found, as refusals: a directory length that
// claims 2 GiB in a 1 KiB archive is an error (it used to allocate 2 GiB), and
// so is an entry whose size the archive does not hold (fs.ReadFile sized its
// buffer from the claim).
func TestHostileHeaderIsRefused(t *testing.T) {
	seeds := fuzzSeeds()
	if _, err := Open(bytes.NewReader(seeds["dirlen-huge"])); err == nil {
		t.Error("a 2 GiB directory in a 1 KiB archive was opened")
	}
	for _, name := range []string{"entry-huge", "entry-negative-size"} {
		f, err := Open(bytes.NewReader(seeds[name]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var refused int
		ents, _ := fs.ReadDir(f, ".")
		for _, e := range ents {
			if _, err := fs.ReadFile(f, e.Name()); errors.Is(err, ErrBadEntry) {
				refused++
			}
		}
		if refused != 1 {
			t.Errorf("%s: %d entries refused with ErrBadEntry, want 1", name, refused)
		}
	}
}
