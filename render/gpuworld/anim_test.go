// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"bytes"
	"os"
	"sort"
	"testing"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"
)

func loadE1M1(t testing.TB) (*worldview.World, *render.ColorMap) {
	t.Helper()
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
	return w, cm
}

// atlasMatches counts the drawn faces whose atlas rectangle at the frame's mip
// level is not what the software path's DrawSurface builds in the world's
// current animation state.
func atlasMatches(t testing.TB, g *Geometry, w *worldview.World, draws []FaceDraw, env *render.SurfaceEnv) (checked, wrong int) {
	t.Helper()
	for _, d := range draws {
		f := w.Faces[d.Face]
		ord := int(g.Ord[d.Face])
		adj, tex := w.SurfaceState(f)
		e := *env
		w.LightEnv(&e)
		want, ok := bakeMip(f, adj, tex, int(d.Mip), &e)
		if !ok {
			continue
		}
		r := g.rects[ord][d.Mip]
		at := g.Surf
		if d.Mip > 0 {
			at = g.SurfLow
		}
		checked++
		for row := 0; row < int(r.H); row++ {
			if !bytes.Equal(at.Pixels[(int(r.Y)+row)*at.Width+int(r.X):][:r.W], want[row*int(r.W):][:r.W]) {
				wrong++
				break
			}
		}
	}
	return checked, wrong
}

// TestSurfacesFollowTheAnimation: at rest a frame re-bakes nothing; under the
// animation of time t, every face a frame draws has, in the atlas, the surface
// DrawSurface builds for the light styles and the texture frame of t; the same
// frame again re-bakes nothing (D_CacheSurface's reuse test).
func TestSurfacesFollowTheAnimation(t *testing.T) {
	w, cm := loadE1M1(t)
	g, err := Build(w, cm)
	if err != nil {
		t.Fatal(err)
	}
	env := &render.SurfaceEnv{Colormap: cm, HaveLightData: w.HaveLightData}
	views := w.AnimatedViews(6)
	if len(views) == 0 {
		t.Fatal("no animated face to look at")
	}
	var draws []FaceDraw
	for _, v := range views {
		if draws, err = g.Draws(w, v, 320, 200, draws, Chosen); err != nil {
			t.Fatal(err)
		}
		if len(g.Updates) != 0 {
			t.Fatalf("the resting world re-baked %d surfaces", len(g.Updates))
		}
	}
	total := 0
	for _, tm := range []float32{0.05, 0.42, 0.79, 1.16} {
		w.Anim = w.AnimationAt(tm)
		for i, v := range views {
			if draws, err = g.Draws(w, v, 320, 200, draws, Chosen); err != nil {
				t.Fatal(err)
			}
			total += len(g.Updates)
			if checked, wrong := atlasMatches(t, g, w, draws, env); wrong != 0 || checked == 0 {
				t.Fatalf("t %.2f view %d: %d of %d drawn surfaces are not what DrawSurface builds", tm, i, wrong, checked)
			}
			if _, err = g.Draws(w, v, 320, 200, draws, Chosen); err != nil {
				t.Fatal(err)
			}
			if len(g.Updates) != 0 {
				t.Fatalf("t %.2f view %d: the same frame again re-baked %d surfaces", tm, i, len(g.Updates))
			}
		}
	}
	if total == 0 {
		t.Fatal("the animation re-baked nothing: the views show no animated surface")
	}
	t.Logf("%d surfaces re-baked over 4 times x %d views", total, len(views))
}

// TestUpdateBudget: a frame re-bakes at most UpdateBudget bytes; the surfaces
// left over are counted (Deferred) and done by the following frames of the same
// view, which converge on the animation's surfaces.
func TestUpdateBudget(t *testing.T) {
	w, cm := loadE1M1(t)
	g, err := Build(w, cm)
	if err != nil {
		t.Fatal(err)
	}
	env := &render.SurfaceEnv{Colormap: cm, HaveLightData: w.HaveLightData}
	view := w.AnimatedViews(1)[0]
	w.Anim = w.AnimationAt(0.42)
	g.UpdateBudget = 1 << 10
	var draws []FaceDraw
	frames, sawDeferred := 0, false
	for ; frames < 200; frames++ {
		if draws, err = g.Draws(w, view, 320, 200, draws, Chosen); err != nil {
			t.Fatal(err)
		}
		if g.UpdateBytes > g.UpdateBudget && len(g.Updates) != 1 { // one surface may exceed the budget, alone

			t.Fatalf("frame %d re-baked %d bytes over the budget %d", frames, g.UpdateBytes, g.UpdateBudget)
		}
		sawDeferred = sawDeferred || g.Deferred > 0
		if g.Deferred == 0 {
			break
		}
	}
	if !sawDeferred {
		t.Fatal("a 1 KiB budget deferred nothing")
	}
	if frames == 200 {
		t.Fatal("the deferred surfaces never converged")
	}
	if checked, wrong := atlasMatches(t, g, w, draws, env); wrong != 0 || checked == 0 {
		t.Fatalf("after %d frames %d of %d surfaces are stale", frames, wrong, checked)
	}
	t.Logf("converged after %d frames at 1 KiB per frame", frames+1)
}

// TestUploadCostPerFrame states what the animation costs in a game: a camera
// at each animated view for five seconds at 60 frames per second, the light
// styles and '+' textures at the game's times. The styles move at 10 Hz and the
// textures at 5 Hz, so most frames upload nothing and a frame in about six
// uploads the surfaces that changed.
func TestUploadCostPerFrame(t *testing.T) {
	w, cm := loadE1M1(t)
	g, err := Build(w, cm)
	if err != nil {
		t.Fatal(err)
	}
	var draws []FaceDraw
	for vi, view := range w.AnimatedViews(4) {
		var bytesPerFrame []int
		var rects, up int
		for f := 0; f < 300; f++ {
			w.Anim = w.AnimationAt(float32(f) / 60)
			if draws, err = g.Draws(w, view, 1280, 800, draws, Chosen); err != nil {
				t.Fatal(err)
			}
			bytesPerFrame = append(bytesPerFrame, g.UpdateBytes)
			rects += len(g.Updates)
			if g.UpdateBytes > 0 {
				up++
			}
			if g.UpdateBytes > MaxUpdateBytes {
				t.Fatalf("view %d frame %d: %d bytes over the budget", vi, f, g.UpdateBytes)
			}
		}
		sum := 0
		for _, b := range bytesPerFrame {
			sum += b
		}
		sort.Ints(bytesPerFrame)
		t.Logf("view %d at 1280x800: %d faces; %d of 300 frames upload; %d rectangles; mean %d B/frame, p95 %d, max %d (budget %d)",
			vi, len(draws), up, rects, sum/300, bytesPerFrame[285], bytesPerFrame[299], MaxUpdateBytes)
	}
}

// TestSurfacesFollowDynamicLights: a surface a dynamic light reaches is baked
// with it in every frame it is lit and once more after (D_CacheSurface's
// cache->dlight); after that the frame re-bakes nothing. The atlas holds what
// DrawSurface builds with the world's lights, and a GPU that ignores them
// (IgnoreDLights, the planted defect) holds the surfaces without.
func TestSurfacesFollowDynamicLights(t *testing.T) {
	w, cm := loadE1M1(t)
	g, err := Build(w, cm)
	if err != nil {
		t.Fatal(err)
	}
	env := &render.SurfaceEnv{Colormap: cm, HaveLightData: w.HaveLightData}
	view := w.Views()[0]
	var draws []FaceDraw
	frame := func() int {
		t.Helper()
		if draws, err = g.Draws(w, view, 320, 200, draws, Chosen); err != nil {
			t.Fatal(err)
		}
		return len(g.Updates)
	}
	if n := frame(); n != 0 {
		t.Fatalf("no light: %d surfaces re-baked", n)
	}
	// a muzzle flash in front of the view: 220 units, minlight 32, alive until 0.1
	w.DLights[0] = render.DLight{Origin: [3]float32{view.Origin[0] + 18, view.Origin[1], view.Origin[2] + 16}, Radius: 220, MinLight: 32, Die: 0.1}
	lit := frame()
	if lit == 0 {
		t.Fatal("a muzzle flash re-baked no surface")
	}
	if checked, wrong := atlasMatches(t, g, w, draws, env); wrong != 0 || checked == 0 {
		t.Fatalf("%d of %d drawn surfaces are not what DrawSurface builds with the light", wrong, checked)
	}
	if again := frame(); again != lit {
		t.Fatalf("the same light a frame later re-baked %d surfaces, want the %d it lit again", again, lit)
	}
	w.Now = 1 // the light is dead
	if clean := frame(); clean != lit {
		t.Fatalf("after the light: %d surfaces re-baked to take it off, want %d", clean, lit)
	}
	if checked, wrong := atlasMatches(t, g, w, draws, env); wrong != 0 || checked == 0 {
		t.Fatalf("after the light %d of %d surfaces are stale", wrong, checked)
	}
	if n := frame(); n != 0 {
		t.Fatalf("a frame after that re-baked %d surfaces", n)
	}

	// the planted defect: the lights are not baked in
	w.Now = 0
	g.IgnoreDLights = true
	frame()
	if _, wrong := atlasMatches(t, g, w, draws, env); wrong == 0 {
		t.Fatal("a GPU that ignores the lights holds the lit surfaces")
	}
}
