// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"

	"github.com/go-quake1/engine/anorms"
	"github.com/go-quake1/engine/mdl"
)

// AliasShadeRange is the per-frame shading envelope applied to alias
// models. Per-vertex light is:
//
//	dot   = max(0, dot(anorms[verts[i].LightNormalIndex], LightDir))
//	light = Ambient + dot * (DirectMax - DirectMin)
//	value = clamp(light * 255, 0, 255)
//
// tyrquake's R_AliasSetUpColor stores the equivalent quantities in
// r_ambientlight + r_shadelight (mapped into the 6-bit colormap range
// at the lookup site); here the constants are normalised to [0, 1] so
// callers can think in fractions and the mapping into the colormap
// row is applied uniformly via ColorMap.LightIndex at the rasterizer
// side.
type AliasShadeRange struct {
	Ambient   float32    // typical 1.0 = full ambient (matches gameplay default)
	DirectMin float32    // typical 0.0
	DirectMax float32    // typical 1.0 -- range of the per-vertex contribution
	LightDir  [3]float32 // unit vector of incoming light
}

// DefaultAliasShade returns a sensible default: full ambient, light
// coming from +X (a directional source aligned with the world forward
// axis), max direct contribution 1.0.
//
// Mirrors tyrquake's "viewmodel + no map lighting" path where
// r_ambientlight floors at 24 (~0.094 of the 8-bit range) and
// r_shadelight rides up to the same; here Ambient=1.0 (full bright)
// is chosen as the gameplay default so models look like they did in
// the pre-light-pass DrawAlias commit when callers DON'T configure a
// custom shade range.
func DefaultAliasShade() AliasShadeRange {
	return AliasShadeRange{
		Ambient:   1.0,
		DirectMin: 0.0,
		DirectMax: 1.0,
		LightDir:  [3]float32{1, 0, 0},
	}
}

// ErrAliasLightModelNil is returned by ComputeAliasVertexLights when
// passed a nil vertex slice (a caller-side nil-model bug). An empty
// (non-nil) slice is NOT an error -- it returns an empty result.
var ErrAliasLightModelNil = errors.New("render: nil model in alias-light compute")

// ComputeAliasVertexLights returns a per-vertex light value in [0, 255]
// for every vertex in `verts` (typically sourced from a frame pose's
// TriVertx slice). The formula is documented on AliasShadeRange.
//
// Each LightNormalIndex >= len(anorms.Table) clamps to normal-index 0
// (defensive against corrupt models; tyrquake silently uses normal
// index 0). A nil `verts` slice returns ErrAliasLightModelNil.
func ComputeAliasVertexLights(verts []mdl.TriVertx, shade AliasShadeRange) ([]int, error) {
	if verts == nil {
		return nil, ErrAliasLightModelNil
	}
	out := make([]int, len(verts))
	span := shade.DirectMax - shade.DirectMin
	for i, tv := range verts {
		idx := int(tv.LightNormalIndex)
		if idx >= len(anorms.Table) {
			idx = 0
		}
		n := anorms.Table[idx]
		dot := n[0]*shade.LightDir[0] + n[1]*shade.LightDir[1] + n[2]*shade.LightDir[2]
		if dot < 0 {
			dot = 0
		}
		light := shade.Ambient + dot*span
		v := int(math.Round(float64(light * 255)))
		if v < 0 {
			v = 0
		} else if v > 255 {
			v = 255
		}
		out[i] = v
	}
	return out, nil
}

// AvgTriangleLight returns the integer mean of three vertex lights
// from `lights` at indices v0, v1, v2. Used by DrawAliasLit to derive
// a per-triangle scalar light value compatible with the existing
// FillTexturedPolygon scalar `lightLevel` parameter.
func AvgTriangleLight(lights []int, v0, v1, v2 int) int {
	return (lights[v0] + lights[v1] + lights[v2]) / 3
}

// DrawAliasLit is DrawAlias with per-triangle dynamic shading derived
// from the model's per-vertex light-normal table.
//
// Per ComputeAliasVertexLights every vertex gets a light value in
// [0, 255]; per triangle the mean of its three vertex lights is then
// mapped onto the colormap's 6-bit row index and passed as
// FillTexturedPolygon's existing scalar `lightLevel`. The mapping is
// "brighter light -> smaller row index", matching tyrquake's
// `(255 - light) >> 2` arithmetic in R_AliasSetUpColor's tail.
//
// SCOPE: this commit implements OPTION B from the per-vertex-lighting
// design -- per-triangle averaging. Different triangles shade
// differently based on their average vertex orientation (visibly
// better than the uniform-per-call lightLevel of DrawAlias), but
// WITHIN a triangle the lighting is flat. Per-pixel gouraud shading
// (per-vertex light interpolated along scanlines) requires a sibling
// FillTexturedPolygonLit + LightLevel field on TexturedVertex and is
// a follow-up batch.
//
// Returns the same nil-arg + bad-frame sentinels as DrawAlias.
func DrawAliasLit(fb *FrameBuffer, rd *RefDef, cm *ColorMap, shade AliasShadeRange,
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
	if verts == nil {
		// Empty FrameGroup -- FramePose returns nil; there is no work
		// for the triangle loop. Skip the compute (which would error
		// on the nil slice) and return early.
		return nil
	}
	lights, _ := ComputeAliasVertexLights(verts, shade)
	return drawAliasFromPoseLit(fb, rd, cm, lights, model, skin, ent, verts)
}

// drawAliasFromPoseLit is drawAliasFromPose with a per-vertex `lights`
// slice supplying each triangle's scalar light level (mean of its 3
// vertex lights, mapped into the colormap's 6-bit row range).
//
// Structurally identical to drawAliasFromPose -- the only delta is
// that `lightLevel` for FillTexturedPolygon is recomputed per triangle
// from the supplied per-vertex light array.
func drawAliasFromPoseLit(fb *FrameBuffer, rd *RefDef, cm *ColorMap, lights []int,
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

	type screenVert struct {
		x, y, z float32
	}
	sv := make([]screenVert, len(verts))
	for i, tv := range verts {
		obj := [3]float32{
			float32(tv.V[0])*model.Header.Scale[0] + model.Header.ScaleOrigin[0],
			float32(tv.V[1])*model.Header.Scale[1] + model.Header.ScaleOrigin[1],
			float32(tv.V[2])*model.Header.Scale[2] + model.Header.ScaleOrigin[2],
		}
		rotated := TransformVector(entRot, obj)
		world := [3]float32{
			rotated[0] + ent.Origin[0],
			rotated[1] + ent.Origin[1],
			rotated[2] + ent.Origin[2],
		}
		vp := TransformAffine(view, world)
		sv[i].z = vp[2]
		if vp[2] >= AliasNearClip {
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
		if v0.z < AliasNearClip || v1.z < AliasNearClip || v2.z < AliasNearClip {
			continue
		}
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

		// Per-triangle scalar light: mean of the 3 vertex lights
		// (0..255), mapped onto the colormap's 6-bit row index
		// (0 = brightest, ColorMapRows-1 = darkest). Matches the
		// "brighter -> smaller row" sense tyrquake's R_AliasSetUpColor
		// produces via `(255 - r_ambientlight) >> VID_CBITS`.
		avg := AvgTriangleLight(lights, i0, i1, i2)
		row := (255 - avg) * ColorMapRows / 256

		if err := FillTexturedPolygon(fb, skin, cm, row, tri[:]); err != nil {
			return err
		}
	}
	return nil
}
