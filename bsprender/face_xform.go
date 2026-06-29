// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender

import (
	"errors"
	"math"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/render"
)

// FaceVerts is the per-face accessor bundle [TransformFace] reads
// from. It deliberately exposes its geometry via closures + plain
// scalars so the transformer does NOT depend on the full
// *model.BrushModel surface; tests can synthesize a face inline, and
// future fast paths (instanced brush entities, fully-decoded surf
// records) can plug into the same shape.
//
// Field semantics:
//
//   - NumVerts is the face's vertex count (the C upstream's
//     surf->numedges, since each edge contributes one vertex once you
//     resolve the winding via the negative-surfedge trick).
//
//   - Vert(i) returns the world-space vertex at position i in
//     [0, NumVerts). Callers (typically [NewBrushFaceVerts]) walk
//     surf->firstedge .. firstedge+numedges and resolve each surfedge
//     into the head/tail of the referenced edge:
//
//     se = surfedges[firstedge + i]
//     if se >= 0: edge = edges[se];  v = verts[edge.V[0]]
//     else:       edge = edges[-se]; v = verts[edge.V[1]]
//
//     The transformer never recomputes that, so the closure form lets
//     callers cache or hard-code as they please.
//
//   - UVAxisS / UVOffS / UVAxisT / UVOffT are the two affine forms the
//     texinfo lump stores, exactly as the C code uses them:
//
//     u = vert . UVAxisS + UVOffS
//     v = vert . UVAxisT + UVOffT
//
//     Output is in TEXTURE-PIXEL space (Q1's texinfo vectors are scaled
//     so the dot+offset lands directly in 0..W / 0..H), which is the
//     space [render.FillTexturedPolygon]'s sampler reads.
//
//   - TextureWidth / TextureHeight carry the bound texture's pixel
//     dimensions so callers can size the sampler. Kept on the bundle
//     for symmetry with the closures even though [TransformFace]
//     itself doesn't read them (FillTexturedPolygon clamps to
//     tex.Width/tex.Height on its own).
type FaceVerts struct {
	NumVerts      int
	Vert          func(i int) [3]float32
	UVAxisS       [3]float32
	UVOffS        float32
	UVAxisT       [3]float32
	UVOffT        float32
	TextureWidth  int
	TextureHeight int
}

// Sentinel errors returned by [TransformFace] and [NewBrushFaceVerts].
var (
	// ErrFaceTooFewVerts is returned when NumVerts < 3 -- a polygon
	// needs at least three corners. Mirrors render.ErrTexFillFewVerts
	// so the surface transformer fails the same shape as the
	// rasterizer would.
	ErrFaceTooFewVerts = errors.New("bsprender: face has < 3 vertices")
	// ErrFaceTooManyVerts is returned when NumVerts > render.MaxPolyVerts.
	// The rasterizer's per-edge scratch buffers are sized at
	// MaxPolyVerts; refusing the face here keeps the transformer from
	// ever handing the rasterizer a slice it would reject.
	ErrFaceTooManyVerts = errors.New("bsprender: face vertex count exceeds render.MaxPolyVerts")
	// ErrFaceBehindCamera is returned when every vertex on the face
	// has a view-space forward depth < ParticleNearClip. The caller
	// (the per-frame world rasterizer) treats it as "skip this face,
	// nothing to draw" -- it's not a real error, just a signal that
	// the cheap reject already proved the face contributes nothing.
	ErrFaceBehindCamera = errors.New("bsprender: face fully behind camera (caller should skip)")
	// ErrFaceNilModel is returned by NewBrushFaceVerts when bm == nil.
	ErrFaceNilModel = errors.New("bsprender: nil BrushModel passed to NewBrushFaceVerts")
	// ErrFaceIdxRange is returned by NewBrushFaceVerts when faceIdx
	// is outside [0, len(Faces)).
	ErrFaceIdxRange = errors.New("bsprender: face index out of [0, NumFaces)")
)

// TransformFace projects one BSP face's world-space vertices into
// screen-space [render.TexturedVertex] slots, ready to feed
// [render.FillTexturedPolygon]. The returned slice is freshly
// allocated; the caller owns it.
//
// Behaviour (mirrors the relevant slice of R_RenderFace + the
// per-vertex transform in r_misc.c, minus the per-edge clipper which
// is a follow-up batch):
//
//  1. View transform: each world-space vertex is mapped to view-space
//     via [render.TransformAffine] using the supplied `view` matrix
//     (which the caller obtained from [render.RefDef.SetupView]).
//     With render.ViewMatrix's row layout (right, up, forward), the
//     view-space components are:
//
//     vp[0] = right   . (p - origin)
//     vp[1] = up      . (p - origin)
//     vp[2] = forward . (p - origin)        -- depth (positive = in front)
//
//  2. Near-clip reject: if EVERY vertex's vp[2] is < ParticleNearClip
//     the face contributes nothing and the function returns
//     ErrFaceBehindCamera. The constant is reused from the particle
//     near-clip so the whole renderer agrees on what "too close /
//     behind the camera" means.
//
//  3. Perspective divide: each vertex's vp is mapped to screen-space
//     using the framebuffer's center + a scale derived from fovX:
//
//     tanHalfX = tan(fovX/2)
//     scale    = (fb.Width/2) / tanHalfX
//     sx       = halfW + vp[0] * scale / max(vp[2], ParticleNearClip)
//     sy       = halfH - vp[1] * scale / max(vp[2], ParticleNearClip)
//
//     The clamp on vp[2] is a stop-gap: partially-behind-camera faces
//     (some verts past the near plane, some in front) would otherwise
//     divide by a tiny / negative depth and explode. The proper fix is
//     a Sutherland-Hodgman polygon clip against the near plane, which
//     is a separate batch; for now we clamp and ship a renderable -- if
//     visually-skewed -- silhouette for those cases. Pure rejects
//     (every-vert-behind) still error via step 2.
//
//  4. UV: each vertex's UV is computed from the texinfo axes +
//     offsets, in texture-pixel space (the dot+offset already lands in
//     0..W / 0..H for Q1 texinfo vectors).
//
// Errors:
//
//	ErrFaceTooFewVerts   faceVerts.NumVerts < 3
//	ErrFaceTooManyVerts  faceVerts.NumVerts > render.MaxPolyVerts
//	ErrFaceBehindCamera  every vertex's view-space depth < ParticleNearClip
//
// Caller is expected to validate fb / view ahead of time; nil fb
// would panic on the half-width read, which is acceptable because the
// per-frame renderer threads a non-nil framebuffer through the whole
// pipeline.
//
// tyrquake source: the world-space-to-view-space transform is r_misc.c
// (R_TransformVector applied per edge vertex by R_RenderFace); the
// per-vertex perspective divide is in d_polyse.c's vertex setup; the
// UV affine forms are in surf->texinfo->vecs as consumed by
// R_DrawSurface's UV setup path.
func TransformFace(
	view render.Affine,
	fb *render.FrameBuffer,
	fovX float32,
	faceVerts FaceVerts,
) ([]render.TexturedVertex, error) {
	if faceVerts.NumVerts < 3 {
		return nil, ErrFaceTooFewVerts
	}
	if faceVerts.NumVerts > render.MaxPolyVerts {
		return nil, ErrFaceTooManyVerts
	}

	// Stage 1: view-space transform + behind-camera classification.
	// Stash the view-space verts so step 3 doesn't re-transform; the
	// MaxPolyVerts cap keeps this on the stack-friendly side.
	type viewVert struct {
		world [3]float32
		view  [3]float32
	}
	vs := make([]viewVert, faceVerts.NumVerts)
	anyInFront := false
	for i := 0; i < faceVerts.NumVerts; i++ {
		w := faceVerts.Vert(i)
		vp := render.TransformAffine(view, w)
		vs[i].world = w
		vs[i].view = vp
		if vp[2] >= render.ParticleNearClip {
			anyInFront = true
		}
	}
	if !anyInFront {
		return nil, ErrFaceBehindCamera
	}

	// Stage 2: per-fb perspective scale. Matches DrawParticles'
	// derivation so screen-space coords are consistent across all
	// projected primitives. fovX is in degrees.
	const deg2rad = math.Pi / 180
	tanHalfX := float32(math.Tan(float64(fovX/2) * deg2rad))
	// Guard against caller passing a degenerate fovX; the per-frame
	// world loop validates fov upstream, so this is belt-and-braces.
	// We DON'T error here: every-vert-behind already errored above;
	// at this point we're committed to producing a polygon, and a
	// zero/negative tanHalfX is best surfaced as a downstream
	// "off-screen" silhouette than as a transformer rejection.
	if tanHalfX <= 0 {
		tanHalfX = 1
	}
	halfW := float32(fb.Width) / 2
	halfH := float32(fb.Height) / 2
	scale := halfW / tanHalfX

	out := make([]render.TexturedVertex, faceVerts.NumVerts)
	for i := 0; i < faceVerts.NumVerts; i++ {
		w := vs[i].world
		vp := vs[i].view
		// Stage 3: clamp depth to ParticleNearClip for the divide.
		// This is the temporary stand-in for a proper near-plane
		// clipper; documented above.
		depth := vp[2]
		if depth < render.ParticleNearClip {
			depth = render.ParticleNearClip
		}
		invZ := 1 / depth
		sx := halfW + vp[0]*scale*invZ
		sy := halfH - vp[1]*scale*invZ

		// Stage 4: UV in texture-pixel space. Direct dot+offset; the
		// rasterizer clamps to tex bounds on read.
		u := w[0]*faceVerts.UVAxisS[0] + w[1]*faceVerts.UVAxisS[1] + w[2]*faceVerts.UVAxisS[2] + faceVerts.UVOffS
		v := w[0]*faceVerts.UVAxisT[0] + w[1]*faceVerts.UVAxisT[1] + w[2]*faceVerts.UVAxisT[2] + faceVerts.UVOffT

		out[i] = render.TexturedVertex{
			X: sx,
			Y: sy,
			U: u,
			V: v,
		}
	}
	return out, nil
}

// TransformFacePerspective is the perspective-correct sibling of
// [TransformFace]: same projection + UV math, but every output vertex
// also carries its view-space Z so the caller can feed
// [render.FillPerspectiveTexturedPolygon] / its lit variant. Real
// Quake renders world surfaces with sub-span perspective-correct UV
// (1/z interpolation across 8-pixel spans); the plain affine path
// makes large polygons (floors, big walls) "swim" as the camera moves.
//
// Z is the same value the projection divide saw: the view-space depth
// after the near-clip clamp ([render.ParticleNearClip] floor). The
// sampler reciprocates Z so as long as Z > 0 the math is stable.
//
// Errors mirror [TransformFace]:
//
//	ErrFaceTooFewVerts   faceVerts.NumVerts < 3
//	ErrFaceTooManyVerts  faceVerts.NumVerts > render.MaxPolyVerts
//	ErrFaceBehindCamera  every vertex's view-space depth < ParticleNearClip
func TransformFacePerspective(
	view render.Affine,
	fb *render.FrameBuffer,
	fovX float32,
	faceVerts FaceVerts,
) ([]render.PerspTexturedVertex, error) {
	if faceVerts.NumVerts < 3 {
		return nil, ErrFaceTooFewVerts
	}
	if faceVerts.NumVerts > render.MaxPolyVerts {
		return nil, ErrFaceTooManyVerts
	}

	type viewVert struct {
		world [3]float32
		view  [3]float32
	}
	vs := make([]viewVert, faceVerts.NumVerts)
	anyInFront := false
	for i := 0; i < faceVerts.NumVerts; i++ {
		w := faceVerts.Vert(i)
		vp := render.TransformAffine(view, w)
		vs[i].world = w
		vs[i].view = vp
		if vp[2] >= render.ParticleNearClip {
			anyInFront = true
		}
	}
	if !anyInFront {
		return nil, ErrFaceBehindCamera
	}

	const deg2rad = math.Pi / 180
	tanHalfX := float32(math.Tan(float64(fovX/2) * deg2rad))
	if tanHalfX <= 0 {
		tanHalfX = 1
	}
	halfW := float32(fb.Width) / 2
	halfH := float32(fb.Height) / 2
	scale := halfW / tanHalfX

	out := make([]render.PerspTexturedVertex, faceVerts.NumVerts)
	for i := 0; i < faceVerts.NumVerts; i++ {
		w := vs[i].world
		vp := vs[i].view
		depth := vp[2]
		if depth < render.ParticleNearClip {
			depth = render.ParticleNearClip
		}
		invZ := 1 / depth
		sx := halfW + vp[0]*scale*invZ
		sy := halfH - vp[1]*scale*invZ

		u := w[0]*faceVerts.UVAxisS[0] + w[1]*faceVerts.UVAxisS[1] + w[2]*faceVerts.UVAxisS[2] + faceVerts.UVOffS
		v := w[0]*faceVerts.UVAxisT[0] + w[1]*faceVerts.UVAxisT[1] + w[2]*faceVerts.UVAxisT[2] + faceVerts.UVOffT

		out[i] = render.PerspTexturedVertex{
			X: sx,
			Y: sy,
			Z: depth,
			U: u,
			V: v,
		}
	}
	return out, nil
}

// NewBrushFaceVerts builds a [FaceVerts] closure bundle resolving the
// world-space vertex sequence for the face at faceIdx in the supplied
// *model.BrushModel. The returned bundle's Vert closure walks the
// model's surfedges + edges + verts on each call (no precomputation),
// so it stays cheap to construct.
//
// Edge winding follows tyrquake's R_RenderFace exactly: positive
// surfedge indices use the head vertex of the referenced edge; negative
// surfedge indices use the tail vertex of the absolute-valued edge.
// See engine/bsprender/face_xform.go's package-level comment on
// FaceVerts for the formula.
//
// Texinfo + texture metadata: the bundle resolves the face's TexInfo
// from the TexInfos lump and the linked MipTex from the Textures
// directory; UVAxisS / UVOffS / UVAxisT / UVOffT come from texinfo.Vecs;
// TextureWidth / TextureHeight from the miptex header. A face that
// indexes a missing-texture slot (miptex offset == -1) returns 0 for
// the dimensions, matching tyrquake's "no_texture" fallback (a missing
// texture still draws -- the sampler just hits the placeholder).
//
// Returns:
//
//	ErrFaceNilModel   bm == nil
//	ErrFaceIdxRange   faceIdx outside [0, len(Faces))
//	<lump-read err>   propagated from the bspfile decoders (Faces /
//	                  Surfedges / Edges / Vertexes / TexInfos / Textures)
//
// tyrquake equivalent: model.c:Mod_LoadFaces + the per-face surface
// setup inline-decoded inside R_RenderFace (no dedicated factory
// upstream).
func NewBrushFaceVerts(bm *model.BrushModel, faceIdx int) (FaceVerts, error) {
	if bm == nil {
		return FaceVerts{}, ErrFaceNilModel
	}
	faces, err := bm.File.Faces()
	if err != nil {
		return FaceVerts{}, err
	}
	if faceIdx < 0 || faceIdx >= len(faces) {
		return FaceVerts{}, ErrFaceIdxRange
	}
	face := faces[faceIdx]

	surfedges, err := bm.File.Surfedges()
	if err != nil {
		return FaceVerts{}, err
	}
	edges, err := bm.File.Edges()
	if err != nil {
		return FaceVerts{}, err
	}
	verts, err := bm.File.Vertexes()
	if err != nil {
		return FaceVerts{}, err
	}
	texinfos, err := bm.File.TexInfos()
	if err != nil {
		return FaceVerts{}, err
	}

	// Texinfo + miptex resolve. TexInfo may be out of range on a
	// corrupted file (tyrquake validates at load time; we mirror that
	// by simply leaving the UV axes at zero in that case, which
	// produces a single-texel sample -- visually wrong but
	// non-crashing, matching the C "missing texinfo" fallback).
	var (
		ti         bspfile.TexInfo
		hasTexInfo bool
	)
	if int(face.TexInfo) >= 0 && int(face.TexInfo) < len(texinfos) {
		ti = texinfos[face.TexInfo]
		hasTexInfo = true
	}

	// Resolve texture dimensions via the Textures directory; absent
	// or sentinel-offset miptex slots leave the dims at 0.
	var (
		texW, texH int
	)
	if hasTexInfo {
		tex, terr := bm.File.Textures()
		if terr != nil {
			return FaceVerts{}, terr
		}
		mt, present, mterr := tex.MipTex(int(ti.MipTex))
		if mterr != nil {
			return FaceVerts{}, mterr
		}
		if present && mt != nil {
			texW = int(mt.Width)
			texH = int(mt.Height)
		}
	}

	first := int(face.FirstEdge)
	n := int(face.NumEdges)

	// Closure: resolves the i-th vertex of the face by walking
	// surfedges -> edges -> verts with the negative-surfedge winding
	// trick from R_RenderFace.
	vertFn := func(i int) [3]float32 {
		se := int32(surfedges[first+i])
		var v bspfile.Vertex
		if se >= 0 {
			e := edges[se]
			v = verts[e.V0]
		} else {
			e := edges[-se]
			v = verts[e.V1]
		}
		return [3]float32{v.X, v.Y, v.Z}
	}

	bundle := FaceVerts{
		NumVerts:      n,
		Vert:          vertFn,
		TextureWidth:  texW,
		TextureHeight: texH,
	}
	if hasTexInfo {
		bundle.UVAxisS = [3]float32{ti.Vecs[0][0], ti.Vecs[0][1], ti.Vecs[0][2]}
		bundle.UVOffS = ti.Vecs[0][3]
		bundle.UVAxisT = [3]float32{ti.Vecs[1][0], ti.Vecs[1][1], ti.Vecs[1][2]}
		bundle.UVOffT = ti.Vecs[1][3]
	}
	return bundle, nil
}
