// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/go-quake1/engine/internal/fuzzcheck"
)

// picSeeds are the committed seeds (testdata/fuzz/FuzzParsePic, written by
// TestWritePicFuzzSeeds): valid lumps, cuts of them and corrupted dimensions, all
// synthetic.
func picSeeds() map[string][]byte {
	pic := &Pic{Width: 4, Height: 3, Pixels: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}}
	valid, _ := EncodePic(pic)
	big, _ := EncodePic(&Pic{Width: 64, Height: 64, Pixels: bytes.Repeat([]byte{7}, 64*64)})
	put := func(b []byte, off int, v uint32) []byte {
		c := bytes.Clone(b)
		binary.LittleEndian.PutUint32(c[off:], v)
		return c
	}
	return map[string][]byte{
		"valid":           valid,
		"valid-64x64":     big,
		"trailing":        append(bytes.Clone(valid), 0, 0, 0, 0),
		"cut-body":        valid[:len(valid)-3],
		"cut-header":      valid[:5],
		"empty":           {},
		"width-max":       put(valid, 0, PicMaxDim),
		"width-over-max":  put(valid, 0, PicMaxDim+1),
		"width-huge":      put(valid, 0, 0x7fffffff),
		"width-zero":      put(valid, 0, 0),
		"height-negative": put(valid, 4, 0xfffffff0),
		"both-huge":       put(put(valid, 0, 0x7fffffff), 4, 0x7fffffff),
	}
}

func TestWritePicFuzzSeeds(t *testing.T) {
	if os.Getenv("WRITE_FUZZ_SEEDS") != "1" {
		t.Skip("WRITE_FUZZ_SEEDS=1 writes the seed corpus")
	}
	for name, data := range picSeeds() {
		if err := fuzzcheck.WriteSeed("testdata/fuzz", "FuzzParsePic", name, data); err != nil {
			t.Fatal(err)
		}
	}
}

// FuzzParsePic: render.ParsePic on any bytes: no panic, and no allocation above
// fuzzcheck.Limit however the dimensions read (they are checked against the
// lump before the pixels are made); a pic that parses has width*height pixels and
// encodes back to the lump it was parsed from (less trailing bytes).
func FuzzParsePic(f *testing.F) {
	for _, s := range picSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzcheck.Bounded(t, len(data), func() {
			p, err := ParsePic(data)
			if err != nil {
				return
			}
			if p.Width <= 0 || p.Height <= 0 || len(p.Pixels) != p.Width*p.Height {
				t.Fatalf("pic %dx%d with %d pixels", p.Width, p.Height, len(p.Pixels))
			}
			enc, err := EncodePic(p)
			if err != nil {
				t.Fatalf("a parsed pic does not encode: %v", err)
			}
			if !bytes.Equal(enc, data[:len(enc)]) {
				t.Fatal("a parsed pic does not encode back to its lump")
			}
		})
	})
}

// The hostile seeds are refused with an error, not parsed and not a panic.
func TestHostilePicsAreRefused(t *testing.T) {
	seeds := picSeeds()
	for _, name := range []string{"cut-body", "cut-header", "empty", "width-over-max", "width-huge", "width-zero", "height-negative", "both-huge"} {
		if _, err := ParsePic(seeds[name]); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	for _, name := range []string{"valid", "valid-64x64", "trailing", "width-max"} {
		// width-max: 4096 x 3 pixels asked of a 12 byte body is a truncated lump.
		_, err := ParsePic(seeds[name])
		if name == "width-max" {
			if err == nil {
				t.Errorf("%s: parsed", name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
