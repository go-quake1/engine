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
// from R_LightPoint (ported as bsprender.LightSampler).

// AliasStandInLight is the R_LightPoint value (0..255) used where there is no
// map to sample (synthetic scenes): 76, the ambient 0.3 the engine's earlier
// shade range had.
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

// aliasToView maps a model-space point (in model bytes, as the vertices are)
// to view space for an entity at origin.
func aliasToView(model *mdl.Model, view Affine, entRot Mat3, origin [3]float32) func([3]float32) [3]float32 {
	hdr := model.Header
	return func(b [3]float32) [3]float32 {
		obj := [3]float32{
			b[0]*hdr.Scale[0] + hdr.ScaleOrigin[0],
			b[1]*hdr.Scale[1] + hdr.ScaleOrigin[1],
			b[2]*hdr.Scale[2] + hdr.ScaleOrigin[2],
		}
		r := TransformVector(entRot, obj)
		return TransformAffine(view, [3]float32{r[0] + origin[0], r[1] + origin[1], r[2] + origin[2]})
	}
}

func aliasClassify(av aliasView, model *mdl.Model, ent AliasEntityInterp, toView func([3]float32) [3]float32) (draw bool, accept int) {
	mins, maxs := AliasBBox(model, ent.FrameIdx, ent.FrameIdxNext)
	return aliasCheckBBox(av, mins, maxs, model.Header.Size*(1.0/11.0), toView)
}

// AliasClassify is R_AliasCheckBBox for an entity in a w x h frame: whether
// the model is drawn at all, and its trivial-accept value ([AliasClipped],
// [AliasUnclipped], [AliasFarSubdiv]). It reports false when the frame, the
// view or the model is unusable.
func AliasClassify(w, h int, rd *RefDef, model *mdl.Model, ent AliasEntityInterp) (draw bool, accept int) {
	if rd == nil || model == nil || ent.FrameIdx < 0 || ent.FrameIdx >= len(model.Frames) || ent.FrameIdxNext < 0 || ent.FrameIdxNext >= len(model.Frames) {
		return false, 0
	}
	tanHalfX := float32(math.Tan(float64(rd.FovX/2) * math.Pi / 180))
	if tanHalfX <= 0 {
		return false, 0
	}
	return aliasClassify(newAliasView(w, h, tanHalfX), model, ent, aliasToView(model, rd.SetupView(),
		entityRotation(ent.AnglePitch, ent.AngleYaw, ent.AngleRoll), ent.Origin))
}

// DrawAliasInterpQuake draws an entity as Quake's software renderer does: the
// lighting of R_AliasSetupLighting for lightPoint, the vertices projected to
// integer screen positions (R_AliasTransformAndProjectFinalVerts), the
// bounding-box test of R_AliasCheckBBox, and the triangles drawn by d_polyse.c's
// fixed-point edge walk ([PolysetDrawTriangle]) or, for a model that is far
// enough away and whole in the view, by recursive subdivision
// ([PolysetDrawSubdivTriangle]). Not ported: r_aclip.c's clipping of
// triangles that cross the near plane (they are dropped).
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
	view := rd.SetupView()
	entRot := entityRotation(ent.AnglePitch, ent.AngleYaw, ent.AngleRoll)
	light := NewAliasLighting(lightPoint, viewModel, nil, entRot)

	if skin.Width*skin.Height != len(skin.Pixels) {
		return ErrPicShape
	}
	hdr := model.Header
	toView := aliasToView(model, view, entRot, ent.Origin)
	// R_AliasCheckBBox: a model entirely off one side of the screen is not
	// drawn, and the unclipped ones far enough away take the recursive drawer.
	av := newAliasView(fb.Width, fb.Height, tanHalfX)
	draw, accept := aliasClassify(av, model, ent, toView)
	if !draw {
		return nil
	}
	// R_AliasTransformAndProjectFinalVerts (and R_AliasPreparePoints for the
	// clipped case): u and v are truncated to integers; a vertex nearer than
	// ALIAS_Z_CLIP_PLANE has none.
	fvs := make([]FinalVert, len(verts))
	near := make([]bool, len(verts))
	for i, tv := range verts {
		vp := toView([3]float32{float32(tv.V[0]), float32(tv.V[1]), float32(tv.V[2])})
		st := model.STVerts[i]
		fv := &fvs[i]
		fv.V[2], fv.V[3] = int32(st.S)<<16, int32(st.T)<<16
		fv.V[4] = light.VertexLight(tv.LightNormalIndex)
		fv.OnSeam = st.OnSeam != 0
		if vp[2] >= AliasClipPlane {
			near[i] = true
			px, py := av.project(vp[0], vp[1], vp[2])
			fv.V[0], fv.V[1] = int32(px), int32(py)
		}
	}
	seamFixup := (int32(hdr.SkinWidth) >> 1) << 16
	// the walk and its span list (one entry per row; grown when a triangle is
	// taller) are reused by every triangle
	var spanBuf [128]polySpan
	walk := polyWalk{spans: spanBuf[:0]}
	if accept == AliasFarSubdiv {
		PolysetDrawFinalVerts(fb, skin, cm, fvs)
	}
	for _, t := range model.Triangles {
		i0, i1, i2 := int(t.VertIndex[0]), int(t.VertIndex[1]), int(t.VertIndex[2])
		// r_aclip.c's clipping of partly visible triangles is not ported: a
		// triangle with a vertex nearer than the clip plane is dropped
		if !near[i0] || !near[i1] || !near[i2] {
			continue
		}
		if accept == AliasFarSubdiv {
			PolysetDrawSubdivTriangle(fb, skin, cm, fvs[i0], fvs[i1], fvs[i2], t.FacesFront != 0, seamFixup)
		} else {
			walk.drawTriangle(fb, skin, cm, &fvs[i0], &fvs[i1], &fvs[i2], t.FacesFront != 0, seamFixup)
		}
	}
	return nil
}
