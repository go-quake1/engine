// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"testing"

	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/worldview"
)

func TestPackerDedupesByKey(t *testing.T) {
	p := newShelf(64)
	key := new(int)
	a, _ := p.place(key, 1000, 10, func(a *Atlas, r Rect) { blitRows(a, r, make([]byte, 10000), 1000) })
	b, _ := p.place(new(int), 1000, 20, func(a *Atlas, r Rect) { blitRows(a, r, make([]byte, 20000), 1000) })
	if a != (Rect{X: 0, Y: 0, W: 1000, H: 10}) || b != (Rect{X: 1000, Y: 0, W: 1000, H: 20}) {
		t.Fatalf("first row: %v %v", a, b)
	}
	if again, _ := p.place(key, 1, 1, nil); again != a {
		t.Fatal("the same key must give the same rectangle")
	}
	at := p.finish()
	if at.Width != AtlasWidth || at.Height != 20 || len(at.Pixels) != AtlasWidth*20 {
		t.Fatalf("atlas %dx%d", at.Width, at.Height)
	}
	if _, err := p.place(new(int), 100, 64, nil); !errors.Is(err, ErrAtlasFull) {
		t.Fatalf("past the height: %v", err)
	}
}

func TestUniform(t *testing.T) {
	u := Uniform(worldview.View{Origin: [3]float32{1, 2, 3}, Angles: [3]float32{0, 0, 0}}, 90, 320, 200)
	if len(u) != UniformSize {
		t.Fatalf("%d bytes", len(u))
	}
	f := func(i int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(u[4*i:])) }
	if tan := f(12); math.Abs(float64(tan)-1) > 1e-6 {
		t.Errorf("tan(fov/2) = %v, want 1 at 90 degrees", tan)
	}
	if f(13) != 16 {
		t.Errorf("near clip %v", f(13))
	}
	if r := f(14); math.Abs(float64(r)-1.6) > 1e-5 { // halfW / (halfH tan)
		t.Errorf("aspect %v, want 1.6", r)
	}
}

// TestBuildE1M1 builds the GPU content of e1m1 (needs the shareware pak).
func TestBuildE1M1(t *testing.T) {
	path := os.Getenv("QUAKE_PAK")
	if path == "" {
		t.Skip("QUAKE_PAK not set")
	}
	fsys, _, err := bench.OpenPak(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := worldview.LoadMap(fsys, "e1m1")
	if err != nil {
		t.Fatal(err)
	}
	g, err := Build(w)
	if err != nil {
		t.Fatal(err)
	}
	if g.Faces == 0 || len(g.Vertices)%VertexStride != 0 || len(g.Indices)%3 != 0 || g.Tex.Height > MaxAtlasHeight {
		t.Fatalf("faces %d, vertex bytes %d, indices %d, texture atlas %dx%d, lightmap atlas %dx%d",
			g.Faces, len(g.Vertices), len(g.Indices), g.Tex.Width, g.Tex.Height, g.Lm.Width, g.Lm.Height)
	}
	t.Logf("e1m1: %d faces, %d vertices, %d triangles, texture atlas %dx%d, lightmap atlas %dx%d",
		g.Faces, len(g.Vertices)/VertexStride, len(g.Indices)/3, g.Tex.Width, g.Tex.Height, g.Lm.Width, g.Lm.Height)
	var draws []Draw
	for i, v := range w.Views() {
		if draws, err = g.Draws(w, v, 320, 200, draws); err != nil {
			t.Fatal(err)
		}
		if len(draws) == 0 {
			t.Errorf("view %d draws nothing", i)
		}
	}
}
