// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runner

import (
	"os"
	"testing"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"
)

func loadE1M1(t testing.TB) (*worldview.World, *render.ColorMap) {
	t.Helper()
	path := os.Getenv("QUAKE_PAK")
	if path == "" {
		t.Skip("QUAKE_PAK not set: no pak0.pak to load e1m1 from")
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

// drawRunnerWorld draws the faces the reference draws with the runner's
// worldSurfaces, in the given order (forward: the reference's; reverse: the
// runner's painter's order).
func drawRunnerWorld(t testing.TB, ws *worldSurfaces, w *worldview.World, cm *render.ColorMap, v worldview.View, fb *render.FrameBuffer, reverse bool, style func(byte) int) {
	t.Helper()
	for i := range fb.Pixels {
		fb.Pixels[i] = worldview.ClearIndex
	}
	faces, err := w.Visible(v, fb.Width, fb.Height)
	if err != nil {
		t.Fatal(err)
	}
	rd := render.RefDef{
		VRect: render.VRect{Width: fb.Width, Height: fb.Height}, ViewAngles: v.Angles, ViewOrigin: v.Origin,
		FovX: w.FOV, FovY: w.FOV,
	}
	view := rd.SetupView()
	ws.beginFrame(fb.Width, fb.Height, w.FOV)
	allFaces, _ := w.File.Faces()
	planes, _ := w.File.Planes()
	for n := range faces {
		idx := faces[n]
		if reverse {
			idx = faces[len(faces)-1-n]
			// the runner's backface cull (R_RecursiveWorldNode's SURF_PLANEBACK test)
			f := allFaces[idx]
			if pn := int(f.PlaneNum); pn >= 0 && pn < len(planes) &&
				!faceFrontFacing(planes[pn].Normal, planes[pn].Dist, f.Side, v.Origin) {
				continue
			}
		}
		fv, err := bsprender.NewBrushFaceVerts(w.BM, idx)
		if err != nil {
			continue
		}
		info, err := bsprender.FaceLightmapInfo(w.BM, idx)
		if err != nil {
			continue
		}
		mi, ok, _ := w.BM.FaceMipTexIdx(idx)
		if !ok {
			continue
		}
		ws.draw(fb, cm, view, w.FOV, info, fv, idx, mi, mi, style)
	}
	ws.endFrame()
}

func countDiff(a, b []byte) int {
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

// TestWorldSurfacesMatchReference draws the 16 fixed e1m1 views with the
// runner's surface-cache world and with worldview.DrawSoftware and counts the
// pixels that differ. With the same draw order (back to front) and the resting
// light styles the two are the same code path on the same inputs: 0 pixels differ.
func TestWorldSurfacesMatchReference(t *testing.T) {
	w, cm := loadE1M1(t)
	ws, err := newWorldSurfaces(w.File)
	if err != nil {
		t.Fatal(err)
	}
	rest := bsprender.NormalLightStyle
	total, totalRev := 0, 0
	views := w.Views()
	for k, v := range views {
		ref, _ := render.NewFrameBuffer(320, 200)
		if err := w.DrawSoftware(ref, cm, v); err != nil {
			t.Fatal(err)
		}
		got, _ := render.NewFrameBuffer(320, 200)
		drawRunnerWorld(t, ws, w, cm, v, got, true, rest) // the runner's own order, with its backface cull
		if d := countDiff(ref.Pixels, got.Pixels); d != 0 {
			t.Errorf("view %d: %d pixels differ from worldview.DrawSoftware", k, d)
			total += d
		}
		fwd, _ := render.NewFrameBuffer(320, 200)
		drawRunnerWorld(t, ws, w, cm, v, fwd, false, rest) // the walk's order: far over near
		totalRev += countDiff(ref.Pixels, fwd.Pixels)
	}
	t.Logf("%d views, 320x200: %d pixels differ in the runner's own order; %d in the walk's order (the old reference order, shown to differ); cache builds %d hits %d thrash frames %d",
		len(views), total, totalRev, ws.Builds, ws.Hits, ws.ThrashFrames)
	if totalRev == 0 {
		t.Error("the walk's order gives the same frame as the back-to-front one: the comparison pins nothing")
	}
}

// TestWorldSurfacesLightStyleAndAnimation: a light style that changes rebuilds
// the surface in place (same block, new pixels) and the same value hits the
// cache; a changed texture rebuilds too.
func TestWorldSurfacesLightStyleAndAnimation(t *testing.T) {
	w, cm := loadE1M1(t)
	ws, err := newWorldSurfaces(w.File)
	if err != nil {
		t.Fatal(err)
	}
	v := w.Views()[0]
	fb, _ := render.NewFrameBuffer(320, 200)
	drawRunnerWorld(t, ws, w, cm, v, fb, false, bsprender.NormalLightStyle)
	b0, h0 := ws.Builds, ws.Hits
	drawRunnerWorld(t, ws, w, cm, v, fb, false, bsprender.NormalLightStyle)
	if ws.Builds != b0 || ws.Hits <= h0 {
		t.Errorf("same light twice: builds %d -> %d, hits %d -> %d; want no build and hits", b0, ws.Builds, h0, ws.Hits)
	}
	dim := func(byte) int { return 100 }
	before := append([]byte(nil), fb.Pixels...)
	drawRunnerWorld(t, ws, w, cm, v, fb, false, dim)
	if ws.Builds == b0 {
		t.Error("a changed light style value built nothing")
	}
	if countDiff(before, fb.Pixels) == 0 {
		t.Error("a dimmer light changed no pixel")
	}
	// An animated texture is a changed texture: swap one visible face's texture
	// for another miptex and the surface is rebuilt in place.
	faces, _ := w.Visible(v, 320, 200)
	var f *surfFace
	for _, idx := range faces {
		if g := ws.faces[idx]; g != nil && g.sf.Desc.Samples != nil {
			f = g
			break
		}
	}
	if f == nil {
		t.Skip("no lit face visible")
	}
	b1 := ws.Builds
	other := f.sf.Desc.Texture
	for _, st := range ws.tex {
		if st != nil && st != other && st.Width == other.Width && st.Height == other.Height {
			other = st
			break
		}
	}
	if other == f.sf.Desc.Texture {
		t.Skip("no second texture of the same size")
	}
	ws.beginFrame(320, 200, w.FOV)
	for mip := 0; mip < render.SurfMips; mip++ {
		if blk := f.sf.Spot(mip); blk != nil {
			f.sf.Desc.Texture = other
			nb, err := ws.cache.Cache(&f.sf, mip, [4]int{100, 100, 100, 100}, &ws.env)
			if err != nil || nb != blk {
				t.Fatalf("mip %d: changed texture did not rebuild in place (err %v)", mip, err)
			}
			if nb.Texture != other {
				t.Errorf("mip %d: block texture not updated", mip)
			}
			break
		}
	}
	_ = b1
}

// BenchmarkWorldSurfaces is the runner's lit-face pass over the 16 e1m1 views
// at 320 x 200 (painter's order, resting light), cache warm.
func BenchmarkWorldSurfaces(b *testing.B) {
	w, cm := loadE1M1(b)
	ws, err := newWorldSurfaces(w.File)
	if err != nil {
		b.Fatal(err)
	}
	fb, _ := render.NewFrameBuffer(320, 200)
	views := w.Views()
	for _, v := range views {
		drawRunnerWorld(b, ws, w, cm, v, fb, true, bsprender.NormalLightStyle)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		drawRunnerWorld(b, ws, w, cm, views[i%len(views)], fb, true, bsprender.NormalLightStyle)
	}
}
