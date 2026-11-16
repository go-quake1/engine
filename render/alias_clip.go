// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

// This file ports r_aclip.c (reference/common/r_aclip.c): the clipping of a
// partly visible alias triangle against the near plane (ALIAS_Z_CLIP_PLANE) and
// the four sides of the alias view rectangle, which gives a convex polygon the
// caller draws as a fan. The tests of the clip flags are those of
// R_AliasPreparePoints (r_alias.c).

// The clip flags of finalvert_t.flags (r_local.h).
const (
	AliasLeftClip   = 1 << 0
	AliasTopClip    = 1 << 1
	AliasRightClip  = 1 << 2
	AliasBottomClip = 1 << 3
	AliasZClip      = 1 << 4
	// AliasXYClipMask is ALIAS_XY_CLIP_MASK.
	AliasXYClipMask = AliasLeftClip | AliasTopClip | AliasRightClip | AliasBottomClip
)

// AliasClipRect is r_refdef.aliasvrect: the clip rectangle in pixels, X and Y
// its top left and Right and Bottom aliasvrectright and aliasvrectbottom
// (x + width and y + height: the first pixel past it).
type AliasClipRect struct{ X, Y, Right, Bottom int32 }

// AliasClipFlags is the xy part of R_AliasPreparePoints' flags for a projected
// vertex: which sides of the rectangle it is beyond.
func AliasClipFlags(u, v int32, r AliasClipRect) uint8 {
	var f uint8
	if u < r.X {
		f |= AliasLeftClip
	}
	if v < r.Y {
		f |= AliasTopClip
	}
	if u > r.Right {
		f |= AliasRightClip
	}
	if v > r.Bottom {
		f |= AliasBottomClip
	}
	return f
}

// aliasProject is R_AliasProjectFinalVert on a view-space point: the screen
// position of (x, y, z) with y up, z at least the clip plane.
type aliasProject func(x, y, z float32) (u, v float32)

// aliasClipZ is R_Alias_clip_z: out is where the edge between the final
// vertices a and b, one of them nearer than the clip plane, crosses the plane,
// the interpolation starting from the unclipped vertex (r_aclip.c chooses it
// by comparing the vertices' v, the clipped one having none). pa and pb are
// the vertices' view-space positions (auxvert_t).
func aliasClipZ(a, b *FinalVert, pa, pb *[3]float32, proj aliasProject, r AliasClipRect, out *FinalVert) {
	// the vertex that was not clipped is the origin of the interpolation
	if a.Flags&AliasZClip != 0 {
		a, b, pa, pb = b, a, pb, pa
	}
	scale := (float32(AliasClipPlane) - pa[2]) / (pb[2] - pa[2])
	x := pa[0] + float32((pb[0]-pa[0])*scale)
	y := pa[1] + float32((pb[1]-pa[1])*scale)
	for i := 2; i <= 4; i++ {
		// int + (int - int) * float, stored in an int: truncated
		out.V[i] = int32(float32(a.V[i]) + float32(float32(b.V[i]-a.V[i])*scale))
	}
	u, v := proj(x, y, AliasClipPlane)
	out.V[0], out.V[1] = int32(u), int32(v)
	out.Flags = AliasClipFlags(out.V[0], out.V[1], r)
}

// aliasClipSide is R_Alias_clip_left, _right, _top and _bottom: out is where the
// edge between a and b crosses the line component comp (0 for u, 1 for v) =
// limit, every component interpolated and rounded (+ 0.5, in double, as the C
// has it), the interpolation starting from the vertex with the larger
// component.
func aliasClipSide(a, b *FinalVert, comp int, limit int32, out *FinalVert) {
	if a.V[comp] < b.V[comp] {
		a, b = b, a
	}
	scale := float32(limit-a.V[comp]) / float32(b.V[comp]-a.V[comp])
	for i := 0; i < 6; i++ {
		out.V[i] = int32(float64(float32(a.V[i])+float32(float32(b.V[i]-a.V[i])*scale)) + 0.5)
	}
}

// aliasClipPoly is R_AliasClip: the polygon in[0:count] clipped against one
// side (the vertices with flag set are outside it), into out, returning the
// new count. clip makes the vertex where the edge from in[j] to in[i] crosses
// the side.
func aliasClipPoly(in, out []FinalVert, flag uint8, count int, r AliasClipRect, clip func(j, i int, out *FinalVert)) int {
	j, k := count-1, 0
	for i := 0; i < count; j, i = i, i+1 {
		oldflags := in[j].Flags & flag
		flags := in[i].Flags & flag
		if flags != 0 && oldflags != 0 {
			continue
		}
		if (oldflags != 0) != (flags != 0) {
			clip(j, i, &out[k])
			out[k].OnSeam = false
			out[k].Flags = AliasClipFlags(out[k].V[0], out[k].V[1], r)
			k++
		}
		if flags == 0 {
			out[k] = in[i]
			k++
		}
	}
	return k
}

// AliasClipTriangle is R_AliasClipTriangle: the triangle tri (final vertices,
// each with its clip flags; aux their view-space positions) clipped against the
// near plane and then the left, right, bottom and top sides of r. It writes the
// polygon to out (at most 8 vertices: a vertex of the triangle on the seam of a
// back-facing one has seamFixup added to its s first, as the C does while it
// copies them, and the flags of the vertices written are cleared, so the
// rasteriser does not fix the seam twice) and returns its vertex count, 0 when
// nothing is left. The polygon is drawn as the fan (0, i, i + 1); its
// vertices are clamped into r.
func AliasClipTriangle(tri [3]FinalVert, aux [3][3]float32, facesFront bool, seamFixup int32, r AliasClipRect, proj aliasProject, out *[8]FinalVert) int {
	var fv [2][8]FinalVert
	// copy vertexes and fix seam texture coordinates
	for i := 0; i < 3; i++ {
		fv[0][i] = tri[i]
		if !facesFront && fv[0][i].OnSeam {
			fv[0][i].V[2] += seamFixup
		}
	}
	clipflags := fv[0][0].Flags | fv[0][1].Flags | fv[0][2].Flags
	pingpong, k := 0, 3
	if clipflags&AliasZClip != 0 {
		av := aux
		k = aliasClipPoly(fv[0][:], fv[1][:], AliasZClip, 3, r, func(j, i int, o *FinalVert) {
			aliasClipZ(&fv[0][j], &fv[0][i], &av[j], &av[i], proj, r, o)
		})
		if k == 0 {
			return 0
		}
		pingpong = 1
		clipflags = fv[1][0].Flags | fv[1][1].Flags | fv[1][2].Flags
	}
	side := func(flag uint8, comp int, limit int32) bool {
		in := fv[pingpong][:]
		k = aliasClipPoly(in, fv[pingpong^1][:], flag, k, r, func(j, i int, o *FinalVert) {
			aliasClipSide(&in[j], &in[i], comp, limit, o)
		})
		pingpong ^= 1
		return k != 0
	}
	if clipflags&AliasLeftClip != 0 && !side(AliasLeftClip, 0, r.X) {
		return 0
	}
	if clipflags&AliasRightClip != 0 && !side(AliasRightClip, 0, r.Right) {
		return 0
	}
	if clipflags&AliasBottomClip != 0 && !side(AliasBottomClip, 1, r.Bottom) {
		return 0
	}
	if clipflags&AliasTopClip != 0 && !side(AliasTopClip, 1, r.Y) {
		return 0
	}
	for i := 0; i < k; i++ {
		p := &fv[pingpong][i]
		p.V[0] = min(max(p.V[0], r.X), r.Right)
		p.V[1] = min(max(p.V[1], r.Y), r.Bottom)
		p.Flags, p.OnSeam = 0, false
		out[i] = *p
	}
	return k
}
