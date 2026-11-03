// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import (
	"fmt"
	"math"

	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/render"
)

// SpecialKind tells the two textures the lit faces do not cover.
type SpecialKind int

const (
	KindSky  SpecialKind = iota + 1 // "sky*": the two-layer scrolling sky
	KindTurb                        // "*": water, slime, lava and teleporters, warped
)

// SpecialFace is a sky or water face: a texture and no lightmap. The
// engine fills it with render.FillSkyPolygon or render.FillTurbulentPolygon
// (game.setupRenderer), affinely in screen space, unlit.
type SpecialFace struct {
	Idx   int
	Kind  SpecialKind
	Name  string
	Tex   *render.Pic
	Verts bsprender.FaceVerts
}

// DemoTime is the time in seconds the sky scroll and the water warp are
// evaluated at by the fixed views and their checks.
const DemoTime float32 = 3.7

// VisibleSpecial returns the sky and water faces the last [World.Visible]
// call selected, in the walk's order. The slice is reused by the next call.
func (w *World) VisibleSpecial() []*SpecialFace {
	out := make([]*SpecialFace, len(w.visSpecial))
	for i, idx := range w.visSpecial {
		out[i] = w.Special[idx]
	}
	return out
}

// DrawSpecialSoftware draws the visible faces of one kind over fb the way
// the engine does: TransformFace, then render.FillSkyPolygon (sky, at time
// t) or render.FillTurbulentPolygon (water, through cm at light level 0).
func (w *World) DrawSpecialSoftware(fb *render.FrameBuffer, cm *render.ColorMap, v View, kind SpecialKind, t float32) error {
	if _, err := w.Visible(v, fb.Width, fb.Height); err != nil {
		return err
	}
	view := w.rd.SetupView()
	for _, f := range w.VisibleSpecial() {
		if f.Kind != kind {
			continue
		}
		verts, err := bsprender.TransformFace(view, fb, w.FOV, f.Verts)
		if err != nil {
			continue
		}
		switch kind {
		case KindSky:
			_ = render.FillSkyPolygon(fb, f.Tex, verts, t)
		case KindTurb:
			_ = render.FillTurbulentPolygon(fb, f.Tex, cm, 0, verts, t)
		default:
			return fmt.Errorf("worldview: unknown special kind %d", kind)
		}
	}
	return nil
}

// SpecialViews returns n fixed views that each look at one face of kind
// from a spot that sees it: the faces are taken at an even stride from the
// face list, and each is seen from the first of a fixed list of distances,
// on either side of its plane, whose eye point is inside the map and has a
// clear line to a point just in front of the face's centre. Fewer than n are returned when the map
// has fewer such faces.
func (w *World) SpecialViews(kind SpecialKind, n int) []View {
	var faces []*SpecialFace
	for _, f := range w.Special {
		if f != nil && f.Kind == kind {
			faces = append(faces, f)
		}
	}
	var out []View
	used := map[[3]int]bool{}
	for k := 0; k < len(faces) && len(out) < n; k++ {
		f := faces[(k*len(faces))/n%len(faces)]
		if k >= n {
			f = faces[k]
		}
		var c [3]float32
		for i := 0; i < f.Verts.NumVerts; i++ {
			p := f.Verts.Vert(i)
			for a := 0; a < 3; a++ {
				c[a] += p[a] / float32(f.Verts.NumVerts)
			}
		}
		key := [3]int{int(c[0]) / 64, int(c[1]) / 64, int(c[2]) / 64}
		if used[key] {
			continue
		}
		p0, p1, p2 := f.Verts.Vert(0), f.Verts.Vert(1), f.Verts.Vert(2)
		a := [3]float32{p1[0] - p0[0], p1[1] - p0[1], p1[2] - p0[2]}
		b := [3]float32{p2[0] - p0[0], p2[1] - p0[1], p2[2] - p0[2]}
		nrm := [3]float32{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
		l := float32(math.Sqrt(float64(nrm[0]*nrm[0] + nrm[1]*nrm[1] + nrm[2]*nrm[2])))
		if l == 0 {
			continue
		}
		for i := range nrm {
			nrm[i] /= l
		}
	search:
		for _, dist := range []float32{128, 192, 96, 256, 384} {
			for _, side := range []float32{1, -1} {
				eye := [3]float32{c[0] + side*dist*nrm[0], c[1] + side*dist*nrm[1], c[2] + side*dist*nrm[2]}
				// The centre lies on the face, in the solid's boundary: the
				// line ends 4 units in front of it.
				front := [3]float32{c[0] + side*4*nrm[0], c[1] + side*4*nrm[1], c[2] + side*4*nrm[2]}
				if w.BM.PointInLeaf(eye) <= 0 || !w.clear(eye, front) {
					continue
				}
				dx, dy, dz := c[0]-eye[0], c[1]-eye[1], c[2]-eye[2]
				yaw := float32(math.Atan2(float64(dy), float64(dx)) * 180 / math.Pi)
				pitch := float32(-math.Atan2(float64(dz), math.Hypot(float64(dx), float64(dy))) * 180 / math.Pi)
				out = append(out, View{Origin: eye, Angles: [3]float32{pitch, yaw, 0}})
				used[key] = true
				break search
			}
		}
	}
	return out
}
