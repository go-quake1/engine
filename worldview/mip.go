// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import (
	"math"

	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/render"
)

// MipView is what the mip choice of a frame depends on: scale_for_mip
// (D_ViewChanged) and the tangents of the view frustum's half angles.
type MipView struct {
	Scale    float32 // scale_for_mip
	TX, TY   float32 // tan of the horizontal and vertical half angles
	Scales   render.MipScales
	MinLevel int // d_minmip

	invNX, invNY float32 // 1 / sqrt(1 + tan^2): a side plane's unit normal

	scratch *clipScratch // the clip's buffers: a MipView is not for concurrent use
}

// NewMipView is the mip state of a width x height frame at fovX degrees with
// the default d_mipscale (1) and d_mipcap (0).
func NewMipView(width, height int, fovX float32) MipView {
	tx := float32(math.Tan(float64(fovX/2) * math.Pi / 180))
	ty := tx * float32(height) / float32(width)
	return MipView{
		Scale:  render.ScaleForMip(width, fovX),
		TX:     tx,
		TY:     ty,
		Scales: render.DefaultMipScales,
		invNX:  float32(1 / math.Sqrt(1+float64(tx)*float64(tx))),
		invNY:  float32(1 / math.Sqrt(1+float64(ty)*float64(ty))),

		scratch: new(clipScratch),
	}
}

// Level is the mip level of face f seen through view: D_MipLevelForScale of
// nearzi * scale_for_mip * mipadjust (d_edge.c D_DrawSurface), nearzi being
// [NearZI] of the face.
//
// Quake's nearzi is r_nearzi, the largest 1/z over the surface's clipped edges
// (r_draw.c, R_EmitEdge and R_ClipEdge), which only the edge-based hidden
// surface pass has. The stand-in is the face's polygon clipped to the view
// frustum (the screen rectangle and the near plane), the largest 1/z of the
// clipped polygon's vertices: the same quantity for a face seen whole.
func (mv MipView) Level(view render.Affine, f *Face) int {
	var buf [render.MaxPolyVerts][3]float32
	pts := buf[:f.Verts.NumVerts]
	for i := range pts {
		pts[i] = render.TransformAffine(view, f.Verts.Vert(i))
	}
	return mv.LevelOf(pts, f)
}

// LevelOf is [MipView.Level] for the face's vertices already in view space
// (the draw list transforms them once for its near-plane test too).
func (mv MipView) LevelOf(pts [][3]float32, f *Face) int {
	zi, ok := nearZI(pts, mv.TX, mv.TY, mv.scratch)
	if !ok {
		return 3
	}
	return mv.Scales.Level(zi*mv.Scale*f.MipAdjust, mv.MinLevel)
}

// FastLevel is [MipView.Level] for a face whose bounding sphere lies wholly
// inside the frustum and in front of the near plane: its polygon then needs no
// clipping, nearzi is 1 over its smallest view-space z, and only the z row of
// the view transform is evaluated. ok is false when the sphere touches a
// frustum plane (the caller takes the general path); the level, when ok, is
// the one Level gives.
func (mv MipView) FastLevel(view render.Affine, f *Face) (level int, ok bool) {
	c := f.Centre
	x := view.R[0][0]*c[0] + view.R[0][1]*c[1] + view.R[0][2]*c[2] + view.T[0]
	y := view.R[1][0]*c[0] + view.R[1][1]*c[1] + view.R[1][2]*c[2] + view.T[1]
	z := view.R[2][0]*c[0] + view.R[2][1]*c[1] + view.R[2][2]*c[2] + view.T[2]
	r := f.Radius
	if z-render.ParticleNearClip < r ||
		(z*mv.TX-x)*mv.invNX < r || (z*mv.TX+x)*mv.invNX < r ||
		(z*mv.TY-y)*mv.invNY < r || (z*mv.TY+y)*mv.invNY < r {
		return 0, false
	}
	minZ := float32(math.MaxFloat32)
	for _, p := range f.Pts {
		if pz := view.R[2][0]*p[0] + view.R[2][1]*p[1] + view.R[2][2]*p[2] + view.T[2]; pz < minZ {
			minZ = pz
		}
	}
	return mv.Scales.Level((1/minZ)*mv.Scale*f.MipAdjust, mv.MinLevel), true
}

// LevelFront is the general path of the draw list in one pass over the
// vertices: it transforms the polygon into buf, tells whether any vertex is in
// front of the near plane (the software renderer's test for drawing the face
// at all) and, if so, the face's level as [MipView.Level] gives it.
func (mv MipView) LevelFront(view render.Affine, f *Face, buf *[render.MaxPolyVerts][3]float32) (level int, front bool) {
	pts := buf[:len(f.Pts)]
	minZ := float32(math.MaxFloat32)
	outside := false
	for i, p := range f.Pts {
		q := render.TransformAffine(view, p)
		pts[i] = q
		if q[2] < minZ {
			minZ = q[2]
		}
		if q[2] >= render.ParticleNearClip {
			front = true
		} else {
			outside = true
		}
		if q[0] > q[2]*mv.TX || -q[0] > q[2]*mv.TX || q[1] > q[2]*mv.TY || -q[1] > q[2]*mv.TY {
			outside = true
		}
	}
	if !front {
		return 0, false
	}
	if !outside {
		return mv.Scales.Level((1/minZ)*mv.Scale*f.MipAdjust, mv.MinLevel), true
	}
	return mv.LevelOf(pts, f), true
}

// NearZI is [NearZIOf] of the polygon fv transformed by view.
func NearZI(view render.Affine, fv bsprender.FaceVerts, tx, ty float32) (float32, bool) {
	var buf [render.MaxPolyVerts][3]float32
	pts := buf[:fv.NumVerts]
	for i := range pts {
		pts[i] = render.TransformAffine(view, fv.Vert(i))
	}
	return NearZIOf(pts, tx, ty)
}

// clipPlane is the signed distance of p inside plane k: 0 the near plane, 1
// and 2 the left and right of the frustum, 3 and 4 the bottom and top.
func clipPlane(k int, p [3]float32, tx, ty float32) float32 {
	switch k {
	case 0:
		return p[2] - render.ParticleNearClip
	case 1:
		return p[2]*tx - p[0]
	case 2:
		return p[2]*tx + p[0]
	case 3:
		return p[2]*ty - p[1]
	}
	return p[2]*ty + p[1]
}

// NearZIOf is the largest 1/z over the vertices of the view-space polygon pts
// (x right, y up, z forward) clipped to the frustum of half-angle tangents tx
// and ty and the near plane render.ParticleNearClip. ok is false when nothing
// is left. A polygon wholly inside the frustum (most faces) is answered from
// its smallest z, without clipping.
func NearZIOf(pts [][3]float32, tx, ty float32) (float32, bool) {
	return nearZI(pts, tx, ty, nil)
}

// clipScratch holds the two polygons of a clip.
type clipScratch [2][render.MaxPolyVerts*2 + 8][3]float32

func nearZI(pts [][3]float32, tx, ty float32, scratch *clipScratch) (float32, bool) {
	minZ := float32(math.MaxFloat32)
	outside := false
	for _, p := range pts {
		if p[2] < minZ {
			minZ = p[2]
		}
		if p[2] < render.ParticleNearClip || p[0] > p[2]*tx || -p[0] > p[2]*tx || p[1] > p[2]*ty || -p[1] > p[2]*ty {
			outside = true
		}
	}
	if !outside {
		return 1 / minZ, true
	}
	buf := scratch
	if buf == nil {
		buf = new(clipScratch)
	}
	in := append(buf[0][:0], pts...)
	cur := 0
	for k := 0; k < 5; k++ {
		out := buf[1-cur][:0]
		n := len(in)
		for i := 0; i < n; i++ {
			a, b := in[i], in[(i+1)%n]
			da, db := clipPlane(k, a, tx, ty), clipPlane(k, b, tx, ty)
			if da >= 0 {
				out = append(out, a)
			}
			if (da >= 0) != (db >= 0) {
				t := da / (da - db)
				out = append(out, [3]float32{a[0] + t*(b[0]-a[0]), a[1] + t*(b[1]-a[1]), a[2] + t*(b[2]-a[2])})
			}
		}
		in = out
		cur = 1 - cur
		if len(in) == 0 {
			return 0, false
		}
	}
	minZ = float32(math.MaxFloat32)
	for _, p := range in {
		if p[2] < minZ {
			minZ = p[2]
		}
	}
	return 1 / minZ, true
}
