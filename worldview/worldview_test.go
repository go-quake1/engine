// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"
)

// pakEnv names the env var that points at the shareware pak0.pak. The
// pak is not in the repository: the tests skip without it.
const pakEnv = "QUAKE_PAK"

func load(t testing.TB) (*worldview.World, *render.ColorMap) {
	t.Helper()
	path := os.Getenv(pakEnv)
	if path == "" {
		t.Skipf("%s not set: no pak0.pak to load e1m1 from", pakEnv)
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
	return w, cm
}

// TestSurfaceCacheOnE1M1 draws the 16 views through the surface cache: the mip
// levels the views choose, what the cache holds, and that a second pass over
// the same views builds nothing.
func TestSurfaceCacheOnE1M1(t *testing.T) {
	w, cm := load(t)
	views := w.Views()
	var hist [render.SurfMips]int
	noLight := 0
	for _, f := range w.Faces {
		if f != nil && f.Surf.Desc.Samples == nil {
			noLight++
		}
	}
	mv := worldview.NewMipView(320, 200, w.FOV)
	for _, v := range views {
		fb, _ := render.NewFrameBuffer(320, 200)
		if err := w.DrawSoftware(fb, cm, v); err != nil {
			t.Fatal(err)
		}
		faces, _ := w.Visible(v, 320, 200)
		view := (&render.RefDef{ViewAngles: v.Angles, ViewOrigin: v.Origin}).SetupView()
		for _, idx := range faces {
			hist[mv.Level(view, w.Faces[idx])]++
		}
	}
	first := w.SurfaceCacheStats()
	t.Logf("mip levels of the visible faces over %d views: %v; faces without a lightmap: %d; first pass %+v", len(views), hist, noLight, first)
	for k, n := range hist {
		if n == 0 {
			t.Errorf("no face at mip %d in the 16 views: the views do not exercise it", k)
		}
	}
	for _, v := range views {
		fb, _ := render.NewFrameBuffer(320, 200)
		_ = w.DrawSoftware(fb, cm, v)
	}
	second := w.SurfaceCacheStats()
	t.Logf("second pass %+v", second)
	if second.Hits <= first.Hits {
		t.Error("the second pass never hit the cache")
	}
}

// TestSurfaceCacheMovesTheReference measures how far the surface cache moved
// the reference: the per-pixel lightmapped frame of v0.6.1 (which the GPU of
// v0.6.1 matched to 0.006 %) against the surface-cache frame, with the same
// metric as the browser check (unexplained: not equal at any of the 8
// neighbours).
func TestSurfaceCacheMovesTheReference(t *testing.T) {
	w, cm := load(t)
	differ, unexplained, total := 0, 0, 0
	for _, v := range w.Views() {
		a, _ := render.NewFrameBuffer(320, 200)
		b, _ := render.NewFrameBuffer(320, 200)
		if err := w.DrawLightmapped(a, cm, v); err != nil {
			t.Fatal(err)
		}
		if err := w.DrawSoftware(b, cm, v); err != nil {
			t.Fatal(err)
		}
		for y := 0; y < 200; y++ {
			for x := 0; x < 320; x++ {
				g, r := a.Pixels[y*a.Pitch+x], b.Pixels[y*b.Pitch+x]
				total++
				if g == r {
					continue
				}
				differ++
				near := false
				for dy := -1; dy <= 1 && !near; dy++ {
					for dx := -1; dx <= 1 && !near; dx++ {
						nx, ny := x+dx, y+dy
						near = nx >= 0 && ny >= 0 && nx < 320 && ny < 200 && b.Pixels[ny*b.Pitch+nx] == g
					}
				}
				if !near {
					unexplained++
				}
			}
		}
	}
	t.Logf("v0.6.1's reference against the surface-cache reference: %d of %d differ (%.4f), %d unexplained (%.4f)",
		differ, total, float64(differ)/float64(total), unexplained, float64(unexplained)/float64(total))
	if float64(unexplained)/float64(total) < 0.03 {
		t.Errorf("the surface cache did not move the reference: %d unexplained", unexplained)
	}
}

// TestFastLevelIsLevel: where the bounding-sphere shortcut applies it gives
// the level of the clipping path, for every visible face of the 16 views.
func TestFastLevelIsLevel(t *testing.T) {
	w, _ := load(t)
	mv := worldview.NewMipView(320, 200, w.FOV)
	fast, all := 0, 0
	for _, v := range w.Views() {
		faces, _ := w.Visible(v, 320, 200)
		view := (&render.RefDef{ViewAngles: v.Angles, ViewOrigin: v.Origin}).SetupView()
		for _, idx := range faces {
			f := w.Faces[idx]
			all++
			if l, ok := mv.FastLevel(view, f); ok {
				fast++
				if want := mv.Level(view, f); l != want {
					t.Fatalf("face %d: fast level %d, clipped level %d", idx, l, want)
				}
			}
		}
	}
	t.Logf("the shortcut applies to %d of %d visible faces", fast, all)
	if fast == 0 {
		t.Error("the shortcut never applies")
	}
}

func TestLoadMapMissing(t *testing.T) {
	fsys, _, _ := bench.OpenPak("")
	if _, err := worldview.LoadMap(fsys, "e1m1"); err == nil {
		t.Fatal("the procedural pak has no e1m1")
	}
}

// TestReferenceFrames: the fixed e1m1 views draw non-trivial, deterministic
// world-only frames. The digests are logged: they identify the reference
// the GPU world is compared with.
func TestReferenceFrames(t *testing.T) {
	w, cm := load(t)
	views := w.Views()
	if len(views) < 8 {
		t.Fatalf("%d views, want at least 8", len(views))
	}
	drawn := 0
	for i, v := range views {
		a, _ := render.NewFrameBuffer(320, 200)
		b, _ := render.NewFrameBuffer(320, 200)
		if err := w.DrawSoftware(a, cm, v); err != nil {
			t.Fatal(err)
		}
		if err := w.DrawSoftware(b, cm, v); err != nil {
			t.Fatal(err)
		}
		if string(a.Pixels) != string(b.Pixels) {
			t.Fatalf("view %d: two draws differ", i)
		}
		seen := map[byte]bool{}
		for _, p := range a.Pixels {
			seen[p] = true
		}
		sum := sha256.Sum256(a.Pixels)
		t.Logf("view %2d origin %v yaw %3.0f: %3d indices, sha256 %s", i, v.Origin, v.Angles[1], len(seen), hex.EncodeToString(sum[:6]))
		if len(seen) > 8 {
			drawn++
		}
	}
	if drawn < len(views)/2 {
		t.Fatalf("only %d of %d views draw a textured world", drawn, len(views))
	}
}
