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
}

// NewMipView is the mip state of a width x height frame at fovX degrees with
// the default d_mipscale (1) and d_mipcap (0).
func NewMipView(width, height int, fovX float32) MipView {
	tx := float32(math.Tan(float64(fovX/2) * math.Pi / 180))
	return MipView{
		Scale:  render.ScaleForMip(width, fovX),
		TX:     tx,
		TY:     tx * float32(height) / float32(width),
		Scales: render.DefaultMipScales,
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
	zi, ok := NearZI(view, f.Verts, mv.TX, mv.TY)
	if !ok {
		return 3
	}
	return mv.Scales.Level(zi*mv.Scale*f.MipAdjust, mv.MinLevel)
}

// NearZI is the largest 1/z over the vertices of the polygon fv clipped to
// the frustum of half-angle tangents tx and ty and the near plane
// render.ParticleNearClip, in view space (x right, y up, z forward). ok is
// false when nothing is left.
func NearZI(view render.Affine, fv bsprender.FaceVerts, tx, ty float32) (float32, bool) {
	var buf [2][render.MaxPolyVerts*2 + 8][3]float32
	in := buf[0][:0]
	minZ := float32(math.MaxFloat32)
	outside := false
	for i := 0; i < fv.NumVerts; i++ {
		p := render.TransformAffine(view, fv.Vert(i))
		in = append(in, p)
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
	planes := [5]func(p [3]float32) float32{
		func(p [3]float32) float32 { return p[2] - render.ParticleNearClip },
		func(p [3]float32) float32 { return p[2]*tx - p[0] },
		func(p [3]float32) float32 { return p[2]*tx + p[0] },
		func(p [3]float32) float32 { return p[2]*ty - p[1] },
		func(p [3]float32) float32 { return p[2]*ty + p[1] },
	}
	cur := 0
	for _, d := range planes {
		out := buf[1-cur][:0]
		n := len(in)
		for i := 0; i < n; i++ {
			a, b := in[i], in[(i+1)%n]
			da, db := d(a), d(b)
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
