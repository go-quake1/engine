// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"testing"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/vfs"
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
	v := vfs.New()
	v.Add(fsys)
	cm, err := assets.LoadColorMapFrom(v)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Build(w, cm)
	if err != nil {
		t.Fatal(err)
	}
	if g.Faces == 0 || len(g.Vertices)%VertexStride != 0 || len(g.Indices)%3 != 0 || g.Surf.Height > MaxAtlasHeight {
		t.Fatalf("faces %d, vertex bytes %d, indices %d, surface atlas %dx%d",
			g.Faces, len(g.Vertices), len(g.Indices), g.Surf.Width, g.Surf.Height)
	}
	t.Logf("e1m1: %d faces, %d vertices, %d triangles, surface atlas %dx%d (%d bytes of surfaces per mip level: %v)",
		g.Faces, len(g.Vertices)/VertexStride, len(g.Indices)/3, g.Surf.Width, g.Surf.Height, g.SurfBytes[0], g.SurfBytes)

	// The atlas holds exactly what the software path's DrawSurface builds,
	// at the rectangle the face table gives.
	env := &render.SurfaceEnv{Colormap: cm, HaveLightData: w.HaveLightData}
	checked := 0
	for _, f := range w.Faces {
		if f == nil || g.Ord[f.Idx] < 0 {
			continue
		}
		ord := int(g.Ord[f.Idx])
		for mip := 0; mip < render.SurfMips; mip++ {
			rect := g.SurfTab[ord*64+mip*16:]
			x, y := int(binary.LittleEndian.Uint32(rect)), int(binary.LittleEndian.Uint32(rect[4:]))
			sw, sh := int(binary.LittleEndian.Uint32(rect[8:])), int(binary.LittleEndian.Uint32(rect[12:]))
			if sw != f.Surf.Desc.Extents[0]>>mip || sh != f.Surf.Desc.Extents[1]>>mip {
				t.Fatalf("face %d mip %d: rect %dx%d, extents %v", f.Idx, mip, sw, sh, f.Surf.Desc.Extents)
			}
			want := make([]byte, sw*sh)
			if err := render.DrawSurface(want, &f.Surf.Desc, worldview.StyleAdj(f.Surf.Desc.Styles), mip, env); err != nil {
				t.Fatal(err)
			}
			for row := 0; row < sh; row++ {
				if !bytes.Equal(g.Surf.Pixels[(y+row)*g.Surf.Width+x:][:sw], want[row*sw:][:sw]) {
					t.Fatalf("face %d mip %d row %d differs from DrawSurface", f.Idx, mip, row)
				}
			}
		}
		if checked++; checked >= 400 {
			break
		}
	}

	// The frame's mip levels are worldview's.
	var draws []FaceDraw
	mips := make([]uint32, len(w.Faces))
	var hist [render.SurfMips]int
	for i, v := range w.Views() {
		if draws, err = g.Draws(w, v, 320, 200, draws, Chosen); err != nil {
			t.Fatal(err)
		}
		if len(draws) == 0 {
			t.Errorf("view %d draws nothing", i)
		}
		for _, d := range draws {
			mips[d.Face] = d.Mip
		}
		view := (&render.RefDef{ViewAngles: v.Angles, ViewOrigin: v.Origin}).SetupView()
		mv := worldview.NewMipView(320, 200, w.FOV)
		faces, _ := w.Visible(v, 320, 200)
		for _, idx := range faces {
			if g.Ord[idx] < 0 {
				continue
			}
			if want := uint32(mv.Level(view, w.Faces[idx])); mips[idx] != want && g.Count[idx] > 0 {
				// faces wholly behind the near plane are not in the list
				fv := w.Faces[idx].Verts
				front := false
				for k := 0; k < fv.NumVerts && !front; k++ {
					front = render.TransformAffine(view, fv.Vert(k))[2] >= render.ParticleNearClip
				}
				if front {
					t.Fatalf("view %d face %d: mip %d, worldview says %d", i, idx, mips[idx], want)
				}
			}
			hist[mips[idx]]++
		}
	}
	t.Logf("mip levels of the faces drawn over the views: %v", hist)
}

// BenchmarkDraws locates the GPU path's CPU cost: the draw list with the
// mip choice (NearZI per visible face) and with every face at level 0.
func BenchmarkDraws(b *testing.B) {
	path := os.Getenv("QUAKE_PAK")
	if path == "" {
		b.Skip("QUAKE_PAK not set")
	}
	fsys, _, err := bench.OpenPak(path)
	if err != nil {
		b.Fatal(err)
	}
	w, err := worldview.LoadMap(fsys, "e1m1")
	if err != nil {
		b.Fatal(err)
	}
	v := vfs.New()
	v.Add(fsys)
	cm, err := assets.LoadColorMapFrom(v)
	if err != nil {
		b.Fatal(err)
	}
	g, err := Build(w, cm)
	if err != nil {
		b.Fatal(err)
	}
	views := w.Views()
	for name, pol := range map[string]MipPolicy{"chosen": Chosen, "level0": {Fixed: 0}} {
		b.Run(name, func(b *testing.B) {
			var draws []FaceDraw
			for i := 0; i < b.N; i++ {
				draws, _ = g.Draws(w, views[i%len(views)], 1280, 800, draws, pol)
			}
		})
	}
}

func TestMipPolicy(t *testing.T) {
	if Chosen.Fixed != -1 || (MipPolicy{}).Fixed != 0 {
		t.Fatal("Chosen must be Fixed -1; the zero MipPolicy is level 0 (the ablation)")
	}
}
