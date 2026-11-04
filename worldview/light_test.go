// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview_test

import (
	"os"
	"testing"

	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/worldview"
)

// highestBelow finds, without the BSP, the lit face whose polygon the vertical
// line down from p crosses highest: the oracle for R_LightSurfPoint, which
// walks the tree. It uses the face polygon; the C only tests the lightmap
// rectangle, so a point just outside a polygon can differ.
func highestBelow(t *testing.T, w *worldview.World, p [3]float32) int {
	t.Helper()
	faces, _ := w.File.Faces()
	planes, _ := w.File.Planes()
	surfedges, _ := w.File.Surfedges()
	edges, _ := w.File.Edges()
	verts, _ := w.File.Vertexes()
	best, bestZ := -1, float32(-1e30)
	for i, f := range faces {
		if w.Faces[i] == nil {
			continue
		}
		pl := planes[f.PlaneNum]
		if pl.Normal[2] == 0 {
			continue
		}
		z := (pl.Dist - pl.Normal[0]*p[0] - pl.Normal[1]*p[1]) / pl.Normal[2]
		if z > p[2] || z <= bestZ {
			continue
		}
		var xs, ys []float32
		for k := 0; k < int(f.NumEdges); k++ {
			se := int32(surfedges[int(f.FirstEdge)+k])
			var v bspfile.Vertex
			if se >= 0 {
				v = verts[edges[se].V0]
			} else {
				v = verts[edges[-se].V1]
			}
			xs, ys = append(xs, v.X), append(ys, v.Y)
		}
		pos, neg := false, false
		for k := range xs {
			j := (k + 1) % len(xs)
			cr := (xs[j]-xs[k])*(p[1]-ys[k]) - (ys[j]-ys[k])*(p[0]-xs[k])
			if cr > 0 {
				pos = true
			} else if cr < 0 {
				neg = true
			}
		}
		if pos && neg {
			continue
		}
		best, bestZ = i, z
	}
	return best
}

// TestEntityLightsE1M1 checks R_LightSurfPoint's tree walk against a brute
// force over the face polygons on every alias entity of e1m1, and pins the
// lights of some entities (values of this port, regression pins: the walk
// is pinned on hand-worked values in bsprender.TestLightPointWorked).
func TestEntityLightsE1M1(t *testing.T) {
	w, _ := load(t)
	fsys, _, err := bench.OpenPak(os.Getenv(pakEnv))
	if err != nil {
		t.Fatal(err)
	}
	ents, err := w.AliasEntities(fsys)
	if err != nil {
		t.Fatal(err)
	}
	agree, lit := 0, 0
	distinct := map[int]bool{}
	for i, e := range ents {
		face, _, _, ok := w.Lights.SurfPoint(e.Origin)
		want := highestBelow(t, w, e.Origin)
		if !ok {
			want = -1 // the walk found no lit face
		}
		if ok {
			lit++
		}
		if want == face || (!ok && want == -1) {
			agree++
		} else {
			t.Logf("entity %d %s %v: tree face %d (ok %v), brute force face %d", i, e.Class, e.Origin, face, ok, want)
		}
		distinct[e.Light] = true
	}
	if lit < len(ents)*9/10 {
		t.Errorf("only %d of %d entities have a lit surface below", lit, len(ents))
	}
	if agree < len(ents)*9/10 {
		t.Errorf("tree and brute force agree on %d of %d entities, want 90%%", agree, len(ents))
	}
	if len(distinct) < 20 {
		t.Errorf("only %d distinct lights over %d entities", len(distinct), len(ents))
	}
	t.Logf("%d entities: %d lit, tree and brute force agree on %d, %d distinct lights", len(ents), lit, agree, len(distinct))
	pins := map[int]int{0: 66, 1: 21, 2: 168, 3: 9, 52: 0, 53: 112}
	for i, want := range pins {
		if ents[i].Light != want {
			t.Errorf("entity %d %s: light %d, want %d", i, ents[i].Class, ents[i].Light, want)
		}
	}
}

// BenchmarkLightPoint is R_LightPoint over the alias entities of e1m1.
func BenchmarkLightPoint(b *testing.B) {
	if os.Getenv(pakEnv) == "" {
		b.Skip("QUAKE_PAK not set")
	}
	fsys, _, err := bench.OpenPak(os.Getenv(pakEnv))
	if err != nil {
		b.Skip(err)
	}
	w, err := worldview.LoadMap(fsys, "e1m1")
	if err != nil {
		b.Fatal(err)
	}
	ents, err := w.AliasEntities(fsys)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, e := range ents {
			w.LightEnt(&e)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(ents)), "ns/entity")
}
