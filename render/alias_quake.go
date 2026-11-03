// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"math"

	"github.com/go-quake1/engine/anorms"
	"github.com/go-quake1/engine/mdl"
)

// This file ports Quake's software alias lighting as tyrquake has it:
// R_AliasSetupLighting (r_alias.c), the per-vertex light of
// R_AliasTransformFinalVert, and the Gouraud interpolation of the light
// across a triangle that d_polyse.c performs (r_lstepx / r_lstepy, the
// colormap row taken as (light & 0xFF00) >> 8).
//
// What is the engine's own, and not Quake's: the choice of covered pixels and
// the affine skin interpolation use the engine's scanline fill rather than
// d_polyse.c's fixed-point edge walk (D_PolysetRecursiveTriangle,
// D_PolysetScanLeftEdge), and the light of a pixel is the plane through the
// three vertex lights at its centre, not the stepped integer sum with the
// ceil of r_lstepx. The ambient light comes from the caller: Quake takes it
// from R_LightPoint, which is not ported.

// AliasStandInLight is the R_LightPoint value (0..255) the engine uses for
// every entity until R_LightPoint is ported: 76, the ambient 0.3 the engine's
// earlier shade range had.
const AliasStandInLight = 76

// AliasLightMin is LIGHT_MIN (r_alias.c): no vertex is lit below it.
const AliasLightMin = 5

// AliasLighting is what R_AliasSetupLighting leaves for one entity:
// r_ambientlight and r_shadelight in colormap units (row * 256), and
// r_plightvec, the world light direction (0, 0, -1) in the model's frame.
type AliasLighting struct {
	Ambient int32
	Shade   float32
	Vec     [3]float32
}

// NewAliasLighting is R_AliasSetupLighting. lightPoint is R_LightPoint's
// value at the entity's origin (0..255), viewModel is the view weapon (its
// light is at least 24), dlights are the levels the dynamic lights add
// (radius minus distance, when positive), and rot is the entity's rotation
// ([EntityRotation]).
func NewAliasLighting(lightPoint int, viewModel bool, dlights []int, rot Mat3) AliasLighting {
	ambient := lightPoint
	if viewModel && ambient < 24 {
		ambient = 24
	}
	shade := float32(ambient)
	for _, d := range dlights {
		if d > 0 {
			ambient += d
		}
	}
	// clamp lighting so it doesn't overbright as much
	if ambient > 128 {
		ambient = 128
	}
	if float32(ambient)+shade > 192 {
		shade = float32(192 - ambient)
	}
	// guarantee that no vertex will ever be lit below LIGHT_MIN
	if ambient < AliasLightMin {
		ambient = AliasLightMin
	}
	if shade < 0 {
		shade = 0
	}
	l := AliasLighting{Ambient: int32((255 - ambient) << ColorMapBits), Shade: shade * ColorMapRows}
	// the light vector (0, 0, -1) rotated into the model's frame: the third
	// row of the rotation, negated.
	l.Vec = [3]float32{-rot[2][0], -rot[2][1], -rot[2][2]}
	return l
}

// ColorMapBits is VID_CBITS: the colormap has 1 << 6 rows.
const ColorMapBits = 6

// VertexLight is the light of a vertex with the given light normal index, in
// colormap units (row * 256): r_ambientlight, less (int)(r_shadelight *
// lightcos) when the normal faces the light, not below zero.
func (l AliasLighting) VertexLight(normal byte) int32 {
	idx := int(normal)
	if idx >= anorms.Count {
		idx = 0
	}
	n := anorms.Table[idx]
	cos := n[0]*l.Vec[0] + n[1]*l.Vec[1] + n[2]*l.Vec[2]
	temp := l.Ambient
	if cos < 0 {
		temp += int32(l.Shade * cos)
		if temp < 0 {
			temp = 0
		}
	}
	return temp
}

// GouraudVertex is a triangle corner with a skin coordinate and a light in
// colormap units (row * 256).
type GouraudVertex struct {
	X, Y, U, V, L float32
}

// FillGouraudTriangle paints a triangle with the skin sampled affinely
// (as [FillTexturedPolygon]) and the light interpolated linearly in screen
// space; the colormap row is (int(light) & 0xFF00) >> 8, as d_polyse.c does,
// clamped to the colormap.
func FillGouraudTriangle(fb *FrameBuffer, tex *Pic, cm *ColorMap, v [3]GouraudVertex) error {
	if fb == nil {
		return ErrTexFillNilFB
	}
	if tex == nil {
		return ErrTexFillNilTex
	}
	if tex.Width*tex.Height != len(tex.Pixels) {
		return ErrPicShape
	}
	yMin, yMax := min(v[0].Y, v[1].Y, v[2].Y), max(v[0].Y, v[1].Y, v[2].Y)
	yStart, yEnd := max(int(math.Floor(float64(yMin))), 0), min(int(math.Ceil(float64(yMax))), fb.Height)
	for y := yStart; y < yEnd; y++ {
		yf := float32(y) + 0.5
		var xs, us, vs, ls [3]float32
		n := 0
		for i := 0; i < 3; i++ {
			a, b := v[i], v[(i+1)%3]
			if (a.Y <= yf && b.Y > yf) || (b.Y <= yf && a.Y > yf) {
				t := (yf - a.Y) / (b.Y - a.Y)
				xs[n], us[n], vs[n], ls[n] = a.X+t*(b.X-a.X), a.U+t*(b.U-a.U), a.V+t*(b.V-a.V), a.L+t*(b.L-a.L)
				n++
			}
		}
		if n < 2 {
			continue
		}
		if xs[0] > xs[1] {
			xs[0], xs[1], us[0], us[1], vs[0], vs[1], ls[0], ls[1] = xs[1], xs[0], us[1], us[0], vs[1], vs[0], ls[1], ls[0]
		}
		x0, x1 := max(int(math.Ceil(float64(xs[0]))), 0), min(int(math.Floor(float64(xs[1]))), fb.Width-1)
		if x0 > x1 {
			continue
		}
		span := xs[1] - xs[0]
		dU, dV, dL := (us[1]-us[0])/span, (vs[1]-vs[0])/span, (ls[1]-ls[0])/span
		row := y * fb.Pitch
		for x := x0; x <= x1; x++ {
			d := float32(x) + 0.5 - xs[0]
			ui := positiveMod(int(math.Floor(float64(us[0]+d*dU))), tex.Width)
			vi := positiveMod(int(math.Floor(float64(vs[0]+d*dV))), tex.Height)
			fb.Pixels[row+x] = cm.LightIndex((int(ls[0]+d*dL)&0xFF00)>>8, tex.Pixels[vi*tex.Width+ui])
		}
	}
	return nil
}

// aliasPose is the pose of an interpolated entity, as DrawAliasInterpLit
// builds it.
func aliasPose(model *mdl.Model, ent AliasEntityInterp) []mdl.TriVertx {
	a := FramePoseAt(model.Frames[ent.FrameIdx], ent.ClTime)
	if ent.Lerp == 0 || ent.FrameIdxNext == ent.FrameIdx {
		return a
	}
	b := FramePoseAt(model.Frames[ent.FrameIdxNext], ent.ClTime)
	if ent.Lerp == 1 {
		return b
	}
	return lerpAliasPose(a, b, ent.Lerp)
}

// DrawAliasInterpQuake draws an entity with Quake's lighting: the lighting
// of R_AliasSetupLighting for lightPoint, Gouraud-shaded. Projection, near
// clip, back-face cull and the skin seam are those of [DrawAlias].
func DrawAliasInterpQuake(fb *FrameBuffer, rd *RefDef, cm *ColorMap, lightPoint int, viewModel bool,
	model *mdl.Model, skin *Pic, ent AliasEntityInterp) error {
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
	if ent.Lerp < 0 || ent.Lerp > 1 {
		return ErrAliasInterpRange
	}
	if ent.FrameIdx < 0 || ent.FrameIdx >= len(model.Frames) || ent.FrameIdxNext < 0 || ent.FrameIdxNext >= len(model.Frames) {
		return ErrAliasBadFrame
	}
	verts := aliasPose(model, ent)
	if len(verts) == 0 {
		return nil
	}
	tanHalfX := float32(math.Tan(float64(rd.FovX/2) * math.Pi / 180))
	if tanHalfX <= 0 {
		return nil
	}
	halfW, halfH := float32(fb.Width)/2, float32(fb.Height)/2
	scale := halfW / tanHalfX
	view := rd.SetupView()
	entRot := entityRotation(ent.AnglePitch, ent.AngleYaw, ent.AngleRoll)
	light := NewAliasLighting(lightPoint, viewModel, nil, entRot)

	type sv struct{ x, y, z, l float32 }
	pts := make([]sv, len(verts))
	for i, tv := range verts {
		obj := [3]float32{
			float32(tv.V[0])*model.Header.Scale[0] + model.Header.ScaleOrigin[0],
			float32(tv.V[1])*model.Header.Scale[1] + model.Header.ScaleOrigin[1],
			float32(tv.V[2])*model.Header.Scale[2] + model.Header.ScaleOrigin[2],
		}
		r := TransformVector(entRot, obj)
		vp := TransformAffine(view, [3]float32{r[0] + ent.Origin[0], r[1] + ent.Origin[1], r[2] + ent.Origin[2]})
		pts[i].z = vp[2]
		pts[i].l = float32(light.VertexLight(tv.LightNormalIndex))
		if vp[2] >= AliasNearClip {
			pts[i].x = halfW + vp[0]*scale/vp[2]
			pts[i].y = halfH - vp[1]*scale/vp[2]
		}
	}
	half := float32(model.Header.SkinWidth) / 2
	for _, t := range model.Triangles {
		i0, i1, i2 := int(t.VertIndex[0]), int(t.VertIndex[1]), int(t.VertIndex[2])
		a, b, c := pts[i0], pts[i1], pts[i2]
		if a.z < AliasNearClip || b.z < AliasNearClip || c.z < AliasNearClip {
			continue
		}
		if (b.x-a.x)*(c.y-a.y)-(c.x-a.x)*(b.y-a.y) > 0 {
			continue
		}
		var tri [3]GouraudVertex
		for k, i := range [3]int{i0, i1, i2} {
			st := model.STVerts[i]
			s := float32(st.S)
			if t.FacesFront == 0 && st.OnSeam != 0 {
				s += half
			}
			p := pts[i]
			tri[k] = GouraudVertex{X: p.x, Y: p.y, U: s, V: float32(st.T), L: p.l}
		}
		if err := FillGouraudTriangle(fb, skin, cm, tri); err != nil {
			return err
		}
	}
	return nil
}
