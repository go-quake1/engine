// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package mdl

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/go-quake1/engine/internal/fuzzcheck"
)

// fuzzSeeds are the committed seeds (testdata/fuzz/FuzzLoad, written by
// TestWriteFuzzSeeds): a small valid model with a single skin, a group skin, a
// single frame and a group frame, cuts of it and corruptions of its counts, all
// synthetic.
func fuzzSeeds() map[string][]byte {
	tv := func(a byte) TriVertx { return TriVertx{V: [3]byte{a, a + 1, a + 2}, LightNormalIndex: a} }
	valid := func() []byte {
		raw, _ := build(buildSpec{
			skinWidth: 2, skinHeight: 2, numVerts: 3, numTris: 1,
			skins: []skinSpec{
				{kind: SkinSingle, single: singleSkinSpec{pixels: []byte{1, 2, 3, 4}}},
				{kind: SkinGroup, group: groupSkinSpec{
					intervals: []float32{0.1, 0.2},
					skins:     []singleSkinSpec{{pixels: []byte{5, 6, 7, 8}}, {pixels: []byte{9, 9, 9, 9}}},
				}},
			},
			stverts:   []STVert{{S: 0, T: 0}, {S: 1, T: 0}, {OnSeam: 1, S: 0, T: 1}},
			triangles: []Triangle{{FacesFront: 1, VertIndex: [3]int32{0, 1, 2}}},
			frames: []frameSpec{
				{kind: FrameSingle, single: singleFrameSpec{bboxMin: tv(0), bboxMax: tv(9), name: "stand1", verts: []TriVertx{tv(1), tv(2), tv(3)}}},
				{kind: FrameGroup, group: groupFrameSpec{
					bboxMin: tv(0), bboxMax: tv(9), intervals: []float32{0.1, 0.2},
					frames: []singleFrameSpec{
						{bboxMin: tv(0), bboxMax: tv(9), name: "a", verts: []TriVertx{tv(1), tv(2), tv(3)}},
						{bboxMin: tv(0), bboxMax: tv(9), name: "b", verts: []TriVertx{tv(4), tv(5), tv(6)}},
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
	// Offsets: the header is 84 bytes; the first skin is a tag (4) and 4 pixels;
	// the second a tag, a count at 84+8+4, ...
	const groupSkinCount = 84 + 8 + 4
	return map[string][]byte{
		"valid":            valid(),
		"cut-frames":       valid()[:len(valid())-40],
		"cut-header":       valid()[:50],
		"bad-magic":        mut(func(b []byte) { b[1] = 'X' }),
		"bad-version":      mut(func(b []byte) { put(b, 4, 7) }),
		"skins-huge":       mut(func(b []byte) { put(b, 48, 0x7fffffff) }),
		"verts-huge":       mut(func(b []byte) { put(b, 60, 0x7fffffff) }),
		"tris-huge":        mut(func(b []byte) { put(b, 64, 0x7fffffff) }),
		"frames-huge":      mut(func(b []byte) { put(b, 68, 0x7fffffff) }),
		"skin-size-huge":   mut(func(b []byte) { put(b, 52, 0x7fffffff); put(b, 56, 0x7fffffff) }),
		"counts-negative":  mut(func(b []byte) { put(b, 60, 0xfffffff0) }),
		"group-skin-huge":  mut(func(b []byte) { put(b, groupSkinCount, 0x7fffffff) }),
		"group-skin-large": mut(func(b []byte) { put(b, groupSkinCount, 0x00ffffff) }),
		"group-frame-huge": mut(func(b []byte) {
			// the second frame's group count: find the frame tag of the group frame
			i := bytes.LastIndex(b, []byte{1, 0, 0, 0, 2, 0, 0, 0}) // FrameGroup, 2 frames
			if i >= 0 {
				put(b, i+4, 0x7fffffff)
			}
		}),
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
	m, err := Load(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return // a refusal is the right answer to most inputs
	}
	h := m.Header
	if len(m.Skins) != int(h.NumSkins) || len(m.STVerts) != int(h.NumVerts) ||
		len(m.Triangles) != int(h.NumTris) || len(m.Frames) != int(h.NumFrames) {
		t.Fatalf("tables %d/%d/%d/%d for header %d/%d/%d/%d", len(m.Skins), len(m.STVerts), len(m.Triangles), len(m.Frames),
			h.NumSkins, h.NumVerts, h.NumTris, h.NumFrames)
	}
	px := int(h.SkinWidth) * int(h.SkinHeight)
	for _, s := range m.Skins {
		switch s.Type {
		case SkinSingle:
			if len(s.Single.Pixels) != px {
				t.Fatalf("skin of %d bytes for %dx%d", len(s.Single.Pixels), h.SkinWidth, h.SkinHeight)
			}
		case SkinGroup:
			if s.Group == nil || len(s.Group.Skins) != len(s.Group.Intervals) {
				t.Fatalf("group skin: %+v", s.Group)
			}
			for _, g := range s.Group.Skins {
				if len(g.Pixels) != px {
					t.Fatalf("group skin of %d bytes for %dx%d", len(g.Pixels), h.SkinWidth, h.SkinHeight)
				}
			}
		}
	}
	for _, f := range m.Frames {
		for _, at := range []float32{0, 0.05, 1, 1e9} {
			sf := f.PoseAt(at)
			if len(sf.Verts) != 0 && len(sf.Verts) != int(h.NumVerts) {
				t.Fatalf("pose of %d vertices for NumVerts %d", len(sf.Verts), h.NumVerts)
			}
		}
	}
}

// FuzzLoad: mdl.Load and every skin, frame and vertex table it returns, on any
// bytes: no panic, and no allocation above fuzzcheck.Limit however the header's
// counts and the groups' counts read; a model that loads has the tables its
// header counts, with skins of width x height bytes and frames of NumVerts
// vertices.
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
	m, err := Load(bytes.NewReader(d), int64(len(d)))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Skins) != 2 || len(m.Frames) != 2 || len(m.Frames[1].Group.Frames) != 2 || m.Frames[0].Single.Name != "stand1" {
		t.Fatalf("model %+v", m.Header)
	}
}

// The hostile seeds are refused with an error: the group counts of 2 G (which
// used to size an 8 GiB make before the bytes they need were checked), and the
// header counts above what the file holds.
func TestHostileModelsAreRefused(t *testing.T) {
	seeds := fuzzSeeds()
	for _, name := range []string{"cut-frames", "cut-header", "bad-magic", "bad-version", "skins-huge", "verts-huge", "tris-huge", "frames-huge", "skin-size-huge", "counts-negative", "group-skin-huge", "group-skin-large", "group-frame-huge"} {
		d := seeds[name]
		if _, err := Load(bytes.NewReader(d), int64(len(d))); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}
