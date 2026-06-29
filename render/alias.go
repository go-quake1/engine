// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"

	"github.com/go-quake1/engine/mathlib"
	"github.com/go-quake1/engine/mdl"
)

// AliasNearClip is the minimum view-space forward depth at which an
// alias-model triangle is drawn. Triangles with any vertex closer than
// this are skipped. tyrquake: ALIAS_Z_CLIP_PLANE in r_local.h.
const AliasNearClip = 8.0

// Sentinel errors returned by DrawAlias.
var (
	ErrAliasNilFB     = errors.New("render: nil framebuffer in alias draw")
	ErrAliasNilModel  = errors.New("render: nil mdl in alias draw")
	ErrAliasNilRefDef = errors.New("render: nil refdef in alias draw")
	ErrAliasNilSkin   = errors.New("render: nil skin texture in alias draw")
	ErrAliasBadFrame  = errors.New("render: alias frame index out of range")
)

// AliasEntity is the per-entity state DrawAlias reads. Callers
// derive it from the entity's position + orientation + current
// animation frame.
type AliasEntity struct {
	Origin     [3]float32 // entity position in world space
	AngleYaw   float32    // entity facing direction (degrees)
	AnglePitch float32    // entity vertical lean (degrees; usually 0)
	AngleRoll  float32    // entity roll (degrees; usually 0)
	FrameIdx   int        // index into model.Frames
	SkinIdx    int        // (reserved) index into the caller's skin table

	// ClTime is the current client time in seconds, used to pick the
	// active sub-frame of an mdl FrameGroup via mdl.Frame.PoseAt.
	// FrameSingle frames ignore it; FrameGroup frames (torches, flames,
	// armor shimmer, ...) cycle through their sub-frames at the rate
	// recorded by the mdl's Intervals[]. Zero is a valid value (gives
	// sub-frame 0 = the first pose of the cycle).
	ClTime float32
}

// DrawAlias rasterizes one alias model into fb. For each triangle in
// model.Triangles the three .mdl byte-packed vertices are decoded
// (scale + scaleOrigin), rotated by the entity's angles, translated
// by the entity's origin, view-transformed via rd.SetupView(),
// perspective-projected to screen pixels, back-face-culled, and
// finally rasterized with FillTexturedPolygon using the entity's
// skin texture and uniform lightLevel.
//
// SCOPE: this commit implements a SINGLE-frame draw -- only
// FrameSingle records render; FrameGroup records use Group.Frames[0]
// as the still pose. Per-tic interpolation (R_AliasLerpVerts) is a
// follow-up batch.
//
// SCOPE: per-vertex lighting via the lightnormalindex is NOT applied;
// every triangle uses the caller's uniform lightLevel. Per-vertex
// shading is a follow-up batch.
//
// Texture coordinates come from model.STVerts indexed by the
// triangle's VertIndex. Per tyrquake, when a triangle is back-facing
// (Triangle.FacesFront == 0) and the vertex is on the skin seam
// (STVert.OnSeam != 0), s is offset by SkinWidth/2 to land in the
// back-skin half. The clamp inside FillTexturedPolygon protects
// against any out-of-range UV the caller hands us.
//
// Back-face cull uses the signed area of the screen-space triangle
// in the framebuffer's pixel coordinate system (X right, Y down).
// In that system a counter-clockwise winding has NEGATIVE signed
// area, so triangles with signed area > 0 are facing AWAY from the
// camera and skipped.
//
// Returns:
//
//	ErrAliasNilFB     fb == nil
//	ErrAliasNilModel  model == nil
//	ErrAliasNilRefDef rd == nil
//	ErrAliasNilSkin   skin == nil
//	ErrAliasBadFrame  ent.FrameIdx out of [0, len(model.Frames))
//	(propagated)      any FillTexturedPolygon error
//
// tyrquake: R_AliasDrawModel + R_AliasPreparePoints in r_alias.c.
func DrawAlias(fb *FrameBuffer, rd *RefDef, cm *ColorMap, lightLevel int,
	model *mdl.Model, skin *Pic, ent AliasEntity) error {
	if fb == nil {
		return ErrAliasNilFB
	}
	if model == nil {
		return ErrAliasNilModel
	}
	if rd == nil {
		return ErrAliasNilRefDef
	}
	if skin == nil {
		return ErrAliasNilSkin
	}
	if ent.FrameIdx < 0 || ent.FrameIdx >= len(model.Frames) {
		return ErrAliasBadFrame
	}

	verts := FramePoseAt(model.Frames[ent.FrameIdx], ent.ClTime)
	return drawAliasFromPose(fb, rd, cm, lightLevel, model, skin, ent, verts)
}

// drawAliasFromPose runs the per-vertex transform + per-triangle
// rasterization workflow over a CALLER-SUPPLIED vertex slice. Shared
// by DrawAlias (single frame) and DrawAliasInterp (lerped pose).
// Caller must have already validated fb/rd/model/skin and the frame
// index(es); this helper just renders the supplied pose.
func drawAliasFromPose(fb *FrameBuffer, rd *RefDef, cm *ColorMap, lightLevel int,
	model *mdl.Model, skin *Pic, ent AliasEntity, verts []mdl.TriVertx) error {

	const deg2rad = math.Pi / 180
	tanHalfX := float32(math.Tan(float64(rd.FovX/2) * deg2rad))
	if tanHalfX <= 0 {
		return nil
	}
	halfW := float32(fb.Width) / 2
	halfH := float32(fb.Height) / 2
	scale := halfW / tanHalfX

	view := rd.SetupView()
	entRot := entityRotation(ent.AnglePitch, ent.AngleYaw, ent.AngleRoll)

	// Per-vertex scratch -- decoded once, reused by every triangle that
	// references the vertex. Holds screen X/Y (post perspective divide)
	// and view-space Z (for near-clip test).
	type screenVert struct {
		x, y, z float32
	}
	sv := make([]screenVert, len(verts))
	for i, tv := range verts {
		// 1. decode byte vert -> object-space float using model scale + bias.
		obj := [3]float32{
			float32(tv.V[0])*model.Header.Scale[0] + model.Header.ScaleOrigin[0],
			float32(tv.V[1])*model.Header.Scale[1] + model.Header.ScaleOrigin[1],
			float32(tv.V[2])*model.Header.Scale[2] + model.Header.ScaleOrigin[2],
		}
		// 2. rotate by entity angles (object -> world offset). The
		// entity's local axes are (forward, -right, up); applying
		// entRot maps an object-space vertex v = (xForward, ySide,
		// zUp) to its world-relative offset.
		rotated := TransformVector(entRot, obj)
		// 3. translate by entity origin -> world space.
		world := [3]float32{
			rotated[0] + ent.Origin[0],
			rotated[1] + ent.Origin[1],
			rotated[2] + ent.Origin[2],
		}
		// 4. world -> view.
		vp := TransformAffine(view, world)
		sv[i].z = vp[2]
		if vp[2] >= AliasNearClip {
			// 5. perspective divide.
			invZ := 1 / vp[2]
			sv[i].x = halfW + vp[0]*scale*invZ
			sv[i].y = halfH - vp[1]*scale*invZ
		}
	}

	skinWidthHalf := float32(model.Header.SkinWidth) / 2

	var tri [3]TexturedVertex
	for _, t := range model.Triangles {
		i0, i1, i2 := int(t.VertIndex[0]), int(t.VertIndex[1]), int(t.VertIndex[2])
		v0, v1, v2 := sv[i0], sv[i1], sv[i2]
		// 6. near-clip: drop the triangle if any vertex is too close.
		if v0.z < AliasNearClip || v1.z < AliasNearClip || v2.z < AliasNearClip {
			continue
		}
		// 7. back-face cull via signed area in screen space.
		area := (v1.x-v0.x)*(v2.y-v0.y) - (v2.x-v0.x)*(v1.y-v0.y)
		if area > 0 {
			continue
		}

		st0 := model.STVerts[i0]
		st1 := model.STVerts[i1]
		st2 := model.STVerts[i2]
		s0 := float32(st0.S)
		s1 := float32(st1.S)
		s2 := float32(st2.S)
		// Seam fixup: back-facing triangles land in the right-hand
		// (back-skin) half. tyrquake: seamfixupX16 in d_polyse.c.
		if t.FacesFront == 0 {
			if st0.OnSeam != 0 {
				s0 += skinWidthHalf
			}
			if st1.OnSeam != 0 {
				s1 += skinWidthHalf
			}
			if st2.OnSeam != 0 {
				s2 += skinWidthHalf
			}
		}
		tri[0] = TexturedVertex{X: v0.x, Y: v0.y, U: s0, V: float32(st0.T)}
		tri[1] = TexturedVertex{X: v1.x, Y: v1.y, U: s1, V: float32(st1.T)}
		tri[2] = TexturedVertex{X: v2.x, Y: v2.y, U: s2, V: float32(st2.T)}

		if err := FillTexturedPolygon(fb, skin, cm, lightLevel, tri[:]); err != nil {
			return err
		}
	}
	return nil
}

// entityRotation builds the 3x3 matrix that rotates an object-space
// vertex into the entity's local frame in world coordinates. With
// pitch=yaw=roll=0 the matrix is the identity (object X = forward,
// Y = side, Z = up). tyrquake: the COLUMNS-(forward,-right,up) shape
// of t2matrix in R_AliasSetUpTransform, r_alias.c.
func entityRotation(pitch, yaw, roll float32) Mat3 {
	forward, right, up := mathlib.AngleVectors(mathlib.Vec3{pitch, yaw, roll})
	return Mat3{
		{forward[0], -right[0], up[0]},
		{forward[1], -right[1], up[1]},
		{forward[2], -right[2], up[2]},
	}
}

// FramePose returns the per-vertex byte triples for the time=0
// pose of a Frame: FrameSingle returns its single record verbatim,
// FrameGroup returns the FIRST sub-frame. Prefer FramePoseAt(f, t)
// for FrameGroup-bearing models (torches, flames, armor shimmer)
// where the sub-frame must cycle with cl.time.
//
// Exported so out-of-package callers (e.g. the quake-tamago alias
// pass) can spot-check ComputeAliasVertexLights against the same
// pose DrawAliasLit consumed.
func FramePose(f mdl.Frame) []mdl.TriVertx {
	return FramePoseAt(f, 0)
}

// FramePoseAt returns the per-vertex byte triples for a Frame at the
// given client time. Delegates to mdl.Frame.PoseAt, which picks the
// active sub-frame of a FrameGroup based on its Intervals[]. Used by
// DrawAlias / DrawAliasLit / DrawAliasInterp{,Lit} when the entity
// carries a non-zero AliasEntity.ClTime.
func FramePoseAt(f mdl.Frame, time float32) []mdl.TriVertx {
	if f.Type == mdl.FrameGroup {
		sub := f.PoseAt(time)
		return sub.Verts
	}
	return f.Single.Verts
}
