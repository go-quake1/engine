// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"math"

	"github.com/go-quake1/engine/mdl"
)

// This file ports R_AliasCheckBBox and R_AliasGetBBox (r_alias.c lines 171 to
// 321): the test that rejects a model whose bounding box is entirely off one
// side of the screen or behind the near plane, and decides how the rest are
// drawn. A box that is whole inside the view, in front of the near plane and
// further away than r_aliastransition + size * r_resfudge (res_scale of
// R_ViewChanged) is "trivial accept 3": its triangles are drawn by recursive
// subdivision (D_DrawSubdiv) and its vertices as points.

// AliasClipPlane is ALIAS_Z_CLIP_PLANE (r_local.h): the near plane of alias
// models.
const AliasClipPlane = 5

// Alias trivial-accept values (entity->trivial_accept).
const (
	AliasClipped    = 0 // partly outside the view or the near plane
	AliasUnclipped  = 1 // whole inside the view
	AliasFarSubdiv  = 3 // whole inside, far enough for the recursive drawer
	aliasXYClipMask = 0xF
)

// aliasFrameBBox is the box of a frame in model bytes, as the frame
// description of the model holds it (a group frame has its own).
func aliasFrameBBox(f mdl.Frame) (mins, maxs [3]float32) {
	bmin, bmax := f.Single.BBoxMin, f.Single.BBoxMax
	if f.Type == mdl.FrameGroup && f.Group != nil {
		bmin, bmax = f.Group.BBoxMin, f.Group.BBoxMax
	}
	for i := 0; i < 3; i++ {
		mins[i], maxs[i] = float32(bmin.V[i]), float32(bmax.V[i])
	}
	return
}

// AliasBBox is R_AliasGetBBox: the box of the frame, or of both frames when
// an interpolation is between two.
func AliasBBox(model *mdl.Model, from, to int) (mins, maxs [3]float32) {
	mins, maxs = aliasFrameBBox(model.Frames[from])
	if to != from {
		m1, x1 := aliasFrameBBox(model.Frames[to])
		for i := 0; i < 3; i++ {
			mins[i], maxs[i] = min(mins[i], m1[i]), max(maxs[i], x1[i])
		}
	}
	return
}

// aliasView is the projection constants of R_ViewChanged for a w x h frame:
// xscale (= yscale, a pixel aspect of 1), xcenter and ycenter (half the frame
// less a half pixel), and the res_scale of the alias transition.
type aliasView struct {
	w, h             int
	scale            float32
	xcenter, ycenter float32
	transition       float32 // r_aliastransition
	fudge            float32 // r_resfudge
}

func newAliasView(w, h int, tanHalfX float32) aliasView {
	v := aliasView{w: w, h: h, scale: float32(w) / 2 / tanHalfX, xcenter: float32(w)*0.5 - 0.5, ycenter: float32(h)*0.5 - 0.5}
	// res_scale = sqrt(w h / (320 152)) * (2 / horizontalFieldOfView), where
	// horizontalFieldOfView = 2 tan(fov_x / 2)
	res := float32(math.Sqrt(float64(float32(w)*float32(h)/(320*152)))) * (1 / tanHalfX)
	v.transition = 200 * res // r_aliastransbase
	v.fudge = 100 * res      // r_aliastransadj
	return v
}

// project is (x * xscale * zi) + xcenter and the same for y (down), for a view
// space point with z at least the clip plane.
func (v aliasView) project(x, y, z float32) (px, py float32) {
	zi := 1 / z
	return float32(float32(x*v.scale)*zi) + v.xcenter, float32(float32(-y*v.scale)*zi) + v.ycenter
}

// aliasCheckBBox is R_AliasCheckBBox for the eight corners of the box
// (mins, maxs in model bytes, brought to view space by corner). It reports
// whether the model is to be drawn at all and its trivial-accept value.
func aliasCheckBBox(av aliasView, mins, maxs [3]float32, size float32, corner func(p [3]float32) [3]float32) (draw bool, accept int) {
	var base [8][3]float32
	for i := 0; i < 4; i++ {
		base[i][0], base[i+4][0] = mins[0], maxs[0]
	}
	for _, i := range []int{0, 3, 5, 6} {
		base[i][1] = mins[1]
	}
	for _, i := range []int{1, 2, 4, 7} {
		base[i][1] = maxs[1]
	}
	for _, i := range []int{0, 1, 4, 5} {
		base[i][2] = mins[2]
	}
	for _, i := range []int{2, 3, 6, 7} {
		base[i][2] = maxs[2]
	}
	type pt struct {
		v     [3]float32
		zclip bool
	}
	pts := make([]pt, 8, 16)
	zclipped, zfullyclipped := false, true
	minz := 9999
	for i := range base {
		pts[i].v = corner(base[i])
		if pts[i].v[2] < AliasClipPlane {
			// we must clip points that are closer than the near clip plane
			pts[i].zclip = true
			zclipped = true
		} else {
			if pts[i].v[2] < float32(minz) {
				minz = int(pts[i].v[2])
			}
			zfullyclipped = false
		}
	}
	if zfullyclipped {
		return false, 0 // everything was near-z-clipped
	}
	if zclipped {
		// organize points by edges, use edges to get new points
		for _, e := range [12][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 0}, {4, 5}, {5, 6}, {6, 7}, {7, 4}, {0, 5}, {1, 4}, {2, 7}, {3, 6}} {
			p0, p1 := pts[e[0]], pts[e[1]]
			if p0.zclip != p1.zclip {
				frac := (AliasClipPlane - p0.v[2]) / (p1.v[2] - p0.v[2])
				pts = append(pts, pt{v: [3]float32{
					p0.v[0] + (p1.v[0]-p0.v[0])*frac,
					p0.v[1] + (p1.v[1]-p0.v[1])*frac,
					AliasClipPlane,
				}})
			}
		}
	}
	// project the vertices that remain after clipping
	anyclip, allclip := 0, aliasXYClipMask
	for _, p := range pts {
		if p.zclip {
			continue // we don't need to bother with vertices that were z-clipped
		}
		v0, v1 := av.project(p.v[0], p.v[1], p.v[2])
		flags := 0
		if v0 < 0 {
			flags |= 1 // ALIAS_LEFT_CLIP
		}
		if v1 < 0 {
			flags |= 2 // ALIAS_TOP_CLIP
		}
		if v0 > float32(av.w) {
			flags |= 4 // ALIAS_RIGHT_CLIP
		}
		if v1 > float32(av.h) {
			flags |= 8 // ALIAS_BOTTOM_CLIP
		}
		anyclip |= flags
		allclip &= flags
	}
	if allclip != 0 {
		return false, 0 // trivial reject off one side
	}
	if anyclip == 0 && !zclipped {
		accept = AliasUnclipped
		if float32(minz) > av.transition+size*av.fudge {
			accept |= 2
		}
	}
	return true, accept
}
