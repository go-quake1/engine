// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bspfile_test

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bspfile/synthbsp"
	"github.com/go-quake1/engine/internal/fuzzcheck"
	"github.com/go-quake1/engine/model"
)

//go:embed testdata/fuzz/FuzzOpen/crasher-node-cycle
var crasherNodeCycle string

// fuzzSeeds are the committed seeds (testdata/fuzz/FuzzOpen, written by
// TestWriteFuzzSeeds): the synthetic maps of synthbsp, cuts of them and
// corruptions of the lump table (offsets and lengths), all synthetic.
func fuzzSeeds(t testing.TB) map[string][]byte {
	pvs, _, err := synthbsp.BuildFiveLeafPVS()
	if err != nil {
		t.Fatal(err)
	}
	faces, _, err := synthbsp.BuildWithFaces()
	if err != nil {
		t.Fatal(err)
	}
	put := func(b []byte, off int, v uint32) []byte {
		c := bytes.Clone(b)
		binary.LittleEndian.PutUint32(c[off:], v)
		return c
	}
	lumpOfs := func(i int) int { return 4 + i*8 }
	lumpLen := func(i int) int { return 4 + i*8 + 4 }
	return map[string][]byte{
		"pvs":                pvs,
		"faces":              faces,
		"header-only":        faces[:124],
		"cut-lumps":          faces[:len(faces)/2],
		"cut-header":         faces[:30],
		"bad-version":        put(faces, 0, 30),
		"entities-huge":      put(faces, lumpLen(int(bspfile.LumpEntities)), 0x7fffffff),
		"faces-negative":     put(faces, lumpLen(int(bspfile.LumpFaces)), 0xffffff00),
		"faces-ofs-past-end": put(faces, lumpOfs(int(bspfile.LumpFaces)), 0x7ffffff0),
		// A zero-length lump whose offset is outside the file: no byte is wanted, but
		// the accessors slice the file at that offset.
		"empty-lump-ofs-past-end": put(put(faces, lumpLen(int(bspfile.LumpVisibility)), 0), lumpOfs(int(bspfile.LumpVisibility)), 0x7ffffff0),
		"textures-count-huge": func() []byte {
			b := bytes.Clone(faces)
			ofs := int(binary.LittleEndian.Uint32(b[lumpOfs(int(bspfile.LumpTextures)):]))
			binary.LittleEndian.PutUint32(b[ofs:], 0x7fffffff)
			return b
		}(),
		"textures-count-negative": func() []byte {
			b := bytes.Clone(faces)
			ofs := int(binary.LittleEndian.Uint32(b[lumpOfs(int(bspfile.LumpTextures)):]))
			binary.LittleEndian.PutUint32(b[ofs:], 0xfffffff0)
			return b
		}(),
	}
}

func TestWriteFuzzSeeds(t *testing.T) {
	if os.Getenv("WRITE_FUZZ_SEEDS") != "1" {
		t.Skip("WRITE_FUZZ_SEEDS=1 writes the seed corpus")
	}
	for name, data := range fuzzSeeds(t) {
		if err := fuzzcheck.WriteSeed("testdata/fuzz", "FuzzOpen", name, data); err != nil {
			t.Fatal(err)
		}
	}
}

// walk opens data as a map and calls every accessor, walks what they return
// and loads every brush model.
func walk(t testing.TB, data []byte) {
	f, err := bspfile.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return // a refusal is the right answer to most inputs
	}
	for k := bspfile.LumpKind(0); k < bspfile.HeaderLumps; k++ {
		_ = f.LumpBytes(k)
	}
	_ = f.Entities()
	_ = f.Visibility()
	_ = f.Lighting()
	_, _ = f.Vertexes()
	_, _ = f.Edges()
	_, _ = f.Surfedges()
	_, _ = f.Planes()
	_, _ = f.Nodes()
	_, _ = f.ClipNodes()
	_, _ = f.Leafs()
	_, _ = f.Faces()
	_, _ = f.TexInfos()
	_, _ = f.MarkSurfaces()
	if tl, err := f.Textures(); err == nil {
		for i := range tl.Offsets {
			mt, ok, err := tl.MipTex(i)
			if err != nil || !ok {
				continue
			}
			for lv := 0; lv < bspfile.MipLevels; lv++ {
				if px, err := mt.Pixels(lv); err == nil {
					_ = px[len(px)-1]
				}
			}
		}
	}
	if ms, err := f.Models(); err == nil {
		for i := range ms {
			_, _ = model.LoadBrush(f, i)
		}
	}
}

// FuzzOpen: bspfile.Open, every lump accessor and model.LoadBrush on any
// bytes: no panic, and no allocation above fuzzcheck.Limit however the lump
// table and the texture directory's count read.
func FuzzOpen(f *testing.F) {
	for _, s := range fuzzSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzcheck.Bounded(t, len(data), func() { walk(t, data) })
	})
}

// A valid seed is served whole: parse then valid output.
func TestFuzzSeedValidParses(t *testing.T) {
	d := fuzzSeeds(t)["faces"]
	f, err := bspfile.Open(bytes.NewReader(d), int64(len(d)))
	if err != nil {
		t.Fatal(err)
	}
	if fs, err := f.Faces(); err != nil || len(fs) == 0 {
		t.Fatalf("faces: %d, %v", len(fs), err)
	}
	if ms, err := f.Models(); err != nil || len(ms) == 0 {
		t.Fatalf("models: %d, %v", len(ms), err)
	}
}

// The second crasher the fuzz target found (testdata/fuzz/FuzzOpen/crasher-node-cycle):
// a node table whose children point back up the tree sent model.LoadBrush's
// parent walk into unbounded recursion (a fatal stack overflow, which no recover
// stops). LoadBrush now refuses it.
func TestCyclicNodeTableIsRefused(t *testing.T) {
	// The crasher is a corpus file, embedded (the emulated CI lanes run the test
	// binary outside the package directory); its bytes are in the corpus format.
	raw := crasherNodeCycle
	var data []byte
	if _, err := fmt.Sscanf(raw[strings.IndexByte(raw, '\n')+1:], "[]byte(%q)", &data); err != nil {
		t.Fatal(err)
	}
	f, err := bspfile.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	ms, err := f.Models()
	if err != nil || len(ms) == 0 {
		t.Fatalf("models: %d, %v", len(ms), err)
	}
	refused := 0
	for i := range ms {
		if _, err := model.LoadBrush(f, i); errors.Is(err, model.ErrNodeCycle) {
			refused++
		}
	}
	if refused == 0 {
		t.Error("no submodel of the cyclic map was refused with ErrNodeCycle")
	}
}
