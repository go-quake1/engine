// Copyright (c) 2026 the go-quake1/engine/authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package spr

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/go-quake1/engine/internal/fuzzcheck"
)

// fuzzSeeds are the committed seeds (testdata/fuzz/FuzzLoad, written by
// TestWriteFuzzSeeds): a small valid sprite with a single and a group frame,
// cuts of it and corruptions of its counts and sizes, all synthetic.
func fuzzSeeds() map[string][]byte {
	valid := func() []byte {
		raw, _ := build(buildSpec{
			width: 4, height: 4,
			frames: []frameSpec{
				{kind: FrameSingle, single: singleSpec{originX: -2, originY: -2, w: 4, h: 4, bitmap: bytes.Repeat([]byte{7}, 16)}},
				{kind: FrameGroup, group: groupSpec{
					intervals: []float32{0.1, 0.2},
					frames: []singleSpec{
						{w: 2, h: 2, bitmap: []byte{1, 2, 3, 4}},
						{w: 2, h: 1, bitmap: []byte{5, 6}},
					},
				}},
			},
		})
		return raw
	}
	mut := func(f func(b []byte)) []byte {
		b := valid()
		f(b)
		return b
	}
	put := func(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
	return map[string][]byte{
		"valid":             valid(),
		"cut-frame":         valid()[:60],
		"cut-header":        valid()[:20],
		"bad-magic":         mut(func(b []byte) { b[0] = 'X' }),
		"bad-version":       mut(func(b []byte) { put(b, 4, 99) }),
		"frames-huge":       mut(func(b []byte) { put(b, 24, 0x7fffffff) }),
		"frames-negative":   mut(func(b []byte) { put(b, 24, 0xffffff00) }),
		"frame-size-huge":   mut(func(b []byte) { put(b, 36+4+8, 0x7fffffff); put(b, 36+4+12, 0x7fffffff) }),
		"frame-negative":    mut(func(b []byte) { put(b, 36+4+8, 0xffffff00) }),
		"frame-bad-type":    mut(func(b []byte) { put(b, 36, 7) }),
		"group-count-huge":  mut(func(b []byte) { put(b, 36+4+16+16+4, 0x7fffffff) }),
		"group-count-large": mut(func(b []byte) { put(b, 36+4+16+16+4, 0x00ffffff) }),
	}
}

func TestWriteFuzzSeeds(t *testing.T) {
	if os.Getenv("WRITE_FUZZ_SEEDS") != "1" {
		t.Skip("WRITE_FUZZ_SEEDS=1 writes the seed corpus")
	}
	for name, data := range fuzzSeeds() {
		if err := fuzzcheck.WriteSeed("testdata/fuzz", "FuzzLoad", name, data); err != nil {
			t.Fatal(err)
		}
	}
}

func walk(t testing.TB, data []byte) {
	sp, err := Load(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return // a refusal is the right answer to most inputs
	}
	if len(sp.Frames) != int(sp.Header.NumFrames) {
		t.Fatalf("%d frames decoded for NumFrames %d", len(sp.Frames), sp.Header.NumFrames)
	}
	for i := range sp.Frames {
		for _, at := range []float32{0, 0.05, 1, 1e9} {
			if sf, err := SelectFrame(sp, i, at); err == nil {
				if len(sf.Bitmap) != int(sf.Width)*int(sf.Height) {
					t.Fatalf("frame %d: bitmap %d bytes for %dx%d", i, len(sf.Bitmap), sf.Width, sf.Height)
				}
			}
			_, _ = Flatten(sp, i, at)
		}
	}
	_, _ = SelectFrame(sp, len(sp.Frames), 0)
}

// FuzzLoad: spr.Load and the frames it returns, on any bytes: no panic, and no
// allocation above fuzzcheck.Limit however the header's frame count and the
// frames' and groups' sizes read; a sprite that loads has as many frames as its
// header says, with bitmaps of width x height bytes.
func FuzzLoad(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzcheck.Bounded(t, len(data), func() { walk(t, data) })
	})
}

// A valid seed loads whole: parse then valid output.
func TestFuzzSeedValidLoads(t *testing.T) {
	d := fuzzSeeds()["valid"]
	sp, err := Load(bytes.NewReader(d), int64(len(d)))
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.Frames) != 2 || sp.Frames[1].Group == nil || len(sp.Frames[1].Group.Frames) != 2 {
		t.Fatalf("frames %+v", sp.Frames)
	}
}

// The hostile seeds are refused with an error, not loaded and not a panic.
func TestHostileSpritesAreRefused(t *testing.T) {
	seeds := fuzzSeeds()
	for _, name := range []string{"cut-frame", "cut-header", "bad-magic", "bad-version", "frames-huge", "frames-negative", "frame-size-huge", "frame-negative", "frame-bad-type", "group-count-huge", "group-count-large"} {
		d := seeds[name]
		if _, err := Load(bytes.NewReader(d), int64(len(d))); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}
